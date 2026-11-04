// Package update checks GitHub releases and replaces the running binary.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wetakush/solaragent/internal/app"
	"github.com/wetakush/solaragent/internal/config"
)

const repo = "wetakush/solaragent"

type Release struct {
	Tag    string `json:"tag_name"`
	URL    string `json:"html_url"`
	Body   string `json:"body"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func (r *Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// AssetName is the release asset for this platform.
func AssetName() string {
	n := fmt.Sprintf("solar-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "solaragent/"+app.Version)
	if strings.Contains(url, "api.github.com") {
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

// Latest fetches the newest release (or a specific tag).
func Latest(ctx context.Context, tag string) (*Release, error) {
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	if tag != "" {
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		url = "https://api.github.com/repos/" + repo + "/releases/tags/" + tag
	}
	resp, err := get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r Release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Newer reports whether version a is greater than b (semver-ish, "dev" is oldest).
func Newer(a, b string) bool {
	pa, pb := parse(a), parse(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

// IsDev reports whether this is a local development build.
func IsDev() bool { return app.Version == "dev" || app.Version == "" }

// Apply downloads the release asset, verifies its checksum and replaces the running binary.
func Apply(ctx context.Context, r *Release, progress func(done, total int64)) (string, error) {
	var assetURL, sumsURL string
	var size int64
	for _, a := range r.Assets {
		switch a.Name {
		case AssetName():
			assetURL, size = a.URL, a.Size
		case "checksums.txt":
			sumsURL = a.URL
		}
	}
	if assetURL == "" {
		return "", fmt.Errorf("release %s has no build for %s/%s", r.Tag, runtime.GOOS, runtime.GOARCH)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".solar-update-*")
	if err != nil {
		return "", fmt.Errorf("can't write to %s (%v) — reinstall or run with permissions", dir, err)
	}
	defer os.Remove(tmp.Name())

	resp, err := get(ctx, assetURL)
	if err != nil {
		tmp.Close()
		return "", err
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 64*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				resp.Body.Close()
				tmp.Close()
				return "", err
			}
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil {
				progress(done, size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			resp.Body.Close()
			tmp.Close()
			return "", rerr
		}
	}
	resp.Body.Close()
	tmp.Close()

	if sumsURL != "" {
		want, err := checksum(ctx, sumsURL, AssetName())
		if err != nil {
			return "", err
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return "", fmt.Errorf("checksum mismatch for %s", AssetName())
		}
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return "", err
	}
	return exe, nil
}

func checksum(ctx context.Context, url, name string) (string, error) {
	resp, err := get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %s", name)
}

// ---- background auto-update ----

type state struct {
	LastCheck time.Time `json:"last_check"`
	Latest    string    `json:"latest"`
}

func statePath() string { return filepath.Join(config.DataDir(), "update.json") }

func loadState() state {
	var s state
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveState(s state) {
	_ = os.MkdirAll(config.DataDir(), 0o755)
	b, _ := json.Marshal(s)
	_ = os.WriteFile(statePath(), b, 0o644)
}

// Mode returns "auto", "notify" or "off" from config / env.
func Mode(cfg *config.Config) string {
	if os.Getenv("SOLAR_DISABLE_AUTOUPDATE") != "" {
		return "off"
	}
	switch strings.ToLower(string(cfg.AutoUpdate)) {
	case "off", "false", "no", "disabled":
		return "off"
	case "notify":
		return "notify"
	}
	return "auto"
}

// Outcome describes what a background check did.
type Outcome struct {
	Version string // newer version found
	Applied bool
	Err     error
}

// Background checks at most once per interval and installs a newer release when mode is "auto".
// It returns nil when nothing needs to be reported.
func Background(ctx context.Context, cfg *config.Config, interval time.Duration) *Outcome {
	mode := Mode(cfg)
	if mode == "off" || IsDev() {
		return nil
	}
	st := loadState()
	if time.Since(st.LastCheck) < interval {
		if st.Latest != "" && Newer(st.Latest, app.Version) && mode == "notify" {
			return &Outcome{Version: st.Latest}
		}
		return nil
	}
	r, err := Latest(ctx, "")
	st.LastCheck = time.Now()
	if err != nil {
		saveState(st)
		return nil
	}
	st.Latest = r.Version()
	saveState(st)
	if !Newer(r.Version(), app.Version) {
		return nil
	}
	if mode == "notify" {
		return &Outcome{Version: r.Version()}
	}
	_, err = Apply(ctx, r, nil)
	return &Outcome{Version: r.Version(), Applied: err == nil, Err: err}
}
