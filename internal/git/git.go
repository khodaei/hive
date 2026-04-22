package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Layout describes how a repo is set up.
type Layout int

const (
	LayoutNormal Layout = iota
	LayoutBare          // bare repo with .bare/ directory and worktrees as siblings
)

// DetectLayout checks if a repo uses the bare worktree layout (.bare/ dir exists).
func DetectLayout(repoPath string) Layout {
	barePath := filepath.Join(repoPath, ".bare")
	info, err := os.Stat(barePath)
	if err == nil && info.IsDir() {
		return LayoutBare
	}
	return LayoutNormal
}

// RepoRoot returns the root directory for worktree operations.
// For bare repos, this is the parent of .bare/.
// For normal repos, this is the repo path itself.
func RepoRoot(repoPath string) string {
	layout := DetectLayout(repoPath)
	switch layout {
	case LayoutBare:
		// repoPath might be the root (containing .bare/) or a worktree inside it.
		// Walk up to find the directory containing .bare/
		dir := repoPath
		for {
			if _, err := os.Stat(filepath.Join(dir, ".bare")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return repoPath // shouldn't happen, fallback
			}
			dir = parent
		}
	default:
		return repoPath
	}
}

// Fetch runs git fetch in the repo.
func Fetch(repoPath string) error {
	gitDir := gitDirFlag(repoPath)
	args := []string{}
	if gitDir != "" {
		args = append(args, "-C", gitDir)
	} else {
		args = append(args, "-C", repoPath)
	}
	args = append(args, "fetch")

	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// DefaultBranch detects the default branch of the remote.
func DefaultBranch(repoPath string) (string, error) {
	gitDir := gitDirFlag(repoPath)
	args := []string{}
	if gitDir != "" {
		args = append(args, "-C", gitDir)
	} else {
		args = append(args, "-C", repoPath)
	}
	args = append(args, "symbolic-ref", "refs/remotes/origin/HEAD")

	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git symbolic-ref: %w (try running 'git remote set-head origin --auto')", err)
	}
	ref := strings.TrimSpace(string(out))
	// refs/remotes/origin/main -> main
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1], nil
}

// CreateWorktree creates a new worktree with a new branch based on a remote base branch.
func CreateWorktree(repoPath, branchName, baseBranch string) (string, error) {
	if branchName == "" {
		return "", fmt.Errorf("branch name cannot be empty")
	}
	if baseBranch == "" {
		return "", fmt.Errorf("base branch cannot be empty")
	}
	if strings.HasPrefix(branchName, "-") {
		return "", fmt.Errorf("branch name cannot start with '-'")
	}

	root := RepoRoot(repoPath)
	layout := DetectLayout(root)

	var worktreePath string
	var args []string

	switch layout {
	case LayoutBare:
		barePath := filepath.Join(root, ".bare")
		worktreePath = filepath.Join(root, sanitizeFolderName(branchName))
		args = []string{"-C", barePath, "worktree", "add",
			worktreePath, "-b", branchName, "origin/" + baseBranch}
	default:
		worktreePath = filepath.Join(root, sanitizeFolderName(branchName))
		args = []string{"-C", root, "worktree", "add",
			worktreePath, "-b", branchName, "origin/" + baseBranch}
	}

	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return worktreePath, nil
}

// RemoveWorktree removes a git worktree.
func RemoveWorktree(repoPath, worktreePath string) error {
	root := RepoRoot(repoPath)
	gitDir := gitDirFlag(root)
	args := []string{}
	if gitDir != "" {
		args = append(args, "-C", gitDir)
	} else {
		args = append(args, "-C", root)
	}
	args = append(args, "worktree", "remove", worktreePath)

	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// gitDirFlag returns the appropriate path for -C flag.
// For bare repos, returns the .bare directory. For normal repos, returns "".
func gitDirFlag(repoPath string) string {
	if DetectLayout(repoPath) == LayoutBare {
		return filepath.Join(repoPath, ".bare")
	}
	return ""
}

// sanitizeFolderName converts a branch name to a flat folder name.
// e.g., "feature/my-branch" -> "feature-my-branch"
func sanitizeFolderName(name string) string {
	return strings.ReplaceAll(name, "/", "-")
}
