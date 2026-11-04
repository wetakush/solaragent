// Package session persists conversations per project directory.
package session

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wetakush/solaragent/internal/config"
	"github.com/wetakush/solaragent/internal/llm"
	"github.com/wetakush/solaragent/internal/tools"
)

// FileSnap records a file's contents before the agent first touched it in a turn.
type FileSnap struct {
	Path    string  `json:"path"`
	Content *string `json:"content"` // nil = did not exist
}

type Turn struct {
	MsgIndex int        `json:"msg_index"` // index of the user message that started the turn
	Files    []FileSnap `json:"files,omitempty"`
}

type Session struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Cwd      string        `json:"cwd"`
	Created  time.Time     `json:"created"`
	Updated  time.Time     `json:"updated"`
	Model    string        `json:"model,omitempty"`
	Agent    string        `json:"agent,omitempty"`
	Messages []llm.Message `json:"messages"`
	Usage    llm.Usage     `json:"usage"`
	Cost     float64       `json:"cost"`
	LastCtx  int64         `json:"last_ctx"` // prompt size of the most recent request
	Todos    []tools.Todo  `json:"todos,omitempty"`
	Turns    []Turn        `json:"turns,omitempty"`
	Parent   string        `json:"parent,omitempty"`
}

func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return time.Now().Format("060102") + "-" + hex.EncodeToString(b)
}

type Store struct{ dir string }

func NewStore(cwd string) *Store {
	h := sha1.Sum([]byte(cwd))
	name := filepath.Base(cwd) + "-" + hex.EncodeToString(h[:4])
	return &Store{dir: filepath.Join(config.DataDir(), "projects", name, "sessions")}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) New(cwd string) *Session {
	now := time.Now()
	return &Session{ID: NewID(), Title: "New session", Cwd: cwd, Created: now, Updated: now}
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) Save(sess *Session) error {
	if len(sess.Messages) == 0 {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	sess.Updated = time.Now()
	b, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	tmp := s.path(sess.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(sess.ID))
}

func (s *Store) Load(id string) (*Session, error) {
	p := s.path(id)
	if _, err := os.Stat(p); err != nil {
		// allow unique prefixes
		list, _ := s.List()
		var match []*Session
		for _, x := range list {
			if strings.HasPrefix(x.ID, id) {
				match = append(match, x)
			}
		}
		if len(match) != 1 {
			return nil, fmt.Errorf("session %q not found", id)
		}
		p = s.path(match[0].ID)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(b, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) Delete(id string) error { return os.Remove(s.path(id)) }

// List returns sessions (without messages trimmed) newest first.
func (s *Store) List() ([]*Session, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Session
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var sess Session
		if json.Unmarshal(b, &sess) != nil || sess.Parent != "" {
			continue
		}
		out = append(out, &sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

func (s *Store) Latest() *Session {
	l, _ := s.List()
	if len(l) == 0 {
		return nil
	}
	return l[0]
}

// Undo reverts the last turn: restores touched files and truncates messages.
// It returns the text of the user message that started the undone turn.
func (sess *Session) Undo() (string, []string, bool) {
	if len(sess.Turns) == 0 {
		return "", nil, false
	}
	t := sess.Turns[len(sess.Turns)-1]
	sess.Turns = sess.Turns[:len(sess.Turns)-1]
	var restored []string
	for i := len(t.Files) - 1; i >= 0; i-- {
		f := t.Files[i]
		if f.Content == nil {
			_ = os.Remove(f.Path)
		} else {
			_ = os.MkdirAll(filepath.Dir(f.Path), 0o755)
			_ = os.WriteFile(f.Path, []byte(*f.Content), 0o644)
		}
		restored = append(restored, f.Path)
	}
	text := ""
	if t.MsgIndex < len(sess.Messages) {
		if m := sess.Messages[t.MsgIndex]; m.Role == llm.RoleUser {
			text = m.Content
			if m.Display != "" {
				text = m.Display
			}
		}
		sess.Messages = sess.Messages[:t.MsgIndex]
	}
	return text, restored, true
}

// ModifiedFiles lists files changed during the session with +/- counts.
type FileChange struct {
	Path    string
	Added   int
	Removed int
}

func (sess *Session) ModifiedFiles() []FileChange {
	idx := map[string]int{}
	var out []FileChange
	for _, m := range sess.Messages {
		if m.Role != llm.RoleTool || m.IsError || m.Meta == nil {
			continue
		}
		if _, ok := m.Meta["diff"]; !ok {
			continue
		}
		p, _ := m.Meta["path"].(string)
		a, _ := toInt(m.Meta["added"])
		r, _ := toInt(m.Meta["removed"])
		if i, ok := idx[p]; ok {
			out[i].Added += a
			out[i].Removed += r
		} else {
			idx[p] = len(out)
			out = append(out, FileChange{p, a, r})
		}
	}
	return out
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

// Markdown exports the conversation as a Markdown document.
func (sess *Session) Markdown() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", sess.Title)
	fmt.Fprintf(&sb, "_Exported from [SolarAgent](https://github.com/wetakush/solaragent) · %s · %s_\n\n",
		sess.Created.Format("2006-01-02 15:04"), sess.Model)
	for _, m := range sess.Messages {
		switch m.Role {
		case llm.RoleUser:
			fmt.Fprintf(&sb, "## 👤 User\n\n%s\n\n", m.Content)
		case llm.RoleAssistant:
			if m.Content != "" || len(m.ToolCalls) > 0 {
				sb.WriteString("## ☀ Assistant\n\n")
			}
			if m.Content != "" {
				sb.WriteString(m.Content + "\n\n")
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "**Tool: %s**\n\n```json\n%s\n```\n\n", tc.Name, tc.Input)
			}
		case llm.RoleTool:
			out := m.Content
			if len(out) > 3000 {
				out = out[:3000] + "\n…"
			}
			fmt.Fprintf(&sb, "<details><summary>%s result</summary>\n\n```\n%s\n```\n\n</details>\n\n", m.ToolName, out)
		}
	}
	return sb.String()
}
