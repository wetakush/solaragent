<div align="center">

```
███████╗ ██████╗ ██╗      █████╗ ██████╗
██╔════╝██╔═══██╗██║     ██╔══██╗██╔══██╗
███████╗██║   ██║██║     ███████║██████╔╝
╚════██║██║   ██║██║     ██╔══██║██╔══██╗
███████║╚██████╔╝███████╗██║  ██║██║  ██║
╚══════╝ ╚═════╝ ╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝
            A   G   E   N   T
```

**The open-source AI coding agent for your terminal.**

Open source (MIT) · made by [github.com/wetakush](https://github.com/wetakush)

</div>

## INSTALL

**macOS** (Apple Silicon & Intel) · **Linux** · **FreeBSD**

```bash
curl -fsSL https://raw.githubusercontent.com/wetakush/solaragent/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/wetakush/solaragent/main/install.ps1 | iex
```

**From source** (any OS with Go 1.24+)

```bash
git clone https://github.com/wetakush/solaragent && cd solaragent && make install
```

Then run `solar` — the setup wizard opens on first launch. The installers pick the right binary for your CPU,
verify its SHA-256 checksum and put `solar` on your PATH (`SOLAR_INSTALL_DIR` overrides the location).
SolarAgent keeps itself up to date automatically. Prebuilt binaries: [releases](https://github.com/wetakush/solaragent/releases).

---

SolarAgent is an AI coding agent that lives in your terminal, in the spirit of opencode.
It reads, writes and edits code, runs commands, searches your project, plans multi-step work
and talks to almost any LLM provider — all in a fast, good-looking TUI built with Go and Bubble Tea.

## ✨ Features

- **Beautiful TUI** — gradient branding, streaming Markdown with syntax highlighting, colored diffs,
  live tool status, a sidebar with context usage, cost, todos, MCP status and modified files. 11 themes.
- **Any provider** — Anthropic (native API, adaptive thinking, prompt caching), [NiceAI](https://api.niceai.rest),
  OpenAI, OpenRouter, Google Gemini, Groq, DeepSeek, xAI, Mistral, Together, Ollama, LM Studio, or any OpenAI-compatible endpoint.
- **Beautiful setup wizard** — `solar setup` walks you through provider → key → live verification → default model.
  Add custom OpenAI-compatible endpoints right from the CLI. Also available as `/connect` inside the TUI.
- **Auto-updates** — SolarAgent checks GitHub releases in the background and updates itself
  (checksum-verified). `solar upgrade` to update manually; `"autoupdate": "notify" | "off"` to opt out.
- **Real agent tools** — `bash`, `read`, `write`, `edit`, `multiedit`, `ls`, `glob`, `grep` (ripgrep),
  `webfetch`, `todowrite`/`todoread`, and `task` (a read-only research sub-agent).
- **Agents** — `build` (full access) and `plan` (read-only); switch with <kbd>Tab</kbd>. Define your own.
- **MCP** — connect local (stdio) and remote (streamable HTTP) Model Context Protocol servers.
- **Permission modes** — *ask* (approve edits & commands with a diff preview), *accept edits*, or
  *bypass permissions*. Cycle with <kbd>Shift+Tab</kbd>, start with `solar --dangerously-skip-permissions`
  (alias `--yolo`), or set `"permission_mode"` in config.
- **Rewind** — <kbd>Esc</kbd> <kbd>Esc</kbd> (or `/rewind`) jumps back to any earlier message: restore the
  conversation *and* the files, or the conversation only, then edit and resend.
- **Fork** — `/fork` branches the chat into a new session; fork from any earlier message in the rewind picker.
- **Sessions** — persisted per project; resume, rename, export to Markdown, **undo** a turn (restores files).
- **Context management** — `/compact` and automatic compaction near the context limit.
- **Custom commands** — Markdown files with `$ARGUMENTS`, `$1`, `` !`shell` `` and `@file` expansion.
- **Power-user input** — `@file` fuzzy attach, `!cmd` shell mode, message queueing, history,
  external `$EDITOR`, command palette, leader keys.
- **Scriptable** — `solar run "…"` streams to stdout, reads stdin, and can emit NDJSON events.
- **Project instructions** — `AGENTS.md` (also `CLAUDE.md` / `SOLAR.md`) and `/init` to generate one.

## 🔑 Connect a provider

```bash
solar setup               # interactive wizard: provider → API key → verify → default model
solar setup niceai        # jump straight to a provider
```

The wizard runs automatically on first launch. Keys are stored in `~/.local/share/solar/auth.json` (0600).
You can also export an env var (`NICEAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `GEMINI_API_KEY`,
`GROQ_API_KEY`, `DEEPSEEK_API_KEY`, `XAI_API_KEY`, `MISTRAL_API_KEY`, `TOGETHER_API_KEY`), or use `/connect`
inside the TUI. Ollama and LM Studio are detected automatically when running.

## 🖥 Usage

```bash
solar                       # open the TUI in the current directory
solar ~/code/project        # …in another directory
solar -c                    # continue the last session
solar -m openai/gpt-5 -a plan
solar run "add a --verbose flag to the CLI"
git diff | solar run "write a commit message"
solar run --format json "list TODOs" | jq .
```

### Keys

| Key | Action |
| --- | --- |
| <kbd>Enter</kbd> | send (queues while the agent works) |
| <kbd>Alt+Enter</kbd> / <kbd>Ctrl+J</kbd> / trailing `\` | newline |
| <kbd>Tab</kbd> | cycle agents (build ⇄ plan ⇄ custom) |
| <kbd>Shift+Tab</kbd> | permission mode: ask → accept edits → bypass |
| <kbd>Esc</kbd> <kbd>Esc</kbd> (idle) | rewind to a previous message (enter: chat + code, ctrl+r: chat only, ctrl+f: fork) |
| <kbd>Ctrl+K</kbd> | command palette |
| <kbd>Ctrl+O</kbd> | switch model |
| <kbd>Ctrl+T</kbd> | themes (live preview) |
| <kbd>Ctrl+E</kbd> | compose in `$EDITOR` |
| <kbd>Ctrl+N</kbd> | new session |
| <kbd>Ctrl+X</kbd> then `n l m a t u c e x h q` | leader: new, sessions, models, agents, themes, undo, compact, editor, export, help, quit |
| <kbd>Esc</kbd> <kbd>Esc</kbd> (busy) | interrupt the agent |
| <kbd>PgUp</kbd>/<kbd>PgDn</kbd>, mouse wheel | scroll |
| `@path` | attach a file / directory |
| `!cmd` | run a shell command and share the output |

### Slash commands

`/new` `/sessions` `/models` `/agents` `/connect` `/themes` `/effort` `/init` `/compact` `/undo`
`/rewind` `/fork` `/permissions` `/rename` `/export` `/copy` `/editor` `/thinking` `/details` `/yolo` `/mcp` `/status` `/upgrade` `/help` `/quit`

### Updating

```bash
solar upgrade             # install the latest release
solar upgrade --check     # just check
solar upgrade 1.1.0       # install a specific version
```

Background updates run at most every 12 hours. Disable with `"autoupdate": "off"` or `SOLAR_DISABLE_AUTOUPDATE=1`.

## ⚙️ Configuration

Global: `~/.config/solar/solar.json` · Project: `./solar.json` (JSONC, `{env:VAR}` substitution).
See [`docs/solar.example.jsonc`](docs/solar.example.jsonc).

```jsonc
{
  "model": "anthropic/claude-opus-5-5",
  "small_model": "anthropic/claude-haiku-4-5",   // used for session titles
  "theme": "solar",
  "effort": "high",                               // low | medium | high | xhigh | max
  "permission_mode": "ask",                       // ask | accept-edits | bypass
  "autoupdate": "auto",                           // auto | notify | off
  "permission": { "bash": "ask", "edit": "ask", "webfetch": "allow" },
  "provider": {
    "my-vllm": { "base_url": "http://gpu-box:8000/v1", "models": ["qwen3-coder"] }
  },
  "mcp": {
    "filesystem": { "type": "local", "command": ["npx", "-y", "@modelcontextprotocol/server-filesystem", "."] },
    "docs":       { "type": "remote", "url": "https://example.com/mcp", "headers": { "Authorization": "Bearer {env:DOCS_TOKEN}" } }
  },
  "agent": {
    "review": { "description": "Code reviewer", "prompt": "Review changes for bugs.", "tools": { "write": false, "edit": false } }
  },
  "instructions": ["CONTRIBUTING.md"]
}
```

### Custom commands — `.solar/command/test.md`

```markdown
---
description: run tests and fix failures
agent: build
---
Run the test suite. Current failures:
!`go test ./... 2>&1 | tail -40`
Fix them. Focus on: $ARGUMENTS
```

Then type `/test auth package`.

### Custom agents — `.solar/agent/docs.md`

```markdown
---
description: writes documentation
tools: read, glob, grep, write, edit
color: accent
---
You are a technical writer. Write clear, concise docs.
```

## 🧱 Architecture

```
main.go              entry point
internal/cli         cobra commands (tui, run, auth, models, sessions, export, mcp, agents, config)
internal/tui         Bubble Tea UI: chat blocks, dialogs, sidebar, completion, permissions
internal/agent       the model ⇄ tools loop, permissions, compaction, titles, sub-agents
internal/llm         provider interface; Anthropic (official SDK) and OpenAI-compatible clients
internal/tools       built-in tools
internal/mcp         MCP client (stdio + streamable HTTP)
internal/session     persistence, undo snapshots, export
internal/config      config + credentials
internal/setup       interactive provider setup wizard
internal/update      self-update from GitHub releases
internal/theme       color themes & gradients
```

## 📦 Releasing

Releases are built locally and published with the GitHub CLI:

```bash
scripts/release.sh v1.2.0   # tests, tags, cross-compiles 7 targets, uploads binaries + checksums
```

Installed copies pick up new releases automatically.

## 🤝 Contributing

Issues and PRs are welcome! Run `make test` before submitting.

## 📄 License

[MIT](LICENSE) © [wetakush](https://github.com/wetakush) — SolarAgent is free and open-source software.
