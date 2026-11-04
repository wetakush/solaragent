// Package cli defines the `solar` command-line interface.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/wetakush/solaragent/internal/agent"
	"github.com/wetakush/solaragent/internal/app"
	"github.com/wetakush/solaragent/internal/config"
	"github.com/wetakush/solaragent/internal/llm"
	"github.com/wetakush/solaragent/internal/setup"
	"github.com/wetakush/solaragent/internal/update"
	"github.com/wetakush/solaragent/internal/theme"
	"github.com/wetakush/solaragent/internal/tui"
	"github.com/wetakush/solaragent/internal/util"
)

var th = theme.Get("solar")

var (
	sPrimary = lipgloss.NewStyle().Foreground(th.C(th.Primary)).Bold(true)
	sMuted   = lipgloss.NewStyle().Foreground(th.C(th.Muted))
	sText    = lipgloss.NewStyle().Foreground(th.C(th.Text))
	sOK      = lipgloss.NewStyle().Foreground(th.C(th.Success))
	sErr     = lipgloss.NewStyle().Foreground(th.C(th.Error))
	sSec     = lipgloss.NewStyle().Foreground(th.C(th.Secondary))
)

func banner() string {
	lines := th.GradientLines([]string{
		"███████╗ ██████╗ ██╗      █████╗ ██████╗ ",
		"██╔════╝██╔═══██╗██║     ██╔══██╗██╔══██╗",
		"███████╗██║   ██║██║     ███████║██████╔╝",
		"╚════██║██║   ██║██║     ██╔══██║██╔══██╗",
		"███████║╚██████╔╝███████╗██║  ██║██║  ██║",
		"╚══════╝ ╚═════╝ ╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝",
	})
	return strings.Join(lines, "\n") + "\n" +
		sMuted.Render("  the open-source AI coding agent · v"+app.Version) + "\n" +
		sMuted.Render("  made by ") + sSec.Render(app.Author) + "\n"
}

func cwdOrArg(args []string) (string, error) {
	dir, _ := os.Getwd()
	if len(args) > 0 {
		dir = args[0]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return abs, nil
}

func Execute() {
	if err := root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, sErr.Render("✗ ")+err.Error())
		os.Exit(1)
	}
}

func root() *cobra.Command {
	var opts tui.Options
	cmd := &cobra.Command{
		Use:   "solar [project]",
		Short: "SolarAgent — the open-source AI coding agent for your terminal",
		Long: banner() + `
SolarAgent is an open-source AI coding agent that lives in your terminal.
It reads, writes and edits code, runs commands, searches your project and
talks to any LLM provider (Anthropic, OpenAI, OpenRouter, Gemini, Groq,
DeepSeek, xAI, Mistral, Ollama, LM Studio or any OpenAI-compatible API).

Source: https://` + app.Repo,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       app.Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := cwdOrArg(args)
			if err != nil {
				return err
			}
			if !term.IsTerminal(int(os.Stdout.Fd())) {
				return fmt.Errorf("the interactive UI needs a terminal — use `solar run \"message\"` for scripting")
			}
			if opts.Model == "" {
				a := app.New(dir, nil)
				none := len(a.ConfiguredProviders()) == 0
				if none {
					if _, err := setup.Run(a, ""); err != nil {
						a.Close()
						return err
					}
				}
				a.Close()
			}
			return tui.Run(dir, opts)
		},
	}
	cmd.SetVersionTemplate("SolarAgent {{.Version}} · open source · " + app.Author + "\n")
	cmd.Flags().StringVarP(&opts.Model, "model", "m", "", "model to use (provider/model)")
	cmd.Flags().StringVarP(&opts.Agent, "agent", "a", "", "agent to start with (build, plan, …)")
	cmd.Flags().BoolVarP(&opts.Continue, "continue", "c", false, "continue the most recent session")
	cmd.Flags().StringVarP(&opts.SessionID, "session", "s", "", "resume a session by id")
	cmd.Flags().StringVarP(&opts.Prompt, "prompt", "p", "", "send a prompt on startup")

	cmd.AddCommand(runCmd(), setupCmd(), upgradeCmd(), authCmd(), modelsCmd(), sessionsCmd(), exportCmd(), mcpCmd(), agentsCmd(), configCmd(), versionCmd())
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print version", Run: func(cmd *cobra.Command, args []string) {
		fmt.Print(banner())
	}}
}

// ---------- run ----------

func runCmd() *cobra.Command {
	var model, agentName, sessionID, format string
	var cont, safe, quiet bool
	cmd := &cobra.Command{
		Use:   "run [message…]",
		Short: "Run the agent non-interactively with a message (stdin is appended)",
		Example: `  solar run "explain the auth flow in this repo"
  git diff | solar run "write a commit message for this diff"
  solar run -a plan -m openai/gpt-5 "how would you add caching?"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := strings.Join(args, " ")
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				b, _ := io.ReadAll(os.Stdin)
				if s := strings.TrimSpace(string(b)); s != "" {
					if msg != "" {
						msg += "\n\n"
					}
					msg += s
				}
			}
			if strings.TrimSpace(msg) == "" {
				return fmt.Errorf("no message given")
			}
			cwd, _ := os.Getwd()
			a := app.New(cwd, nil)
			defer a.Close()
			a.MCP.WaitReady(15 * time.Second)

			var sess = a.Store.New(cwd)
			if sessionID != "" {
				s, err := a.Store.Load(sessionID)
				if err != nil {
					return err
				}
				sess = s
			} else if cont {
				if s := a.Store.Latest(); s != nil {
					sess = s
				}
			}
			if model == "" {
				model = a.Cfg.Model
			}
			if model == "" && sess.Model != "" {
				model = sess.Model
			}
			if model == "" {
				if r := app.RecentModels(); len(r) > 0 {
					model = r[0]
				}
			}
			if model == "" {
				model = a.DefaultModel()
			}
			ag, ok := a.Agent(agentName)
			if !ok {
				return fmt.Errorf("unknown agent %q", agentName)
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			events := make(chan agent.Event, 256)
			r := agent.NewRunner(a, sess, events)
			r.AutoApprove = !safe
			if display := msg; display != a.ExpandMentions(msg) {
				r.NextDisplay = display
			}
			info := a.ModelInfo(model)
			if !quiet && format != "json" {
				fmt.Fprintln(os.Stderr, th.GradientText("☀ SolarAgent", 0, true)+sMuted.Render(" · "+info.Name+" · "+ag.Name+" · "+sess.ID))
			}
			errCh := make(chan error, 1)
			go func() {
				defer close(events)
				errCh <- r.Run(ctx, a.ExpandMentions(msg), ag, model, a.Cfg.Effort)
			}()
			enc := json.NewEncoder(os.Stdout)
			lastWasText := false
			for ev := range events {
				if format == "json" {
					emitJSON(enc, ev)
					if ev.Kind == agent.EvPermission {
						ev.Perm.Reply <- agent.Deny
					}
					continue
				}
				switch ev.Kind {
				case agent.EvText:
					fmt.Print(ev.Text)
					lastWasText = true
				case agent.EvToolDone:
					if lastWasText {
						fmt.Println()
						lastWasText = false
					}
					if !quiet {
						icon := sOK.Render("✓")
						if ev.Result.IsError {
							icon = sErr.Render("✗")
						}
						title := ev.Result.Title
						if len(title) > 90 {
							title = title[:90] + "…"
						}
						fmt.Fprintf(os.Stderr, "  %s %s %s\n", icon, sPrimary.Render(ev.ToolName), sMuted.Render(title))
					}
				case agent.EvPermission:
					fmt.Fprintf(os.Stderr, "  %s %s\n", sErr.Render("⊘"), sMuted.Render("denied "+ev.ToolName+" (--safe mode)"))
					ev.Perm.Reply <- agent.Deny
				case agent.EvStep:
					if lastWasText {
						fmt.Println()
						lastWasText = false
					}
				case agent.EvError:
					fmt.Fprintln(os.Stderr, sErr.Render("✗ ")+ev.Err.Error())
				}
			}
			if lastWasText {
				fmt.Println()
			}
			err := <-errCh
			r.FinishTitle(3 * time.Second)
			if !quiet && format != "json" {
				fmt.Fprintln(os.Stderr, sMuted.Render(fmt.Sprintf("◆ %s tokens · $%.4f · session %s",
					util.HumanTokens(sess.Usage.Input+sess.Usage.Output+sess.Usage.CacheRead), sess.Cost, sess.ID)))
			}
			if err != nil && err != context.Canceled {
				return fmt.Errorf("run failed")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&model, "model", "m", "", "model to use (provider/model)")
	cmd.Flags().StringVarP(&agentName, "agent", "a", "build", "agent to use")
	cmd.Flags().BoolVarP(&cont, "continue", "c", false, "continue the most recent session")
	cmd.Flags().StringVarP(&sessionID, "session", "s", "", "session id to continue")
	cmd.Flags().BoolVar(&safe, "safe", false, "deny tools that would normally ask for permission")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "only print the response text")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json (newline-delimited events)")
	return cmd
}

func emitJSON(enc *json.Encoder, ev agent.Event) {
	names := map[agent.EventKind]string{agent.EvText: "text", agent.EvReasoning: "reasoning", agent.EvToolPending: "tool_pending",
		agent.EvToolRun: "tool_run", agent.EvToolDone: "tool_done", agent.EvPermission: "permission", agent.EvStep: "step",
		agent.EvUsage: "usage", agent.EvTitle: "title", agent.EvTodos: "todos", agent.EvCompacting: "compacting",
		agent.EvCompacted: "compacted", agent.EvDone: "done", agent.EvError: "error"}
	out := map[string]any{"type": names[ev.Kind]}
	if ev.Text != "" {
		out["text"] = ev.Text
	}
	if ev.ToolName != "" {
		out["tool"] = ev.ToolName
		out["tool_id"] = ev.ToolID
	}
	if ev.ToolInput != "" {
		out["input"] = json.RawMessage(ev.ToolInput)
	}
	if ev.Result != nil {
		out["output"] = ev.Result.Output
		out["is_error"] = ev.Result.IsError
		out["title"] = ev.Result.Title
	}
	if ev.Err != nil {
		out["error"] = ev.Err.Error()
	}
	if ev.Kind == agent.EvUsage {
		out["usage"] = ev.Usage
		out["cost"] = ev.Cost
	}
	if ev.Todos != nil {
		out["todos"] = ev.Todos
	}
	_ = enc.Encode(out)
}

// ---------- auth ----------

func authCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Manage provider credentials"}
	cmd.AddCommand(&cobra.Command{
		Use: "login [provider]", Short: "Connect a provider (interactive wizard)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
				pre := ""
				if len(args) > 0 {
					pre = args[0]
				}
				return runSetup(pre)
			}
			fmt.Print(banner() + "\n")
			var id string
			if len(args) > 0 {
				id = args[0]
			} else {
				var choices []llm.ProviderSpec
				for _, s := range llm.Builtins {
					if !s.Local {
						choices = append(choices, s)
					}
				}
				fmt.Println(sPrimary.Render("Select a provider:"))
				for i, s := range choices {
					fmt.Printf("  %s %s %s\n", sPrimary.Render(fmt.Sprintf("%2d", i+1)), sText.Render(s.Name), sMuted.Render(s.Docs))
				}
				fmt.Print("\n" + sPrimary.Render("› ") + "number: ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				var n int
				if _, err := fmt.Sscan(strings.TrimSpace(line), &n); err != nil || n < 1 || n > len(choices) {
					return fmt.Errorf("invalid choice")
				}
				id = choices[n-1].ID
			}
			spec, ok := llm.SpecByID(id)
			name := id
			if ok {
				name = spec.Name
				if spec.Docs != "" {
					fmt.Println(sMuted.Render("Get a key at " + spec.Docs))
				}
			}
			fmt.Print(sPrimary.Render("› ") + name + " API key: ")
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				b = []byte(line)
			}
			key := strings.TrimSpace(string(b))
			if key == "" {
				return fmt.Errorf("no key entered")
			}
			auth := config.LoadAuth()
			auth[id] = config.AuthEntry{Type: "api", Key: key}
			if err := auth.Save(); err != nil {
				return err
			}
			fmt.Println(sOK.Render("✓ ") + "Saved " + name + " key " + sMuted.Render(config.MaskKey(key)) + " to " + sMuted.Render(config.AuthFile()))
			return nil
		},
	}, &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List configured providers",
		Run: func(cmd *cobra.Command, args []string) {
			cwd, _ := os.Getwd()
			a := app.New(cwd, nil)
			defer a.Close()
			fmt.Println(sPrimary.Render("Providers") + sMuted.Render("  ("+config.AuthFile()+")"))
			for _, s := range llm.Builtins {
				src := a.KeySource(s.ID)
				mark, detail := sMuted.Render("○"), sMuted.Render("not configured")
				if src != "" {
					mark, detail = sOK.Render("●"), sMuted.Render(src)
				} else if s.Local {
					detail = sMuted.Render("local · " + s.BaseURL)
				}
				fmt.Printf("  %s %-20s %s\n", mark, s.Name, detail)
			}
		},
	}, &cobra.Command{
		Use: "logout <provider>", Short: "Remove a stored API key", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			auth := config.LoadAuth()
			if _, ok := auth[args[0]]; !ok {
				return fmt.Errorf("no stored key for %s", args[0])
			}
			delete(auth, args[0])
			if err := auth.Save(); err != nil {
				return err
			}
			fmt.Println(sOK.Render("✓ ") + "Removed " + args[0])
			return nil
		},
	})
	return cmd
}

// ---------- models ----------

func modelsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "models [provider]", Short: "List available models",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			a := app.New(cwd, nil)
			defer a.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			models := a.ListModels(ctx)
			if len(models) == 0 {
				return fmt.Errorf("no providers configured — run `solar auth login`")
			}
			last := ""
			for _, m := range models {
				if len(args) > 0 && m.Provider != args[0] {
					continue
				}
				if m.Provider != last {
					fmt.Println("\n" + sPrimary.Render(m.Provider))
					last = m.Provider
				}
				extra := ""
				if m.InputPrice > 0 {
					extra = fmt.Sprintf("$%g/$%g per MTok", m.InputPrice, m.OutputPrice)
				}
				fmt.Printf("  %-48s %s\n", sText.Render(m.Ref()), sMuted.Render(strings.TrimSpace(util.HumanTokens(int64(m.Context))+" ctx  "+extra)))
			}
			return nil
		},
	}
}

// ---------- sessions ----------

func sessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "sessions", Aliases: []string{"ls"}, Short: "List sessions for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			a := app.New(cwd, nil)
			defer a.Close()
			list, err := a.Store.List()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println(sMuted.Render("No sessions yet in " + cwd))
				return nil
			}
			for _, s := range list {
				fmt.Printf("  %s  %-44s %s\n", sPrimary.Render(s.ID), sText.Render(trunc(s.Title, 44)),
					sMuted.Render(fmt.Sprintf("%s · %d msgs · $%.3f", s.Updated.Format("Jan 02 15:04"), len(s.Messages), s.Cost)))
			}
			fmt.Println(sMuted.Render("\nResume with: solar -s <id>   ·   latest: solar -c"))
			return nil
		},
	}
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func exportCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use: "export <session-id>", Short: "Export a session as Markdown", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, _ := os.Getwd()
			a := app.New(cwd, nil)
			defer a.Close()
			s, err := a.Store.Load(args[0])
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				fmt.Print(s.Markdown())
				return nil
			}
			if err := os.WriteFile(out, []byte(s.Markdown()), 0o644); err != nil {
				return err
			}
			fmt.Println(sOK.Render("✓ ") + "Exported to " + out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "output file (default stdout)")
	return cmd
}

// ---------- mcp / agents / config ----------

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Inspect MCP servers"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "Connect to configured MCP servers and list their tools",
		Run: func(cmd *cobra.Command, args []string) {
			cwd, _ := os.Getwd()
			a := app.New(cwd, nil)
			defer a.Close()
			a.MCP.WaitReady(30 * time.Second)
			sts := a.MCP.Statuses()
			if len(sts) == 0 {
				fmt.Println(sMuted.Render("No MCP servers configured. Add them under \"mcp\" in solar.json."))
				return
			}
			for _, s := range sts {
				mark := sOK.Render("●")
				if s.State != "connected" {
					mark = sErr.Render("●")
				}
				fmt.Printf("  %s %-20s %s\n", mark, s.Name, sMuted.Render(fmt.Sprintf("%s · %d tools %s", s.State, s.Tools, s.Error)))
			}
			for _, t := range a.MCP.Tools() {
				fmt.Printf("     %s %s\n", sSec.Render(t.Name()), sMuted.Render(trunc(firstLine(t.Description()), 70)))
			}
		},
	})
	return cmd
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func agentsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "agents", Short: "List available agents",
		Run: func(cmd *cobra.Command, args []string) {
			cwd, _ := os.Getwd()
			a := app.New(cwd, nil)
			defer a.Close()
			for _, ag := range a.Agents {
				kind := "primary"
				if ag.Subagent {
					kind = "subagent"
				}
				fmt.Printf("  %-12s %-9s %s\n", sPrimary.Render(ag.Name), sMuted.Render(kind), sText.Render(ag.Description))
			}
		},
	}
}

func configCmd() *cobra.Command {
	return &cobra.Command{
		Use: "config", Short: "Show config locations and the resolved configuration",
		Run: func(cmd *cobra.Command, args []string) {
			cwd, _ := os.Getwd()
			cfg, errs := config.Load(cwd)
			fmt.Println(sPrimary.Render("Global config ") + config.GlobalConfigPath())
			fmt.Println(sPrimary.Render("Credentials   ") + config.AuthFile())
			fmt.Println(sPrimary.Render("Data dir      ") + config.DataDir())
			fmt.Println(sPrimary.Render("Project root  ") + config.FindProjectRoot(cwd))
			for _, e := range errs {
				fmt.Println(sErr.Render("✗ ") + e.Error())
			}
			for k, p := range cfg.Providers {
				if p.APIKey != "" {
					p.APIKey = config.MaskKey(p.APIKey)
					cfg.Providers[k] = p
				}
			}
			b, _ := json.MarshalIndent(cfg, "", "  ")
			fmt.Println("\n" + string(b))
		},
	}
}

// ---------- setup ----------

func runSetup(preselect string) error {
	cwd, _ := os.Getwd()
	a := app.New(cwd, nil)
	defer a.Close()
	res, err := setup.Run(a, preselect)
	if err != nil {
		return err
	}
	if res.Saved {
		msg := sOK.Render("✓ ") + "Connected " + sPrimary.Render(res.Provider)
		if res.Model != "" {
			msg += sMuted.Render(" · default model ") + sText.Render(res.Model)
		}
		fmt.Println(msg)
	}
	return nil
}

func setupCmd() *cobra.Command {
	return &cobra.Command{
		Use: "setup [provider]", Short: "Interactive wizard to connect a provider and pick a model",
		Aliases: []string{"connect"}, Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !term.IsTerminal(int(os.Stdout.Fd())) {
				return fmt.Errorf("setup needs a terminal — use `solar auth login <provider>` with a piped key instead")
			}
			pre := ""
			if len(args) > 0 {
				pre = args[0]
			}
			return runSetup(pre)
		},
	}
}

// ---------- upgrade ----------

func upgradeCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use: "upgrade [version]", Aliases: []string{"update"}, Short: "Update SolarAgent to the latest (or given) release",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			tag := ""
			if len(args) > 0 {
				tag = args[0]
			}
			fmt.Println(th.GradientText("☀ SolarAgent", 0, true) + sMuted.Render(" · current v"+app.Version))
			r, err := update.Latest(ctx, tag)
			if err != nil {
				return fmt.Errorf("can't fetch release info: %w", err)
			}
			if tag == "" && !update.Newer(r.Version(), app.Version) && !update.IsDev() {
				fmt.Println(sOK.Render("✓ ") + "Already up to date " + sMuted.Render("(v"+app.Version+")"))
				return nil
			}
			fmt.Println(sPrimary.Render("↑ ") + "New version " + sPrimary.Render("v"+r.Version()) + sMuted.Render("  "+r.URL))
			if check {
				return nil
			}
			barW := 30
			last := -1
			path, err := update.Apply(ctx, r, func(done, total int64) {
				if total <= 0 {
					return
				}
				pct := int(done * 100 / total)
				if pct == last {
					return
				}
				last = pct
				filled := pct * barW / 100
				var bar strings.Builder
				for i := 0; i < barW; i++ {
					if i < filled {
						bar.WriteString(lipgloss.NewStyle().Foreground(th.Blend(float64(i) / float64(barW))).Render("━"))
					} else {
						bar.WriteString(lipgloss.NewStyle().Foreground(th.C(th.Subtle)).Render("━"))
					}
				}
				fmt.Printf("\r  %s %s", bar.String(), sMuted.Render(fmt.Sprintf("%3d%%  %.1f MB", pct, float64(total)/1e6)))
			})
			fmt.Println()
			if err != nil {
				return err
			}
			fmt.Println(sOK.Render("✓ ") + "Updated to " + sPrimary.Render("v"+r.Version()) + sMuted.Render("  ("+path+")"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check, don't install")
	return cmd
}
