package store

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex // guards all write operations
}

// Column represents a kanban column.
type Column string

const (
	ColumnBacklog  Column = "backlog"
	ColumnActive   Column = "active"
	ColumnReview   Column = "review"
	ColumnDone     Column = "done"
	ColumnArchived Column = "archived"
)

// Status represents a card's live status.
type Status string

const (
	StatusWorking    Status = "working"
	StatusIdle       Status = "idle"
	StatusNeedsInput Status = "needs_input"
	StatusErrored    Status = "errored"
	StatusPaused     Status = "paused"
	StatusArchived   Status = "archived"
	StatusUnknown    Status = "unknown"
)

type Repo struct {
	Name          string
	Path          string
	DefaultBranch string
	SetupScript   string
}

type Card struct {
	ID              string
	Title           string
	Prompt          string
	RepoName        string
	Branch          string
	WorktreePath    string
	ColumnID        Column
	Status          Status
	TmuxSession     string
	ClaudeSessionID string
	CreatedAt       int64
	UpdatedAt       int64
	LastActivityAt  int64

	// Token/cost tracking (v1.2)
	TotalInputTokens      int64
	TotalOutputTokens     int64
	TotalCacheReadTokens  int64
	TotalCacheWriteTokens int64
	TotalCostUSD          float64
	LastModelUsed         string
	TranscriptOffset      int64

	// Notifications (v1.3)
	NotificationsMuted bool

	// PR link
	PRURL string

	// Pending prompt: sent to Claude when session first reaches idle
	PendingPrompt string

	// Last time the user attached (or resumed) this session via hive.
	// Bumped only by explicit user action, not by the status poller.
	LastAttachedAt int64

	// LLM-generated short summary of the conversation + its cache keys.
	// SummaryTranscriptMtime matches the transcript file's mtime (Unix sec)
	// the summary was computed from — used to invalidate when the chat grows.
	// SummaryGeneratedAt is when the LLM call returned.
	Summary                string
	SummaryTranscriptMtime int64
	SummaryGeneratedAt     int64
}

type StatusEvent struct {
	ID         int64
	CardID     string
	Status     Status
	Detail     string
	ObservedAt int64
}

// Open opens (or creates) the SQLite database and runs migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Enable WAL mode for better concurrent read performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Set busy timeout so SQLite retries internally on lock contention
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// execWrite executes a write query with mutex protection.
// SQLite's busy_timeout pragma handles contention at the driver level.
func (s *Store) execWrite(query string, args ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(query, args...)
	return err
}

func (s *Store) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS repos (
			name           TEXT PRIMARY KEY,
			path           TEXT NOT NULL,
			default_branch TEXT NOT NULL,
			setup_script   TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS cards (
			id                TEXT PRIMARY KEY,
			title             TEXT NOT NULL,
			prompt            TEXT,
			repo_name         TEXT NOT NULL,
			branch            TEXT NOT NULL,
			worktree_path     TEXT NOT NULL,
			column_id         TEXT NOT NULL,
			status            TEXT NOT NULL,
			tmux_session      TEXT,
			claude_session_id TEXT,
			created_at        INTEGER NOT NULL,
			updated_at        INTEGER NOT NULL,
			last_activity_at  INTEGER,
			FOREIGN KEY (repo_name) REFERENCES repos(name)
		)`,
		`CREATE TABLE IF NOT EXISTS status_events (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			card_id     TEXT NOT NULL,
			status      TEXT NOT NULL,
			detail      TEXT,
			observed_at INTEGER NOT NULL,
			FOREIGN KEY (card_id) REFERENCES cards(id) ON DELETE CASCADE
		)`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}

	// v1.2 migrations: add token/cost columns (idempotent via ALTER TABLE IF NOT EXISTS pattern)
	alterMigrations := []string{
		"ALTER TABLE cards ADD COLUMN total_input_tokens INTEGER DEFAULT 0",
		"ALTER TABLE cards ADD COLUMN total_output_tokens INTEGER DEFAULT 0",
		"ALTER TABLE cards ADD COLUMN total_cache_read_tokens INTEGER DEFAULT 0",
		"ALTER TABLE cards ADD COLUMN total_cache_write_tokens INTEGER DEFAULT 0",
		"ALTER TABLE cards ADD COLUMN total_cost_usd REAL DEFAULT 0",
		"ALTER TABLE cards ADD COLUMN last_model_used TEXT",
		"ALTER TABLE cards ADD COLUMN transcript_offset INTEGER DEFAULT 0",
	}
	for _, m := range alterMigrations {
		s.db.Exec(m) // ignore "duplicate column" errors for idempotency
	}

	// v1.3 migrations
	s.db.Exec("ALTER TABLE cards ADD COLUMN notifications_muted INTEGER DEFAULT 0")
	s.db.Exec("ALTER TABLE cards ADD COLUMN pr_url TEXT")
	s.db.Exec("ALTER TABLE cards ADD COLUMN pending_prompt TEXT")

	// CLI-first migrations: track the last time the user attached.
	s.db.Exec("ALTER TABLE cards ADD COLUMN last_attached_at INTEGER DEFAULT 0")

	// Summary cache: local-LLM summary text + invalidation keys.
	s.db.Exec("ALTER TABLE cards ADD COLUMN summary_text TEXT")
	s.db.Exec("ALTER TABLE cards ADD COLUMN summary_transcript_mtime INTEGER DEFAULT 0")
	s.db.Exec("ALTER TABLE cards ADD COLUMN summary_generated_at INTEGER DEFAULT 0")

	// Migrate review cards to active (review column removed from UI)
	s.db.Exec("UPDATE cards SET column_id='active' WHERE column_id='review'")

	// Cost snapshots table
	s.db.Exec(`CREATE TABLE IF NOT EXISTS cost_snapshots (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		card_id     TEXT NOT NULL,
		cost_usd    REAL NOT NULL,
		observed_at INTEGER NOT NULL,
		FOREIGN KEY (card_id) REFERENCES cards(id) ON DELETE CASCADE
	)`)

	return nil
}

// --- Repo CRUD ---

func (s *Store) UpsertRepo(r Repo) error {
	return s.execWrite(
		`INSERT INTO repos (name, path, default_branch, setup_script)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   path=excluded.path,
		   default_branch=excluded.default_branch,
		   setup_script=excluded.setup_script`,
		r.Name, r.Path, r.DefaultBranch, r.SetupScript,
	)
}

func (s *Store) GetRepo(name string) (Repo, error) {
	var r Repo
	var setup sql.NullString
	err := s.db.QueryRow(
		"SELECT name, path, default_branch, setup_script FROM repos WHERE name = ?", name,
	).Scan(&r.Name, &r.Path, &r.DefaultBranch, &setup)
	if err != nil {
		return Repo{}, err
	}
	r.SetupScript = setup.String
	return r, nil
}

func (s *Store) ListRepos() ([]Repo, error) {
	rows, err := s.db.Query("SELECT name, path, default_branch, setup_script FROM repos ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var repos []Repo
	for rows.Next() {
		var r Repo
		var setup sql.NullString
		if err := rows.Scan(&r.Name, &r.Path, &r.DefaultBranch, &setup); err != nil {
			return nil, err
		}
		r.SetupScript = setup.String
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

// --- Card CRUD ---

func (s *Store) InsertCard(c Card) error {
	return s.execWrite(
		`INSERT INTO cards (id, title, prompt, repo_name, branch, worktree_path,
		  column_id, status, tmux_session, claude_session_id, created_at, updated_at, last_activity_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Title, c.Prompt, c.RepoName, c.Branch, c.WorktreePath,
		c.ColumnID, c.Status, c.TmuxSession, c.ClaudeSessionID,
		c.CreatedAt, c.UpdatedAt, c.LastActivityAt,
	)
}

const cardSelectCols = `id, title, prompt, repo_name, branch, worktree_path,
	column_id, status, tmux_session, claude_session_id, created_at, updated_at, last_activity_at,
	COALESCE(total_input_tokens,0), COALESCE(total_output_tokens,0),
	COALESCE(total_cache_read_tokens,0), COALESCE(total_cache_write_tokens,0),
	COALESCE(total_cost_usd,0), COALESCE(last_model_used,''), COALESCE(transcript_offset,0),
	COALESCE(notifications_muted,0), COALESCE(pr_url,''), COALESCE(pending_prompt,''),
	COALESCE(last_attached_at,0),
	COALESCE(summary_text,''), COALESCE(summary_transcript_mtime,0), COALESCE(summary_generated_at,0)`

func scanCard(scanner interface{ Scan(...any) error }) (Card, error) {
	var c Card
	var prompt, tmux, claude sql.NullString
	var lastActivity sql.NullInt64
	var muted int64
	err := scanner.Scan(&c.ID, &c.Title, &prompt, &c.RepoName, &c.Branch, &c.WorktreePath,
		&c.ColumnID, &c.Status, &tmux, &claude, &c.CreatedAt, &c.UpdatedAt, &lastActivity,
		&c.TotalInputTokens, &c.TotalOutputTokens, &c.TotalCacheReadTokens, &c.TotalCacheWriteTokens,
		&c.TotalCostUSD, &c.LastModelUsed, &c.TranscriptOffset,
		&muted, &c.PRURL, &c.PendingPrompt,
		&c.LastAttachedAt,
		&c.Summary, &c.SummaryTranscriptMtime, &c.SummaryGeneratedAt)
	if err != nil {
		return Card{}, err
	}
	c.Prompt = prompt.String
	c.TmuxSession = tmux.String
	c.ClaudeSessionID = claude.String
	c.LastActivityAt = lastActivity.Int64
	c.NotificationsMuted = muted != 0
	return c, nil
}

func (s *Store) GetCard(id string) (Card, error) {
	row := s.db.QueryRow(`SELECT `+cardSelectCols+` FROM cards WHERE id = ?`, id)
	c, err := scanCard(row)
	if err != nil {
		return Card{}, err
	}
	return c, nil
}

func (s *Store) ListCards() ([]Card, error) {
	return s.listCards("", nil)
}

func (s *Store) ListCardsByColumn(col Column) ([]Card, error) {
	return s.listCards("WHERE column_id = ?", []any{string(col)})
}

func (s *Store) ListCardsByRepo(repoName string) ([]Card, error) {
	return s.listCards("WHERE repo_name = ?", []any{repoName})
}

func (s *Store) listCards(where string, args []any) ([]Card, error) {
	q := `SELECT ` + cardSelectCols + ` FROM cards ` + where + ` ORDER BY created_at DESC`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cards []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

func (s *Store) UpdateCardStatus(id string, status Status) error {
	now := time.Now().Unix()
	return s.execWrite(
		"UPDATE cards SET status = ?, updated_at = ?, last_activity_at = ? WHERE id = ?",
		status, now, now, id,
	)
}

func (s *Store) UpdateCardColumn(id string, col Column) error {
	now := time.Now().Unix()
	return s.execWrite(
		"UPDATE cards SET column_id = ?, updated_at = ? WHERE id = ?",
		col, now, id,
	)
}

func (s *Store) UpdateCardTmuxSession(id string, session string) error {
	now := time.Now().Unix()
	return s.execWrite(
		"UPDATE cards SET tmux_session = ?, updated_at = ? WHERE id = ?",
		session, now, id,
	)
}

func (s *Store) UpdateCardClaudeSession(id string, sessionID string) error {
	now := time.Now().Unix()
	return s.execWrite(
		"UPDATE cards SET claude_session_id = ?, updated_at = ? WHERE id = ?",
		sessionID, now, id,
	)
}

func (s *Store) UpdateCardWorktreePath(id string, path string) error {
	now := time.Now().Unix()
	return s.execWrite(
		"UPDATE cards SET worktree_path = ?, updated_at = ? WHERE id = ?",
		path, now, id,
	)
}

func (s *Store) UpdateCardMuted(id string, muted bool) error {
	v := 0
	if muted {
		v = 1
	}
	return s.execWrite("UPDATE cards SET notifications_muted = ? WHERE id = ?", v, id)
}

func (s *Store) UpdateCardBranch(id string, branch string) error {
	return s.execWrite("UPDATE cards SET branch = ? WHERE id = ?", branch, id)
}

func (s *Store) UpdateCardPRURL(id string, prURL string) error {
	return s.execWrite("UPDATE cards SET pr_url = ? WHERE id = ?", prURL, id)
}

func (s *Store) UpdateCardPendingPrompt(id string, prompt string) error {
	return s.execWrite("UPDATE cards SET pending_prompt = ? WHERE id = ?", prompt, id)
}

// UpdateCardLastAttached stamps the card with the current time; used by
// `hive attach` / `hive last` to know which session the user most recently
// worked with.
func (s *Store) UpdateCardLastAttached(id string) error {
	return s.execWrite("UPDATE cards SET last_attached_at = ? WHERE id = ?", time.Now().Unix(), id)
}

// UpdateCardSummary stores a fresh LLM-generated summary and the transcript
// mtime it was computed from. Pass (id, "", 0) to clear a cached summary.
func (s *Store) UpdateCardSummary(id, summary string, transcriptMtime int64) error {
	return s.execWrite(
		"UPDATE cards SET summary_text = ?, summary_transcript_mtime = ?, summary_generated_at = ? WHERE id = ?",
		summary, transcriptMtime, time.Now().Unix(), id,
	)
}

// FindLastArchivedCard returns the most-recently-updated card whose status is
// StatusArchived (and, by convention, whose column is Done). Used by the
// picker's Left-arrow "undo archive" action. Returns sql.ErrNoRows if none.
func (s *Store) FindLastArchivedCard() (Card, error) {
	row := s.db.QueryRow(
		`SELECT ` + cardSelectCols + ` FROM cards
		 WHERE status = 'archived'
		 ORDER BY updated_at DESC LIMIT 1`,
	)
	return scanCard(row)
}

// FindLastAttachedCard returns the most-recently-attached card that is still
// attachable (not in an archived column/status). Callers get sql.ErrNoRows
// when no eligible card exists.
func (s *Store) FindLastAttachedCard() (Card, error) {
	row := s.db.QueryRow(
		`SELECT ` + cardSelectCols + ` FROM cards
		 WHERE COALESCE(last_attached_at,0) > 0
		   AND column_id != 'archived'
		   AND status != 'archived'
		 ORDER BY last_attached_at DESC LIMIT 1`,
	)
	return scanCard(row)
}

func (s *Store) UpdateCardCost(id string, inputTok, outputTok, cacheReadTok, cacheWriteTok int64, costUSD float64, model string, offset int64) error {
	now := time.Now().Unix()
	return s.execWrite(
		`UPDATE cards SET total_input_tokens=?, total_output_tokens=?,
		 total_cache_read_tokens=?, total_cache_write_tokens=?,
		 total_cost_usd=?, last_model_used=?, transcript_offset=?, updated_at=?
		 WHERE id=?`,
		inputTok, outputTok, cacheReadTok, cacheWriteTok,
		costUSD, model, offset, now, id,
	)
}

func (s *Store) InsertCostSnapshot(cardID string, costUSD float64, observedAt int64) error {
	return s.execWrite(
		"INSERT INTO cost_snapshots (card_id, cost_usd, observed_at) VALUES (?, ?, ?)",
		cardID, costUSD, observedAt,
	)
}

func (s *Store) DeleteCard(id string) error {
	return s.execWrite("DELETE FROM cards WHERE id = ?", id)
}

// --- Status Events ---

func (s *Store) InsertStatusEvent(e StatusEvent) error {
	return s.execWrite(
		"INSERT INTO status_events (card_id, status, detail, observed_at) VALUES (?, ?, ?, ?)",
		e.CardID, e.Status, e.Detail, e.ObservedAt,
	)
}

func (s *Store) ListStatusEvents(cardID string, limit int) ([]StatusEvent, error) {
	rows, err := s.db.Query(
		"SELECT id, card_id, status, detail, observed_at FROM status_events WHERE card_id = ? ORDER BY observed_at DESC LIMIT ?",
		cardID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []StatusEvent
	for rows.Next() {
		var e StatusEvent
		var detail sql.NullString
		if err := rows.Scan(&e.ID, &e.CardID, &e.Status, &detail, &e.ObservedAt); err != nil {
			return nil, err
		}
		e.Detail = detail.String
		events = append(events, e)
	}
	return events, rows.Err()
}
