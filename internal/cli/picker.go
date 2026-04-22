package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/khodaei/hive/internal/store"
	"github.com/khodaei/hive/internal/tmux"
)

// ErrPickerCancelled is returned when the user cancels the picker (Esc / Ctrl-C).
var ErrPickerCancelled = errors.New("picker cancelled")

// ErrNoTTY is returned when a picker is invoked without an interactive terminal.
var ErrNoTTY = errors.New("picker requires a TTY")

// PickAction is the user's chosen action when returning from PickWithActions.
// Empty string means "default" (Enter → attach). Non-empty values indicate a
// side-key action the caller should execute before re-prompting.
type PickAction string

const (
	ActionAttach    PickAction = ""      // Enter
	ActionArchive   PickAction = "right" // move selection to Done / archived
	ActionUnarchive PickAction = "left"  // restore the most-recently archived
)

// Pick presents the given cards and returns the user's selection.
// Uses fzf when on $PATH; falls back to a minimal numbered-list prompt.
// Requires stdin and stderr to be TTYs (picker UI writes to stderr so stdout
// can still carry a parseable result for scripts that wrap the picker).
func Pick(cards []store.Card, initialQuery string) (store.Card, error) {
	if len(cards) == 0 {
		return store.Card{}, errors.New("no cards to pick from")
	}
	if !isTTY(os.Stdin) || !isTTY(os.Stderr) {
		return store.Card{}, ErrNoTTY
	}
	if _, err := exec.LookPath("fzf"); err == nil {
		card, _, err := pickFzf(cards, initialQuery, false)
		return card, err
	}
	return pickNumbered(cards, initialQuery)
}

// PickWithActions is Pick plus Right/Left arrow key actions. On a side-key
// press, returns (card, ActionArchive|ActionUnarchive, nil); the caller is
// expected to perform the action and re-invoke PickWithActions to continue.
// Falls back to Pick (no actions) when fzf isn't installed.
func PickWithActions(cards []store.Card, initialQuery string) (store.Card, PickAction, error) {
	if len(cards) == 0 {
		return store.Card{}, "", errors.New("no cards to pick from")
	}
	if !isTTY(os.Stdin) || !isTTY(os.Stderr) {
		return store.Card{}, "", ErrNoTTY
	}
	if _, err := exec.LookPath("fzf"); err != nil {
		// Numbered-list fallback doesn't do side-key actions.
		card, err := pickNumbered(cards, initialQuery)
		return card, ActionAttach, err
	}
	return pickFzf(cards, initialQuery, true)
}

// IsTTY reports whether the given file descriptor is a character device.
func IsTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// IsInteractive reports whether stdin and stderr are both TTYs — the policy
// for "am I allowed to prompt the user?" checks.
func IsInteractive() bool {
	return IsTTY(os.Stdin) && IsTTY(os.Stderr)
}

func isTTY(f *os.File) bool { return IsTTY(f) }

// --- ANSI helpers ---

const (
	ansiReset   = "\x1b[0m"
	ansiDim     = "\x1b[2m"
	ansiBold    = "\x1b[1m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func stripAnsi(s string) string { return ansiRE.ReplaceAllString(s, "") }

func colorize(s, color string) string {
	if color == "" || s == "" {
		return s
	}
	return color + s + ansiReset
}

// Column widths (in runes). Chosen so every row — and the header line — lands
// on the same default-8 tab stops, fits in ~80-col terminals, and still has
// room for "needs_input" in the STATUS column. Fields are padded *before*
// ANSI color codes are applied so escape sequences don't throw off the count.
const (
	colTitle  = 26 // TITLE picks up 1 of the 3 cells freed from STATUS.
	colRepo   = 16 // REPO picks up the other 2 (so full repo names like
	//              'advertiser-360' no longer truncate).
	colStatus = 7  // Tight. "❓ needs_input" → "❓ need…", "⚙ working" →
	//              "⚙ work…". The icon still carries the attention-grab.
	colAge = 4
)

// formatRow renders one card as a tab-separated row for the picker.
// Columns: ID \t TITLE \t STATUS \t REPO \t AGE.
// The ID is hidden via fzf's --with-nth=2..; it stays as field 1 so --preview
// can reference {1}. Cost / full metadata live in the preview sidebar.
func formatRow(c store.Card) string {
	title := padRunes(truncateRunes(emptyDash(c.Title), colTitle), colTitle)
	repo := padRunes(truncateRunes(emptyDash(c.RepoName), colRepo), colRepo)

	statusPlain := StatusIcon(c.Status) + " " + string(c.Status)
	status := padRunes(truncateRunes(statusPlain, colStatus), colStatus)

	age := padRunes(humanAge(c.UpdatedAt), colAge)

	fields := []string{
		c.ID,
		title,
		colorizeStatus(c.Status, status),
		colorize(repo, ansiCyan),
		colorize(age, ansiDim),
	}
	return strings.Join(fields, "\t")
}

// padRunes right-pads s with spaces to exactly n runes. No-op if already >= n.
func padRunes(s string, n int) string {
	r := utf8.RuneCountInString(s)
	if r >= n {
		return s
	}
	return s + strings.Repeat(" ", n-r)
}

// truncateRunes returns the first n runes of s, adding "…" when truncation
// occurred (and n >= 2). Pure rune-based so multibyte characters are safe.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n < 2 {
		r := []rune(s)
		return string(r[:n])
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func colorizeStatus(st store.Status, padded string) string {
	var color string
	switch st {
	case store.StatusWorking:
		color = ansiYellow
	case store.StatusIdle:
		color = ansiGreen
	case store.StatusNeedsInput:
		color = ansiMagenta + ansiBold
	case store.StatusErrored:
		color = ansiRed + ansiBold
	case store.StatusPaused, store.StatusArchived:
		color = ansiDim
	}
	return colorize(padded, color)
}

// pickerHeader builds the two-line fzf --header string: a key-hint banner
// plus the column header row, with each column padded to the same width
// used by formatRow so the headers line up with the data columns.
func pickerHeader(withActions bool) string {
	cols := strings.Join([]string{
		padRunes("TITLE", colTitle),
		padRunes("STATUS", colStatus),
		padRunes("REPO", colRepo),
		"AGE",
	}, "\t")
	hint := "↑/↓ select · enter attach · ctrl-/ toggle details · esc quit"
	if withActions {
		hint = "↑/↓ select · enter attach · → archive · ← undo · ctrl-/ details · esc quit"
	}
	return hint + "\n" + cols
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func humanAge(ts int64) string {
	if ts == 0 {
		return "-"
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// paneSnippet returns the most recent *interesting* line of the tmux pane,
// stripped of ANSI codes and trimmed to 80 chars. "Interesting" excludes box
// drawing, single-character prompts, and other noise so the picker doesn't
// show random punctuation as the preview.
func paneSnippet(session string) string {
	if session == "" || !tmux.HasSession(session) {
		return ""
	}
	out, err := tmux.CapturePane(session)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := stripAnsi(strings.TrimSpace(lines[i]))
		if !isInterestingSnippet(l) {
			continue
		}
		if len([]rune(l)) > 80 {
			l = string([]rune(l)[:80])
		}
		return l
	}
	return ""
}

// isInterestingSnippet filters out lines that are pure punctuation, box
// drawing, or too short to be meaningful. A line is "interesting" if it has
// at least 3 alphanumeric runes — a heuristic that skips Claude Code's input
// box borders, `>` prompts, and status-bar separators while keeping actual
// output lines.
func isInterestingSnippet(s string) bool {
	if len(s) < 4 {
		return false
	}
	alnum := 0
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			alnum++
			if alnum >= 3 {
				return true
			}
		}
	}
	return false
}

func pickFzf(cards []store.Card, initialQuery string, withActions bool) (store.Card, PickAction, error) {
	var b strings.Builder
	byID := make(map[string]store.Card, len(cards))
	for _, c := range cards {
		b.WriteString(formatRow(c))
		b.WriteByte('\n')
		byID[c.ID] = c
	}

	// Preview: run `hive card {id}` on the highlighted row. Pass the current
	// executable via $HIVE_BIN so this works even when hive isn't on $PATH.
	previewCmd := `"$HIVE_BIN" card {1} 2>/dev/null || echo "(no details)"`

	// No --height → fzf uses the alt-screen (full-terminal takeover, prior
	// shell history restored on exit). With --border=rounded that'd look
	// heavy as a full-screen frame, so we drop the border too — the alt
	// screen's own clearing is enough separation.
	args := []string{
		"--ansi",
		"--delimiter=\t",
		"--with-nth=2..",
		"--layout=reverse",
		"--header", pickerHeader(withActions),
		"--preview", previewCmd,
		"--preview-window", "right:45%:wrap:border-left",
		"--bind", "ctrl-/:toggle-preview",
		"--prompt", "hive❯ ",
		"--pointer", "▶",
		"--marker", "◆",
		"--info", "inline-right",
	}
	if withActions {
		// --expect causes fzf to return the name of the matching key as the
		// first output line, followed by the selected row. We bind bare
		// Right/Left to archive/unarchive. Users who prefer to keep the
		// in-query text cursor can still drive navigation with Up/Down.
		args = append(args, "--expect", "right,left")
	}
	if initialQuery != "" {
		args = append(args, "--query", initialQuery)
	}

	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		exePath = "hive"
	}

	cmd := exec.Command("fzf", args...)
	cmd.Stdin = strings.NewReader(b.String())
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "HIVE_BIN="+exePath)

	out, err := cmd.Output()
	if err != nil {
		// fzf exits 130 when the user cancels.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 130 {
			return store.Card{}, "", ErrPickerCancelled
		}
		return store.Card{}, "", fmt.Errorf("fzf: %w", err)
	}

	raw := strings.TrimRight(string(out), "\n")
	if raw == "" {
		return store.Card{}, "", ErrPickerCancelled
	}

	// With --expect, the first line is the matched key (empty for Enter),
	// and the second is the selected row. Without, the whole thing is the
	// selected row.
	var action PickAction
	rowLine := raw
	if withActions {
		parts := strings.SplitN(raw, "\n", 2)
		if len(parts) == 0 {
			return store.Card{}, "", ErrPickerCancelled
		}
		action = PickAction(parts[0])
		if len(parts) < 2 {
			return store.Card{}, "", ErrPickerCancelled
		}
		rowLine = parts[1]
	}

	fields := strings.SplitN(rowLine, "\t", 2)
	id := fields[0]
	c, ok := byID[id]
	if !ok {
		return store.Card{}, "", fmt.Errorf("fzf returned unknown card id %q", id)
	}
	return c, action, nil
}

func pickNumbered(cards []store.Card, initialQuery string) (store.Card, error) {
	shown := cards
	if initialQuery != "" {
		if m := Matches(cards, initialQuery); len(m) > 0 {
			shown = m
		} else {
			fmt.Fprintf(os.Stderr, "(no match for %q — showing all cards)\n", initialQuery)
		}
	}

	fmt.Fprintln(os.Stderr, "Select a card (install fzf for a fuzzy picker):")
	for i, c := range shown {
		fmt.Fprintf(os.Stderr, "  %2d. %-30s  %s %s  [%s]  %s\n",
			i+1, truncateRunes(c.Title, 30), StatusIcon(c.Status), c.Status,
			emptyDash(c.RepoName), humanAge(c.UpdatedAt))
	}
	fmt.Fprint(os.Stderr, "> ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return store.Card{}, ErrPickerCancelled
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return store.Card{}, ErrPickerCancelled
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(shown) {
		return store.Card{}, fmt.Errorf("invalid selection %q", line)
	}
	return shown[n-1], nil
}

