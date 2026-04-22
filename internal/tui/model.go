package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/khodaei/hive/internal/config"
	"github.com/khodaei/hive/internal/poller"
	"github.com/khodaei/hive/internal/status"
	"github.com/khodaei/hive/internal/store"
)

// defaultColumns defines the kanban columns shown by default.
var defaultColumns = []store.Column{
	store.ColumnBacklog,
	store.ColumnActive,
	store.ColumnDone,
}

// columnsWithArchive appends the Archive column to the default set.
var columnsWithArchive = []store.Column{
	store.ColumnBacklog,
	store.ColumnActive,
	store.ColumnDone,
	store.ColumnArchived,
}

var columnTitles = map[store.Column]string{
	store.ColumnBacklog:  "Todo",
	store.ColumnActive:   "Active",
	store.ColumnReview:   "Review",
	store.ColumnDone:     "Done",
	store.ColumnArchived: "Archive",
}

// Model is the top-level Bubble Tea model for the kanban board.
type Model struct {
	store      *store.Store
	cfg        config.Config
	poller     *poller.Poller
	classifier *status.Classifier

	// Board state
	cards     []store.Card
	colIdx    int                  // which column is focused
	cardIdx   map[store.Column]int // which card is selected in each column
	filter    string
	filtering bool

	// UI state
	width        int
	height       int
	help         bool
	detailView   bool   // card detail overlay (i key)
	spendView    bool   // aggregate spend overlay ($ key)
	followUp     bool   // send-follow-up overlay (f key)
	followUpText string // text being typed for follow-up
	showArchived bool   // whether the Archive column is visible (A key)

	// Multi-select
	selected map[string]bool // card ID -> selected

	// Text input state (shared across all input modes)
	inputCursor int // cursor position within the current input string

	// Create wizard state
	creating      bool
	createBacklog bool
	createStep    int
	createInput   string
	createTitle   string
	createRepo    string
	createBranch  string
	createPrompt  string
	repoList      []config.Repo
	repoIdx       int

	// Confirm dialog
	confirming      bool
	confirmPrompt   string
	confirmAction   func() tea.Cmd
	confirmNoAction func() tea.Cmd // optional: action on 'n' (for archive prompt mode)

	// Pending archive undo windows
	pendingArchives map[string]*pendingArchive // card ID -> pending state

	// Error display
	errMsg    string
	errExpiry time.Time
}

// pendingArchive tracks a card in the 10-second undo window.
type pendingArchive struct {
	cardID       string
	prevColumn   store.Column
	prevStatus   store.Status
	tmuxSession  string
	deleteWT     bool
	worktreePath string
	expiresAt    time.Time
}

// NewModel creates a new TUI model.
func NewModel(s *store.Store, cfg config.Config) Model {
	classifier := status.New(time.Duration(cfg.IdleThresholdSec) * time.Second)

	p := poller.New(s, classifier, cfg, func(sc poller.StatusChange) {
		// The TUI polls for changes via tick — no direct msg sending needed.
	})

	m := Model{
		store:           s,
		cfg:             cfg,
		poller:          p,
		classifier:      classifier,
		cardIdx:         make(map[store.Column]int),
		pendingArchives: make(map[string]*pendingArchive),
		selected:        make(map[string]bool),
		colIdx:          1, // start on Active column
	}

	return m
}

// statusChangeMsg is sent from the poller when a card's status changes.
type statusChangeMsg struct {
	change poller.StatusChange
}

// cardsRefreshMsg triggers a reload of cards from the store.
type cardsRefreshMsg struct{}

// errMsg is used to display an error temporarily.
type showErrMsg struct {
	msg string
}

// clearErrMsg clears the error display.
type clearErrMsg struct{}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	m.poller.Start()
	return func() tea.Msg {
		return cardsRefreshMsg{}
	}
}

// cardsForColumn returns the cards that should be displayed in a column,
// respecting the current filter.
func (m Model) cardsForColumn(col store.Column) []store.Card {
	var result []store.Card
	for _, c := range m.cards {
		if c.ColumnID != col {
			continue
		}
		if m.filter != "" && c.RepoName != m.filter {
			continue
		}
		result = append(result, c)
	}
	return result
}

// visibleColumns returns the set of columns to display based on whether the
// Archive view is toggled on.
func (m Model) visibleColumns() []store.Column {
	if m.showArchived {
		return columnsWithArchive
	}
	return defaultColumns
}

// currentColumn returns the currently focused column.
func (m Model) currentColumn() store.Column {
	cols := m.visibleColumns()
	if m.colIdx >= len(cols) {
		return cols[len(cols)-1]
	}
	return cols[m.colIdx]
}

// selectedCard returns the currently selected card, or nil if none.
func (m Model) selectedCard() *store.Card {
	col := m.currentColumn()
	cards := m.cardsForColumn(col)
	idx := m.cardIdx[col]
	if idx >= 0 && idx < len(cards) {
		return &cards[idx]
	}
	return nil
}
