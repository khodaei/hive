package transcripts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrNoTranscript is returned when the worktree has no Claude transcript yet.
var ErrNoTranscript = errors.New("no transcript for worktree")

// SummaryOpts configures a single summarize call.
type SummaryOpts struct {
	OllamaURL   string        // e.g. http://localhost:11434
	Model       string        // e.g. llama3.2:3b
	TurnsWindow int           // how many recent turns to feed the model
	MaxTokens   int           // hard cap on generation length
	Timeout     time.Duration // HTTP timeout for the /api/generate call
}

// SummaryResult is what Summarize returns on success.
type SummaryResult struct {
	Text            string // model output, trimmed
	TranscriptPath  string // source file used
	TranscriptMtime int64  // mtime (Unix sec) of TranscriptPath — cache key
}

// Summarize feeds the most recent turns from the newest transcript into an
// Ollama model and returns the resulting short summary. Returns
// ErrNoTranscript if the worktree has no .jsonl yet.
func Summarize(ctx context.Context, worktreePath string, opts SummaryOpts) (*SummaryResult, error) {
	if opts.OllamaURL == "" {
		return nil, errors.New("summary: ollama_url is empty")
	}
	if opts.Model == "" {
		return nil, errors.New("summary: model is empty")
	}
	if opts.TurnsWindow <= 0 {
		opts.TurnsWindow = 20
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 120
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}

	paths, err := ListTranscripts(worktreePath)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, ErrNoTranscript
	}
	path := paths[0]

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat transcript: %w", err)
	}
	mtime := info.ModTime().Unix()

	turns, err := parseTurns(path, opts.TurnsWindow)
	if err != nil {
		return nil, err
	}
	if len(turns) == 0 {
		return nil, ErrNoTranscript
	}

	text, err := callOllama(ctx, opts, buildPrompt(turns))
	if err != nil {
		return nil, err
	}

	return &SummaryResult{
		Text:            text,
		TranscriptPath:  path,
		TranscriptMtime: mtime,
	}, nil
}

// buildPrompt renders a compact chat transcript for the summarizer. Kept
// short and explicit about the output shape so a small model (3B) stays on
// task and produces something we can post-process reliably.
func buildPrompt(turns []Turn) string {
	// Raw strings can't contain backticks, so we compose the instruction
	// block from a few pieces — the visible output format remains stable.
	tick := "`"
	instructions := "You are a terse engineering summarizer. Read the chat between a developer and Claude Code below and reply in EXACTLY this format, nothing else:\n\n" +
		"Goal: <one line, ≤15 words — the user's overall objective>\n" +
		"Progress:\n" +
		"- <bullet, ≤18 words>\n" +
		"- <bullet, ≤18 words>\n" +
		"Next: <one line, ≤15 words — the open question or next step, or \"done\" if nothing pending>\n\n" +
		"Rules:\n" +
		"- Start lines with \"Goal:\", \"Progress:\", or \"Next:\" exactly (capitalised, with the colon).\n" +
		"- Progress has 1–3 bullets, each starting with \"- \" (hyphen + space).\n" +
		"- Wrap code, paths, and identifiers in backticks (e.g. " + tick + "src/main.go" + tick + ", " + tick + "myFunc()" + tick + ").\n" +
		"- No preamble, no praise, no headers beyond those three labels.\n\n" +
		"--- transcript ---\n"

	var b strings.Builder
	b.WriteString(instructions)
	for _, t := range turns {
		who := "user"
		if t.Role == "assistant" {
			who = "claude"
		}
		b.WriteString(who)
		b.WriteString(": ")
		b.WriteString(truncateTurn(t.Text, 1200))
		b.WriteString("\n")
	}
	b.WriteString("--- end ---\nSummary:")
	return b.String()
}

// truncateTurn caps a single turn's text at maxRunes, breaking on a valid
// rune boundary (never mid-codepoint) and appending a single ellipsis when
// truncation occurred. Empty input passes through unchanged.
func truncateTurn(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	// Walk forward counting runes; when we pass maxRunes, snip there.
	seen := 0
	for i := range s {
		if seen == maxRunes {
			return s[:i] + "…"
		}
		seen++
	}
	return s
}

// ollamaRequest is the subset of Ollama's /api/generate body we care about.
type ollamaRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

func callOllama(ctx context.Context, opts SummaryOpts, prompt string) (string, error) {
	body, err := json.Marshal(ollamaRequest{
		Model:  opts.Model,
		Prompt: prompt,
		Stream: false,
		Options: map[string]any{
			"num_predict": opts.MaxTokens,
			"temperature": 0.2, // tighter summaries
		},
	})
	if err != nil {
		return "", err
	}

	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	url := strings.TrimRight(opts.OllamaURL, "/") + "/api/generate"
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: opts.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ollama read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out ollamaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("ollama decode: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("ollama: %s", out.Error)
	}
	text := strings.TrimSpace(out.Response)
	if text == "" {
		return "", errors.New("ollama returned empty response")
	}
	return text, nil
}
