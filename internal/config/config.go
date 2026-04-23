package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Repo struct {
	Name          string `yaml:"name"`
	Path          string `yaml:"path"`
	DefaultBranch string `yaml:"default_branch"`
	SetupScript   string `yaml:"setup_script,omitempty"`
}

type QuietHours struct {
	Start string `yaml:"start"` // "22:00" format
	End   string `yaml:"end"`   // "07:00" format
}

type Notifications struct {
	Enabled        bool       `yaml:"enabled"`
	OnNeedsInput   bool       `yaml:"on_needs_input"`
	OnErrored      bool       `yaml:"on_errored"`
	OnIdle         bool       `yaml:"on_idle"`
	IdleTooLongMin int        `yaml:"idle_too_long_min"` // 0 = off
	QuietHours     QuietHours `yaml:"quiet_hours"`
}

type Config struct {
	Repos             []Repo        `yaml:"repos"`
	PollIntervalSec   int           `yaml:"poll_interval_sec"`
	IdleThresholdSec  int           `yaml:"idle_threshold_sec"`
	ClaudeCmd         string        `yaml:"claude_cmd"`
	TmuxPrefix        string        `yaml:"tmux_session_prefix"`
	ArchiveBehavior   string        `yaml:"archive_behavior"`
	LogMaxSizeMB      int           `yaml:"log_max_size_mb"`
	MaxCostPerSession float64       `yaml:"max_cost_per_session"` // 0 = disabled
	CostAlertsEnabled bool          `yaml:"cost_alerts_enabled"`
	BranchPrefix      string        `yaml:"branch_prefix"` // e.g. "amir/"
	Notifications     Notifications `yaml:"notifications"`
}

func DefaultConfig() Config {
	return Config{
		Repos:            []Repo{},
		PollIntervalSec:  3,
		IdleThresholdSec: 5,
		ClaudeCmd:        "claude",
		TmuxPrefix:       "hv_",
		Notifications: Notifications{
			Enabled:      true,
			OnNeedsInput: true,
			OnErrored:    true,
			OnIdle:       false,
		},
	}
}

// Dir returns the hive config directory (~/.hive).
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".hive"), nil
}

// Load reads the config from ~/.hive/config.yaml.
// If the file doesn't exist, it creates it with defaults.
func Load() (Config, error) {
	dir, err := Dir()
	if err != nil {
		return Config{}, err
	}

	path := filepath.Join(dir, "config.yaml")

	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg := DefaultConfig()
		if err := ensureDir(dir); err != nil {
			return Config{}, err
		}
		if err := writeConfig(path, cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	applyDefaults(&cfg)

	if err := validate(cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// LoadFrom reads config from a specific path (useful for testing).
func LoadFrom(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	applyDefaults(&cfg)

	if err := validate(cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.PollIntervalSec <= 0 {
		cfg.PollIntervalSec = 3
	}
	if cfg.IdleThresholdSec <= 0 {
		cfg.IdleThresholdSec = 5
	}
	if cfg.ClaudeCmd == "" {
		cfg.ClaudeCmd = "claude"
	}
	// Resolve claude command to absolute path so it works inside tmux
	// sessions that may have a different PATH.
	if cfg.ClaudeCmd == "claude" {
		if resolved, err := exec.LookPath("claude"); err == nil {
			cfg.ClaudeCmd = resolved
		}
	}
	if cfg.TmuxPrefix == "" {
		cfg.TmuxPrefix = "hv_"
	}
	if cfg.ArchiveBehavior == "" {
		cfg.ArchiveBehavior = "prompt"
	}
	if cfg.LogMaxSizeMB <= 0 {
		cfg.LogMaxSizeMB = 10
	}
}

func validate(cfg Config) error {
	if cfg.PollIntervalSec <= 0 {
		return fmt.Errorf("poll_interval_sec must be > 0")
	}
	switch cfg.ArchiveBehavior {
	case "keep", "prompt", "delete":
		// valid
	default:
		return fmt.Errorf("archive_behavior must be keep, prompt, or delete (got %q)", cfg.ArchiveBehavior)
	}
	return nil
}

// RepoValidationError describes a repo that failed validation.
type RepoValidationError struct {
	Name   string
	Path   string
	Reason string
}

func (e RepoValidationError) Error() string {
	return fmt.Sprintf("repo %q (%s): %s", e.Name, e.Path, e.Reason)
}

// ValidateRepos checks that all configured repo paths exist and are git working trees.
// Returns all errors at once (not fail-fast) so the user can fix them all in one pass.
func ValidateRepos(cfg Config) []RepoValidationError {
	var errs []RepoValidationError
	for _, r := range cfg.Repos {
		if r.Name == "" {
			errs = append(errs, RepoValidationError{Name: "(empty)", Path: r.Path, Reason: "repo name is empty"})
			continue
		}
		if r.Path == "" {
			errs = append(errs, RepoValidationError{Name: r.Name, Path: "(empty)", Reason: "repo path is empty"})
			continue
		}
		info, err := os.Stat(r.Path)
		if err != nil {
			errs = append(errs, RepoValidationError{Name: r.Name, Path: r.Path, Reason: "path does not exist"})
			continue
		}
		if !info.IsDir() {
			errs = append(errs, RepoValidationError{Name: r.Name, Path: r.Path, Reason: "path is not a directory"})
			continue
		}
		// Check if it's a git repo (handles both normal and bare worktree layouts)
		cmd := exec.Command("git", "-C", r.Path, "rev-parse", "--is-inside-work-tree")
		if err := cmd.Run(); err != nil {
			// Also check for bare layout (.bare directory)
			barePath := filepath.Join(r.Path, ".bare")
			if _, berr := os.Stat(barePath); berr != nil {
				errs = append(errs, RepoValidationError{Name: r.Name, Path: r.Path, Reason: "not a git repository"})
			}
		}
	}
	return errs
}

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func writeConfig(path string, cfg Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	header := []byte("# hive configuration\n# See SPEC.md for details on each field.\n\n")
	return os.WriteFile(path, append(header, data...), 0o644)
}
