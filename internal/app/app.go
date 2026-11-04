// Package app wires configuration, providers, agents, MCP and sessions together.
package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wetakush/solaragent/internal/config"
	"github.com/wetakush/solaragent/internal/llm"
	"github.com/wetakush/solaragent/internal/mcp"
	"github.com/wetakush/solaragent/internal/session"
	"github.com/wetakush/solaragent/internal/tools"
)

// Version is set at build time via -ldflags "-X .../internal/app.Version=x.y.z".
var Version = "dev"
const Repo = "github.com/wetakush/solaragent"
const Author = "github.com/wetakush"

type App struct {
	Cwd      string
	Root     string
	Cfg      *config.Config
	CfgErrs  []error
	Auth     config.Auth
	Store    *session.Store
	MCP      *mcp.Manager
	Agents   []AgentDef
	Commands []Command

	mu        sync.Mutex
	providers map[string]llm.Provider
	modelsMu  sync.Mutex
	models    []llm.ModelInfo
}

func New(cwd string, onMCPChange func()) *App {
	cfg, errs := config.Load(cwd)
	a := &App{
		Cwd: cwd, Root: config.FindProjectRoot(cwd), Cfg: cfg, CfgErrs: errs,
		Auth: config.LoadAuth(), Store: session.NewStore(cwd), providers: map[string]llm.Provider{},
	}
	a.Agents = a.loadAgents()
	a.Commands = a.loadCommands()
	a.MCP = mcp.Start(cfg.MCP, onMCPChange)
	return a
}

func (a *App) Close() { a.MCP.Close() }

// ---- providers ----

func (a *App) apiKey(id string) string {
	if pc, ok := a.Cfg.Providers[id]; ok && pc.APIKey != "" {
		return pc.APIKey
	}
	if e, ok := a.Auth[id]; ok && e.Key != "" {
		return e.Key
	}
	if spec, ok := llm.SpecByID(id); ok {
		for _, k := range spec.EnvKeys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
	}
	return ""
}

// KeySource describes where a provider's credentials come from.
func (a *App) KeySource(id string) string {
	if pc, ok := a.Cfg.Providers[id]; ok && pc.APIKey != "" {
		return "config"
	}
	if e, ok := a.Auth[id]; ok && e.Key != "" {
		return "auth"
	}
	if spec, ok := llm.SpecByID(id); ok {
		for _, k := range spec.EnvKeys {
			if os.Getenv(k) != "" {
				return "env:" + k
			}
		}
	}
	return ""
}

func (a *App) Provider(id string) (llm.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, ok := a.providers[id]; ok {
		return p, nil
	}
	pc, hasCfg := a.Cfg.Providers[id]
	spec, isBuiltin := llm.SpecByID(id)
	if !isBuiltin && !hasCfg {
		return nil, fmt.Errorf("unknown provider %q (add it under \"provider\" in solar.json)", id)
	}
	if hasCfg && pc.Disable {
		return nil, fmt.Errorf("provider %q is disabled in config", id)
	}
	kind, base := spec.Kind, spec.BaseURL
	if pc.Kind != "" {
		kind = pc.Kind
	}
	if kind == "" {
		kind = "openai"
	}
	if pc.BaseURL != "" {
		base = pc.BaseURL
	}
	key := a.apiKey(id)
	if key == "" && !spec.Local && !hasCfg {
		return nil, fmt.Errorf("no API key for %s — run `solar auth login` or set %s", id, strings.Join(spec.EnvKeys, "/"))
	}
	var p llm.Provider
	if kind == "anthropic" {
		p = llm.NewAnthropic(id, key, base)
	} else {
		var models []llm.ModelInfo
		for _, m := range spec.Models {
			models = append(models, m)
		}
		for _, mid := range pc.Models {
			mi := llm.Lookup(id, mid)
			models = append(models, mi)
		}
		if base == "" {
			return nil, fmt.Errorf("provider %q needs a base_url", id)
		}
		p = llm.NewOpenAI(id, key, base, pc.Headers, models)
	}
	a.providers[id] = p
	return p, nil
}

// ConfiguredProviders returns providers that have credentials (or are local and reachable).
func (a *App) ConfiguredProviders() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range llm.Builtins {
		if pc, ok := a.Cfg.Providers[s.ID]; ok && pc.Disable {
			continue
		}
		if a.apiKey(s.ID) != "" {
			out = append(out, s.ID)
			seen[s.ID] = true
		} else if s.Local {
			if _, ok := a.Cfg.Providers[s.ID]; ok || reachable(s.BaseURL) {
				out = append(out, s.ID)
				seen[s.ID] = true
			}
		}
	}
	var custom []string
	for id, pc := range a.Cfg.Providers {
		if !seen[id] && !pc.Disable {
			if _, builtin := llm.SpecByID(id); !builtin || a.apiKey(id) != "" {
				custom = append(custom, id)
			}
		}
	}
	sort.Strings(custom)
	return append(out, custom...)
}

var reachCache sync.Map

func reachable(base string) bool {
	if v, ok := reachCache.Load(base); ok {
		return v.(bool)
	}
	c := http.Client{Timeout: 250 * time.Millisecond}
	resp, err := c.Get(base + "/models")
	ok := err == nil
	if resp != nil {
		resp.Body.Close()
	}
	reachCache.Store(base, ok)
	return ok
}

// DefaultModel picks the configured model or the best available one.
func (a *App) DefaultModel() string {
	if a.Cfg.Model != "" {
		return a.Cfg.Model
	}
	provs := a.ConfiguredProviders()
	for _, p := range provs {
		if p == "anthropic" {
			return "anthropic/claude-opus-5-5"
		}
	}
	prefs := map[string]string{
		"niceai":     "kimi-k3-1-pro",
		"openrouter": "anthropic/claude-opus-5-5",
		"google":     "gemini-2.5-pro",
		"deepseek":   "deepseek-chat",
		"openai":     "gpt-5",
		"xai":        "grok-4",
		"mistral":    "mistral-large-latest",
		"groq":       "llama-3.3-70b-versatile",
	}
	for _, p := range provs {
		if m, ok := prefs[p]; ok {
			return p + "/" + m
		}
		if pc, ok := a.Cfg.Providers[p]; ok && len(pc.Models) > 0 {
			return p + "/" + pc.Models[0]
		}
	}
	return "anthropic/claude-opus-5-5"
}

func (a *App) SmallModel(main string) string {
	if a.Cfg.SmallModel != "" {
		return a.Cfg.SmallModel
	}
	p, _, _ := llm.ParseRef(main)
	if p == "anthropic" {
		return "anthropic/claude-haiku-4-5"
	}
	return main
}

// ModelInfo returns catalog data for a "provider/model" ref.
func (a *App) ModelInfo(ref string) llm.ModelInfo {
	p, m, err := llm.ParseRef(ref)
	if err != nil {
		return llm.ModelInfo{ID: ref, Name: ref, Context: 128_000}
	}
	a.modelsMu.Lock()
	for _, mi := range a.models {
		if mi.Provider == p && mi.ID == m {
			a.modelsMu.Unlock()
			return mi
		}
	}
	a.modelsMu.Unlock()
	if p == "openrouter" && strings.HasPrefix(m, "anthropic/") {
		mi := llm.Lookup("anthropic", strings.TrimPrefix(m, "anthropic/"))
		mi.Provider, mi.ID, mi.Effort, mi.Thinking = p, m, false, false
		return mi
	}
	return llm.Lookup(p, m)
}

// ListModels gathers models from all configured providers concurrently.
func (a *App) ListModels(ctx context.Context) []llm.ModelInfo {
	provs := a.ConfiguredProviders()
	results := make([][]llm.ModelInfo, len(provs))
	var wg sync.WaitGroup
	for i, id := range provs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			p, err := a.Provider(id)
			if err != nil {
				return
			}
			ms, _ := p.ListModels(ctx)
			results[i] = ms
		}(i, id)
	}
	wg.Wait()
	var all []llm.ModelInfo
	for _, r := range results {
		all = append(all, r...)
	}
	a.modelsMu.Lock()
	a.models = all
	a.modelsMu.Unlock()
	return all
}

// ---- recent models ----

func recentPath() string { return filepath.Join(config.DataDir(), "recent_models") }

func RecentModels() []string {
	b, err := os.ReadFile(recentPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func PushRecentModel(ref string) {
	list := []string{ref}
	for _, r := range RecentModels() {
		if r != ref && len(list) < 8 {
			list = append(list, r)
		}
	}
	_ = os.MkdirAll(config.DataDir(), 0o755)
	_ = os.WriteFile(recentPath(), []byte(strings.Join(list, "\n")+"\n"), 0o644)
}

// ---- prompt helpers ----

var mentionRe = regexp.MustCompile(`(?:^|\s)@([^\s@]+)`)

// ExpandMentions appends the contents of @file mentions to the prompt.
func (a *App) ExpandMentions(text string) string {
	var extra strings.Builder
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		rel := strings.TrimRight(m[1], ".,;:!?)")
		if seen[rel] {
			continue
		}
		seen[rel] = true
		p := rel
		if !filepath.IsAbs(p) {
			p = filepath.Join(a.Cwd, p)
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			ents, _ := os.ReadDir(p)
			var names []string
			for _, e := range ents {
				n := e.Name()
				if e.IsDir() {
					n += "/"
				}
				names = append(names, n)
			}
			fmt.Fprintf(&extra, "\n\n<directory path=%q>\n%s\n</directory>", rel, strings.Join(names, "\n"))
			continue
		}
		if st.Size() > 256*1024 {
			fmt.Fprintf(&extra, "\n\n<file path=%q>(file too large to attach: %d bytes — use the read tool)</file>", rel, st.Size())
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fmt.Fprintf(&extra, "\n\n<file path=%q>\n%s\n</file>", rel, string(b))
	}
	if extra.Len() == 0 {
		return text
	}
	return text + "\n\nAttached context:" + extra.String()
}

// Instructions collects AGENTS.md / CLAUDE.md style files.
func (a *App) Instructions() string {
	var parts []string
	seen := map[string]bool{}
	add := func(p string) {
		abs, _ := filepath.Abs(p)
		if seen[abs] {
			return
		}
		b, err := os.ReadFile(p)
		if err != nil || len(strings.TrimSpace(string(b))) == 0 {
			return
		}
		seen[abs] = true
		parts = append(parts, fmt.Sprintf("<instructions source=%q>\n%s\n</instructions>", p, strings.TrimSpace(string(b))))
	}
	add(filepath.Join(config.ConfigDir(), "AGENTS.md"))
	for d := a.Cwd; ; d = filepath.Dir(d) {
		found := false
		for _, n := range []string{"AGENTS.md", "SOLAR.md", "CLAUDE.md"} {
			if _, err := os.Stat(filepath.Join(d, n)); err == nil {
				add(filepath.Join(d, n))
				found = true
				break
			}
		}
		_ = found
		if d == a.Root || filepath.Dir(d) == d {
			break
		}
	}
	for _, ins := range a.Cfg.Instructions {
		p := ins
		if !filepath.IsAbs(p) {
			p = filepath.Join(a.Root, p)
		}
		matches, _ := filepath.Glob(p)
		for _, m := range matches {
			add(m)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ToolsFor returns tools available to an agent.
func (a *App) ToolsFor(ag AgentDef) []tools.Tool {
	var out []tools.Tool
	all := append(tools.Builtins(), a.MCP.Tools()...)
	for _, t := range all {
		if ag.Allows(t.Name()) {
			out = append(out, t)
		}
	}
	return out
}

// SetKey stores an API key for a provider and resets its cached client.
func (a *App) SetKey(id, key string) error {
	a.Auth[id] = config.AuthEntry{Type: "api", Key: strings.TrimSpace(key)}
	if err := a.Auth.Save(); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.providers, id)
	a.mu.Unlock()
	return nil
}

// Reload re-reads configuration and credentials (e.g. after the setup wizard).
func (a *App) Reload() {
	cfg, errs := config.Load(a.Cwd)
	a.mu.Lock()
	a.Cfg, a.CfgErrs = cfg, errs
	a.Auth = config.LoadAuth()
	a.providers = map[string]llm.Provider{}
	a.mu.Unlock()
	a.Agents = a.loadAgents()
	a.Commands = a.loadCommands()
}
