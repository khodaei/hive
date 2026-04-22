# hive — CLI-first plan

Shape hive into a CLI-first tool where the primary loop is:
1. Create a session from anywhere with one command.
2. Pick/attach with a fuzzy picker.
3. Send / peek / archive without leaving the shell.

TUI and web stay available, but CLI verbs are canonical and fully-featured.

## Taste calls (locked in)

1. **Collapse `hive claude` into `hive new`.** Single canonical creation verb. `hive claude` is removed (not aliased — we haven't shipped it widely).
2. **Picker via `fzf` if installed, fallback to built-in.** Shell out to `fzf` when on `$PATH`; otherwise use an in-process Bubble Tea list so the binary still works standalone. No new runtime deps.
3. **Ambiguous queries open the picker.** Never silently attach to the wrong session. If `[query]` matches 0 or >1 cards, drop into the picker pre-filtered with the query. Exact match (card ID or unique title prefix) attaches immediately.

## Command surface — target state

| Command | Purpose | Status today |
|---|---|---|
| `hive` (no args) | Open fuzzy picker over all cards | new |
| `hive new [title]` | Create session: auto-detect repo from cwd, worktree + tmux + claude + optional prompt | replaces `hive claude` + `hive` quick-create |
| `hive a [query]` / `hive attach [query]` | Attach (exact → attach, ambiguous → picker) | exists, UUID-only |
| `hive last` / `hive -` | Attach most-recently-attached card | new |
| `hive send [query] <msg>` | Send text to a session; `-`/`-e` for stdin/editor | exists, UUID-only |
| `hive peek [query]` | Dump last N lines of pane without attaching | new |
| `hive ls` | Compact colored list; `--repo --status --column --json --since` | exists, thin |
| `hive status [--short\|--json]` | One-line summary for shell prompts | exists, add flags |
| `hive watch` | Live-updating list (redraws) | new |
| `hive done [query]` | Archive with undo window | partial (TUI only) |
| `hive resume [query]` | Restart a Done card via `claude --resume` | partial |
| `hive rm [query]` / `hive kill [query]` | Hard delete with confirm | exists, UUID-only |
| `hive search <term>` | Grep across pane captures + Claude transcripts | new |
| `hive doctor` | Verify tmux, git, claude, config, orphans | new |
| `hive prune` | Remove Done cards + worktrees older than N days | new |
| `hive cd [query]` | Print worktree path (for shell alias) | new |
| `hive tui` / `hive serve` / `hive app` | Unchanged | existing |
| Admin: `config`, `auth`, `template`, `repo`, `scan`, `import`, `install-url-handler`, `menubar-script`, `version` | Unchanged or light polish | existing |

**Removed:** `hive claude` (collapsed into `new`), `hive create` (kept as `new --strict` or removed — decide during M2).

## Cross-cutting: query resolution

A `[query]` argument resolves a card. Used by `a`, `send`, `peek`, `done`, `resume`, `rm`, `cd`, `tail`, `open`, `mute`, `rename`.

Resolution order:
1. Exact card ID match → attach.
2. Unique prefix of card ID → attach.
3. Unique case-insensitive substring of title → attach.
4. 0 matches or >1 matches → open picker pre-filtered with the query.
5. If stdout is not a TTY and picker would open → print matches to stderr and exit 2 (scriptable).

Implement once in `internal/cli/resolve.go`; every verb calls it.

## Cross-cutting: the picker

`internal/cli/picker.go`:
- Builds tab-separated rows: `ID\tTITLE\tREPO\tSTATUS\tAGE\tCOST\tPANE_SNIPPET`.
- If `fzf` on `$PATH`: exec `fzf --ansi --with-nth=2.. --delimiter='\t' --preview='hive peek {1}' --preview-window=right:60%:wrap --bind=ctrl-/:toggle-preview`.
- Else: Bubble Tea list with the same rows, `/` to filter, Enter to select, `Tab` to toggle preview.
- Returns the selected card ID on stdout (or exit code 130 on cancel). Verbs consume it.

Pane snippet = last ~3 non-blank lines of the cached tmux capture, so fzf search covers content out of the box.

## Milestones

### M0 — naming + picker plumbing (1 day)
**Goal:** unify the create verb and land shared resolve/picker packages. No user-visible features yet beyond `hive new`.

- Rename `hive claude` handler → `runNew`; delete the `case "claude"` dispatch. Keep behavior identical.
- Wire `hive` (no-arg) to open the picker instead of running quick-create (behind a feature flag env var `HIVE_PICKER=1` for one release if we want a soft rollout; otherwise flip).
- New package `internal/cli/resolve.go` — query → card ID, using `store.ListCards`.
- New package `internal/cli/picker.go` — fzf-or-builtin; pure function `PickCardID(cards, initialQuery string) (string, error)`.
- Move shared flag parsing out of the per-command funcs into `internal/cli/flags.go`.

**Done when:** `hive new "x"`, `hive new . "x"`, `hive new "x" -p "..."`, `hive new "x" -r repo` all work; `hive claude` is gone; `hive` with no args opens a picker; Enter on a row prints the card ID.

### M1 — attach / send / peek use query resolution (1 day)
**Goal:** the core loop runs without typing UUIDs.

- `hive a [query]` alias + fuzzy resolve via `resolve.go`. `hive attach` keeps working.
- `hive send [query] <msg>`; add `-` (stdin) and `-e` (`$EDITOR`).
- `hive peek [query] [-n 40]` — reads `tmux capture-pane -t <s> -p`, prints last N lines with basic color.
- `hive last` / `hive -` — maintain a `last_attached_at` column (or reuse `updated_at`) and attach the max.

**Done when:** daily flow works: `hive` → picker → attach; `hive a fix-au` attaches; `hive send fix-au "keep going"` sends; `hive peek fix-au` prints without attaching.

### M2 — lifecycle verbs (1 day)
- `hive done [query]` with the same undo window semantics as the TUI. If invoked non-interactively (no TTY or `--yes`), skip undo window and finalize immediately.
- `hive resume [query]` — new tmux session + `claude --resume <session_id>` in the stored worktree, then attach.
- `hive rm [query]` / `hive kill [query]` — confirm prompt unless `--yes`.
- Retire `hive create` (duplicate of `new`). Keep one release with a deprecation notice.

**Done when:** I can run a full session lifecycle without opening the TUI.

### M3 — list, status, watch (0.5 day)
- `hive ls` flags: `--repo`, `--status`, `--column`, `--since 1h`, `--json`, `--format tsv`.
- `hive status --short` → `3⚙ 1❓ $2.47` for PS1; `--json` for scripting.
- `hive watch` — redraws `hive ls` every 2s using `tea.WithAltScreen` + a ticker. Escape exits. (No status classifier changes — it just re-reads the store.)

### M4 — discovery & convenience (1 day)
- `hive search <term>` — greps across:
  - Current pane captures (live `tmux capture-pane` per active card).
  - Claude transcripts in `~/.claude/projects/<encoded-cwd>/*.jsonl` for cards with a `claude_session_id` or matching `worktree_path`.
  - Outputs: `<card_id>  <title>  <matched line>`.
- `hive cd [query]` — prints worktree path; users alias `hc='cd $(hive cd)'`.
- `hive open [query]` — `$EDITOR` / `code <path>` on the worktree.
- `hive tail [query]` — `tail -f` equivalent (poll `capture-pane` 2×/s).
- `hive doctor` — checks tmux/git/claude on PATH, config validity, orphaned tmux sessions, stale worktrees (worktree dir missing for a non-done card), DB writable.

### M5 — CLI hardening (0.5 day)
- Exit codes: `0` success, `1` user error, `2` query ambiguous, `3` dependency missing, `4` config invalid.
- `--json` on every read-only command where it's meaningful.
- `--quiet` and `NO_COLOR` respect everywhere.
- Rewrite `printUsage()` into grouped subcommand help. Consider `hive help <verb>` for per-command help.
- Update `completions/_hive` (zsh) + add bash + fish. Completions call `hive ls --json` for live card IDs/titles.

### M6 — nice-to-haves (as appetite permits)
- `hive prune [--older-than 7d] [--done-only] [--dry-run]` — remove cards + worktrees; interactive confirm by default.
- `hive repo add/ls/rm` — no YAML editing needed.
- `hive config edit` / `hive config reload`.
- `hive mute` / `hive unmute`.
- `hive rename` / `hive move`.
- `hive link [query]` — copy `hive://focus/<id>` or the tmux attach cmd.
- `hive exec [query] -- <cmd>` — run a command inside the card's worktree.
- Shell widget: zsh function bound to `^G` that runs the picker and inserts `hive a <id>` into the command line.

## File-level breakdown

**New:**
- `internal/cli/resolve.go` — query → card ID.
- `internal/cli/picker.go` — fzf-or-builtin picker.
- `internal/cli/flags.go` — shared flag parsing for `-p`, `-r`, `-b`, `-w`, stdin/editor helpers.
- `internal/cli/output.go` — JSON/tsv/table formatters; color + `NO_COLOR`.
- `internal/cli/doctor.go` — environment checks.
- `internal/cli/search.go` — transcript + pane grep.
- Per-verb files under `cmd/hive/` or a `internal/cli/cmd_*.go` split to shrink `main.go`.

**Changed:**
- `cmd/hive/main.go` — dispatch table; delete `case "claude"`; wire new verbs; replace `printUsage`.
- `internal/store/store.go` — add `last_attached_at` (or reuse `updated_at`); queries used by list/status/search.
- `internal/tmux/` — expose `CapturePane(session, lines int) ([]string, error)` for `peek` / `search` / `tail`.
- `completions/_hive` — regenerate against full command set.

**Untouched unless needed:** poller, status classifier, notify, server, web, templates, auth, urlscheme, menubar.

## Ambiguities to decide before M2

1. **Worktree naming.** `hive new "fix auth bug"` today derives branch = `fix-auth-bug` and worktree folder from that. Fine for me — confirm before locking in for other users.
2. **Cross-repo picker default.** Should the picker show archived (Done) cards by default? I'd show Active + Review by default, with `Tab` to cycle filter sets.
3. **`hive new` vs `hive new .`.** Today `.` means "use cwd as existing worktree." Keep that, or make it the default when cwd is inside a known repo and require `--new-worktree` to create one? Current behavior is safer; proposed is faster once you trust it.
4. **Transcript search scope.** Full history grep can be slow on large projects. Default to last 7 days; `--all` to override.
5. **fzf preview** — `hive peek {1}` runs the binary per preview refresh. Fine perf-wise (tmux capture is fast), but if it flickers we cache captures in-store briefly.

## Risks

- **fzf version skew** — older fzf lacks some `--bind` syntax. Require `fzf >= 0.40` in `doctor`; degrade to built-in picker otherwise.
- **Orphaned worktrees from prune** — destructive. Default to `--dry-run` unless `--yes`.
- **Query resolution surprises** — shipping picker-on-ambiguity mitigates this. Log resolved ID to stderr when matching by prefix/substring so the user sees what was picked.
- **Non-TTY scripting paths** — the picker must never open when stdin/stdout is not a TTY. Enforce in `picker.go`.

## Exit criteria for v1 of CLI-first

I can go a week without opening `hive tui`. Daily flow:
- `hive new "task"` from anywhere → session up.
- `hive` → pick → attach.
- `hive send fix-au "keep going"` mid-compile.
- `hive done fix-au` when finished.
- `hive search "flaky"` finds the three sessions where I debugged the same flake last week.

If any of those takes more than one command, the plan missed something.
