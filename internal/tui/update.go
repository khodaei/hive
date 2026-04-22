package tui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	gitpkg "github.com/khodaei/hive/internal/git"
	"github.com/khodaei/hive/internal/store"
	"github.com/khodaei/hive/internal/tmux"
	"github.com/khodaei/hive/internal/transcripts"
)

// tickMsg triggers a periodic refresh.
type tickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case cardsRefreshMsg:
		return m.refreshCards()

	case tickMsg:
		// Check for expired archive undo windows
		var cmds []tea.Cmd
		now := time.Now()
		for cardID, pa := range m.pendingArchives {
			if now.After(pa.expiresAt) {
				cmds = append(cmds, m.finalizeArchiveCmd(cardID))
			}
		}
		model, refreshCmd := m.refreshCards()
		cmds = append(cmds, refreshCmd)
		return model, tea.Batch(cmds...)

	case archiveExpiredMsg:
		return m, m.finalizeArchiveCmd(msg.cardID)

	case showErrMsg:
		m.errMsg = msg.msg
		m.errExpiry = time.Now().Add(5 * time.Second)
		return m, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
			return clearErrMsg{}
		})

	case clearErrMsg:
		if time.Now().After(m.errExpiry) {
			m.errMsg = ""
		}
		return m, nil

	case archiveConfirmedMsg:
		card := msg.card
		cmd := m.doStartArchiveUndo(&card, msg.deleteWT)
		return m, cmd

	case resumeMsg:
		// Session was just recreated — attach to it immediately
		cmd := tea.ExecProcess(tmux.AttachCommand(msg.tmuxSession), func(err error) tea.Msg {
			if err != nil {
				return showErrMsg{msg: fmt.Sprintf("tmux attach: %v", err)}
			}
			return cardsRefreshMsg{}
		})
		return m, cmd
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Handle filter input mode
	if m.filtering {
		return m.handleFilterKey(key, msg)
	}

	// Handle create wizard
	if m.creating {
		return m.handleCreateKey(key, msg)
	}

	// Handle confirm dialog
	if m.confirming {
		return m.handleConfirmKey(key)
	}

	// Handle follow-up text input
	if m.followUp {
		return m.handleFollowUpKey(key, msg)
	}

	// Handle detail/spend overlays
	if m.detailView || m.spendView {
		switch key {
		case "esc", "i", "$", "q":
			m.detailView = false
			m.spendView = false
		case "c", "ctrl+c":
			// Copy prompt to clipboard
			if m.detailView {
				if card := m.selectedCard(); card != nil && card.Prompt != "" {
					return m, func() tea.Msg {
						cmd := exec.Command("pbcopy")
						cmd.Stdin = strings.NewReader(card.Prompt)
						if err := cmd.Run(); err != nil {
							return showErrMsg{msg: fmt.Sprintf("copy: %v", err)}
						}
						return showErrMsg{msg: "Prompt copied to clipboard"}
					}
				}
			}
		}
		return m, nil
	}

	// Handle help overlay
	if m.help {
		if key == "?" || key == "q" || key == "esc" {
			m.help = false
		}
		return m, nil
	}

	switch key {
	case "q", "ctrl+c":
		// Finalize all pending archives before quitting
		m.FinalizeAllPending()
		if m.poller != nil {
			m.poller.Stop()
		}
		return m, tea.Quit

	case "?":
		m.help = true
		return m, nil

	case "j", "down":
		m.moveDown()
		return m, nil

	case "k", "up":
		m.moveUp()
		return m, nil

	case "h", "left":
		m.moveLeft()
		return m, nil

	case "l", "right":
		m.moveRight()
		return m, nil

	case "i":
		if m.selectedCard() != nil {
			m.detailView = true
		}
		return m, nil

	case "$":
		m.spendView = true
		return m, nil

	case "n":
		return m.startCreate(false)

	case "N":
		return m.startCreate(true)

	case "H":
		return m.moveCardLeft()

	case "L":
		return m.moveCardRight()

	case "enter":
		return m.handleEnter()

	case "d":
		return m.handleArchive()

	case "D":
		return m.handleDelete()

	case "A":
		// Toggle Archive column visibility. If currently focused on a column
		// that's about to disappear, snap back to the last visible one.
		m.showArchived = !m.showArchived
		if m.colIdx >= len(m.visibleColumns()) {
			m.colIdx = len(m.visibleColumns()) - 1
		}
		return m, nil

	case "r":
		return m.handleResume()

	case "f":
		return m.startFollowUp()

	case "m":
		return m.handleToggleMute()

	case "u":
		return m.handleUndo()

	case " ":
		// Toggle multi-select
		card := m.selectedCard()
		if card != nil {
			if m.selected[card.ID] {
				delete(m.selected, card.ID)
			} else {
				m.selected[card.ID] = true
			}
		}
		return m, nil

	case "esc":
		if len(m.selected) > 0 {
			m.selected = make(map[string]bool)
			return m, nil
		}
		return m, nil

	case "/":
		m.filtering = true
		m.filter = ""
		m.inputCursor = 0
		return m, nil
	}

	return m, nil
}

// --- Navigation ---

func (m *Model) moveDown() {
	col := m.currentColumn()
	cards := m.cardsForColumn(col)
	if idx := m.cardIdx[col]; idx < len(cards)-1 {
		m.cardIdx[col] = idx + 1
	}
}

func (m *Model) moveUp() {
	col := m.currentColumn()
	if idx := m.cardIdx[col]; idx > 0 {
		m.cardIdx[col] = idx - 1
	}
}

func (m *Model) moveLeft() {
	if m.colIdx > 0 {
		m.colIdx--
	}
}

func (m *Model) moveRight() {
	if m.colIdx < len(m.visibleColumns())-1 {
		m.colIdx++
	}
}

// --- Move card between columns ---

func (m Model) moveCardLeft() (tea.Model, tea.Cmd) {
	card := m.selectedCard()
	if card == nil || m.colIdx == 0 {
		return m, nil
	}
	cols := m.visibleColumns()
	targetCol := cols[m.colIdx-1]
	return m, m.moveCardToColumn(card, targetCol)
}

func (m Model) moveCardRight() (tea.Model, tea.Cmd) {
	card := m.selectedCard()
	cols := m.visibleColumns()
	if card == nil || m.colIdx >= len(cols)-1 {
		return m, nil
	}
	targetCol := cols[m.colIdx+1]
	return m, m.moveCardToColumn(card, targetCol)
}

func (m Model) moveCardToColumn(card *store.Card, target store.Column) tea.Cmd {
	return func() tea.Msg {
		// Moving to Active from Backlog: spin up worktree + tmux + claude
		if target == store.ColumnActive && card.TmuxSession == "" {
			return m.activateBacklogCard(card)
		}

		if err := m.store.UpdateCardColumn(card.ID, target); err != nil {
			return showErrMsg{msg: fmt.Sprintf("move card: %v", err)}
		}
		return cardsRefreshMsg{}
	}
}

func (m Model) activateBacklogCard(card *store.Card) tea.Msg {
	// Find repo
	var repo *store.Repo
	for _, r := range m.cfg.Repos {
		if r.Name == card.RepoName {
			repo = &store.Repo{
				Name: r.Name, Path: r.Path,
				DefaultBranch: r.DefaultBranch, SetupScript: r.SetupScript,
			}
			break
		}
	}
	if repo == nil {
		return showErrMsg{msg: fmt.Sprintf("repo %q not found", card.RepoName)}
	}

	tmuxName := m.cfg.TmuxPrefix + card.ID

	if err := gitpkg.Fetch(repo.Path); err != nil {
		return showErrMsg{msg: fmt.Sprintf("git fetch: %v", err)}
	}

	wtPath, err := gitpkg.CreateWorktree(repo.Path, card.Branch, repo.DefaultBranch)
	if err != nil {
		return showErrMsg{msg: fmt.Sprintf("create worktree: %v", err)}
	}

	if err := tmux.NewSession(tmuxName, wtPath); err != nil {
		return showErrMsg{msg: fmt.Sprintf("create tmux session: %v", err)}
	}
	if err := tmux.SendKeys(tmuxName, m.cfg.ClaudeCmd); err != nil {
		return showErrMsg{msg: fmt.Sprintf("launch claude: %v", err)}
	}

	if card.Prompt != "" {
		time.Sleep(2 * time.Second)
		tmux.SendKeys(tmuxName, card.Prompt)
	}

	m.store.UpdateCardColumn(card.ID, store.ColumnActive)
	m.store.UpdateCardStatus(card.ID, store.StatusWorking)
	m.store.UpdateCardTmuxSession(card.ID, tmuxName)
	m.store.UpdateCardWorktreePath(card.ID, wtPath)

	return cardsRefreshMsg{}
}

// --- Filter ---

func (m Model) handleFilterKey(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		m.filtering = false
		return m, nil
	case "esc":
		m.filtering = false
		m.filter = ""
		m.inputCursor = 0
		return m, nil
	default:
		m.inputHandleKey(&m.filter, key, msg)
		return m, nil
	}
}

// --- Create wizard ---

func (m Model) startCreate(backlog bool) (tea.Model, tea.Cmd) {
	m.creating = true
	m.createBacklog = backlog
	m.createStep = 0
	m.createInput = ""
	m.inputCursor = 0
	m.createTitle = ""
	m.createRepo = ""
	m.createBranch = ""
	m.createPrompt = ""
	m.repoList = m.cfg.Repos
	m.repoIdx = 0
	return m, nil
}

func (m Model) handleCreateKey(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.creating = false
		return m, nil

	case "enter":
		return m.advanceCreate()

	case "tab":
		// In repo selection step, tab cycles through repos
		if m.createStep == 1 && len(m.repoList) > 0 {
			m.repoIdx = (m.repoIdx + 1) % len(m.repoList)
			m.inputSetText(&m.createInput, m.repoList[m.repoIdx].Name)
		}
		return m, nil

	default:
		m.inputHandleKey(&m.createInput, key, msg)
		return m, nil
	}
}

func (m Model) advanceCreate() (tea.Model, tea.Cmd) {
	switch m.createStep {
	case 0: // Title
		if m.createInput == "" {
			return m, nil
		}
		m.createTitle = m.createInput
		m.createStep = 1
		// Pre-fill with first repo
		if len(m.repoList) > 0 {
			m.inputSetText(&m.createInput, m.repoList[0].Name)
		} else {
			m.inputSetText(&m.createInput, "")
		}
		return m, nil

	case 1: // Repo
		m.createRepo = m.createInput
		m.createStep = 2
		// Pre-fill with default branch
		defaultBranch := ""
		for _, r := range m.repoList {
			if r.Name == m.createRepo {
				defaultBranch = r.DefaultBranch
				break
			}
		}
		m.inputSetText(&m.createInput, defaultBranch)
		return m, nil

	case 2: // Branch
		if m.createInput == "" {
			return m, nil
		}
		m.createBranch = m.createInput
		m.inputSetText(&m.createInput, "")
		m.createStep = 3
		return m, nil

	case 3: // Prompt (optional)
		m.createPrompt = m.createInput
		m.creating = false
		return m, m.executeCreate()
	}
	return m, nil
}

func (m Model) executeCreate() tea.Cmd {
	return func() tea.Msg {
		// Find repo config
		var repo *store.Repo
		for _, r := range m.cfg.Repos {
			if r.Name == m.createRepo {
				repo = &store.Repo{
					Name:          r.Name,
					Path:          r.Path,
					DefaultBranch: r.DefaultBranch,
					SetupScript:   r.SetupScript,
				}
				break
			}
		}
		if repo == nil {
			return showErrMsg{msg: fmt.Sprintf("repo %q not found in config", m.createRepo)}
		}

		// Ensure repo is in store
		m.store.UpsertRepo(store.Repo{
			Name:          repo.Name,
			Path:          repo.Path,
			DefaultBranch: repo.DefaultBranch,
			SetupScript:   repo.SetupScript,
		})

		cardID := GenerateID()
		now := time.Now().Unix()

		// Backlog: no worktree, no tmux, no claude
		if m.createBacklog {
			card := store.Card{
				ID:           cardID,
				Title:        m.createTitle,
				Prompt:       m.createPrompt,
				RepoName:     repo.Name,
				Branch:       m.createBranch,
				WorktreePath: "",
				ColumnID:     store.ColumnBacklog,
				Status:       store.StatusUnknown,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if err := m.store.InsertCard(card); err != nil {
				return showErrMsg{msg: fmt.Sprintf("insert card: %v", err)}
			}
			return cardsRefreshMsg{}
		}

		// Active: fetch, create worktree, tmux, claude
		if err := gitpkg.Fetch(repo.Path); err != nil {
			return showErrMsg{msg: fmt.Sprintf("git fetch: %v", err)}
		}

		wtPath, err := gitpkg.CreateWorktree(repo.Path, m.createBranch, repo.DefaultBranch)
		if err != nil {
			return showErrMsg{msg: fmt.Sprintf("create worktree: %v", err)}
		}

		// Active: worktree + tmux + claude
		tmuxName := m.cfg.TmuxPrefix + cardID

		if repo.SetupScript != "" {
			if err := tmux.NewSession(tmuxName, wtPath); err != nil {
				return showErrMsg{msg: fmt.Sprintf("create tmux session: %v", err)}
			}
			if err := tmux.SendKeys(tmuxName, repo.SetupScript); err != nil {
				return showErrMsg{msg: fmt.Sprintf("run setup script: %v", err)}
			}
			time.Sleep(500 * time.Millisecond)
			if err := tmux.SendKeys(tmuxName, m.cfg.ClaudeCmd); err != nil {
				return showErrMsg{msg: fmt.Sprintf("launch claude: %v", err)}
			}
		} else {
			if err := tmux.NewSession(tmuxName, wtPath); err != nil {
				return showErrMsg{msg: fmt.Sprintf("create tmux session: %v", err)}
			}
			if err := tmux.SendKeys(tmuxName, m.cfg.ClaudeCmd); err != nil {
				return showErrMsg{msg: fmt.Sprintf("launch claude: %v", err)}
			}
		}

		if m.createPrompt != "" {
			time.Sleep(2 * time.Second)
			tmux.SendKeys(tmuxName, m.createPrompt)
		}

		card := store.Card{
			ID:           cardID,
			Title:        m.createTitle,
			Prompt:       m.createPrompt,
			RepoName:     repo.Name,
			Branch:       m.createBranch,
			WorktreePath: wtPath,
			ColumnID:     store.ColumnActive,
			Status:       store.StatusWorking,
			TmuxSession:  tmuxName,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := m.store.InsertCard(card); err != nil {
			return showErrMsg{msg: fmt.Sprintf("insert card: %v", err)}
		}

		return cardsRefreshMsg{}
	}
}

// --- Attach ---

func (m Model) handleEnter() (tea.Model, tea.Cmd) {
	card := m.selectedCard()
	if card == nil {
		return m, nil
	}

	switch card.ColumnID {
	case store.ColumnDone:
		// Resume a done card
		return m.doResume(card)
	default:
		// If tmux session is gone, recreate it (user may have exited instead of detaching)
		if card.TmuxSession == "" || !tmux.HasSession(card.TmuxSession) {
			return m.doResume(card)
		}
		cmd := tea.ExecProcess(tmux.AttachCommand(card.TmuxSession), func(err error) tea.Msg {
			if err != nil {
				return showErrMsg{msg: fmt.Sprintf("tmux attach: %v", err)}
			}
			return cardsRefreshMsg{}
		})
		return m, cmd
	}
}

// --- Archive ---

func (m Model) handleArchive() (tea.Model, tea.Cmd) {
	card := m.selectedCard()
	if card == nil || card.ColumnID == store.ColumnArchived {
		return m, nil
	}

	// If the card is already Done, move it to Archived (hides from main view).
	// No worktree/tmux cleanup here — that already happened when moved to Done.
	if card.ColumnID == store.ColumnDone {
		cardID := card.ID
		st := m.store
		return m, func() tea.Msg {
			st.UpdateCardColumn(cardID, store.ColumnArchived)
			return cardsRefreshMsg{}
		}
	}

	// For "prompt" mode, ask about worktree cleanup
	if m.cfg.ArchiveBehavior == "prompt" && card.WorktreePath != "" {
		m.confirming = true
		m.confirmPrompt = fmt.Sprintf("Archive %q. Delete worktree? (y=delete, n=keep)", card.Title)
		cardCopy := *card
		m.confirmAction = func() tea.Cmd {
			// Will be called from handleConfirmKey which has a value receiver on m
			// We need to do the archive inline there. Use a message instead.
			return func() tea.Msg { return archiveConfirmedMsg{card: cardCopy, deleteWT: true} }
		}
		m.confirmNoAction = func() tea.Cmd {
			return func() tea.Msg { return archiveConfirmedMsg{card: cardCopy, deleteWT: false} }
		}
		return m, nil
	}

	deleteWT := m.cfg.ArchiveBehavior == "delete"
	cmd := m.doStartArchiveUndo(card, deleteWT)
	return m, cmd
}

type archiveConfirmedMsg struct {
	card     store.Card
	deleteWT bool
}

// doStartArchiveUndo mutates pendingArchives and returns a Cmd for DB writes.
func (m *Model) doStartArchiveUndo(card *store.Card, deleteWorktree bool) tea.Cmd {
	m.pendingArchives[card.ID] = &pendingArchive{
		cardID:       card.ID,
		prevColumn:   card.ColumnID,
		prevStatus:   card.Status,
		tmuxSession:  card.TmuxSession,
		deleteWT:     deleteWorktree,
		worktreePath: card.WorktreePath,
		expiresAt:    time.Now().Add(10 * time.Second),
	}

	st := m.store
	cardID := card.ID
	return func() tea.Msg {
		st.UpdateCardColumn(cardID, store.ColumnDone)
		st.UpdateCardStatus(cardID, store.StatusArchived)
		return cardsRefreshMsg{}
	}
}

// archiveExpiredMsg is sent when a pending archive's undo window expires.
type archiveExpiredMsg struct {
	cardID string
}

func (m *Model) handleUndo() (tea.Model, tea.Cmd) {
	card := m.selectedCard()
	if card == nil {
		return m, nil
	}

	pa, ok := m.pendingArchives[card.ID]
	if !ok {
		return m, nil
	}

	// Undo: restore card to previous state (map mutation is synchronous in Update)
	delete(m.pendingArchives, card.ID)

	st := m.store
	prevCol := pa.prevColumn
	prevStatus := pa.prevStatus
	cardID := pa.cardID
	return m, func() tea.Msg {
		st.UpdateCardColumn(cardID, prevCol)
		st.UpdateCardStatus(cardID, prevStatus)
		return cardsRefreshMsg{}
	}
}

// finalizeArchiveCmd extracts the pending state synchronously, then returns a Cmd
// for the async work (tmux kill, worktree delete, session ID capture).
func (m *Model) finalizeArchiveCmd(cardID string) tea.Cmd {
	pa, ok := m.pendingArchives[cardID]
	if !ok {
		return nil
	}
	delete(m.pendingArchives, cardID)

	st := m.store
	return func() tea.Msg {
		// Find claude session ID
		if pa.worktreePath != "" {
			sessionID, _ := transcripts.FindSessionID(pa.worktreePath)
			if sessionID != "" {
				st.UpdateCardClaudeSession(pa.cardID, sessionID)
			}
		}

		// Kill tmux session
		if pa.tmuxSession != "" {
			tmux.KillSession(pa.tmuxSession)
		}

		// Delete worktree if requested
		if pa.deleteWT && pa.worktreePath != "" {
			if err := gitpkg.RemoveWorktree(pa.worktreePath, pa.worktreePath); err != nil {
				log.Printf("failed to remove worktree %s: %v", pa.worktreePath, err)
			}
		}

		return nil
	}
}

// FinalizeAllPending finalizes all pending archives immediately (for shutdown).
func (m *Model) FinalizeAllPending() {
	for cardID := range m.pendingArchives {
		cmd := m.finalizeArchiveCmd(cardID)
		if cmd != nil {
			cmd() // execute synchronously on shutdown
		}
	}
}

// --- Resume ---

func (m Model) handleResume() (tea.Model, tea.Cmd) {
	card := m.selectedCard()
	if card == nil {
		return m, nil
	}
	if card.ColumnID != store.ColumnDone && card.ColumnID != store.ColumnArchived {
		return m, nil
	}
	return m.doResume(card)
}

// resumeMsg is sent after a session is recreated, triggering an attach.
type resumeMsg struct {
	tmuxSession string
}

func (m Model) doResume(card *store.Card) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg {
		tmuxName := m.cfg.TmuxPrefix + card.ID

		// Create new tmux session
		if err := tmux.NewSession(tmuxName, card.WorktreePath); err != nil {
			return showErrMsg{msg: fmt.Sprintf("create tmux session: %v", err)}
		}

		// Resume with session ID if available
		claudeCmd := m.cfg.ClaudeCmd
		if card.ClaudeSessionID != "" {
			claudeCmd = fmt.Sprintf("%s --resume %s", m.cfg.ClaudeCmd, card.ClaudeSessionID)
		}
		if err := tmux.SendKeys(tmuxName, claudeCmd); err != nil {
			return showErrMsg{msg: fmt.Sprintf("launch claude: %v", err)}
		}

		// Update card
		m.store.UpdateCardColumn(card.ID, store.ColumnActive)
		m.store.UpdateCardStatus(card.ID, store.StatusWorking)
		m.store.UpdateCardTmuxSession(card.ID, tmuxName)

		return resumeMsg{tmuxSession: tmuxName}
	}
}

// --- Follow-up ---

func (m Model) startFollowUp() (tea.Model, tea.Cmd) {
	card := m.selectedCard()
	if card == nil || card.TmuxSession == "" {
		return m, nil
	}
	m.followUp = true
	m.followUpText = ""
	m.inputCursor = 0
	return m, nil
}

func (m Model) handleFollowUpKey(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.followUp = false
		m.followUpText = ""
		return m, nil
	case "enter":
		if m.followUpText == "" {
			return m, nil
		}
		card := m.selectedCard()
		if card == nil || card.TmuxSession == "" || !tmux.HasSession(card.TmuxSession) {
			m.followUp = false
			m.followUpText = ""
			return m, func() tea.Msg {
				return showErrMsg{msg: "no active tmux session for this card"}
			}
		}
		text := m.followUpText
		session := card.TmuxSession
		cardID := card.ID
		st := m.store
		m.followUp = false
		m.followUpText = ""
		return m, func() tea.Msg {
			if err := tmux.SendKeys(session, text); err != nil {
				return showErrMsg{msg: fmt.Sprintf("send follow-up: %v", err)}
			}
			st.InsertStatusEvent(store.StatusEvent{
				CardID:     cardID,
				Status:     "user_input_sent",
				Detail:     text,
				ObservedAt: time.Now().Unix(),
			})
			return cardsRefreshMsg{}
		}
	default:
		m.inputHandleKey(&m.followUpText, key, msg)
		return m, nil
	}
}

// --- Mute ---

func (m Model) handleToggleMute() (tea.Model, tea.Cmd) {
	card := m.selectedCard()
	if card == nil {
		return m, nil
	}
	newMuted := !card.NotificationsMuted
	st := m.store
	cardID := card.ID
	return m, func() tea.Msg {
		st.UpdateCardMuted(cardID, newMuted)
		return cardsRefreshMsg{}
	}
}

// --- Delete ---

func (m Model) handleDelete() (tea.Model, tea.Cmd) {
	card := m.selectedCard()
	if card == nil {
		return m, nil
	}

	m.confirming = true
	m.confirmPrompt = fmt.Sprintf("Delete card %q? (y/n)", card.Title)
	cardID := card.ID
	tmuxSession := card.TmuxSession
	m.confirmAction = func() tea.Cmd {
		return func() tea.Msg {
			if tmuxSession != "" {
				tmux.KillSession(tmuxSession)
			}
			m.store.DeleteCard(cardID)
			return cardsRefreshMsg{}
		}
	}
	return m, nil
}

func (m Model) handleConfirmKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y":
		m.confirming = false
		m.confirmNoAction = nil
		if m.confirmAction != nil {
			return m, m.confirmAction()
		}
		return m, nil
	case "n", "N":
		m.confirming = false
		if m.confirmNoAction != nil {
			action := m.confirmNoAction
			m.confirmNoAction = nil
			return m, action()
		}
		return m, nil
	case "esc":
		m.confirming = false
		m.confirmNoAction = nil
		return m, nil
	}
	return m, nil
}

// --- Refresh ---

func (m Model) refreshCards() (tea.Model, tea.Cmd) {
	cards, err := m.store.ListCards()
	if err != nil {
		m.errMsg = fmt.Sprintf("load cards: %v", err)
		return m, tickCmd()
	}
	m.cards = cards

	// Clean up stale multi-select entries (cards that no longer exist)
	cardIDs := make(map[string]bool, len(cards))
	for _, c := range cards {
		cardIDs[c.ID] = true
	}
	for id := range m.selected {
		if !cardIDs[id] {
			delete(m.selected, id)
		}
	}

	// Clamp selection indices
	for _, col := range columnsWithArchive {
		colCards := m.cardsForColumn(col)
		if idx := m.cardIdx[col]; idx >= len(colCards) {
			if len(colCards) > 0 {
				m.cardIdx[col] = len(colCards) - 1
			} else {
				m.cardIdx[col] = 0
			}
		}
	}

	return m, tickCmd()
}

// --- Helpers ---

// GenerateID creates a random 8-char hex ID for cards.
func GenerateID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
