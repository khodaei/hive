package transcripts

import (
	"strings"
	"testing"
)

func TestTruncateTurn(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 10, ""},
		{"short", 10, "short"},
		{"exactly10c", 10, "exactly10c"},
		{"this is longer than ten", 10, "this is lo…"},
		// Multibyte runes must never be sliced mid-codepoint.
		{"héllo wörld!", 5, "héllo…"},
		{"αβγδεζηθ", 4, "αβγδ…"},
		// Non-positive limits disable truncation.
		{"unchanged", 0, "unchanged"},
		{"unchanged", -1, "unchanged"},
	}
	for _, c := range cases {
		if got := truncateTurn(c.in, c.n); got != c.want {
			t.Errorf("truncateTurn(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestBuildPrompt(t *testing.T) {
	p := buildPrompt([]Turn{
		{Role: "user", Text: "add a /healthz endpoint"},
		{Role: "assistant", Text: "I'll add a handler in routes.go and wire it up."},
		{Role: "user", Text: "also return the git sha"},
	})

	// Instructions the model MUST see — their absence would regress summary
	// quality silently.
	mustHave := []string{
		`terse engineering summarizer`,
		`Goal:`,
		`Progress:`,
		`Next:`,
		`--- transcript ---`,
		`--- end ---`,
		`Summary:`,
		// Role labels rendered in the transcript block.
		"user: add a /healthz endpoint",
		"claude: I'll add a handler in routes.go and wire it up.",
		"user: also return the git sha",
	}
	for _, s := range mustHave {
		if !strings.Contains(p, s) {
			t.Errorf("prompt missing %q", s)
		}
	}

	// Prompt must end with the generation anchor so the model keeps going
	// right after "Summary:" rather than repeating the instructions.
	if !strings.HasSuffix(p, "Summary:") {
		t.Errorf("prompt should end with 'Summary:', ends with: %q", lastLine(p))
	}
}

func TestBuildPrompt_TruncatesLongTurns(t *testing.T) {
	long := strings.Repeat("x", 5000)
	p := buildPrompt([]Turn{{Role: "user", Text: long}})
	// Full turn (5000 chars) must not appear verbatim — should be truncated.
	if strings.Contains(p, long) {
		t.Fatalf("long turn was not truncated")
	}
	// Truncation marker should be present.
	if !strings.Contains(p, "…") {
		t.Errorf("expected ellipsis marker in truncated prompt")
	}
}

func lastLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
