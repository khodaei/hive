package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// HasSession returns true if a tmux session with the given name exists.
func HasSession(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	return cmd.Run() == nil
}

// NewSession creates a new detached tmux session with a login shell
// so that ~/.zprofile and ~/.zshrc are sourced (ensuring PATH is set).
func NewSession(name, cwd string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}

	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "-c", cwd, shell, "-l")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux new-session: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// SendKeys sends keystrokes to a tmux session followed by Enter.
func SendKeys(session, keys string) error {
	cmd := exec.Command("tmux", "send-keys", "-t", session, keys, "Enter")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// SendKeysLiteral sends raw keystrokes to a tmux session without appending Enter.
// Uses -l flag to send literal characters (handles escape sequences from xterm.js).
func SendKeysLiteral(session, keys string) error {
	cmd := exec.Command("tmux", "send-keys", "-t", session, "-l", keys)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys-literal: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// CapturePaneFull captures the full scrollback history of a tmux pane.
func CapturePaneFull(session string) (string, error) {
	cmd := exec.Command("tmux", "capture-pane", "-p", "-t", session, "-S", "-1000")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane-full: %w", err)
	}
	return string(out), nil
}

// CapturePane captures the visible content of a tmux pane.
func CapturePane(session string) (string, error) {
	cmd := exec.Command("tmux", "capture-pane", "-p", "-t", session)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return string(out), nil
}

// KillSession kills a tmux session.
func KillSession(name string) error {
	cmd := exec.Command("tmux", "kill-session", "-t", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux kill-session: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// ListSessions returns the names of all active tmux sessions.
func ListSessions() ([]string, error) {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		// tmux returns exit code 1 when no server is running (no sessions)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-sessions: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

// SessionCWD returns the current working directory of a tmux session's active pane.
func SessionCWD(session string) (string, error) {
	cmd := exec.Command("tmux", "display-message", "-t", session, "-p", "#{pane_current_path}")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tmux display-message: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// AttachCommand returns the exec.Cmd for attaching to a tmux session.
// Uses -d to detach any other client first (prevents "already attached" errors).
// The caller is responsible for running this (e.g., via tea.ExecProcess).
func AttachCommand(session string) *exec.Cmd {
	return exec.Command("tmux", "attach-session", "-d", "-t", session)
}
