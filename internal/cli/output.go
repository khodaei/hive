package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/khodaei/hive/internal/store"
)

// OutputFormat selects how a list of cards is rendered.
type OutputFormat int

const (
	FormatTable OutputFormat = iota
	FormatTSV
	FormatJSON
)

// ListFilters narrow which cards are printed.
type ListFilters struct {
	Repo   string        // match on Card.RepoName
	Status string        // match on Card.Status
	Column string        // match on Card.ColumnID
	Since  time.Duration // cards with UpdatedAt within the last Since
}

// ApplyFilters returns the subset of cards matching every non-zero filter.
func ApplyFilters(cards []store.Card, f ListFilters) []store.Card {
	if f.Repo == "" && f.Status == "" && f.Column == "" && f.Since == 0 {
		return cards
	}
	cutoff := int64(0)
	if f.Since > 0 {
		cutoff = time.Now().Add(-f.Since).Unix()
	}
	out := make([]store.Card, 0, len(cards))
	for _, c := range cards {
		if f.Repo != "" && c.RepoName != f.Repo {
			continue
		}
		if f.Status != "" && string(c.Status) != f.Status {
			continue
		}
		if f.Column != "" && string(c.ColumnID) != f.Column {
			continue
		}
		if cutoff > 0 && c.UpdatedAt < cutoff {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ParseDuration is like time.ParseDuration but also accepts a "d" suffix for
// days (e.g. "7d", "2d12h"). Multi-unit strings are supported: the "d"
// component is stripped and the rest passed to time.ParseDuration.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var days time.Duration
	// Greedy: consume a leading "<num>d" prefix if present.
	if i := strings.Index(s, "d"); i > 0 {
		numStr := s[:i]
		var n int
		if _, err := fmt.Sscanf(numStr, "%d", &n); err == nil {
			days = time.Duration(n) * 24 * time.Hour
			s = s[i+1:]
		}
	}
	if s == "" {
		return days, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return days + d, nil
}

// StatusIcon returns a single-column glyph for a status, matching the TUI.
func StatusIcon(status store.Status) string {
	switch status {
	case store.StatusWorking:
		return "⚙"
	case store.StatusIdle:
		return "💤"
	case store.StatusNeedsInput:
		return "❓"
	case store.StatusErrored:
		return "❌"
	case store.StatusPaused:
		return "⏸"
	case store.StatusArchived:
		return "📦"
	}
	return "·"
}

// WriteCards renders cards in the requested format to w.
func WriteCards(w io.Writer, cards []store.Card, format OutputFormat) error {
	switch format {
	case FormatJSON:
		return writeCardsJSON(w, cards)
	case FormatTSV:
		return writeCardsTSV(w, cards)
	default:
		return writeCardsTable(w, cards)
	}
}

// cardJSON is the on-wire shape for `hive ls --json`. It intentionally omits
// the internal offsets / raw prompt text.
type cardJSON struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Repo           string  `json:"repo"`
	Branch         string  `json:"branch"`
	WorktreePath   string  `json:"worktree_path,omitempty"`
	Column         string  `json:"column"`
	Status         string  `json:"status"`
	TmuxSession    string  `json:"tmux_session,omitempty"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
	LastAttachedAt int64   `json:"last_attached_at,omitempty"`
	CostUSD        float64 `json:"cost_usd"`
	Model          string  `json:"model,omitempty"`
}

func toJSON(cards []store.Card) []cardJSON {
	out := make([]cardJSON, 0, len(cards))
	for _, c := range cards {
		out = append(out, cardJSON{
			ID:             c.ID,
			Title:          c.Title,
			Repo:           c.RepoName,
			Branch:         c.Branch,
			WorktreePath:   c.WorktreePath,
			Column:         string(c.ColumnID),
			Status:         string(c.Status),
			TmuxSession:    c.TmuxSession,
			CreatedAt:      c.CreatedAt,
			UpdatedAt:      c.UpdatedAt,
			LastAttachedAt: c.LastAttachedAt,
			CostUSD:        c.TotalCostUSD,
			Model:          c.LastModelUsed,
		})
	}
	return out
}

func writeCardsJSON(w io.Writer, cards []store.Card) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(toJSON(cards))
}

func writeCardsTSV(w io.Writer, cards []store.Card) error {
	fmt.Fprintln(w, "id\ttitle\trepo\tbranch\tcolumn\tstatus\tage\tcost")
	for _, c := range cards {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.2f\n",
			c.ID, c.Title, c.RepoName, c.Branch, c.ColumnID, c.Status,
			humanAge(c.UpdatedAt), c.TotalCostUSD)
	}
	return nil
}

func writeCardsTable(w io.Writer, cards []store.Card) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTITLE\tREPO\tBRANCH\tCOL\tSTATUS\tAGE\tCOST")
	for _, c := range cards {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s %s\t%s\t$%.2f\n",
			shortID(c.ID), truncateRunes(c.Title, 30), emptyDash(c.RepoName),
			emptyDash(c.Branch), c.ColumnID, StatusIcon(c.Status), c.Status,
			humanAge(c.UpdatedAt), c.TotalCostUSD)
	}
	return tw.Flush()
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// StatusSummary is the shape returned by `hive status --json`.
type StatusSummary struct {
	ActiveCount     int     `json:"active"`
	WorkingCount    int     `json:"working"`
	IdleCount       int     `json:"idle"`
	NeedsInputCount int     `json:"needs_input"`
	ErroredCount    int     `json:"errored"`
	PausedCount     int     `json:"paused"`
	DoneCount       int     `json:"done"`
	TotalCostUSD    float64 `json:"total_cost_usd"`
}

// SummarizeCards tallies a StatusSummary from a card list.
func SummarizeCards(cards []store.Card) StatusSummary {
	var s StatusSummary
	for _, c := range cards {
		s.TotalCostUSD += c.TotalCostUSD
		switch c.ColumnID {
		case store.ColumnActive:
			s.ActiveCount++
		case store.ColumnDone:
			s.DoneCount++
		}
		if c.ColumnID != store.ColumnActive {
			continue
		}
		switch c.Status {
		case store.StatusWorking:
			s.WorkingCount++
		case store.StatusIdle:
			s.IdleCount++
		case store.StatusNeedsInput:
			s.NeedsInputCount++
		case store.StatusErrored:
			s.ErroredCount++
		case store.StatusPaused:
			s.PausedCount++
		}
	}
	return s
}

// ShortStatusLine returns the PS1-friendly one-liner, e.g. "3⚙ 1❓ $2.47".
func ShortStatusLine(s StatusSummary) string {
	var parts []string
	if s.WorkingCount > 0 {
		parts = append(parts, fmt.Sprintf("%d⚙", s.WorkingCount))
	}
	if s.IdleCount > 0 {
		parts = append(parts, fmt.Sprintf("%d💤", s.IdleCount))
	}
	if s.NeedsInputCount > 0 {
		parts = append(parts, fmt.Sprintf("%d❓", s.NeedsInputCount))
	}
	if s.ErroredCount > 0 {
		parts = append(parts, fmt.Sprintf("%d❌", s.ErroredCount))
	}
	if len(parts) == 0 {
		parts = append(parts, "idle")
	}
	if s.TotalCostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", s.TotalCostUSD))
	}
	return strings.Join(parts, " ")
}
