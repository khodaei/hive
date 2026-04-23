package transcripts

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// Recap is Claude Code's own structured summary of a session. Claude writes
// `type=system, subtype=away_summary` entries to the JSONL when the session
// goes idle; the content is a 1–3 sentence recap covering goal, progress,
// and next step. Much higher quality than anything a small local model could
// produce — and free, since it's already on disk.
type Recap struct {
	Content string    // free-form prose — sometimes a paragraph, sometimes bulleted
	At      time.Time // the away_summary's own timestamp
	// SourcePath is the transcript .jsonl the recap came from; useful for
	// freshness checks against os.Stat(SourcePath).ModTime().
	SourcePath string
}

// LatestRecap scans the newest transcript for the most recent
// `type=system, subtype=away_summary` entry and returns it. Returns
// (nil, nil) when no transcript exists OR no recap has been written yet —
// those are "no data" states, not errors.
func LatestRecap(worktreePath string) (*Recap, error) {
	paths, err := ListTranscripts(worktreePath)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	path := paths[0]

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	var latest *Recap
	for scanner.Scan() {
		var obj map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &obj); err != nil {
			continue
		}
		if obj["type"] != "system" || obj["subtype"] != "away_summary" {
			continue
		}
		content, _ := obj["content"].(string)
		if content == "" {
			continue
		}
		r := &Recap{Content: content, SourcePath: path}
		if tsStr, ok := obj["timestamp"].(string); ok {
			if t, perr := time.Parse(time.RFC3339Nano, tsStr); perr == nil {
				r.At = t
			}
		}
		latest = r
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return latest, nil
}
