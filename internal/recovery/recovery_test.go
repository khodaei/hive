package recovery

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/khodaei/hive/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestReconcileNoCards(t *testing.T) {
	s := testStore(t)
	result := Reconcile(s, "hv_")

	if result.DeadSessions != 0 {
		t.Errorf("expected 0 dead sessions, got %d", result.DeadSessions)
	}
}

func TestReconcileDeadSession(t *testing.T) {
	s := testStore(t)

	// Create a repo and card pointing to a nonexistent tmux session
	s.UpsertRepo(store.Repo{Name: "test", Path: "/tmp", DefaultBranch: "main"})
	now := time.Now().Unix()
	s.InsertCard(store.Card{
		ID: "dead1", Title: "dead card", RepoName: "test", Branch: "main",
		WorktreePath: "/tmp", ColumnID: store.ColumnActive, Status: store.StatusWorking,
		TmuxSession: "hv_dead1_nonexistent", CreatedAt: now, UpdatedAt: now,
	})

	result := Reconcile(s, "hv_")

	if result.DeadSessions != 1 {
		t.Errorf("expected 1 dead session, got %d", result.DeadSessions)
	}

	// Card should now be errored
	card, err := s.GetCard("dead1")
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != store.StatusErrored {
		t.Errorf("expected status errored, got %q", card.Status)
	}
}

func TestAdoptOrphan(t *testing.T) {
	s := testStore(t)
	s.UpsertRepo(store.Repo{Name: "unknown", Path: "/tmp", DefaultBranch: "main"})

	orphan := OrphanedSession{Name: "hv_orphan123", CWD: "/tmp/some-worktree"}
	err := AdoptOrphan(s, orphan, "hv_")
	if err != nil {
		t.Fatal(err)
	}

	card, err := s.GetCard("orphan123")
	if err != nil {
		t.Fatal(err)
	}
	if card.Title != "adopted: hv_orphan123" {
		t.Errorf("unexpected title: %q", card.Title)
	}
	if card.ColumnID != store.ColumnActive {
		t.Errorf("expected active column, got %q", card.ColumnID)
	}
}
