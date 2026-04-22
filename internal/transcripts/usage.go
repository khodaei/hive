package transcripts

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/khodaei/hive/internal/cost"
)

// jsonEntry is the minimal structure we parse from each JSONL line.
type jsonEntry struct {
	Type    string    `json:"type"`
	Message *msgEntry `json:"message,omitempty"`
}

type msgEntry struct {
	Role  string      `json:"role"`
	Model string      `json:"model"`
	Usage *usageEntry `json:"usage,omitempty"`
}

type usageEntry struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// ReadUsage reads a JSONL transcript file starting from the given byte offset,
// returns aggregated usage and the new offset for next incremental read.
func ReadUsage(path string, offset int64) (cost.Usage, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return cost.Usage{}, offset, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	// Seek to offset
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return cost.Usage{}, offset, fmt.Errorf("seek: %w", err)
		}
	}

	var usage cost.Usage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	// Track offset manually because bufio.Scanner reads ahead
	bytesRead := int64(0)

	for scanner.Scan() {
		line := scanner.Bytes()
		bytesRead += int64(len(line)) + 1 // +1 for \n

		var entry jsonEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed lines
		}

		if entry.Type != "assistant" || entry.Message == nil {
			continue
		}
		if entry.Message.Role != "assistant" || entry.Message.Usage == nil {
			continue
		}

		u := entry.Message.Usage
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		usage.CacheCreationTokens += u.CacheCreationInputTokens
		usage.CacheReadTokens += u.CacheReadInputTokens
		usage.AssistantTurns++

		if entry.Message.Model != "" {
			usage.Model = entry.Message.Model
		}
	}

	if err := scanner.Err(); err != nil {
		return usage, offset, fmt.Errorf("scan: %w", err)
	}

	return usage, offset + bytesRead, nil
}

// ReadUsageForWorktree finds the transcript file for a worktree and reads usage.
func ReadUsageForWorktree(worktreePath string, offset int64) (cost.Usage, int64, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return cost.Usage{}, offset, err
	}

	encoded := encodePath(worktreePath)
	projectDir := fmt.Sprintf("%s/.claude/projects/%s", home, encoded)

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return cost.Usage{}, offset, err
	}

	// Find JSONL files
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 6 && e.Name()[len(e.Name())-6:] == ".jsonl" {
			path := projectDir + "/" + e.Name()
			return ReadUsage(path, offset)
		}
	}

	return cost.Usage{}, offset, fmt.Errorf("no transcript found for %s", worktreePath)
}
