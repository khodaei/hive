# hive

A CLI-first manager for parallel Claude Code sessions across multiple git repos. Run 5–10 agents simultaneously, drive them with one-keystroke verbs from any terminal, and keep an eye on what each one is doing without ever opening a board. A TUI and web UI ship alongside for when you want them.

Each card is a Claude Code session running inside a named tmux session inside a git worktree. The hive store lives in SQLite at `~/.hive/hive.db`; sessions persist independently of whatever UI you're using.

```
hive❯
  ↑/↓ select · enter attach · → archive · ← undo · ctrl-/ details · esc quit
  TITLE                   REPO            STATUS         AGE
▶ Readme Instruction      sigma-agents    💤 idle        1d
  digest 04-20            sigma-agents    💤 idle        1d
  Adv360 - Phase2         advertiser-360  ⏸ paused       59m
  Ad Teller Creatives     advertiser-360  💤 idle        2d
```

---

## Requirements

- **Go 1.22+** (to build the binary).
- **Node.js 18+** (only if you also want the web UI bundle).
- **tmux** — `brew install tmux`
- **git**
- **macOS** for notifications (osascript). Linux works for everything else.

**Optional but highly recommended:**

- **fzf** — `brew install fzf`. Upgrades the no-arg `hive` picker from a numbered list to a full fuzzy finder with preview sidebar. Without fzf, hive falls back to a plain numbered prompt.
- **Ollama** — `brew install ollama`. Powers on-device summaries of each card. Without Ollama, the card preview shows a raw "Recent turns" block instead.

## Install

```sh
git clone <repo-url> ~/Workspace/hive
cd ~/Workspace/hive
make build        # builds the binary (includes web UI bundle)
make install      # go install → ~/go/bin/hive
```

Make sure `~/go/bin` is on your `PATH`:

```sh
echo 'export PATH="$HOME/go/bin:$PATH"' >> ~/.zshrc
```

Verify:

```sh
hive version
hive doctor       # sanity-checks tmux, git, claude, fzf, config, orphans, worktrees
```

## Shell completions

Tab-complete every verb, every flag, and card IDs / titles parsed live from `hive ls --json`.

**zsh** (link into any dir on your `$fpath`):

```sh
ln -sf "$PWD/completions/_hive" ~/.zsh/completions/_hive
# ensure ~/.zsh/completions is in fpath, then:
compinit
```

**bash:**

```sh
echo "source $PWD/completions/hive.bash" >> ~/.bashrc
```

**fish:**

```sh
cp completions/hive.fish ~/.config/fish/completions/hive.fish
```

---

## Configure

Create `~/.hive/config.yaml`. Minimum is just `repos:`:

```yaml
repos:
  - name: my-project
    path: /Users/you/code/my-project
    default_branch: main
  - name: another-repo
    path: /Users/you/code/another-repo
    default_branch: master
    setup_script: "uv sync"    # runs before claude starts

poll_interval_sec: 3
idle_threshold_sec: 5
claude_cmd: claude
tmux_session_prefix: hv_
archive_behavior: prompt       # keep | prompt | delete
branch_prefix: "amir/"         # optional — prepends to auto-derived branch names

notifications:
  enabled: true
  on_needs_input: true
  on_errored: true
  on_idle: false
  idle_too_long_min: 0
  quiet_hours:
    start: "22:00"
    end: "07:00"

# Optional: local-LLM card summaries via Ollama. See "Card summaries" below.
summary:
  enabled: false               # flip to true once Ollama is running
  ollama_url: http://localhost:11434
  ollama_model: llama3.2:3b
  turns_window: 20
  max_tokens: 120
  timeout_sec: 30
  auto_generate_on: [needs_input, idle, archived]
```

Validate:

```sh
hive config check
```

---

## Quick start — the daily loop

```sh
# 1. Create a session from anywhere — auto-detects repo from cwd
hive new "fix auth bug"

# 2. Work, detach with Ctrl-b d, come back later via the picker
hive                                     # fuzzy picker → Enter to attach

# 3. Attach by title or ID prefix
hive a fix-au                            # unique match attaches, else opens picker
hive last                                # attach the most-recently-attached session

# 4. Nudge a running session without attaching
hive send fix-au "keep going"
hive send fix-au -e                      # compose in $EDITOR
echo "run the tests" | hive send fix-au -  # from stdin

# 5. Peek / tail a pane without attaching
hive peek fix-au -n 80
hive tail fix-au                          # live-stream, Ctrl-C exits

# 6. See state at a glance
hive ls --repo my-project --status needs_input --since 1h
hive status --short                       # 3⚙ 1❓ $2.47   (great for PS1)

# 7. Archive when finished (3s Ctrl-C undo window)
hive done fix-au
hive done fix-au -y --delete-worktree     # non-interactive + cleanup

# 8. Resume later
hive resume fix-au                        # re-launches with `claude --resume`
```

**Full verb list**: `hive help` (summary), `hive help <verb>` (per-verb detail).

---

## The picker

`hive` with no args opens an interactive fuzzy picker over every live card. Built on fzf when available.

| Key | Action |
|---|---|
| `↑` / `↓` | Navigate rows |
| Type anything | Fuzzy filter by title / repo / status |
| `Enter` | Attach to the highlighted card |
| `→` | **Archive** the highlighted card (no undo, reloads list) |
| `←` | **Restore** the most-recently archived card |
| `Ctrl-/` | Toggle the details sidebar (card summary + metadata) |
| `Esc` | Quit |

The sidebar shows card metadata — title, repo/branch, column/status, worktree, tmux/Claude session IDs, created/updated/last-attached timestamps, cost + tokens, model, pending prompt, PR URL — plus the **Summary** block (see below) and a freshness marker like *(3m ago)*.

## Card summaries (local LLM via Ollama)

**Opt-in.** When enabled, hive runs every card's Claude transcript through a local Ollama model (`llama3.2:3b` by default, ~2 GB) and shows a three-section summary in the picker sidebar and in `hive card <id>`:

```
Summary  (2m ago)

  Goal: Track visits to GitHub pages for analytics.

  Progress:
    - Created issue #39 with feedback body template.
    - Applied `digest-feedback` label to the issue.

  Next: Migrating hosting to Release Manager for analytics.
```

- **Goal** is yellow, **Progress** is green, **Next** is blue; `` `backticks` `` are cyan. Bullet markers inherit their section's color.
- **Fail-safe**: if Ollama is down, hive shows whatever cached summary exists (or falls back to a raw "Recent turns" block if there isn't one yet). Nothing in hive's hot path ever waits on Ollama.

### Setup

```sh
brew install ollama
ollama serve                    # keep running (or use the Ollama.app)
ollama pull llama3.2:3b         # ~2 GB one-time download
```

Flip the config:

```yaml
summary:
  enabled: true
  ollama_model: llama3.2:3b
  auto_generate_on: [needs_input, idle, archived]
```

Seed existing cards:

```sh
hive summarize --all            # loops every card, ~1–2 s per summary
hive summarize fix-au --force   # regenerate one
```

After that, `hive` → Ctrl-/ shows the colored sidebar.

### Tuning Ollama memory

The default 3B model loads at ~17 GB RSS (mostly KV cache). To cap it:

```sh
launchctl setenv OLLAMA_NUM_CTX 4096           # smaller context window (big KV savings)
launchctl setenv OLLAMA_MAX_LOADED_MODELS 1    # never keep more than one model resident
# Quit + relaunch Ollama.app so the daemon picks up the vars.
```

Ollama auto-unloads models after 5 min idle by default — you don't need to stop it manually.

---

## Background auto-refresh (launchd agent)

`hive` verbs are one-shot — they exit after the command completes. The **poller** (which updates statuses, tracks cost, and fires summary regenerations on `needs_input` / `idle` / `archived` transitions) only runs when one of these is up:

- `hive daemon`
- `hive tui`
- `hive serve`
- `hive app`

For always-fresh summaries and continuous status tracking across terminal sessions, run `hive daemon` as a launchd agent:

```sh
mkdir -p ~/Library/LaunchAgents
cat > ~/Library/LaunchAgents/com.hive.daemon.plist <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.hive.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/YOU/go/bin/hive</string>
    <string>daemon</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    <key>HOME</key><string>/Users/YOU</string>
  </dict>
  <key>WorkingDirectory</key><string>/Users/YOU</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/Users/YOU/.hive/launchd.out</string>
  <key>StandardErrorPath</key><string>/Users/YOU/.hive/launchd.err</string>
  <key>ThrottleInterval</key><integer>30</integer>
</dict>
</plist>
PLIST
launchctl load -w ~/Library/LaunchAgents/com.hive.daemon.plist
```

(Replace `/Users/YOU` with your home path; launchd doesn't expand `~` or `$HOME` in plist string values.)

**Management:**

```sh
launchctl list | grep hive                                 # status
launchctl stop   com.hive.daemon                           # restart (KeepAlive=true means it relaunches)
launchctl unload ~/Library/LaunchAgents/com.hive.daemon.plist   # disable
tail -f ~/.hive/daemon.log                                 # live log
```

---

## CLI reference

Grouped by purpose. Every verb also supports `hive help <verb>` for long-form help.

### Create / attach

| Command | Action |
|---|---|
| `hive new [title] [-p prompt] [-r repo] [-b branch] [-w worktree]` | Create a session; auto-detects repo, creates a worktree. `.` = use cwd. |
| `hive attach`, `hive a [query]` | Resolve by fuzzy query, else open the picker. |
| `hive last`, `hive -` | Attach the most-recently-attached session. |

### Inspect / interact

| Command | Action |
|---|---|
| `hive` | Open the picker over live cards. |
| `hive ls [flags]` | Table listing. `-r repo`, `-s status`, `-c column`, `--since 1h`, `--json`, `-f {table,tsv,json}`. |
| `hive watch [--interval 2s] [ls flags]` | Live-redraw `hive ls`. |
| `hive status [--short\|--json]` | One-line PS1 or structured summary. |
| `hive card [query] [--json]` | Formatted single-card summary (also the picker sidebar source). |
| `hive summarize [query] [--all] [--force]` | Regenerate LLM summary via Ollama. |
| `hive peek [query] [-n N]` | Print the last N lines of a pane (no attach). |
| `hive tail [query] [-n N]` | Live-stream a pane. |
| `hive send [query] <msg>` | Send text to a session. `-` = stdin, `-e` = `$EDITOR`. |
| `hive search <term>` | Grep panes + Claude transcripts. |
| `hive cd [query]` | Print worktree path (for `cd $(hive cd …)`). |
| `hive open [query] [--finder]` | Open worktree in `$EDITOR` (or Finder). |

### Lifecycle

| Command | Action |
|---|---|
| `hive done [query]` | Archive with 3s Ctrl-C undo window. `-y` skips. `--delete-worktree` / `--keep-worktree`. |
| `hive resume [query]` | Re-launch a Done / Archived card with `claude --resume`. |
| `hive rm`, `hive kill [query]` | Hard-delete. Interactive confirm unless `-y`. `--delete-worktree`. |

### Admin

| Command | Action |
|---|---|
| `hive doctor` | Check tmux / git / claude / fzf on PATH, config validity, orphaned tmux sessions, stale worktrees. |
| `hive config check` | Load + validate `~/.hive/config.yaml`. |
| `hive template list\|show\|create` | Reusable session presets (YAML in `~/.hive/templates/`). |
| `hive scan` / `hive import` | Adopt existing tmux sessions into hive. |
| `hive help [verb]` | Top-level or per-verb help. |
| `hive version` | Print version. |

### Long-running processes (for background polling + summaries)

| Command | Action |
|---|---|
| `hive daemon` | Headless poller + notifications. Ideal for launchd. |
| `hive tui` | Bubble Tea kanban board (also starts a poller). |

### Exit codes

`0` success · `1` general error · `2` ambiguous query in a non-interactive context · `3` dependency missing · `4` config invalid

---

## TUI

`hive tui` launches the original Bubble Tea kanban board — useful when you want a persistent at-a-glance view with richer keybindings.

| Key | Action |
|---|---|
| `n` | New session (Active) |
| `N` | New backlog card |
| `Enter` | Attach (Active) / Resume (Done) |
| `Ctrl-b d` | Detach back to board |
| `f` | Send follow-up text without attaching |
| `d` | Archive to Done (undo with `u`) |
| `u` | Undo archive |
| `D` | Delete card (with confirmation) |
| `m` | Mute/unmute notifications |
| `H`/`L` | Move card between columns |
| `h`/`l` | Navigate across columns |
| `j`/`k` | Navigate up/down |
| `Space` | Toggle multi-select |
| `i` | Card detail |
| `$` | Aggregate spend |
| `/` | Filter |
| `?` | Help overlay |
| `q` | Quit (sessions keep running) |

## Templates

```sh
hive template create bugfix --repo my-project \
  --prompt "investigate and fix the bug described in the latest issue"
hive template list
hive template show bugfix
```

Stored as YAML in `~/.hive/templates/`.

---

## How it works

```
       one-shot CLI verbs                       long-running processes
       hive, hive a, hive ls, ...               hive daemon  /  hive tui
               │                                         │
               ▼                                         ▼
       ┌──────────────────────┐             ┌──────────────────────┐
       │ SQLite store         │◀────────────│ Poller goroutine     │
       │ ~/.hive/hive.db      │             │ • tmux capture-pane  │
       │ cards, events,       │             │ • classify status    │
       │ cost_snapshots, ...  │             │ • update cost        │
       └──────────────────────┘             │ • queue summary jobs │
               ▲                            └──────────┬───────────┘
               │                                       │
       hive card / picker sidebar           ┌──────────┴────────────┐
       (reads cache, never calls            │ Claude JSONL transcripts│
       Ollama inline)                       │ Ollama HTTP summary    │
                                            └────────────────────────┘
```

1. **Poller** ticks every `poll_interval_sec`, captures tmux panes for each active card, classifies status.
2. **Classifier** determines status via regex (needs_input > errored > working > idle).
3. **Transcript reader** parses Claude JSONL files for token usage, cost, and summary input.
4. **Summary generator** (opt-in) fires Ollama jobs on status transitions; results are cached on the card row keyed by transcript mtime.
5. Status changes trigger **notifications**.
6. **TUI** and **CLI** both read from the same SQLite store.

Sessions persist independently. Quitting any UI or killing the daemon does not kill any Claude agent.

## Project structure

```
hive/
  cmd/hive/main.go              # CLI entry point (every verb dispatches here)
  completions/                  # zsh / bash / fish shell completions
  internal/
    cli/                        # resolve + picker + formatting shared by CLI verbs
    config/                     # YAML config loading + validation
    cost/                       # Claude model pricing table
    git/                        # Git worktree management
    logrotate/                  # Log file rotation
    notify/                     # macOS notifications (batching, DND)
    poller/                     # Background status + cost + summary poll loop
    recovery/                   # Crash recovery (DB ↔ tmux reconciliation)
    status/                     # Regex-based pane content classifier
    store/                      # SQLite persistence (WAL, mutex, migrations)
    templates/                  # Session templates
    tmux/                       # tmux subprocess wrapper
    transcripts/                # Claude session ID + usage + Ollama summary
    tui/                        # Bubble Tea TUI
```
