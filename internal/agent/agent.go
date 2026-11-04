// Package agent runs the model ⇄ tools loop for a session.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wetakush/solaragent/internal/app"
	"github.com/wetakush/solaragent/internal/llm"
	"github.com/wetakush/solaragent/internal/session"
	"github.com/wetakush/solaragent/internal/tools"
)

type EventKind int

const (
	EvText EventKind = iota
	EvReasoning
	EvToolPending // model started emitting a tool call
	EvToolRun     // tool input complete, executing
	EvToolDone
	EvPermission
	EvStep // an assistant message was committed
	EvUsage
	EvTitle
	EvTodos
	EvCompacting
	EvCompacted
	EvDone
	EvError
)

type Decision int

const (
	Allow Decision = iota
	AllowAlways
	Deny
)

type PermissionRequest struct {
	Tool     string
	Category string
	Title    string
	Input    string
	Reply    chan Decision
}

type Event struct {
	Kind      EventKind
	Text      string
	ToolID    string
	ToolName  string
	ToolInput string
	Result    *tools.Result
	Perm      *PermissionRequest
	Err       error
	Usage     llm.Usage
	Cost      float64
	LastCtx   int64
	Todos     []tools.Todo
}

type Runner struct {
	App         *app.App
	Sess        *session.Session
	Env         *tools.Env
	AutoApprove bool
	Events      chan<- Event
	NextDisplay string // display text for the next user message

	mu      sync.Mutex
	always  map[string]bool
	turn    *session.Turn
	titleCh chan string
}

func NewRunner(a *app.App, sess *session.Session, events chan<- Event) *Runner {
	env := tools.NewEnv(a.Cwd)
	env.Todos = sess.Todos
	r := &Runner{App: a, Sess: sess, Env: env, Events: events, always: map[string]bool{}}
	env.Snapshot = r.snapshot
	return r
}

func (r *Runner) emit(e Event) {
	if r.Events != nil {
		r.Events <- e
	}
}

func (r *Runner) snapshot(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turn == nil {
		return
	}
	for _, f := range r.turn.Files {
		if f.Path == path {
			return
		}
	}
	fs := session.FileSnap{Path: path}
	if b, err := os.ReadFile(path); err == nil {
		s := string(b)
		fs.Content = &s
	}
	r.turn.Files = append(r.turn.Files, fs)
}

// SetAlways marks a permission category as always allowed for this runner.
func (r *Runner) SetAlways(cat string) {
	r.mu.Lock()
	r.always[cat] = true
	r.mu.Unlock()
}

func (r *Runner) save() {
	if r.Sess.Parent == "" {
		_ = r.App.Store.Save(r.Sess)
	}
}

// Run appends a user message and drives the agent until it stops.
func (r *Runner) Run(ctx context.Context, userText string, ag app.AgentDef, model, effort string) error {
	defer r.emit(Event{Kind: EvDone})
	err := r.run(ctx, userText, ag, model, effort)
	if err != nil && !errors.Is(err, context.Canceled) {
		r.emit(Event{Kind: EvError, Err: err})
	}
	return err
}

func (r *Runner) run(ctx context.Context, userText string, ag app.AgentDef, model, effort string) error {
	provID, modelID, err := llm.ParseRef(model)
	if err != nil {
		return err
	}
	prov, err := r.App.Provider(provID)
	if err != nil {
		return err
	}
	info := r.App.ModelInfo(model)

	r.Sess.Model, r.Sess.Agent = model, ag.Name
	r.mu.Lock()
	r.turn = &session.Turn{MsgIndex: len(r.Sess.Messages)}
	r.mu.Unlock()
	r.Sess.Messages = append(r.Sess.Messages, llm.Message{Role: llm.RoleUser, Content: userText, Display: r.NextDisplay, Time: time.Now(), Agent: ag.Name})
	r.NextDisplay = ""
	defer func() {
		r.mu.Lock()
		if r.turn != nil {
			r.Sess.Turns = append(r.Sess.Turns, *r.turn)
			r.turn = nil
		}
		r.mu.Unlock()
		r.Sess.Todos = r.Env.Todos
		r.save()
	}()

	if r.Sess.Title == "" || r.Sess.Title == "New session" {
		r.startTitle(userText, model)
	}

	toolset := r.App.ToolsFor(ag)
	byName := map[string]tools.Tool{}
	var defs []llm.ToolDef
	for _, t := range toolset {
		byName[t.Name()] = t
		defs = append(defs, tools.Def(t))
	}
	r.Env.SubAgent = func(ctx context.Context, desc, prompt string) (string, error) {
		return r.subAgent(ctx, desc, prompt, model, effort)
	}
	system := SystemPrompt(r.App, ag, model)

	maxTokens := 0
	if info.MaxOutput > 0 {
		maxTokens = info.MaxOutput
		if maxTokens > 64000 {
			maxTokens = 64000
		}
	}

	for step := 0; step < 200; step++ {
		r.applyTitle()
		if r.App.Cfg.AutoCompactEnabled() && info.Context > 0 && r.Sess.LastCtx > int64(float64(info.Context)*0.88) {
			if err := r.Compact(ctx, model, ag); err != nil {
				return err
			}
		}
		start := time.Now()
		req := llm.Request{Model: modelID, System: system, Messages: r.Sess.Messages, Tools: defs, MaxTokens: maxTokens, Effort: effort}
		var resp *llm.Response
		var partial strings.Builder
		var streamErr error
		for ev := range prov.Stream(ctx, req) {
			switch ev.Type {
			case llm.EvText:
				partial.WriteString(ev.Text)
				r.emit(Event{Kind: EvText, Text: ev.Text})
			case llm.EvReasoning:
				r.emit(Event{Kind: EvReasoning, Text: ev.Text})
			case llm.EvToolStart:
				r.emit(Event{Kind: EvToolPending, ToolID: ev.ToolCall.ID, ToolName: ev.ToolCall.Name})
			case llm.EvDone:
				resp = ev.Response
			case llm.EvError:
				streamErr = ev.Err
			}
		}
		if resp == nil {
			if partial.Len() > 0 {
				r.Sess.Messages = append(r.Sess.Messages, llm.Message{Role: llm.RoleAssistant, Content: partial.String() + "\n\n_(interrupted)_",
					Time: time.Now(), Model: model, Agent: ag.Name, ElapsedMs: time.Since(start).Milliseconds()})
				r.emit(Event{Kind: EvStep})
			}
			if ctx.Err() != nil {
				return context.Canceled
			}
			if streamErr == nil {
				streamErr = fmt.Errorf("empty response from model")
			}
			return streamErr
		}

		msg := llm.Message{Role: llm.RoleAssistant, Content: resp.Content, Reasoning: resp.Reasoning, ToolCalls: resp.ToolCalls,
			Time: time.Now(), Model: model, Agent: ag.Name, ElapsedMs: time.Since(start).Milliseconds()}
		if _, isAnthropic := prov.(*llm.Anthropic); isAnthropic && len(resp.Raw) > 0 {
			msg.Raw, msg.RawProvider = resp.Raw, "anthropic"
		}
		r.Sess.Messages = append(r.Sess.Messages, msg)
		r.Sess.Usage.Add(resp.Usage)
		r.Sess.Cost += info.Cost(resp.Usage)
		if ctxTok := resp.Usage.ContextTokens(); ctxTok > 0 {
			r.Sess.LastCtx = ctxTok + resp.Usage.Output
		}
		r.emit(Event{Kind: EvStep})
		r.emit(Event{Kind: EvUsage, Usage: r.Sess.Usage, Cost: r.Sess.Cost, LastCtx: r.Sess.LastCtx})
		r.save()

		if len(resp.ToolCalls) == 0 {
			if resp.StopReason == "max_tokens" || resp.StopReason == "length" {
				r.emit(Event{Kind: EvError, Err: fmt.Errorf("response hit the output token limit")})
			}
			return nil
		}
		truncated := resp.StopReason == "max_tokens" || resp.StopReason == "length"
		r.runTools(ctx, resp.ToolCalls, byName, truncated)
		r.save()
		if ctx.Err() != nil {
			return context.Canceled
		}
	}
	return fmt.Errorf("stopped after too many steps")
}

func (r *Runner) runTools(ctx context.Context, calls []llm.ToolCall, byName map[string]tools.Tool, truncated bool) {
	results := make([]llm.Message, len(calls))
	allReadOnly := true
	for _, c := range calls {
		t, ok := byName[c.Name]
		if !ok || t.Permission() != tools.PermNone || c.Name == "task" {
			allReadOnly = false
		}
	}
	exec := func(i int, c llm.ToolCall) {
		r.emit(Event{Kind: EvToolRun, ToolID: c.ID, ToolName: c.Name, ToolInput: c.Input})
		var res tools.Result
		t, ok := byName[c.Name]
		switch {
		case ctx.Err() != nil:
			res = tools.Result{Output: "Tool call was interrupted by the user.", IsError: true, Title: "interrupted"}
		case truncated:
			res = tools.Result{Output: "Your response was cut off by the output token limit, so this tool call's input is incomplete. Retry with smaller steps.", IsError: true, Title: "truncated"}
		case !ok:
			res = tools.Result{Output: fmt.Sprintf("Unknown tool %q. Available tools: %s", c.Name, toolNames(byName)), IsError: true, Title: "unknown tool"}
		default:
			if d := r.checkPermission(ctx, t, c); d == Deny {
				res = tools.Result{Output: "The user denied permission to run this tool. Ask how they would like to proceed.", IsError: true, Title: "denied"}
			} else {
				res = t.Run(ctx, r.Env, json.RawMessage(c.Input))
			}
		}
		if c.Name == "todowrite" && !res.IsError {
			r.emit(Event{Kind: EvTodos, Todos: append([]tools.Todo(nil), r.Env.Todos...)})
		}
		results[i] = llm.Message{Role: llm.RoleTool, ToolCallID: c.ID, ToolName: c.Name, ToolInput: c.Input,
			Content: res.Output, IsError: res.IsError, Meta: withTitle(res), Time: time.Now()}
		rr := res
		r.emit(Event{Kind: EvToolDone, ToolID: c.ID, ToolName: c.Name, ToolInput: c.Input, Result: &rr})
	}
	if allReadOnly && len(calls) > 1 {
		var wg sync.WaitGroup
		for i, c := range calls {
			wg.Add(1)
			go func(i int, c llm.ToolCall) { defer wg.Done(); exec(i, c) }(i, c)
		}
		wg.Wait()
	} else {
		for i, c := range calls {
			exec(i, c)
		}
	}
	r.Sess.Messages = append(r.Sess.Messages, results...)
}

func withTitle(res tools.Result) map[string]any {
	m := map[string]any{}
	for k, v := range res.Meta {
		m[k] = v
	}
	if res.Title != "" {
		m["title"] = res.Title
	}
	return m
}

func toolNames(m map[string]tools.Tool) string {
	var n []string
	for k := range m {
		n = append(n, k)
	}
	return strings.Join(n, ", ")
}

var safeBash = regexp.MustCompile(`^(ls|pwd|cat|head|tail|wc|echo|which|whoami|date|tree|file|stat|du|df|env|printenv|uname|` +
	`git (status|diff|log|show|branch|remote|rev-parse|blame|ls-files)|go (version|env|list|vet|doc)|` +
	`node (-v|--version)|npm (ls|list|view|-v)|python3? (-V|--version)|cargo (--version|tree)|rg|grep|find)( |$)`)

func (r *Runner) checkPermission(ctx context.Context, t tools.Tool, c llm.ToolCall) Decision {
	cat := t.Permission()
	if cat == tools.PermNone {
		return Allow
	}
	rule := r.App.Cfg.Permission[t.Name()]
	if rule == "" {
		rule = r.App.Cfg.Permission[cat]
	}
	if rule == "" {
		switch cat {
		case tools.PermWebFetch, tools.PermMCP:
			rule = "allow"
		default:
			rule = "ask"
		}
	}
	if cat == tools.PermBash {
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(c.Input), &in)
		if rule == "ask" && !strings.ContainsAny(in.Command, ";&|><`$\n") && safeBash.MatchString(strings.TrimSpace(in.Command)) {
			return Allow
		}
	}
	switch rule {
	case "allow":
		return Allow
	case "deny":
		return Deny
	}
	r.mu.Lock()
	always := r.always[cat] || r.AutoApprove
	r.mu.Unlock()
	if always {
		return Allow
	}
	if r.Events == nil {
		return Deny
	}
	req := &PermissionRequest{Tool: t.Name(), Category: cat, Input: c.Input, Reply: make(chan Decision, 1)}
	r.emit(Event{Kind: EvPermission, Perm: req, ToolID: c.ID, ToolName: t.Name()})
	select {
	case d := <-req.Reply:
		if d == AllowAlways {
			r.SetAlways(cat)
		}
		return d
	case <-ctx.Done():
		return Deny
	}
}

// ---- titles ----

func (r *Runner) startTitle(text, model string) {
	r.titleCh = make(chan string, 1)
	small := r.App.SmallModel(model)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pid, mid, err := llm.ParseRef(small)
		if err != nil {
			return
		}
		p, err := r.App.Provider(pid)
		if err != nil {
			return
		}
		if len(text) > 2000 {
			text = text[:2000]
		}
		resp, err := llm.Collect(ctx, p, llm.Request{Model: mid, MaxTokens: 1000, Effort: "low",
			Messages: []llm.Message{{Role: llm.RoleUser, Content: titlePrompt + text}}})
		if err != nil {
			return
		}
		t := strings.TrimSpace(strings.Trim(strings.TrimSpace(resp.Content), `"'#*`))
		if i := strings.IndexByte(t, '\n'); i > 0 {
			t = t[:i]
		}
		if len([]rune(t)) > 60 {
			t = string([]rune(t)[:60])
		}
		if t != "" {
			r.titleCh <- t
		}
	}()
}

func (r *Runner) applyTitle() {
	if r.titleCh == nil {
		return
	}
	select {
	case t := <-r.titleCh:
		r.Sess.Title = t
		r.titleCh = nil
		r.emit(Event{Kind: EvTitle, Text: t})
	default:
	}
}

// PendingTitle returns the channel of an in-flight title generation, if any.
func (r *Runner) PendingTitle() <-chan string {
	ch := r.titleCh
	r.titleCh = nil
	return ch
}

// FinishTitle waits briefly for a pending title (used after a run ends).
func (r *Runner) FinishTitle(wait time.Duration) {
	if r.titleCh == nil {
		return
	}
	select {
	case t := <-r.titleCh:
		r.Sess.Title = t
		r.titleCh = nil
		r.emit(Event{Kind: EvTitle, Text: t})
		r.save()
	case <-time.After(wait):
	}
}

// ---- compaction ----

// Compact replaces the conversation with a model-written summary.
func (r *Runner) Compact(ctx context.Context, model string, ag app.AgentDef) error {
	if len(r.Sess.Messages) < 2 {
		return nil
	}
	r.emit(Event{Kind: EvCompacting})
	pid, mid, err := llm.ParseRef(model)
	if err != nil {
		return err
	}
	p, err := r.App.Provider(pid)
	if err != nil {
		return err
	}
	msgs := append(append([]llm.Message(nil), r.Sess.Messages...), llm.Message{Role: llm.RoleUser, Content: compactPrompt})
	// a trailing assistant message with tool calls must be followed by results; drop dangling calls
	var defs []llm.ToolDef
	for _, t := range r.App.ToolsFor(ag) {
		defs = append(defs, tools.Def(t))
	}
	resp, err := llm.Collect(ctx, p, llm.Request{Model: mid, Messages: msgs, Tools: defs, MaxTokens: 16000, Effort: "medium",
		System: "You are a helpful assistant that writes precise summaries of coding sessions."})
	if err != nil {
		return fmt.Errorf("compaction failed: %w", err)
	}
	info := r.App.ModelInfo(model)
	r.Sess.Usage.Add(resp.Usage)
	r.Sess.Cost += info.Cost(resp.Usage)
	summary := llm.Message{Role: llm.RoleUser, Kind: "summary", Synthetic: true, Time: time.Now(),
		Content: "This session continues from an earlier conversation that was compacted. Summary:\n\n" + resp.Content}
	ack := llm.Message{Role: llm.RoleAssistant, Synthetic: true, Kind: "summary", Time: time.Now(),
		Content: "Understood — I have the context from the summary and will continue from there."}
	r.Sess.Messages = []llm.Message{summary, ack}
	r.Sess.Turns = nil
	r.Sess.LastCtx = resp.Usage.Output + 2000
	r.mu.Lock()
	if r.turn != nil {
		r.turn.MsgIndex = len(r.Sess.Messages)
	}
	r.mu.Unlock()
	r.save()
	r.emit(Event{Kind: EvCompacted, Text: resp.Content})
	r.emit(Event{Kind: EvUsage, Usage: r.Sess.Usage, Cost: r.Sess.Cost, LastCtx: r.Sess.LastCtx})
	return nil
}

// ---- sub-agent ----

func (r *Runner) subAgent(ctx context.Context, desc, prompt, model, effort string) (string, error) {
	ag, ok := r.App.Agent("general")
	if !ok {
		return "", fmt.Errorf("general agent missing")
	}
	if ag.Model != "" {
		model = ag.Model
	}
	sub := &session.Session{ID: session.NewID(), Title: desc, Cwd: r.Sess.Cwd, Parent: r.Sess.ID, Created: time.Now()}
	events := make(chan Event, 64)
	r.mu.Lock()
	always := map[string]bool{}
	for k, v := range r.always {
		always[k] = v
	}
	r.mu.Unlock()
	child := &Runner{App: r.App, Sess: sub, Env: tools.NewEnv(r.App.Cwd), Events: events, always: always, AutoApprove: r.AutoApprove}
	child.Sess.Title = desc // skip title generation
	go func() {
		defer close(events)
		_ = child.run(ctx, prompt, ag, model, effort)
	}()
	var lastErr error
	for ev := range events {
		switch ev.Kind {
		case EvPermission:
			r.emit(ev) // bubble permission prompts to the UI
		case EvError:
			lastErr = ev.Err
		}
	}
	r.Sess.Usage.Add(sub.Usage)
	r.Sess.Cost += sub.Cost
	for i := len(sub.Messages) - 1; i >= 0; i-- {
		if m := sub.Messages[i]; m.Role == llm.RoleAssistant && strings.TrimSpace(m.Content) != "" {
			return m.Content, nil
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "(the sub-agent returned no report)", nil
}
