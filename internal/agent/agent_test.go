package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wetakush/solaragent/internal/app"
	"github.com/wetakush/solaragent/internal/llm"
)

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprint(w, e)
	}
}

func anthropicEv(typ, data string) string { return "event: " + typ + "\ndata: " + data + "\n\n" }

// mockAnthropic scripts: first call -> tool_use(read main.go); second call -> final text.
func mockAnthropic(t *testing.T, bodies *[]string, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		if strings.Contains(body, "Generate a short title") {
			sse(w,
				anthropicEv("message_start", `{"type":"message_start","message":{"id":"m0","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":1}}}`),
				anthropicEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
				anthropicEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Reading main file"}}`),
				anthropicEv("content_block_stop", `{"type":"content_block_stop","index":0}`),
				anthropicEv("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`),
				anthropicEv("message_stop", `{"type":"message_stop"}`))
			return
		}
		mu.Lock()
		*bodies = append(*bodies, body)
		n := len(*bodies)
		mu.Unlock()
		if n == 1 {
			sse(w,
				anthropicEv("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":100,"output_tokens":1,"cache_read_input_tokens":50}}}`),
				anthropicEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`),
				anthropicEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"I should read the file."}}`),
				anthropicEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`),
				anthropicEv("content_block_stop", `{"type":"content_block_stop","index":0}`),
				anthropicEv("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
				anthropicEv("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Let me read it."}}`),
				anthropicEv("content_block_stop", `{"type":"content_block_stop","index":1}`),
				anthropicEv("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`),
				anthropicEv("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"file_pa"}}`),
				anthropicEv("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"th\": \"main.go\"}"}}`),
				anthropicEv("content_block_stop", `{"type":"content_block_stop","index":2}`),
				anthropicEv("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}`),
				anthropicEv("message_stop", `{"type":"message_stop"}`))
			return
		}
		sse(w,
			anthropicEv("message_start", `{"type":"message_start","message":{"id":"m2","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":200,"output_tokens":1}}}`),
			anthropicEv("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
			anthropicEv("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"It prints **hi**."}}`),
			anthropicEv("content_block_stop", `{"type":"content_block_stop","index":0}`),
			anthropicEv("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`),
			anthropicEv("message_stop", `{"type":"message_stop"}`))
	}))
}

func mockOpenAI(t *testing.T, bodies *[]string, mu *sync.Mutex) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		if strings.Contains(body, "Generate a short title") {
			sse(w, `data: {"choices":[{"delta":{"content":"Edit the file"}}]}`+"\n\n", "data: [DONE]\n\n")
			return
		}
		mu.Lock()
		*bodies = append(*bodies, body)
		n := len(*bodies)
		mu.Unlock()
		if n == 1 {
			sse(w,
				`data: {"choices":[{"delta":{"reasoning_content":"plan edit"}}]}`+"\n\n",
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"edit","arguments":""}}]}}]}`+"\n\n",
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"file_path\":\"main.go\",\"old_string\":\"hi\","}}]}}]}`+"\n\n",
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"new_string\":\"hello\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n",
				`data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":15}}`+"\n\n",
				"data: [DONE]\n\n")
			return
		}
		sse(w, `data: {"choices":[{"delta":{"content":"Done."},"finish_reason":"stop"}]}`+"\n\n", "data: [DONE]\n\n")
	}))
}

func setup(t *testing.T, cfg string) string {
	dir := t.TempDir()
	t.Setenv("SOLAR_CONFIG_DIR", filepath.Join(dir, "cfg"))
	t.Setenv("SOLAR_DATA_DIR", filepath.Join(dir, "data"))
	proj := filepath.Join(dir, "proj")
	_ = os.MkdirAll(filepath.Join(dir, "cfg"), 0o755)
	_ = os.MkdirAll(proj, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "cfg", "solar.json"), []byte(cfg), 0o644)
	_ = os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o644)
	return proj
}

func collect(t *testing.T, a *app.App, model, prompt string) (*Runner, []Event) {
	events := make(chan Event, 256)
	sess := a.Store.New(a.Cwd)
	r := NewRunner(a, sess, events)
	r.AutoApprove = true
	ag, _ := a.Agent("build")
	go func() {
		defer close(events)
		_ = r.Run(context.Background(), prompt, ag, model, "high")
	}()
	var evs []Event
	for e := range events {
		evs = append(evs, e)
	}
	return r, evs
}

func TestAnthropicToolLoop(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	srv := mockAnthropic(t, &bodies, &mu)
	defer srv.Close()
	proj := setup(t, fmt.Sprintf(`{"provider":{"anthropic":{"base_url":%q,"api_key":"test"}}}`, srv.URL))
	a := app.New(proj, nil)
	defer a.Close()

	r, evs := collect(t, a, "anthropic/claude-opus-5-5", "what does main.go print?")
	for _, e := range evs {
		if e.Kind == EvError {
			t.Fatalf("error event: %v", e.Err)
		}
	}
	msgs := r.Sess.Messages
	if len(msgs) != 4 {
		t.Fatalf("want 4 messages (user, assistant+tool, tool result, assistant), got %d", len(msgs))
	}
	if msgs[1].Reasoning != "I should read the file." || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "read" {
		t.Fatalf("bad assistant message: %+v", msgs[1])
	}
	if !strings.Contains(msgs[2].Content, `println("hi")`) {
		t.Fatalf("tool result missing file content: %q", msgs[2].Content)
	}
	if msgs[3].Content != "It prints **hi**." {
		t.Fatalf("final = %q", msgs[3].Content)
	}
	// second request must replay thinking (with signature), tool_use and tool_result
	second := bodies[1]
	for _, want := range []string{`"signature":"sig123"`, `"tool_use_id":"toolu_1"`, `"type":"tool_result"`, `"eager_input_streaming":true`, `"fallbacks":"default"`, `"type":"adaptive"`} {
		if !strings.Contains(second, want) {
			t.Errorf("second request missing %s\n%s", want, second)
		}
	}
	if r.Sess.Usage.Input != 300 || r.Sess.Usage.CacheRead != 50 {
		t.Errorf("usage = %+v", r.Sess.Usage)
	}
	if r.Sess.Cost <= 0 {
		t.Errorf("expected positive cost")
	}
	r.FinishTitle(2e9)
	if r.Sess.Title != "Reading main file" {
		t.Errorf("title = %q", r.Sess.Title)
	}
}

func TestOpenAIEditAndUndo(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	srv := mockOpenAI(t, &bodies, &mu)
	defer srv.Close()
	proj := setup(t, fmt.Sprintf(`{"provider":{"mock":{"base_url":%q,"api_key":"x","models":["m1"]}}}`, srv.URL+"/v1"))
	a := app.New(proj, nil)
	defer a.Close()

	r, evs := collect(t, a, "mock/m1", "change hi to hello")
	for _, e := range evs {
		if e.Kind == EvError {
			t.Fatalf("error event: %v", e.Err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(proj, "main.go"))
	if !strings.Contains(string(b), `println("hello")`) {
		t.Fatalf("edit not applied:\n%s", b)
	}
	var second map[string]any
	_ = json.Unmarshal([]byte(bodies[1]), &second)
	msgs := second["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "call_1" {
		t.Fatalf("expected tool message last, got %v", last)
	}
	if r.Sess.Messages[len(r.Sess.Messages)-1].Content != "Done." {
		t.Fatalf("final message wrong")
	}
	if mf := r.Sess.ModifiedFiles(); len(mf) != 1 || mf[0].Added != 1 || mf[0].Removed != 1 {
		t.Fatalf("modified files = %+v", mf)
	}
	// undo restores the file
	text, files, ok := r.Sess.Undo()
	if !ok || text != "change hi to hello" || len(files) != 1 {
		t.Fatalf("undo failed: %v %q %v", ok, text, files)
	}
	b, _ = os.ReadFile(filepath.Join(proj, "main.go"))
	if !strings.Contains(string(b), `println("hi")`) {
		t.Fatalf("undo did not restore file:\n%s", b)
	}
	if len(r.Sess.Messages) != 0 {
		t.Fatalf("undo should truncate messages")
	}
	// session persisted & listable
	if err := a.Store.Save(r.Sess); err != nil {
		t.Fatal(err)
	}
	_ = llm.RoleUser
}

func TestRewindAndFork(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	srv := mockOpenAI(t, &bodies, &mu)
	defer srv.Close()
	proj := setup(t, fmt.Sprintf(`{"provider":{"mock":{"base_url":%q,"api_key":"x","models":["m1"]}}}`, srv.URL+"/v1"))
	a := app.New(proj, nil)
	defer a.Close()
	r, _ := collect(t, a, "mock/m1", "change hi to hello")
	main := filepath.Join(proj, "main.go")
	if b, _ := os.ReadFile(main); !strings.Contains(string(b), "hello") {
		t.Fatal("edit not applied")
	}
	n := len(r.Sess.Messages)

	f := r.Sess.Fork(-1)
	if f.ID == r.Sess.ID || len(f.Messages) != n || f.ForkedFrom != r.Sess.ID || len(f.Turns) != 1 {
		t.Fatalf("bad fork: %+v", f)
	}
	f2 := r.Sess.Fork(0)
	if len(f2.Messages) != 0 || len(f2.Turns) != 0 {
		t.Fatal("fork(0) should be empty")
	}

	turns := r.Sess.UserTurns()
	if len(turns) != 1 || turns[0].Text != "change hi to hello" || turns[0].FilesChanged != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	// chat-only rewind keeps the file
	c := r.Sess.Fork(-1)
	if text, files := c.RewindTo(0, false); text != "change hi to hello" || len(files) != 0 || len(c.Messages) != 0 {
		t.Fatalf("chat-only rewind: %q %v", text, files)
	}
	if b, _ := os.ReadFile(main); !strings.Contains(string(b), "hello") {
		t.Fatal("chat-only rewind must not touch files")
	}
	// full rewind restores the file; the fork is untouched
	if _, files := r.Sess.RewindTo(0, true); len(files) != 1 {
		t.Fatal("expected file restore")
	}
	if b, _ := os.ReadFile(main); strings.Contains(string(b), "hello") {
		t.Fatal("file not restored")
	}
	if len(f.Messages) != n {
		t.Fatal("fork must not change when the original is rewound")
	}
}
