# hive

CLI-first manager for parallel Claude Code sessions across multiple git
repos. See README.md for user-facing docs, PLAN_cli_first.md for the
milestone breakdown behind the CLI surface.

## Tech stack

- Go 1.22+
- Bubble Tea + Lipgloss (Charmbracelet) for the TUI
- modernc.org/sqlite (pure Go, no CGO)
- Shell out to tmux and git (no wrapper libraries)
- osascript for macOS notifications
- Ollama HTTP API for optional local-LLM card summaries

## Conventions

- `go fmt` before every commit
- Keep each internal package small and focused
- Unit tests for the status classifier, cli/resolve, cli/picker,
  cli/output, and transcripts
