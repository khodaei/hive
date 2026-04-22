package templates

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "templates")
	os.MkdirAll(tmplDir, 0o755)

	// Write a template manually
	content := `
name: test-template
repo_name: my-repo
branch_from: main
initial_prompt: fix the bug
`
	os.WriteFile(filepath.Join(tmplDir, "test-template.yaml"), []byte(content), 0o644)

	tmpl, err := loadTemplate(filepath.Join(tmplDir, "test-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	if tmpl.Name != "test-template" {
		t.Errorf("expected name 'test-template', got %q", tmpl.Name)
	}
	if tmpl.RepoName != "my-repo" {
		t.Errorf("expected repo 'my-repo', got %q", tmpl.RepoName)
	}
	if tmpl.InitialPrompt != "fix the bug" {
		t.Errorf("expected prompt 'fix the bug', got %q", tmpl.InitialPrompt)
	}
}
