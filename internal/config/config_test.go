package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLoadFrom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := `
repos:
  - name: test-repo
    path: /tmp/test-repo
    default_branch: main
poll_interval_sec: 5
idle_threshold_sec: 10
claude_cmd: claude
tmux_session_prefix: hv_
notifications:
  enabled: true
  on_needs_input: true
  on_errored: false
  on_idle: false
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(cfg.Repos))
	}
	if cfg.Repos[0].Name != "test-repo" {
		t.Errorf("expected repo name 'test-repo', got %q", cfg.Repos[0].Name)
	}
	if cfg.PollIntervalSec != 5 {
		t.Errorf("expected poll_interval_sec 5, got %d", cfg.PollIntervalSec)
	}
	if cfg.Notifications.OnErrored != false {
		t.Errorf("expected on_errored false")
	}
}

func TestApplyDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Minimal config — defaults should fill in
	content := `
repos: []
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.PollIntervalSec != 3 {
		t.Errorf("expected default poll_interval_sec 3, got %d", cfg.PollIntervalSec)
	}
	// ClaudeCmd may be resolved to an absolute path if 'claude' is on PATH
	if cfg.ClaudeCmd == "" {
		t.Error("expected non-empty claude_cmd")
	}
	if cfg.TmuxPrefix != "hv_" {
		t.Errorf("expected default tmux_session_prefix 'hv_', got %q", cfg.TmuxPrefix)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.PollIntervalSec != 3 {
		t.Errorf("expected poll_interval_sec 3, got %d", cfg.PollIntervalSec)
	}
	if cfg.TmuxPrefix != "hv_" {
		t.Errorf("expected tmux prefix 'hv_', got %q", cfg.TmuxPrefix)
	}
	if !cfg.Notifications.Enabled {
		t.Error("expected notifications enabled by default")
	}
}

func TestApplyDefaultsV11(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `repos: []`
	os.WriteFile(path, []byte(content), 0o644)

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ArchiveBehavior != "prompt" {
		t.Errorf("expected default archive_behavior 'prompt', got %q", cfg.ArchiveBehavior)
	}
	if cfg.LogMaxSizeMB != 10 {
		t.Errorf("expected default log_max_size_mb 10, got %d", cfg.LogMaxSizeMB)
	}
}

func TestValidateArchiveBehavior(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
repos: []
archive_behavior: invalid
`
	os.WriteFile(path, []byte(content), 0o644)

	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected error for invalid archive_behavior")
	}
}

func TestValidateRepos(t *testing.T) {
	// Create a real git repo for the valid case
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "valid-repo")
	exec.Command("git", "init", repoPath).Run()

	cfg := Config{
		Repos: []Repo{
			{Name: "valid", Path: repoPath, DefaultBranch: "main"},
			{Name: "missing", Path: "/nonexistent/path", DefaultBranch: "main"},
			{Name: "", Path: "/whatever", DefaultBranch: "main"},
			{Name: "no-path", Path: "", DefaultBranch: "main"},
		},
	}

	errs := ValidateRepos(cfg)
	if len(errs) != 3 {
		t.Fatalf("expected 3 validation errors, got %d: %v", len(errs), errs)
	}

	// Valid repo should not be in errors
	for _, e := range errs {
		if e.Name == "valid" {
			t.Errorf("valid repo should not have an error: %v", e)
		}
	}
}
