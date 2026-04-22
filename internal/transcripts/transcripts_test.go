package transcripts

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestEncodePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/amir/code/project", "-Users-amir-code-project"},
		{"/Users/amir/my-project/feature-x", "-Users-amir-my-project-feature-x"},
		{"/tmp/test", "-tmp-test"},
		{"/a/b/c/d", "-a-b-c-d"},
	}

	for _, tt := range tests {
		got := encodePath(tt.input)
		if got != tt.want {
			t.Errorf("encodePath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseTurns(t *testing.T) {
	lines := []string{
		// Bare role/content (string content).
		`{"role":"user","content":"hello"}`,
		// Wrapper shape with structured content blocks.
		`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]}}`,
		// Mixed block: text + tool_use. Tool block is dropped.
		`{"role":"assistant","content":[{"type":"text","text":"let me check"},{"type":"tool_use","name":"Bash","input":{}}]}`,
		// Non-chat line. Skipped.
		`{"type":"summary","text":"ignore me"}`,
		// Empty content. Skipped.
		`{"role":"user","content":""}`,
		// Whitespace-collapsing.
		`{"role":"user","content":"line one\n\n   line two"}`,
	}

	path := writeLines(t, lines)
	got, err := parseTurns(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []Turn{
		{Role: "user", Text: "hello"},
		{Role: "assistant", Text: "hi there"},
		{Role: "assistant", Text: "let me check"},
		{Role: "user", Text: "line one line two"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d turns, want %d: %+v", len(got), len(want), got)
	}
	for i, tr := range got {
		if tr != want[i] {
			t.Errorf("turn %d: got %+v, want %+v", i, tr, want[i])
		}
	}
}

func TestParseTurns_RingKeepsLastN(t *testing.T) {
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, `{"role":"user","content":"msg`+strconv.Itoa(i)+`"}`)
	}
	path := writeLines(t, lines)
	got, err := parseTurns(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d turns, want 3", len(got))
	}
	want := []string{"msg9", "msg10", "msg11"}
	for i, tr := range got {
		if tr.Text != want[i] {
			t.Errorf("turn %d: got %q, want %q", i, tr.Text, want[i])
		}
	}
}

func writeLines(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
