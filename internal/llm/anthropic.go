package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Anthropic talks to the Claude Messages API through the official Go SDK.
type Anthropic struct {
	id     string
	client anthropic.Client
}

func NewAnthropic(id, apiKey, baseURL string) *Anthropic {
	opts := []option.RequestOption{option.WithMaxRetries(3)}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if baseURL != "" && baseURL != "https://api.anthropic.com" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Anthropic{id: id, client: anthropic.NewClient(opts...)}
}

func (a *Anthropic) ID() string { return a.id }

func (a *Anthropic) ListModels(ctx context.Context) ([]ModelInfo, error) {
	spec, _ := SpecByID("anthropic")
	out := append([]ModelInfo(nil), spec.Models...)
	for i := range out {
		out[i].Provider = a.id
	}
	return out, nil
}

// supportsFallbacks reports models that accept server-side refusal fallbacks.
func supportsFallbacks(model string) bool {
	switch model {
	case "claude-fable-5-1", "claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5":
		return true
	}
	return false
}

func (a *Anthropic) buildMessages(msgs []Message) []anthropic.BetaMessageParam {
	var out []anthropic.BetaMessageParam
	var pending []anthropic.BetaContentBlockParamUnion // user-side blocks being merged

	flush := func() {
		if len(pending) > 0 {
			out = append(out, anthropic.NewBetaUserMessage(pending...))
			pending = nil
		}
	}
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
			pending = append(pending, anthropic.NewBetaTextBlock(m.Content))
		case RoleTool:
			content := m.Content
			if content == "" {
				content = "(no output)"
			}
			pending = append(pending, anthropic.NewBetaToolResultBlock(m.ToolCallID, content, m.IsError))
		case RoleAssistant:
			flush()
			if m.RawProvider == "anthropic" && len(m.Raw) > 0 {
				var bm anthropic.BetaMessage
				if err := json.Unmarshal(m.Raw, &bm); err == nil && len(bm.Content) > 0 {
					out = append(out, bm.ToParam())
					continue
				}
			}
			var blocks []anthropic.BetaContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewBetaTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				var input any = map[string]any{}
				if tc.Input != "" {
					_ = json.Unmarshal([]byte(tc.Input), &input)
				}
				blocks = append(blocks, anthropic.NewBetaToolUseBlock(tc.ID, input, tc.Name))
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropic.NewBetaTextBlock("…"))
			}
			out = append(out, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: blocks})
		}
	}
	flush()
	return out
}

func (a *Anthropic) Stream(ctx context.Context, req Request) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		info := Lookup("anthropic", req.Model)
		maxTok := req.MaxTokens
		if maxTok == 0 {
			maxTok = 64000
		}
		if info.MaxOutput > 0 && maxTok > info.MaxOutput {
			maxTok = info.MaxOutput
		}
		params := anthropic.BetaMessageNewParams{
			Model:        anthropic.Model(req.Model),
			MaxTokens:    int64(maxTok),
			Messages:     a.buildMessages(req.Messages),
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}
		if req.System != "" {
			params.System = []anthropic.BetaTextBlockParam{{Text: req.System}}
		}
		for _, t := range req.Tools {
			tp := anthropic.BetaToolParam{
				Name:                t.Name,
				Description:         anthropic.String(t.Description),
				EagerInputStreaming: anthropic.Bool(true),
				InputSchema: anthropic.BetaToolInputSchemaParam{
					Properties: t.Schema["properties"],
				},
			}
			tp.InputSchema.Required = toStrings(t.Schema["required"])
			if tp.InputSchema.Properties == nil {
				tp.InputSchema.Properties = map[string]any{}
			}
			params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &tp})
		}
		if info.Thinking {
			params.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
				Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized,
			}}
		}
		if info.Effort && req.Effort != "" {
			params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(req.Effort)}
		}
		if supportsFallbacks(req.Model) && a.id == "anthropic" {
			params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
			params.Betas = append(params.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
		}

		stream := a.client.Beta.Messages.NewStreaming(ctx, params)
		msg := anthropic.BetaMessage{}
		for stream.Next() {
			ev := stream.Current()
			if err := msg.Accumulate(ev); err != nil {
				ch <- Event{Type: EvError, Err: err}
				return
			}
			switch e := ev.AsAny().(type) {
			case anthropic.BetaRawContentBlockStartEvent:
				if e.ContentBlock.Type == "tool_use" {
					ch <- Event{Type: EvToolStart, ToolCall: &ToolCall{ID: e.ContentBlock.ID, Name: e.ContentBlock.Name}}
				}
			case anthropic.BetaRawContentBlockDeltaEvent:
				switch d := e.Delta.AsAny().(type) {
				case anthropic.BetaTextDelta:
					ch <- Event{Type: EvText, Text: d.Text}
				case anthropic.BetaThinkingDelta:
					ch <- Event{Type: EvReasoning, Text: d.Thinking}
				}
			}
		}
		if err := stream.Err(); err != nil {
			ch <- Event{Type: EvError, Err: friendlyAnthropicErr(err)}
			return
		}

		resp := &Response{StopReason: string(msg.StopReason)}
		resp.Usage = Usage{
			Input:      msg.Usage.InputTokens,
			Output:     msg.Usage.OutputTokens,
			CacheRead:  msg.Usage.CacheReadInputTokens,
			CacheWrite: msg.Usage.CacheCreationInputTokens,
		}
		var text, reasoning strings.Builder
		for _, b := range msg.Content {
			switch v := b.AsAny().(type) {
			case anthropic.BetaTextBlock:
				text.WriteString(v.Text)
			case anthropic.BetaThinkingBlock:
				reasoning.WriteString(v.Thinking)
			case anthropic.BetaToolUseBlock:
				raw := v.JSON.Input.Raw()
				if raw == "" {
					raw = "{}"
				}
				resp.ToolCalls = append(resp.ToolCalls, ToolCall{ID: v.ID, Name: v.Name, Input: raw})
			}
		}
		resp.Content, resp.Reasoning = text.String(), reasoning.String()
		if raw, err := json.Marshal(msg); err == nil {
			resp.Raw = raw
		}
		if resp.StopReason == "refusal" && resp.Content == "" {
			resp.Content = "_The model declined to answer this request._"
		}
		ch <- Event{Type: EvDone, Response: resp}
	}()
	return ch
}

func friendlyAnthropicErr(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *anthropic.Error
	if ok := asErr(err, &apiErr); ok {
		switch apiErr.StatusCode {
		case 401:
			return fmt.Errorf("anthropic: invalid API key — run `solar auth login`")
		case 429:
			return fmt.Errorf("anthropic: rate limited, try again shortly")
		case 529:
			return fmt.Errorf("anthropic: API overloaded, try again shortly")
		}
	}
	return err
}

func toStrings(v any) []string {
	switch r := v.(type) {
	case []string:
		return r
	case []any:
		var out []string
		for _, x := range r {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Validate checks that the credentials work by listing models.
func (a *Anthropic) Validate(ctx context.Context) error {
	_, err := a.client.Models.List(ctx, anthropic.ModelListParams{Limit: anthropic.Int(1)})
	return friendlyAnthropicErr(err)
}
