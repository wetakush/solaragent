// Package config loads SolarAgent settings from the global config directory
// and the project (solar.json / .solar/), plus stored credentials.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ProviderConfig struct {
	Kind    string            `json:"kind,omitempty"` // "openai" (default) or "anthropic"
	Name    string            `json:"name,omitempty"`
	BaseURL string            `json:"base_url,omitempty"`
	APIKey  string            `json:"api_key,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Models  []string          `json:"models,omitempty"`
	Disable bool              `json:"disable,omitempty"`
}

type MCPConfig struct {
	Type    string            `json:"type,omitempty"` // "local" (default) | "remote"
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
}

func (m MCPConfig) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

type AgentConfig struct {
	Description string          `json:"description,omitempty"`
	Prompt      string          `json:"prompt,omitempty"`
	Model       string          `json:"model,omitempty"`
	Tools       map[string]bool `json:"tools,omitempty"`
	Color       string          `json:"color,omitempty"`
	Disable     bool            `json:"disable,omitempty"`
}

type Config struct {
	Schema         string                    `json:"$schema,omitempty"`
	Model          string                    `json:"model,omitempty"`
	SmallModel     string                    `json:"small_model,omitempty"`
	Theme          string                    `json:"theme,omitempty"`
	Effort         string                    `json:"effort,omitempty"`
	Providers      map[string]ProviderConfig `json:"provider,omitempty"`
	MCP            map[string]MCPConfig      `json:"mcp,omitempty"`
	Agents         map[string]AgentConfig    `json:"agent,omitempty"`
	Permission     map[string]string         `json:"permission,omitempty"` // tool -> allow|ask|deny
	Instructions   []string                  `json:"instructions,omitempty"`
	AutoCompact    *bool                     `json:"autocompact,omitempty"`
	Username       string                    `json:"username,omitempty"`
	PermissionMode string                    `json:"permission_mode,omitempty"` // ask | accept-edits | bypass
	AutoUpdate     FlexString                `json:"autoupdate,omitempty"`      // auto (default) | notify | off (or true/false)
}

func (c *Config) AutoCompactEnabled() bool { return c.AutoCompact == nil || *c.AutoCompact }

func ConfigDir() string {
	if d := os.Getenv("SOLAR_CONFIG_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "solar")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "solar")
}

func DataDir() string {
	if d := os.Getenv("SOLAR_DATA_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "solar")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "solar")
}

func GlobalConfigPath() string { return filepath.Join(ConfigDir(), "solar.json") }

var envRe = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// stripJSONC removes // and /* */ comments and trailing commas.
func stripJSONC(b []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '/' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '*' {
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out = append(out, c)
	}
	return regexp.MustCompile(`,(\s*[}\]])`).ReplaceAll(out, []byte("$1"))
}

func loadFile(path string, into *Config) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	b = envRe.ReplaceAllFunc(b, func(m []byte) []byte {
		v := os.Getenv(string(envRe.FindSubmatch(m)[1]))
		j, _ := json.Marshal(v)
		return j[1 : len(j)-1]
	})
	var c Config
	if err := json.Unmarshal(stripJSONC(b), &c); err != nil {
		return &ParseError{Path: path, Err: err}
	}
	merge(into, &c)
	return nil
}

type ParseError struct {
	Path string
	Err  error
}

func (e *ParseError) Error() string { return e.Path + ": " + e.Err.Error() }

func merge(dst, src *Config) {
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.SmallModel != "" {
		dst.SmallModel = src.SmallModel
	}
	if src.Theme != "" {
		dst.Theme = src.Theme
	}
	if src.Effort != "" {
		dst.Effort = src.Effort
	}
	if src.AutoCompact != nil {
		dst.AutoCompact = src.AutoCompact
	}
	if src.Username != "" {
		dst.Username = src.Username
	}
	if src.PermissionMode != "" {
		dst.PermissionMode = src.PermissionMode
	}
	if src.AutoUpdate != "" {
		dst.AutoUpdate = src.AutoUpdate
	}
	if dst.Providers == nil {
		dst.Providers = map[string]ProviderConfig{}
	}
	for k, v := range src.Providers {
		dst.Providers[k] = v
	}
	if dst.MCP == nil {
		dst.MCP = map[string]MCPConfig{}
	}
	for k, v := range src.MCP {
		dst.MCP[k] = v
	}
	if dst.Agents == nil {
		dst.Agents = map[string]AgentConfig{}
	}
	for k, v := range src.Agents {
		dst.Agents[k] = v
	}
	if dst.Permission == nil {
		dst.Permission = map[string]string{}
	}
	for k, v := range src.Permission {
		dst.Permission[k] = v
	}
	dst.Instructions = append(dst.Instructions, src.Instructions...)
}

// FindProjectRoot walks up from dir looking for a .git directory.
func FindProjectRoot(dir string) string {
	d := dir
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			return dir
		}
		d = p
	}
}

// Load merges the global config with project config files found between the
// project root and cwd.
func Load(cwd string) (*Config, []error) {
	cfg := &Config{}
	var errs []error
	try := func(p string) {
		if err := loadFile(p, cfg); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	try(GlobalConfigPath())
	try(filepath.Join(ConfigDir(), "solar.jsonc"))
	if p := os.Getenv("SOLAR_CONFIG"); p != "" {
		try(p)
	}
	root := FindProjectRoot(cwd)
	var chain []string
	for d := cwd; ; d = filepath.Dir(d) {
		chain = append([]string{d}, chain...)
		if d == root || filepath.Dir(d) == d {
			break
		}
	}
	for _, d := range chain {
		try(filepath.Join(d, "solar.json"))
		try(filepath.Join(d, "solar.jsonc"))
		try(filepath.Join(d, ".solar", "solar.json"))
	}
	if cfg.Theme == "" {
		cfg.Theme = "solar"
	}
	if cfg.Effort == "" {
		cfg.Effort = "high"
	}
	return cfg, errs
}

// SaveGlobal updates a single top-level key in the global config file,
// preserving the rest of the file's content.
func SaveGlobal(key string, value any) error {
	path := GlobalConfigPath()
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(stripJSONC(b), &m)
	}
	m[key] = value
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ---- credentials ----

type Auth map[string]AuthEntry

type AuthEntry struct {
	Type string `json:"type"` // "api"
	Key  string `json:"key"`
}

func authPath() string { return filepath.Join(DataDir(), "auth.json") }

func LoadAuth() Auth {
	a := Auth{}
	if b, err := os.ReadFile(authPath()); err == nil {
		_ = json.Unmarshal(b, &a)
	}
	return a
}

func (a Auth) Save() error {
	if err := os.MkdirAll(DataDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(a, "", "  ")
	return os.WriteFile(authPath(), b, 0o600)
}

func AuthFile() string { return authPath() }

// MaskKey shows the first and last characters of a secret.
func MaskKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 10 {
		return strings.Repeat("•", len(k))
	}
	return k[:6] + strings.Repeat("•", 6) + k[len(k)-4:]
}

// SaveProvider adds or updates a provider entry in the global config.
func SaveProvider(id string, pc ProviderConfig) error {
	path := GlobalConfigPath()
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(stripJSONC(b), &m)
	}
	provs, _ := m["provider"].(map[string]any)
	if provs == nil {
		provs = map[string]any{}
	}
	entry, _ := provs[id].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	if pc.Name != "" {
		entry["name"] = pc.Name
	}
	if pc.BaseURL != "" {
		entry["base_url"] = pc.BaseURL
	}
	if pc.Kind != "" {
		entry["kind"] = pc.Kind
	}
	if len(pc.Models) > 0 {
		entry["models"] = pc.Models
	}
	provs[id] = entry
	return SaveGlobal("provider", provs)
}

// FlexString accepts a JSON string or boolean.
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	var bv bool
	if json.Unmarshal(b, &bv) == nil {
		if bv {
			*f = "auto"
		} else {
			*f = "off"
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*f = FlexString(s)
	return nil
}
