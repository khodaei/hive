package cli

import "testing"

func TestIsInterestingSnippet(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Noise the original snippet used to leak into the picker.
		{"", false},
		{">", false},
		{")", false},
		{"│ >", false},
		{"╭────────────╮", false},
		{"╰────────────╯", false},
		{"···", false},
		{"(y/N)", false},  // 1 alnum char only

		// Real content lines.
		{"Reading file internal/cli/picker.go", true},
		{"GCP: akhodaei@snapchat.com", true},
		{"Error: tmux session not found", true},
		{"hi there friend", true},
		{"1 + 2 = 3 is arithmetic", true},
	}
	for _, c := range cases {
		if got := isInterestingSnippet(c.in); got != c.want {
			t.Errorf("isInterestingSnippet(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestStripAnsi(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b[1;32mgreen bold\x1b[0m text", "green bold text"},
		{"no codes here", "no codes here"},
	}
	for _, c := range cases {
		if got := stripAnsi(c.in); got != c.want {
			t.Errorf("stripAnsi(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
