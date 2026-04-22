package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/khodaei/hive/internal/config"
)

// Template defines a reusable session configuration.
type Template struct {
	Name          string `yaml:"name"`
	RepoName      string `yaml:"repo_name"`
	BranchFrom    string `yaml:"branch_from,omitempty"`
	InitialPrompt string `yaml:"initial_prompt"`
	SetupScript   string `yaml:"setup_script,omitempty"`
}

// Dir returns the templates directory (~/.hive/templates/).
func Dir() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "templates"), nil
}

// List returns all available templates.
func List() ([]Template, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var templates []Template
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		t, err := loadTemplate(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		templates = append(templates, t)
	}
	return templates, nil
}

// Get returns a template by name.
func Get(name string) (Template, error) {
	if err := validateName(name); err != nil {
		return Template{}, err
	}
	dir, err := Dir()
	if err != nil {
		return Template{}, err
	}

	path := filepath.Join(dir, name+".yaml")
	return loadTemplate(path)
}

// Save writes a template to disk.
func Save(t Template) error {
	if err := validateName(t.Name); err != nil {
		return err
	}
	dir, err := Dir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := yaml.Marshal(t)
	if err != nil {
		return err
	}

	path := filepath.Join(dir, t.Name+".yaml")
	return os.WriteFile(path, data, 0o644)
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("template name cannot be empty")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return fmt.Errorf("template name contains invalid characters: %q", name)
	}
	// Ensure the name only produces a filename in the templates dir
	if filepath.Base(name) != name {
		return fmt.Errorf("template name must be a simple filename: %q", name)
	}
	return nil
}

func loadTemplate(path string) (Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Template{}, fmt.Errorf("read template: %w", err)
	}

	var t Template
	if err := yaml.Unmarshal(data, &t); err != nil {
		return Template{}, fmt.Errorf("parse template: %w", err)
	}

	if t.Name == "" {
		base := filepath.Base(path)
		t.Name = strings.TrimSuffix(base, ".yaml")
	}

	return t, nil
}
