package tmux

import (
	"os/exec"
	"testing"
)

func TestHasSessionNonExistent(t *testing.T) {
	// A session that definitely doesn't exist
	if HasSession("hive_test_nonexistent_session_12345") {
		t.Error("expected HasSession to return false for nonexistent session")
	}
}

func TestListSessions(t *testing.T) {
	// Should not error even if no tmux server is running
	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions should not error: %v", err)
	}
	// sessions may be nil (no server) or a list — both are valid
	_ = sessions
}

func TestAttachCommandShape(t *testing.T) {
	cmd := AttachCommand("test-session")
	if cmd.Path == "" {
		t.Error("expected non-empty command path")
	}
	args := cmd.Args
	// Should be: tmux attach-session -d -t test-session
	found := false
	for _, a := range args {
		if a == "test-session" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected session name in args, got %v", args)
	}
}

func TestSessionCWDNonExistent(t *testing.T) {
	cwd, err := SessionCWD("hive_test_nonexistent_12345")
	// tmux may return empty string or error depending on server state
	if err == nil && cwd != "" {
		t.Errorf("expected empty CWD or error for nonexistent session, got %q", cwd)
	}
}

func TestNewSessionAndCleanup(t *testing.T) {
	// Skip if tmux is not available
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	name := "hive_test_qa_session"

	// Clean up in case a previous test left it
	KillSession(name)

	err := NewSession(name, "/tmp")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer KillSession(name)

	if !HasSession(name) {
		t.Error("expected session to exist after NewSession")
	}

	cwd, err := SessionCWD(name)
	if err != nil {
		t.Fatalf("SessionCWD: %v", err)
	}
	// /tmp may resolve to /private/tmp on macOS
	if cwd != "/tmp" && cwd != "/private/tmp" {
		t.Errorf("expected CWD /tmp or /private/tmp, got %q", cwd)
	}

	content, err := CapturePane(name)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	_ = content // just check it doesn't error

	err = KillSession(name)
	if err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	if HasSession(name) {
		t.Error("expected session to be gone after KillSession")
	}
}
