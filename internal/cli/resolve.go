// Package cli contains helpers shared across hive CLI subcommands:
// card-by-query resolution, the interactive picker, and output formatting.
package cli

import (
	"sort"
	"strings"

	"github.com/khodaei/hive/internal/store"
)

// Resolve looks up a card by a free-form query against a card list.
// Precedence, first match wins:
//  1. Exact card ID.
//  2. Unique card ID prefix.
//  3. Unique case-insensitive title substring.
//
// Returns the matched card and true when unambiguous. On 0 or >1 matches,
// returns a zero card and false; the caller should open the picker.
func Resolve(cards []store.Card, query string) (store.Card, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return store.Card{}, false
	}
	qLower := strings.ToLower(q)

	for _, c := range cards {
		if c.ID == q {
			return c, true
		}
	}

	var idPrefix []store.Card
	for _, c := range cards {
		if strings.HasPrefix(strings.ToLower(c.ID), qLower) {
			idPrefix = append(idPrefix, c)
		}
	}
	if len(idPrefix) == 1 {
		return idPrefix[0], true
	}

	var titleMatch []store.Card
	for _, c := range cards {
		if strings.Contains(strings.ToLower(c.Title), qLower) {
			titleMatch = append(titleMatch, c)
		}
	}
	if len(titleMatch) == 1 {
		return titleMatch[0], true
	}

	return store.Card{}, false
}

// Matches returns all cards matching the query at the strongest-precedence
// tier that produced any hits. Useful for non-TTY error output.
func Matches(cards []store.Card, query string) []store.Card {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	qLower := strings.ToLower(q)

	for _, c := range cards {
		if c.ID == q {
			return []store.Card{c}
		}
	}

	var idPrefix []store.Card
	for _, c := range cards {
		if strings.HasPrefix(strings.ToLower(c.ID), qLower) {
			idPrefix = append(idPrefix, c)
		}
	}
	if len(idPrefix) > 0 {
		sortByRecency(idPrefix)
		return idPrefix
	}

	var titleMatch []store.Card
	for _, c := range cards {
		if strings.Contains(strings.ToLower(c.Title), qLower) {
			titleMatch = append(titleMatch, c)
		}
	}
	sortByRecency(titleMatch)
	return titleMatch
}

func sortByRecency(cs []store.Card) {
	sort.SliceStable(cs, func(i, j int) bool {
		return cs[i].UpdatedAt > cs[j].UpdatedAt
	})
}
