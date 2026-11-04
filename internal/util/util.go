// Package util holds small helpers: file walking, globbing, diffs, fuzzy matching.
package util

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, "dist": true, "build": true, "target": true,
	".cache": true, ".idea": true, ".vscode": true, ".turbo": true, "coverage": true,
	".gradle": true, ".svelte-kit": true, ".nuxt": true, ".tox": true, ".mypy_cache": true,
}

// SkipDirs lists directory names that are never searched.
func SkipDirs() []string {
	var out []string
	for d := range skipDirs {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// Walk visits files under root (relative paths, slash-separated), skipping
// common heavy/vendor directories and anything matched by a root .gitignore.
// fn returns false to skip a directory (or stop descending).
func Walk(root string, fn func(rel string, d os.DirEntry) bool) error {
	ign := loadGitignore(root)
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() && (skipDirs[d.Name()] || ign.match(rel, true)) {
			return filepath.SkipDir
		}
		if !d.IsDir() && ign.match(rel, false) {
			return nil
		}
		if !fn(rel, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return nil
	})
}

type gitignore struct{ pats []ignPat }

type ignPat struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

func loadGitignore(root string) *gitignore {
	g := &gitignore{}
	b, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return g
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := ignPat{}
		if strings.HasPrefix(line, "!") {
			p.negate, line = true, line[1:]
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly, line = true, strings.TrimSuffix(line, "/")
		}
		anchored := strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		if !anchored {
			line = "**/" + line
		}
		re, err := GlobRegexp(line)
		if err != nil {
			continue
		}
		p.re = re
		g.pats = append(g.pats, p)
	}
	return g
}

func (g *gitignore) match(rel string, isDir bool) bool {
	ignored := false
	for _, p := range g.pats {
		if p.dirOnly && !isDir {
			continue
		}
		if p.re.MatchString(rel) {
			ignored = !p.negate
		}
	}
	return ignored
}

// GlobRegexp converts a glob (with **, *, ?, {a,b}, [..]) into an anchored regexp.
func GlobRegexp(glob string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	inBrace := 0
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					sb.WriteString("(?:.*/)?")
				} else {
					sb.WriteString(".*")
				}
			} else {
				sb.WriteString("[^/]*")
			}
		case '?':
			sb.WriteString("[^/]")
		case '{':
			inBrace++
			sb.WriteString("(?:")
		case '}':
			if inBrace > 0 {
				inBrace--
				sb.WriteString(")")
			} else {
				sb.WriteString(`\}`)
			}
		case ',':
			if inBrace > 0 {
				sb.WriteString("|")
			} else {
				sb.WriteString(",")
			}
		case '[':
			j := strings.IndexByte(glob[i:], ']')
			if j < 0 {
				sb.WriteString(`\[`)
			} else {
				cls := glob[i+1 : i+j]
				if strings.HasPrefix(cls, "!") {
					cls = "^" + cls[1:]
				}
				sb.WriteString("[" + cls + "]")
				i += j
			}
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// ---------- diff ----------

// Diff returns a unified-style diff ("@@", "+", "-", " " prefixed lines) with 3 lines of context.
func Diff(a, b string) string {
	if a == b {
		return ""
	}
	al, bl := splitLines(a), splitLines(b)
	ops := diffOps(al, bl)
	type line struct {
		kind byte
		text string
		ai   int
		bi   int
	}
	var all []line
	ai, bi := 0, 0
	for _, op := range ops {
		switch op {
		case ' ':
			all = append(all, line{' ', al[ai], ai, bi})
			ai++
			bi++
		case '-':
			all = append(all, line{'-', al[ai], ai, bi})
			ai++
		case '+':
			all = append(all, line{'+', bl[bi], ai, bi})
			bi++
		}
	}
	const ctx = 3
	keep := make([]bool, len(all))
	for i, l := range all {
		if l.kind != ' ' {
			for j := i - ctx; j <= i+ctx; j++ {
				if j >= 0 && j < len(all) {
					keep[j] = true
				}
			}
		}
	}
	var sb strings.Builder
	prevKept := false
	for i, l := range all {
		if !keep[i] {
			prevKept = false
			continue
		}
		if !prevKept {
			fmt.Fprintf(&sb, "@@ -%d +%d @@\n", l.ai+1, l.bi+1)
		}
		prevKept = true
		sb.WriteByte(l.kind)
		sb.WriteString(l.text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

func diffOps(a, b []string) []byte {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	am, bm := a[pre:len(a)-suf], b[pre:len(b)-suf]
	var ops []byte
	for i := 0; i < pre; i++ {
		ops = append(ops, ' ')
	}
	n, m := len(am), len(bm)
	if n*m > 4_000_000 {
		for range am {
			ops = append(ops, '-')
		}
		for range bm {
			ops = append(ops, '+')
		}
	} else {
		dp := make([][]int32, n+1)
		for i := range dp {
			dp[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if am[i] == bm[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else if dp[i+1][j] >= dp[i][j+1] {
					dp[i][j] = dp[i+1][j]
				} else {
					dp[i][j] = dp[i][j+1]
				}
			}
		}
		i, j := 0, 0
		for i < n && j < m {
			if am[i] == bm[j] {
				ops = append(ops, ' ')
				i++
				j++
			} else if dp[i+1][j] >= dp[i][j+1] {
				ops = append(ops, '-')
				i++
			} else {
				ops = append(ops, '+')
				j++
			}
		}
		for ; i < n; i++ {
			ops = append(ops, '-')
		}
		for ; j < m; j++ {
			ops = append(ops, '+')
		}
	}
	for i := 0; i < suf; i++ {
		ops = append(ops, ' ')
	}
	return ops
}

func DiffStat(d string) (add, del int) {
	for _, l := range strings.Split(d, "\n") {
		if strings.HasPrefix(l, "@@") {
			continue
		}
		if strings.HasPrefix(l, "+") {
			add++
		} else if strings.HasPrefix(l, "-") {
			del++
		}
	}
	return
}

// ---------- fuzzy ----------

// FuzzyScore returns a match score (higher is better) or -1 if query is not a subsequence.
func FuzzyScore(query, target string) int {
	if query == "" {
		return 0
	}
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(target))
	orig := []rune(target)
	score, qi, streak := 0, 0, 0
	last := -1
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] != q[qi] {
			streak = 0
			continue
		}
		s := 1
		if ti == 0 || strings.ContainsRune("/_-. ", t[ti-1]) || (unicode.IsUpper(orig[ti]) && ti > 0 && unicode.IsLower(orig[ti-1])) {
			s += 8
		}
		if last == ti-1 {
			streak++
			s += 4 * streak
		}
		score += s
		last = ti
		qi++
	}
	if qi < len(q) {
		return -1
	}
	score -= len(t) / 8
	if strings.Contains(strings.ToLower(target), strings.ToLower(query)) {
		score += 20
	}
	return score
}

// FuzzyFilter returns indices of items matching query, best first.
func FuzzyFilter(query string, items []string) []int {
	type sc struct{ i, s int }
	var res []sc
	for i, it := range items {
		if s := FuzzyScore(query, it); s >= 0 {
			res = append(res, sc{i, s})
		}
	}
	sort.SliceStable(res, func(a, b int) bool { return res[a].s > res[b].s })
	out := make([]int, len(res))
	for i, r := range res {
		out[i] = r.i
	}
	return out
}

// HumanTokens formats token counts as 1.2k / 3.4M.
func HumanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}
