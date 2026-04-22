package recovery

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/khodaei/hive/internal/store"
	"github.com/khodaei/hive/internal/tmux"
)

// OrphanedSession represents a tmux session with the configured prefix
// that has no corresponding card in the DB.
type OrphanedSession struct {
	Name string
	CWD  string
}

// ReconcileResult holds the outcome of a startup reconciliation.
type ReconcileResult struct {
	DeadSessions     int
	OrphanedSessions []OrphanedSession
}

// Reconcile checks all active cards against live tmux state and fixes inconsistencies.
// - Cards with dead tmux sessions are marked errored.
// - Tmux sessions matching the prefix with no card are reported as orphans.
func Reconcile(s *store.Store, prefix string) ReconcileResult {
	result := ReconcileResult{}

	// 1. Check all active cards for dead tmux sessions
	cards, err := s.ListCardsByColumn(store.ColumnActive)
	if err != nil {
		log.Printf("recovery: list active cards: %v", err)
		return result
	}

	for _, card := range cards {
		if card.TmuxSession == "" {
			continue
		}
		if !tmux.HasSession(card.TmuxSession) {
			log.Printf("recovery: card %s (%s) tmux session %s disappeared",
				card.ID, card.Title, card.TmuxSession)
			s.UpdateCardStatus(card.ID, store.StatusErrored)
			s.InsertStatusEvent(store.StatusEvent{
				CardID:     card.ID,
				Status:     store.StatusErrored,
				Detail:     "tmux session disappeared (detected on startup)",
				ObservedAt: time.Now().Unix(),
			})
			result.DeadSessions++
		}
	}

	// 2. Find orphaned tmux sessions (matching prefix, no card in DB)
	sessions, err := tmux.ListSessions()
	if err != nil {
		log.Printf("recovery: list tmux sessions: %v", err)
		return result
	}

	allCards, err := s.ListCards()
	if err != nil {
		log.Printf("recovery: list all cards: %v", err)
		return result
	}

	// Build set of known tmux session names
	knownSessions := make(map[string]bool)
	for _, c := range allCards {
		if c.TmuxSession != "" {
			knownSessions[c.TmuxSession] = true
		}
	}

	for _, sess := range sessions {
		if !strings.HasPrefix(sess, prefix) {
			continue
		}
		if knownSessions[sess] {
			continue
		}
		// Orphan found — get its working directory
		cwd, _ := tmux.SessionCWD(sess)
		result.OrphanedSessions = append(result.OrphanedSessions, OrphanedSession{
			Name: sess,
			CWD:  cwd,
		})
		log.Printf("recovery: orphaned tmux session %s (cwd: %s)", sess, cwd)
	}

	return result
}

// AdoptOrphan creates a card for an orphaned tmux session.
func AdoptOrphan(s *store.Store, orphan OrphanedSession, prefix string) error {
	cardID := strings.TrimPrefix(orphan.Name, prefix)
	if cardID == "" {
		cardID = orphan.Name
	}

	now := time.Now().Unix()
	card := store.Card{
		ID:           cardID,
		Title:        fmt.Sprintf("adopted: %s", orphan.Name),
		RepoName:     "unknown",
		Branch:       "unknown",
		WorktreePath: orphan.CWD,
		ColumnID:     store.ColumnActive,
		Status:       store.StatusUnknown,
		TmuxSession:  orphan.Name,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	return s.InsertCard(card)
}
