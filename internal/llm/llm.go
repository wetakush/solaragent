// Package llm defines a provider-neutral chat model interface used by the
// agent loop, plus concrete providers (Anthropic native, OpenAI-compatible).
package llm

import (
	"context"
	"encoding/json"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a model's request to run a tool. Input is the raw JSON object.
type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

// Message is one entry of a conversation. Assistant messages may carry the
// provider-native content (Raw) so it can be replayed verbatim (thinking
// blocks, signatures) when talking to the same provider again.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Display    string     `json:"display,omitempty"` // what the user typed, if Content was expanded
	Reasoning  string     `json:"reasoning,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
	ToolInput  string     `json:"tool_input,omitempty"`
	IsError    bool       `json:"is_error,omitempty"`

	Meta      map[string]any `json:"meta,omitempty"`
	Model     string         `json:"model,omitempty"`
	Agent     string         `json:"agent,omitempty"`
	Time      time.Time      `json:"time"`
	ElapsedMs int64          `json:"elapsed_ms,omitempty"`
	Synthetic bool           `json:"synthetic,omitempty"`
	Kind      string         `json:"kind,omitempty"` // "", "shell", "summary", "command"

	Raw         json.RawMessage `json:"raw,omitempty"`
	RawProvider string          `json:"raw_provider,omitempty"`
}

type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any
}

type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	Effort    string // low|medium|high|xhigh|max ("" = provider default)
}

type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// ContextTokens is the size of the prompt that was sent.
func (u Usage) ContextTokens() int64 { return u.Input + u.CacheRead + u.CacheWrite }

func (u *Usage) Add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
}

type Response struct {
	Content    string
	Reasoning  string
	ToolCalls  []ToolCall
	StopReason string
	Usage      Usage
	Raw        json.RawMessage
}

type EventType int

const (
	EvText EventType = iota
	EvReasoning
	EvToolStart
	EvDone
	EvError
)

type Event struct {
	Type     EventType
	Text     string
	ToolCall *ToolCall
	Response *Response
	Err      error
}

type Provider interface {
	ID() string
	Stream(ctx context.Context, req Request) <-chan Event
	ListModels(ctx context.Context) ([]ModelInfo, error)
	Validate(ctx context.Context) error
}

// Collect runs a request and returns the final text (no tools).
func Collect(ctx context.Context, p Provider, req Request) (*Response, error) {
	var resp *Response
	for ev := range p.Stream(ctx, req) {
		switch ev.Type {
		case EvError:
			return nil, ev.Err
		case EvDone:
			resp = ev.Response
		}
	}
	if resp == nil {
		return nil, context.Canceled
	}
	return resp, nil
}
