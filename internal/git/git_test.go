package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "test-repo")

	cmds := [][]string{
		{"git", "init", repo},
		{"git", "-C", repo, "config", "user.email", "test@test.com"},
		{"git", "-C", repo, "config", "user.name", "Test"},
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup cmd %v: %s: %v", args, out, err)
		}
	}

	// Create an initial commit so we have a branch
	dummy := filepath.Join(repo, "README.md")
	if err := os.WriteFile(dummy, []byte("# test"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "add", ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %s: %v", out, err)
	}
	cmd = exec.Command("git", "-C", repo, "commit", "-m", "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %s: %v", out, err)
	}

	return repo
}

func TestDetectLayoutNormal(t *testing.T) {
	repo := setupTestRepo(t)
	if got := DetectLayout(repo); got != LayoutNormal {
		t.Errorf("expected LayoutNormal, got %v", got)
	}
}

func TestDetectLayoutBare(t *testing.T) {
	dir := t.TempDir()
	bareDir := filepath.Join(dir, ".bare")
	if err := os.MkdirAll(bareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DetectLayout(dir); got != LayoutBare {
		t.Errorf("expected LayoutBare, got %v", got)
	}
}

func TestSanitizeFolderName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"my-feature", "my-feature"},
		{"feature/my-branch", "feature-my-branch"},
		{"a/b/c", "a-b-c"},
	}
	for _, tt := range tests {
		if got := sanitizeFolderName(tt.input); got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRepoRoot(t *testing.T) {
	repo := setupTestRepo(t)
	// Normal repo: root is itself
	if got := RepoRoot(repo); got != repo {
		t.Errorf("RepoRoot(%q) = %q, want %q", repo, got, repo)
	}
}
