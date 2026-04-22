package cli

import (
	"testing"

	"github.com/khodaei/hive/internal/store"
)

func mkCards() []store.Card {
	return []store.Card{
		{ID: "a3f1b2c9", Title: "fix auth bug", UpdatedAt: 100},
		{ID: "a3f1b2d0", Title: "add auth tests", UpdatedAt: 200},
		{ID: "b7e2c3d4", Title: "refactor logging", UpdatedAt: 300},
		{ID: "c0d1e2f3", Title: "fix auth bug", UpdatedAt: 400}, // duplicate title
	}
}

func TestResolve_ExactID(t *testing.T) {
	cards := mkCards()
	got, ok := Resolve(cards, "a3f1b2c9")
	if !ok || got.ID != "a3f1b2c9" {
		t.Fatalf("exact ID: got (%v, %v)", got.ID, ok)
	}
}

func TestResolve_UniqueIDPrefix(t *testing.T) {
	cards := mkCards()
	got, ok := Resolve(cards, "b7e")
	if !ok || got.ID != "b7e2c3d4" {
		t.Fatalf("unique prefix: got (%v, %v)", got.ID, ok)
	}
}

func TestResolve_AmbiguousIDPrefix(t *testing.T) {
	cards := mkCards()
	_, ok := Resolve(cards, "a3f")
	if ok {
		t.Fatalf("expected ambiguity on 'a3f'")
	}
}

func TestResolve_UniqueTitleSubstring(t *testing.T) {
	cards := mkCards()
	got, ok := Resolve(cards, "refactor")
	if !ok || got.ID != "b7e2c3d4" {
		t.Fatalf("title substring: got (%v, %v)", got.ID, ok)
	}
}

func TestResolve_TitleCaseInsensitive(t *testing.T) {
	cards := mkCards()
	got, ok := Resolve(cards, "REFACTOR")
	if !ok || got.ID != "b7e2c3d4" {
		t.Fatalf("case-insensitive title: got (%v, %v)", got.ID, ok)
	}
}

func TestResolve_AmbiguousTitle(t *testing.T) {
	cards := mkCards()
	// "auth" matches three titles.
	_, ok := Resolve(cards, "auth")
	if ok {
		t.Fatalf("expected ambiguity on 'auth'")
	}
	// "fix auth bug" appears in two titles verbatim.
	_, ok = Resolve(cards, "fix auth bug")
	if ok {
		t.Fatalf("expected ambiguity on duplicate title 'fix auth bug'")
	}
}

func TestResolve_NoMatch(t *testing.T) {
	cards := mkCards()
	_, ok := Resolve(cards, "nope")
	if ok {
		t.Fatalf("expected no match")
	}
}

func TestResolve_EmptyQuery(t *testing.T) {
	cards := mkCards()
	_, ok := Resolve(cards, "   ")
	if ok {
		t.Fatalf("expected empty query to not resolve")
	}
}

func TestResolve_ExactBeatsAmbiguousPrefix(t *testing.T) {
	// If the full ID matches, return it even when it's also an ambiguous prefix of another.
	cards := []store.Card{
		{ID: "ab", Title: "one"},
		{ID: "abcd", Title: "two"},
	}
	got, ok := Resolve(cards, "ab")
	if !ok || got.ID != "ab" {
		t.Fatalf("exact should win over prefix collision: got (%v, %v)", got.ID, ok)
	}
}

func TestMatches_TitleSubstringSortedByRecency(t *testing.T) {
	cards := mkCards()
	m := Matches(cards, "auth")
	if len(m) != 3 {
		t.Fatalf("expected 3 title matches, got %d", len(m))
	}
	if m[0].UpdatedAt < m[1].UpdatedAt || m[1].UpdatedAt < m[2].UpdatedAt {
		t.Fatalf("matches not sorted by recency: %+v", m)
	}
}

func TestMatches_ReturnsIDPrefixTier(t *testing.T) {
	cards := mkCards()
	m := Matches(cards, "a3f")
	if len(m) != 2 {
		t.Fatalf("expected 2 id-prefix matches, got %d", len(m))
	}
	for _, c := range m {
		if c.ID != "a3f1b2c9" && c.ID != "a3f1b2d0" {
			t.Fatalf("unexpected match: %+v", c)
		}
	}
}
