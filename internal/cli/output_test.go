package cli

import (
	"testing"
	"time"

	"github.com/khodaei/hive/internal/store"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"1h", time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"2h30m", 2*time.Hour + 30*time.Minute, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"2d12h", 2*24*time.Hour + 12*time.Hour, false},
		{"", 0, true},
		{"garbage", 0, true},
	}
	for _, c := range cases {
		got, err := ParseDuration(c.in)
		if c.err {
			if err == nil {
				t.Fatalf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q: got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestApplyFilters(t *testing.T) {
	now := time.Now().Unix()
	cards := []store.Card{
		{ID: "1", RepoName: "repo-a", Status: store.StatusWorking, ColumnID: store.ColumnActive, UpdatedAt: now},
		{ID: "2", RepoName: "repo-b", Status: store.StatusIdle, ColumnID: store.ColumnActive, UpdatedAt: now - 3600},
		{ID: "3", RepoName: "repo-a", Status: store.StatusArchived, ColumnID: store.ColumnDone, UpdatedAt: now - 86400*7},
	}

	if got := ApplyFilters(cards, ListFilters{}); len(got) != 3 {
		t.Fatalf("no-op filter should return all: got %d", len(got))
	}
	if got := ApplyFilters(cards, ListFilters{Repo: "repo-a"}); len(got) != 2 {
		t.Fatalf("repo filter: got %d", len(got))
	}
	if got := ApplyFilters(cards, ListFilters{Status: "idle"}); len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("status filter: %+v", got)
	}
	if got := ApplyFilters(cards, ListFilters{Column: "done"}); len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("column filter: %+v", got)
	}
	// Since=2h keeps cards updated within the last 2h (IDs 1 and 2 — not 3).
	if got := ApplyFilters(cards, ListFilters{Since: 2 * time.Hour}); len(got) != 2 {
		t.Fatalf("since 2h: got %d, want 2", len(got))
	}
	// Since=30m keeps only card 1 (card 2 is 1h old).
	if got := ApplyFilters(cards, ListFilters{Since: 30 * time.Minute}); len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("since 30m: %+v", got)
	}
}

func TestSummarizeCards(t *testing.T) {
	cards := []store.Card{
		{ColumnID: store.ColumnActive, Status: store.StatusWorking, TotalCostUSD: 1.00},
		{ColumnID: store.ColumnActive, Status: store.StatusWorking, TotalCostUSD: 0.50},
		{ColumnID: store.ColumnActive, Status: store.StatusNeedsInput, TotalCostUSD: 0.25},
		{ColumnID: store.ColumnDone, Status: store.StatusArchived, TotalCostUSD: 2.00},
	}
	s := SummarizeCards(cards)
	if s.ActiveCount != 3 {
		t.Fatalf("active=%d", s.ActiveCount)
	}
	if s.WorkingCount != 2 {
		t.Fatalf("working=%d", s.WorkingCount)
	}
	if s.NeedsInputCount != 1 {
		t.Fatalf("needsInput=%d", s.NeedsInputCount)
	}
	if s.DoneCount != 1 {
		t.Fatalf("done=%d", s.DoneCount)
	}
	if s.TotalCostUSD != 3.75 {
		t.Fatalf("cost=%v", s.TotalCostUSD)
	}
}
