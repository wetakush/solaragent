package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

func asErr[T error](err error, target *T) bool { return errors.As(err, target) }

// OpenAI is a client for any OpenAI-compatible /chat/completions endpoint
// (OpenAI, OpenRouter, Gemini, Groq, DeepSeek, xAI, Mistral, Ollama, ...).
type OpenAI struct {
	id      string
	apiKey  string
	baseURL string
	headers map[string]string
	models  []ModelInfo
	http    *http.Client
}

func NewOpenAI(id, apiKey, baseURL string, headers map[string]string, models []ModelInfo) *OpenAI {
	return &OpenAI{id: id, apiKey: apiKey, baseURL: strings.TrimRight(baseURL, "/"), headers: headers,
		models: models, http: &http.Client{Timeout: 0}}
}

func (o *OpenAI) ID() string { return o.id }

func (o *OpenAI) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, o.baseURL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	if o.id == "openrouter" {
		req.Header.Set("HTTP-Referer", "https://github.com/wetakush/solaragent")
		req.Header.Set("X-Title", "SolarAgent")
	}
	for k, v := range o.headers {
		req.Header.Set(k, v)
	}
	return o.http.Do(req)
}

func (o *OpenAI) ListModels(ctx context.Context) ([]ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	out := append([]ModelInfo(nil), o.models...)
	seen := map[string]bool{}
	for _, m := range out {
		seen[m.ID] = true
	}
	resp, err := o.do(ctx, "GET", "/models", nil)
	if err != nil {
		if len(out) > 0 {
			return out, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if len(out) > 0 {
			return out, nil
		}
		return nil, fmt.Errorf("%s: list models: HTTP %d", o.id, resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			DisplayName   string `json:"display_name"`
			ContextLength int    `json:"context_length"`
			Pricing       struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return out, nil
	}
	var fetched []ModelInfo
	for _, d := range body.Data {
		id := strings.TrimPrefix(d.ID, "models/")
		if seen[id] {
			continue
		}
		m := ModelInfo{ID: id, Name: d.Name, Provider: o.id, Context: 128_000, MaxOutput: 16_000}
		if m.Name == "" {
			m.Name = d.DisplayName
		}
		if m.Name == "" {
			m.Name = id
		}
		if d.ContextLength > 0 {
			m.Context = d.ContextLength
		}
		var p, c float64
		fmt.Sscan(d.Pricing.Prompt, &p)
		fmt.Sscan(d.Pricing.Completion, &c)
		m.InputPrice, m.OutputPrice = p*1e6, c*1e6
		fetched = append(fetched, m)
	}
	sort.Slice(fetched, func(i, j int) bool { return fetched[i].ID < fetched[j].ID })
	return append(out, fetched...), nil
}

type oaMsg struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func strp(s string) *string { return &s }

func (o *OpenAI) buildMessages(system string, msgs []Message) []oaMsg {
	var out []oaMsg
	if system != "" {
		out = append(out, oaMsg{Role: "system", Content: strp(system)})
	}
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Role == "user" && out[n-1].Content != nil {
				*out[n-1].Content += "\n\n" + m.Content
				continue
			}
			out = append(out, oaMsg{Role: "user", Content: strp(m.Content)})
		case RoleAssistant:
			am := oaMsg{Role: "assistant"}
			if m.Content != "" || len(m.ToolCalls) == 0 {
				am.Content = strp(m.Content)
			}
			for _, tc := range m.ToolCalls {
				c := oaToolCall{ID: tc.ID, Type: "function"}
				c.Function.Name, c.Function.Arguments = tc.Name, tc.Input
				if c.Function.Arguments == "" {
					c.Function.Arguments = "{}"
				}
				am.ToolCalls = append(am.ToolCalls, c)
			}
			out = append(out, am)
		case RoleTool:
			content := m.Content
			if content == "" {
				content = "(no output)"
			}
			if m.IsError {
				content = "ERROR: " + content
			}
			out = append(out, oaMsg{Role: "tool", ToolCallID: m.ToolCallID, Content: strp(content)})
		}
	}
	return out
}

func (o *OpenAI) Stream(ctx context.Context, req Request) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		body := map[string]any{
			"model":          req.Model,
			"messages":       o.buildMessages(req.System, req.Messages),
			"stream":         true,
			"stream_options": map[string]any{"include_usage": true},
		}
		if req.MaxTokens > 0 && o.id != "ollama" {
			body["max_tokens"] = req.MaxTokens
		}
		if len(req.Tools) > 0 {
			var tools []map[string]any
			for _, t := range req.Tools {
				tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
					"name": t.Name, "description": t.Description, "parameters": t.Schema,
				}})
			}
			body["tools"] = tools
		}
		if req.Effort != "" && (o.id == "openai" || o.id == "openrouter") && isReasoningModel(req.Model) {
			e := req.Effort
			if e == "xhigh" || e == "max" {
				e = "high"
			}
			body["reasoning_effort"] = e
		}
		resp, err := o.do(ctx, "POST", "/chat/completions", body)
		if err != nil {
			ch <- Event{Type: EvError, Err: err}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			ch <- Event{Type: EvError, Err: fmt.Errorf("%s: HTTP %d: %s", o.id, resp.StatusCode, extractErr(b))}
			return
		}

		out := &Response{}
		var text, reasoning strings.Builder
		calls := map[int]*ToolCall{}
		var order []int
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content          string       `json:"content"`
						ReasoningContent string       `json:"reasoning_content"`
						Reasoning        string       `json:"reasoning"`
						ToolCalls        []oaToolCall `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens        int64 `json:"prompt_tokens"`
					CompletionTokens    int64 `json:"completion_tokens"`
					PromptTokensDetails struct {
						CachedTokens int64 `json:"cached_tokens"`
					} `json:"prompt_tokens_details"`
				} `json:"usage"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(data), &chunk) != nil {
				continue
			}
			if chunk.Error != nil {
				ch <- Event{Type: EvError, Err: fmt.Errorf("%s: %s", o.id, chunk.Error.Message)}
				return
			}
			if chunk.Usage != nil {
				cached := chunk.Usage.PromptTokensDetails.CachedTokens
				out.Usage = Usage{Input: chunk.Usage.PromptTokens - cached, Output: chunk.Usage.CompletionTokens, CacheRead: cached}
			}
			for _, c := range chunk.Choices {
				if c.Delta.Content != "" {
					text.WriteString(c.Delta.Content)
					ch <- Event{Type: EvText, Text: c.Delta.Content}
				}
				r := c.Delta.ReasoningContent + c.Delta.Reasoning
				if r != "" {
					reasoning.WriteString(r)
					ch <- Event{Type: EvReasoning, Text: r}
				}
				for _, tc := range c.Delta.ToolCalls {
					cur, ok := calls[tc.Index]
					if !ok {
						cur = &ToolCall{ID: tc.ID}
						calls[tc.Index] = cur
						order = append(order, tc.Index)
					}
					if tc.ID != "" {
						cur.ID = tc.ID
					}
					if tc.Function.Name != "" {
						cur.Name += tc.Function.Name
						ch <- Event{Type: EvToolStart, ToolCall: &ToolCall{ID: cur.ID, Name: cur.Name}}
					}
					cur.Input += tc.Function.Arguments
				}
				if c.FinishReason != "" {
					out.StopReason = c.FinishReason
				}
			}
		}
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			ch <- Event{Type: EvError, Err: err}
			return
		}
		if ctx.Err() != nil {
			ch <- Event{Type: EvError, Err: ctx.Err()}
			return
		}
		for i, idx := range order {
			tc := calls[idx]
			if tc.ID == "" {
				tc.ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
			}
			if strings.TrimSpace(tc.Input) == "" {
				tc.Input = "{}"
			}
			out.ToolCalls = append(out.ToolCalls, *tc)
		}
		out.Content, out.Reasoning = text.String(), reasoning.String()
		ch <- Event{Type: EvDone, Response: out}
	}()
	return ch
}

func isReasoningModel(m string) bool {
	m = strings.ToLower(m)
	for _, p := range []string{"o1", "o3", "o4", "gpt-5", "openai/o", "openai/gpt-5"} {
		if strings.HasPrefix(m, p) {
			return true
		}
	}
	return false
}

func extractErr(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil {
		if e.Error.Message != "" {
			return e.Error.Message
		}
		if e.Message != "" {
			return e.Message
		}
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// Validate checks that the endpoint is reachable and accepts the key.
func (o *OpenAI) Validate(ctx context.Context) error {
	resp, err := o.do(ctx, "GET", "/models", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("the API key was rejected (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, extractErr(b))
	}
	return nil
}

// Probe sends a 1-token completion to confirm the key is accepted for inference.
// Only authentication failures are reported; other errors are ignored.
func (o *OpenAI) Probe(ctx context.Context, model string) error {
	resp, err := o.do(ctx, "POST", "/chat/completions", map[string]any{
		"model": model, "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("the API key was rejected: %s", extractErr(b))
	}
	return nil
}
