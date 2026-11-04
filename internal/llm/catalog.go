package llm

import (
	"fmt"
	"strings"
)

// ModelInfo describes a model. Prices are USD per million tokens; zero means unknown.
type ModelInfo struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Provider    string  `json:"provider"`
	Context     int     `json:"context"`
	MaxOutput   int     `json:"max_output"`
	InputPrice  float64 `json:"input_price"`
	OutputPrice float64 `json:"output_price"`
	CacheRead   float64 `json:"cache_read"`
	CacheWrite  float64 `json:"cache_write"`
	Effort      bool    `json:"effort"`
	Thinking    bool    `json:"thinking"`
}

func (m ModelInfo) Ref() string { return m.Provider + "/" + m.ID }

func (m ModelInfo) Cost(u Usage) float64 {
	cr := m.CacheRead
	if cr == 0 {
		cr = m.InputPrice / 10
	}
	cw := m.CacheWrite
	if cw == 0 {
		cw = m.InputPrice * 1.25
	}
	return (float64(u.Input)*m.InputPrice + float64(u.Output)*m.OutputPrice +
		float64(u.CacheRead)*cr + float64(u.CacheWrite)*cw) / 1e6
}

// ProviderSpec describes a built-in provider.
type ProviderSpec struct {
	ID      string
	Name    string
	Kind    string // "anthropic" | "openai"
	BaseURL string
	EnvKeys []string
	Local   bool // no key needed
	Models  []ModelInfo
	Docs    string
}

func claude(id, name string, ctx, out int, in, outp, cache float64) ModelInfo {
	return ModelInfo{ID: id, Name: name, Provider: "anthropic", Context: ctx, MaxOutput: out,
		InputPrice: in, OutputPrice: outp, CacheRead: cache, CacheWrite: in * 1.25,
		Effort: !strings.Contains(id, "haiku"), Thinking: !strings.Contains(id, "haiku")}
}

var Builtins = []ProviderSpec{
	{ID: "anthropic", Name: "Anthropic", Kind: "anthropic", BaseURL: "https://api.anthropic.com",
		EnvKeys: []string{"ANTHROPIC_API_KEY"}, Docs: "https://console.anthropic.com/settings/keys",
		Models: []ModelInfo{
			claude("claude-opus-5-5", "Claude Opus 5.5", 1_000_000, 128_000, 4, 20, 0.20),
			claude("claude-sonnet-5-5", "Claude Sonnet 5.5", 1_000_000, 128_000, 2, 10, 0.20),
			claude("claude-fable-5-1", "Claude Fable 5.1", 1_000_000, 128_000, 10, 50, 0.25),
			claude("claude-haiku-4-5", "Claude Haiku 4.5", 200_000, 64_000, 1, 5, 0.10),
		}},
	{ID: "niceai", Name: "NiceAI", Kind: "openai", BaseURL: "https://api.niceai.rest/v1",
		EnvKeys: []string{"NICEAI_API_KEY"}, Docs: "https://api.niceai.rest"},
	{ID: "openai", Name: "OpenAI", Kind: "openai", BaseURL: "https://api.openai.com/v1",
		EnvKeys: []string{"OPENAI_API_KEY"}, Docs: "https://platform.openai.com/api-keys"},
	{ID: "openrouter", Name: "OpenRouter", Kind: "openai", BaseURL: "https://openrouter.ai/api/v1",
		EnvKeys: []string{"OPENROUTER_API_KEY"}, Docs: "https://openrouter.ai/keys"},
	{ID: "google", Name: "Google Gemini", Kind: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		EnvKeys: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, Docs: "https://aistudio.google.com/apikey"},
	{ID: "groq", Name: "Groq", Kind: "openai", BaseURL: "https://api.groq.com/openai/v1",
		EnvKeys: []string{"GROQ_API_KEY"}, Docs: "https://console.groq.com/keys"},
	{ID: "deepseek", Name: "DeepSeek", Kind: "openai", BaseURL: "https://api.deepseek.com/v1",
		EnvKeys: []string{"DEEPSEEK_API_KEY"}, Docs: "https://platform.deepseek.com/api_keys"},
	{ID: "xai", Name: "xAI", Kind: "openai", BaseURL: "https://api.x.ai/v1",
		EnvKeys: []string{"XAI_API_KEY"}, Docs: "https://console.x.ai"},
	{ID: "mistral", Name: "Mistral", Kind: "openai", BaseURL: "https://api.mistral.ai/v1",
		EnvKeys: []string{"MISTRAL_API_KEY"}, Docs: "https://console.mistral.ai/api-keys"},
	{ID: "together", Name: "Together AI", Kind: "openai", BaseURL: "https://api.together.xyz/v1",
		EnvKeys: []string{"TOGETHER_API_KEY"}, Docs: "https://api.together.ai/settings/api-keys"},
	{ID: "ollama", Name: "Ollama (local)", Kind: "openai", BaseURL: "http://localhost:11434/v1", Local: true},
	{ID: "lmstudio", Name: "LM Studio (local)", Kind: "openai", BaseURL: "http://localhost:1234/v1", Local: true},
}

func SpecByID(id string) (ProviderSpec, bool) {
	for _, s := range Builtins {
		if s.ID == id {
			return s, true
		}
	}
	return ProviderSpec{}, false
}

// ParseRef splits "provider/model" (model may itself contain slashes).
func ParseRef(ref string) (provider, model string, err error) {
	i := strings.Index(ref, "/")
	if i <= 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("model must look like provider/model, got %q", ref)
	}
	return ref[:i], ref[i+1:], nil
}

// Lookup returns catalog info for a model, falling back to sane defaults.
func Lookup(provider, model string) ModelInfo {
	if s, ok := SpecByID(provider); ok {
		for _, m := range s.Models {
			if m.ID == model {
				return m
			}
		}
	}
	m := ModelInfo{ID: model, Name: model, Provider: provider, Context: 128_000, MaxOutput: 16_000}
	if provider == "anthropic" {
		m.Context, m.MaxOutput, m.Effort, m.Thinking = 200_000, 32_000, false, false
	}
	return m
}

// Build constructs a provider client directly (used for validating new credentials).
func Build(id, kind, apiKey, baseURL string, headers map[string]string) Provider {
	if kind == "anthropic" {
		return NewAnthropic(id, apiKey, baseURL)
	}
	return NewOpenAI(id, apiKey, baseURL, headers, nil)
}
