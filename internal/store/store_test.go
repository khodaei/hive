package store

import (
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAndMigrate(t *testing.T) {
	s := testStore(t)
	_ = s // migration ran successfully
}

func TestRepoCRUD(t *testing.T) {
	s := testStore(t)

	r := Repo{Name: "test-repo", Path: "/tmp/test", DefaultBranch: "main"}
	if err := s.UpsertRepo(r); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetRepo("test-repo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "test-repo" || got.Path != "/tmp/test" || got.DefaultBranch != "main" {
		t.Errorf("unexpected repo: %+v", got)
	}

	// Upsert updates existing
	r.Path = "/tmp/updated"
	if err := s.UpsertRepo(r); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRepo("test-repo")
	if got.Path != "/tmp/updated" {
		t.Errorf("expected updated path, got %q", got.Path)
	}

	repos, err := s.ListRepos()
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Errorf("expected 1 repo, got %d", len(repos))
	}
}

func TestCardCRUD(t *testing.T) {
	s := testStore(t)

	// Must create repo first (foreign key)
	s.UpsertRepo(Repo{Name: "repo1", Path: "/tmp/repo1", DefaultBranch: "main"})

	now := time.Now().Unix()
	c := Card{
		ID:           "abc123",
		Title:        "Test card",
		Prompt:       "fix the bug",
		RepoName:     "repo1",
		Branch:       "fix-bug",
		WorktreePath: "/tmp/repo1/fix-bug",
		ColumnID:     ColumnActive,
		Status:       StatusWorking,
		TmuxSession:  "hv_abc123",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.InsertCard(c); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetCard("abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Test card" {
		t.Errorf("expected title 'Test card', got %q", got.Title)
	}
	if got.ColumnID != ColumnActive {
		t.Errorf("expected column active, got %q", got.ColumnID)
	}

	// Update status
	if err := s.UpdateCardStatus("abc123", StatusNeedsInput); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetCard("abc123")
	if got.Status != StatusNeedsInput {
		t.Errorf("expected status needs_input, got %q", got.Status)
	}

	// Update column
	if err := s.UpdateCardColumn("abc123", ColumnDone); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetCard("abc123")
	if got.ColumnID != ColumnDone {
		t.Errorf("expected column done, got %q", got.ColumnID)
	}

	// List by column
	cards, err := s.ListCardsByColumn(ColumnDone)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Errorf("expected 1 done card, got %d", len(cards))
	}

	// Delete
	if err := s.DeleteCard("abc123"); err != nil {
		t.Fatal(err)
	}
	cards, _ = s.ListCards()
	if len(cards) != 0 {
		t.Errorf("expected 0 cards after delete, got %d", len(cards))
	}
}

func TestStatusEvents(t *testing.T) {
	s := testStore(t)

	s.UpsertRepo(Repo{Name: "repo1", Path: "/tmp/repo1", DefaultBranch: "main"})
	now := time.Now().Unix()
	s.InsertCard(Card{
		ID: "card1", Title: "t", RepoName: "repo1", Branch: "b",
		WorktreePath: "/tmp", ColumnID: ColumnActive, Status: StatusWorking,
		CreatedAt: now, UpdatedAt: now,
	})

	e := StatusEvent{
		CardID:     "card1",
		Status:     StatusNeedsInput,
		Detail:     "detected y/N prompt",
		ObservedAt: now,
	}
	if err := s.InsertStatusEvent(e); err != nil {
		t.Fatal(err)
	}

	events, err := s.ListStatusEvents("card1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Status != StatusNeedsInput {
		t.Errorf("expected status needs_input, got %q", events[0].Status)
	}
}

func TestCascadeDelete(t *testing.T) {
	s := testStore(t)

	s.UpsertRepo(Repo{Name: "repo1", Path: "/tmp/repo1", DefaultBranch: "main"})
	now := time.Now().Unix()
	s.InsertCard(Card{
		ID: "card1", Title: "t", RepoName: "repo1", Branch: "b",
		WorktreePath: "/tmp", ColumnID: ColumnActive, Status: StatusWorking,
		CreatedAt: now, UpdatedAt: now,
	})
	s.InsertStatusEvent(StatusEvent{CardID: "card1", Status: StatusWorking, ObservedAt: now})
	s.InsertStatusEvent(StatusEvent{CardID: "card1", Status: StatusIdle, ObservedAt: now + 1})

	// Delete card should cascade to status_events
	if err := s.DeleteCard("card1"); err != nil {
		t.Fatal(err)
	}

	events, err := s.ListStatusEvents("card1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events after cascade delete, got %d", len(events))
	}
}
