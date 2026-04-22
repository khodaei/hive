package status

import (
	"testing"
	"time"
)

func TestNeedsInput(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()

	tests := []struct {
		name    string
		content string
	}{
		{"y/N prompt", "Some output\n[y/N] "},
		{"Y/n prompt", "Confirm? [Y/n]"},
		{"allow tool", "Allow this tool to run? (y/n)"},
		{"approve edit", "Approve this edit? (y/n)"},
		{"approve command", "Approve this command? (y/n)"},
		{"approve action", "Approve this action? (y/n)"},
		{"numbered choice", "❯ 1. Yes"},
		{"numbered choice with space", "❯  1.  Yes"},
		{"do you want to proceed", "Do you want to proceed with these changes?"},
		{"allow prompt", "? Allow read access to file"},
		{"yes option", "(y)es to continue"},
		{"press enter", "Press Enter to continue"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use unique card ID per test to avoid state interference
			got := c.Classify("card-"+tt.name, tt.content, now)
			if got != NeedsInput {
				t.Errorf("expected NeedsInput for %q, got %s", tt.content, got)
			}
		})
	}
}

func TestErrored(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()

	tests := []struct {
		name    string
		content string
	}{
		{"traceback", "Traceback (most recent call last):\n  File \"test.py\""},
		{"command not found", "zsh: claude: command not found"},
		{"process exited", "[process exited with code 1]"},
		{"process exited code 2", "[process exited with code 2]"},
		{"panic", "panic: runtime error: index out of range"},
		{"fatal error", "Error: fatal: unable to access"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Classify("card-err-"+tt.name, tt.content, now)
			if got != Errored {
				t.Errorf("expected Errored for %q, got %s", tt.content, got)
			}
		})
	}
}

func TestWorking_ContentChanged(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()

	// First call with some content
	got := c.Classify("card1", "compiling...", now)
	if got != Working {
		t.Errorf("first classify: expected Working, got %s", got)
	}

	// Second call with different content
	got = c.Classify("card1", "compiling... done\nrunning tests", now.Add(1*time.Second))
	if got != Working {
		t.Errorf("changed content: expected Working, got %s", got)
	}
}

func TestWorking_RecentChange(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()

	// First call
	c.Classify("card1", "some output", now)

	// Same content but within idle threshold — still working
	got := c.Classify("card1", "some output", now.Add(3*time.Second))
	if got != Working {
		t.Errorf("within threshold: expected Working, got %s", got)
	}
}

func TestIdle(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()

	// First call to set baseline
	c.Classify("card1", "╭──────────────────╮\n> ", now)

	// Same content, past idle threshold, with prompt indicator
	got := c.Classify("card1", "╭──────────────────╮\n> ", now.Add(10*time.Second))
	if got != Idle {
		t.Errorf("stable with prompt: expected Idle, got %s", got)
	}
}

func TestIdlePatterns(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()
	past := now.Add(-10 * time.Second)

	tests := []struct {
		name    string
		content string
	}{
		{"input box border", "╭──────────────────────────────╮"},
		{"bare prompt", "some output\n> "},
		{"fish prompt", "some output\n❯ "},
		{"bash prompt", "user@host:~$ "},
		{"what would you like", "What would you like to do?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cardID := "idle-" + tt.name
			// Set initial state in the past
			c.Classify(cardID, tt.content, past)
			// Now classify again with same content, well past threshold
			got := c.Classify(cardID, tt.content, now)
			if got != Idle {
				t.Errorf("expected Idle for %q, got %s", tt.content, got)
			}
		})
	}
}

func TestUnknown(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()
	past := now.Add(-10 * time.Second)

	// Content that matches no patterns and is stable
	c.Classify("card1", "random gibberish with no prompt", past)
	got := c.Classify("card1", "random gibberish with no prompt", now)
	if got != Unknown {
		t.Errorf("expected Unknown, got %s", got)
	}
}

func TestPriority_NeedsInputOverIdle(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()
	past := now.Add(-10 * time.Second)

	// Content that matches both needs_input and idle patterns
	content := "╭──────────────────╮\nDo you want to proceed? [y/N]\n> "

	c.Classify("card1", content, past)
	got := c.Classify("card1", content, now)
	if got != NeedsInput {
		t.Errorf("needs_input should take priority over idle, got %s", got)
	}
}

func TestPriority_NeedsInputOverErrored(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()

	// Content that matches both needs_input and errored
	content := "Error: fatal error occurred\nDo you want to proceed? [y/N]"
	got := c.Classify("card1", content, now)
	if got != NeedsInput {
		t.Errorf("needs_input should take priority over errored, got %s", got)
	}
}

func TestReset(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()

	c.Classify("card1", "some content", now)

	if _, ok := c.states["card1"]; !ok {
		t.Fatal("expected state for card1")
	}

	c.Reset("card1")

	if _, ok := c.states["card1"]; ok {
		t.Error("expected state to be removed after Reset")
	}
}

func TestExitCodeZeroNotErrored(t *testing.T) {
	c := New(5 * time.Second)
	now := time.Now()

	// Exit code 0 should NOT match errored pattern
	content := "[process exited with code 0]"
	got := c.Classify("card1", content, now)
	if got == Errored {
		t.Error("exit code 0 should not be classified as errored")
	}
}
