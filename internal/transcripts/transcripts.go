package transcripts

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Turn is one round of a Claude Code conversation as we parse it from JSONL.
// Only user / assistant turns with non-empty text payloads are emitted.
type Turn struct {
	Role string // "user" or "assistant"
	Text string // concatenated plain-text content, trimmed
}

// LastTurns returns up to n of the most recent user/assistant turns from the
// newest transcript for worktreePath. Tool-use blocks are skipped. Turns are
// returned oldest-first so callers can print them in chronological order.
// Empty slice (nil error) when no transcripts exist.
func LastTurns(worktreePath string, n int) ([]Turn, error) {
	if n <= 0 {
		return nil, nil
	}
	paths, err := ListTranscripts(worktreePath)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	return parseTurns(paths[0], n)
}

func parseTurns(path string, n int) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Use a ring buffer to keep only the last n emitted turns; avoids holding
	// the entire transcript in memory for long-running sessions.
	ring := make([]Turn, 0, n+1)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var obj map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &obj); err != nil {
			continue
		}
		t, ok := extractTurn(obj)
		if !ok {
			continue
		}
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, t)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return ring, nil
}

// extractTurn pulls a Turn out of a single JSONL line if it encodes a
// user/assistant message. Handles both the bare {"role","content"} shape
// and Claude Code's {"type":"...", "message":{...}} wrapper shape.
func extractTurn(obj map[string]any) (Turn, bool) {
	// Unwrap {"message": {...}} envelopes.
	if msg, ok := obj["message"].(map[string]any); ok {
		obj = msg
	}
	role, _ := obj["role"].(string)
	if role != "user" && role != "assistant" {
		return Turn{}, false
	}

	var text string
	switch c := obj["content"].(type) {
	case string:
		text = c
	case []any:
		var parts []string
		for _, item := range c {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			// Skip tool_use / tool_result blocks — they're noisy in a summary.
			if t, _ := m["type"].(string); t != "" && t != "text" {
				continue
			}
			if s, ok := m["text"].(string); ok {
				parts = append(parts, s)
			}
		}
		text = strings.Join(parts, " ")
	}

	// Collapse whitespace so a multi-line prompt fits on one row.
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if text == "" {
		return Turn{}, false
	}
	return Turn{Role: role, Text: text}, true
}

// FindSessionID looks for the most recent Claude Code session ID
// associated with a given worktree path.
func FindSessionID(worktreePath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}

	// Claude Code stores projects under ~/.claude/projects/<encoded-path>/
	encoded := encodePath(worktreePath)
	projectDir := filepath.Join(home, ".claude", "projects", encoded)

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", fmt.Errorf("read project dir: %w", err)
	}

	// Find .jsonl files, sorted by modification time (newest first)
	type fileEntry struct {
		path    string
		modTime int64
	}
	var jsonlFiles []fileEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			info, err := e.Info()
			if err != nil {
				continue
			}
			jsonlFiles = append(jsonlFiles, fileEntry{
				path:    filepath.Join(projectDir, e.Name()),
				modTime: info.ModTime().Unix(),
			})
		}
	}

	if len(jsonlFiles) == 0 {
		return "", fmt.Errorf("no .jsonl files found in %s", projectDir)
	}

	sort.Slice(jsonlFiles, func(i, j int) bool {
		return jsonlFiles[i].modTime > jsonlFiles[j].modTime
	})

	// Try to extract session ID from the most recent file
	return extractSessionID(jsonlFiles[0].path)
}

// ListTranscripts returns the absolute paths of all .jsonl transcript files
// Claude Code has saved for the given worktree path, newest first. Missing
// directories are not errors — they just return an empty slice.
func ListTranscripts(worktreePath string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".claude", "projects", encodePath(worktreePath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type fe struct {
		path    string
		modTime int64
	}
	var files []fe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fe{
			path:    filepath.Join(dir, e.Name()),
			modTime: info.ModTime().Unix(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime > files[j].modTime })
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.path
	}
	return out, nil
}

// encodePath converts an absolute path to the encoding Claude Code uses.
// Claude Code replaces every / with - (including the leading one).
// e.g., /Users/amir/code/project -> -Users-amir-code-project
func encodePath(p string) string {
	p = filepath.Clean(p)
	return strings.ReplaceAll(p, "/", "-")
}

// extractSessionID reads a JSONL file and looks for a session UUID.
func extractSessionID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		// Look for sessionId or session_id field
		for _, key := range []string{"sessionId", "session_id", "uuid"} {
			if v, ok := entry[key]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s, nil
				}
			}
		}
	}

	// If no session ID found in content, use the filename (without .jsonl)
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".jsonl"), nil
}
