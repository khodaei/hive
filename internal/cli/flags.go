package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/khodaei/hive/internal/store"
)

// ResolveOrPick is the default card-targeting policy for CLI verbs that accept
// a free-form `[query]`. When the query uniquely resolves, it returns that
// card; otherwise it opens the picker pre-filtered with the query. Callers
// should check for ErrPickerCancelled.
func ResolveOrPick(cards []store.Card, query string) (store.Card, error) {
	if query != "" {
		if c, ok := Resolve(cards, query); ok {
			return c, nil
		}
	}
	return Pick(cards, query)
}

// ReadMessage returns the message text for a command like `hive send`.
//   - If editor is true (or text is empty), open $EDITOR on a tempfile.
//   - If text is "-", read all of stdin (trailing newline trimmed).
//   - If text starts with "@", read that file path (trailing newline trimmed).
//   - Otherwise, return text as-is.
//
// A returned empty string with a nil error is treated as a user cancellation.
func ReadMessage(text string, editor bool) (string, error) {
	if editor || text == "" {
		return readFromEditor("")
	}
	if text == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimRight(string(b), "\n"), nil
	}
	if strings.HasPrefix(text, "@") {
		return ResolvePromptArg(text)
	}
	return text, nil
}

// ResolvePromptArg expands an `@file` reference to the file's contents.
// Plain text passes through unchanged (including empty string — callers
// interpret that as "no prompt supplied"). Use this in verbs like `hive new`
// where an empty prompt is distinct from stdin / editor flows and there's no
// other special meaning to `-` etc.
func ResolvePromptArg(arg string) (string, error) {
	if !strings.HasPrefix(arg, "@") {
		return arg, nil
	}
	path := strings.TrimPrefix(arg, "@")
	if path == "" {
		return "", fmt.Errorf("empty path after @")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read prompt file %q: %w", path, err)
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func readFromEditor(seed string) (string, error) {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
	}
	tmp, err := os.CreateTemp("", "hive-msg-*.txt")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if seed != "" {
		if _, err := tmp.WriteString(seed); err != nil {
			tmp.Close()
			return "", err
		}
	}
	tmp.Close()

	// Support $EDITOR values with args (e.g. "code --wait"). Fields-split is
	// good enough; we're not trying to parse shell quoting.
	parts := strings.Fields(ed)
	cmd := exec.Command(parts[0], append(parts[1:], path)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor %s: %w", filepath.Base(parts[0]), err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	msg := strings.TrimRight(string(b), "\n")
	if msg == "" {
		return "", errors.New("empty message")
	}
	return msg, nil
}
