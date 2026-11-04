// Package mcp is a minimal Model Context Protocol client supporting local
// (stdio) and remote (streamable HTTP) servers, exposing their tools.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wetakush/solaragent/internal/config"
	"github.com/wetakush/solaragent/internal/tools"
)

const protocolVersion = "2025-06-18"

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type transport interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(method string, params any) error
	close()
}

// ---- stdio ----

type stdio struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	pending map[int64]chan rpcMsg
	nextID  atomic.Int64
	done    chan struct{}
	stderr  bytes.Buffer
}

func newStdio(cfg config.MCPConfig) (*stdio, error) {
	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("no command configured")
	}
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s := &stdio{cmd: cmd, stdin: in, pending: map[int64]chan rpcMsg{}, done: make(chan struct{})}
	cmd.Stderr = &limitedWriter{buf: &s.stderr, max: 8192}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			var m rpcMsg
			if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil || m.Method != "" {
				continue
			}
			s.mu.Lock()
			ch := s.pending[*m.ID]
			delete(s.pending, *m.ID)
			s.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
		close(s.done)
	}()
	return s, nil
}

type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.buf.Len() < l.max {
		l.buf.Write(p)
	}
	return len(p), nil
}

func (s *stdio) send(m rpcMsg) error {
	b, _ := json.Marshal(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stdin.Write(append(b, '\n'))
	return err
}

func (s *stdio) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	ch := make(chan rpcMsg, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	if err := s.send(rpcMsg{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("%s", m.Error.Message)
		}
		return m.Result, nil
	case <-s.done:
		msg := strings.TrimSpace(s.stderr.String())
		if msg == "" {
			msg = "server exited"
		}
		return nil, fmt.Errorf("%s", lastLine(msg))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *stdio) notify(method string, params any) error {
	return s.send(rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *stdio) close() {
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func lastLine(s string) string {
	ls := strings.Split(s, "\n")
	return ls[len(ls)-1]
}

// ---- streamable HTTP ----

type httpT struct {
	url     string
	headers map[string]string
	session string
	nextID  atomic.Int64
	client  *http.Client
}

func (h *httpT) post(ctx context.Context, m rpcMsg) (*http.Response, error) {
	b, _ := json.Marshal(m)
	req, err := http.NewRequestWithContext(ctx, "POST", h.url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.client.Do(req)
}

func (h *httpT) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := h.nextID.Add(1)
	resp, err := h.post(ctx, rpcMsg{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		h.session = s
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var m rpcMsg
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		found := false
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var x rpcMsg
			if json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &x) == nil && x.ID != nil && *x.ID == id {
				m, found = x, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("no response in event stream")
		}
	} else if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	if m.Error != nil {
		return nil, fmt.Errorf("%s", m.Error.Message)
	}
	return m.Result, nil
}

func (h *httpT) notify(method string, params any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := h.post(ctx, rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func (h *httpT) close() {}

// ---- client & manager ----

type Status struct {
	Name   string
	State  string // connecting | connected | failed | disabled
	Error  string
	Tools  int
	Remote bool
}

type Client struct {
	Name   string
	cfg    config.MCPConfig
	t      transport
	tools  []tools.Tool
	status Status
}

type mcpTool struct {
	client      *Client
	name        string // namespaced
	remoteName  string
	description string
	schema      map[string]any
}

func (t *mcpTool) Name() string           { return t.name }
func (t *mcpTool) Description() string    { return t.description }
func (t *mcpTool) Schema() map[string]any { return t.schema }
func (t *mcpTool) Permission() string     { return tools.PermMCP }

func (t *mcpTool) Run(ctx context.Context, env *tools.Env, input json.RawMessage) tools.Result {
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return tools.Result{Output: "invalid input JSON: " + err.Error(), IsError: true}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	raw, err := t.client.t.call(ctx, "tools/call", map[string]any{"name": t.remoteName, "arguments": args})
	if err != nil {
		return tools.Result{Output: err.Error(), IsError: true, Title: t.remoteName}
	}
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Resource *struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		StructuredContent any  `json:"structuredContent"`
		IsError           bool `json:"isError"`
	}
	_ = json.Unmarshal(raw, &res)
	var sb strings.Builder
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			sb.WriteString(c.Text + "\n")
		case "resource":
			if c.Resource != nil {
				sb.WriteString(c.Resource.URI + "\n" + c.Resource.Text + "\n")
			}
		default:
			fmt.Fprintf(&sb, "[%s content: %s]\n", c.Type, c.MimeType)
		}
	}
	if sb.Len() == 0 && res.StructuredContent != nil {
		b, _ := json.MarshalIndent(res.StructuredContent, "", "  ")
		sb.Write(b)
	}
	out := strings.TrimSpace(sb.String())
	if len(out) > 40000 {
		out = out[:40000] + "\n… (truncated)"
	}
	return tools.Result{Output: out, IsError: res.IsError, Title: t.client.Name + " · " + t.remoteName}
}

func (c *Client) connect(ctx context.Context) error {
	var err error
	if c.cfg.Type == "remote" || (c.cfg.URL != "" && len(c.cfg.Command) == 0) {
		c.status.Remote = true
		c.t = &httpT{url: c.cfg.URL, headers: c.cfg.Headers, client: &http.Client{Timeout: 5 * time.Minute}}
	} else {
		c.t, err = newStdio(c.cfg)
		if err != nil {
			return err
		}
	}
	_, err = c.t.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "solaragent", "version": "1.0.0"},
	})
	if err != nil {
		return err
	}
	_ = c.t.notify("notifications/initialized", map[string]any{})
	raw, err := c.t.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	var lst struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &lst); err != nil {
		return err
	}
	for _, t := range lst.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		c.tools = append(c.tools, &mcpTool{client: c, name: sanitize(c.Name + "_" + t.Name), remoteName: t.Name,
			description: t.Description, schema: schema})
	}
	return nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
	order   []string
}

// Start connects to all configured servers concurrently. onChange is called
// whenever a server's status changes.
func Start(cfgs map[string]config.MCPConfig, onChange func()) *Manager {
	m := &Manager{clients: map[string]*Client{}}
	for name := range cfgs {
		m.order = append(m.order, name)
	}
	sort.Strings(m.order)
	for _, name := range m.order {
		c := &Client{Name: name, cfg: cfgs[name], status: Status{Name: name, State: "connecting"}}
		m.clients[name] = c
		if !c.cfg.IsEnabled() {
			c.status.State = "disabled"
			continue
		}
		go func(c *Client) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := c.connect(ctx)
			m.mu.Lock()
			if err != nil {
				c.status.State, c.status.Error = "failed", err.Error()
				if c.t != nil {
					c.t.close()
				}
			} else {
				c.status.State, c.status.Tools = "connected", len(c.tools)
			}
			m.mu.Unlock()
			if onChange != nil {
				onChange()
			}
		}(c)
	}
	return m
}

func (m *Manager) Tools() []tools.Tool {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []tools.Tool
	for _, n := range m.order {
		if c := m.clients[n]; c.status.State == "connected" {
			out = append(out, c.tools...)
		}
	}
	return out
}

func (m *Manager) Statuses() []Status {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Status
	for _, n := range m.order {
		out = append(out, m.clients[n].status)
	}
	return out
}

// WaitReady blocks until no server is still connecting or the timeout passes.
func (m *Manager) WaitReady(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		busy := false
		for _, s := range m.Statuses() {
			if s.State == "connecting" {
				busy = true
			}
		}
		if !busy {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		if c.t != nil {
			c.t.close()
		}
	}
}
