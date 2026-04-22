package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/khodaei/hive/internal/store"
)

// Color palette
var (
	colorGreen   = lipgloss.Color("#00ff00")
	colorYellow  = lipgloss.Color("#ffff00")
	colorRed     = lipgloss.Color("#ff4444")
	colorBlue    = lipgloss.Color("#4444ff")
	colorGray    = lipgloss.Color("#666666")
	colorWhite   = lipgloss.Color("#ffffff")
	colorDim     = lipgloss.Color("#888888")
	colorHighBg  = lipgloss.Color("#333333")
	colorBorder  = lipgloss.Color("#555555")
	colorFocused = lipgloss.Color("#00aaff")
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite).
			PaddingLeft(1).
			PaddingRight(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWhite).
			Align(lipgloss.Center).
			PaddingBottom(1)

	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			PaddingLeft(1).
			PaddingRight(1).
			MarginBottom(0)

	selectedCardStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorFocused).
				PaddingLeft(1).
				PaddingRight(1).
				MarginBottom(0)

	statusStyles = map[store.Status]lipgloss.Style{
		store.StatusWorking:    lipgloss.NewStyle().Foreground(colorGreen),
		store.StatusIdle:       lipgloss.NewStyle().Foreground(colorYellow),
		store.StatusNeedsInput: lipgloss.NewStyle().Foreground(colorRed).Bold(true),
		store.StatusErrored:    lipgloss.NewStyle().Foreground(colorRed),
		store.StatusArchived:   lipgloss.NewStyle().Foreground(colorGray),
		store.StatusUnknown:    lipgloss.NewStyle().Foreground(colorDim),
	}

	repoStyle = lipgloss.NewStyle().Foreground(colorDim)

	filterStyle = lipgloss.NewStyle().
			Foreground(colorBlue).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	errStyle = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true)
)

// Status icons
var statusIcons = map[store.Status]string{
	store.StatusWorking:    "⚙",
	store.StatusIdle:       "💤",
	store.StatusNeedsInput: "❓",
	store.StatusErrored:    "❌",
	store.StatusArchived:   "📦",
	store.StatusUnknown:    "·",
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Overlays
	if m.help {
		return m.renderHelp()
	}
	if m.detailView {
		return m.renderDetail()
	}
	if m.spendView {
		return m.renderSpend()
	}

	var b strings.Builder

	// Header
	header := titleStyle.Render("hive")
	if m.filter != "" {
		header += " " + filterStyle.Render("[filter: "+m.filter+"]")
	}
	if m.filtering {
		header += " " + filterStyle.Render("/ "+renderInputWithCursor(m.filter, m.inputCursor))
	}
	b.WriteString(header + "\n\n")

	// Error bar
	if m.errMsg != "" {
		b.WriteString(errStyle.Render("  ⚠ "+m.errMsg) + "\n\n")
	}

	// Confirm dialog
	if m.confirming {
		b.WriteString(errStyle.Render("  "+m.confirmPrompt) + "\n\n")
	}

	// Follow-up overlay
	if m.followUp {
		card := m.selectedCard()
		cardTitle := ""
		if card != nil {
			cardTitle = card.Title
		}
		b.WriteString(filterStyle.Render(fmt.Sprintf("  Send to %q: %s", cardTitle, renderInputWithCursor(m.followUpText, m.inputCursor))) + "\n")
		b.WriteString(helpStyle.Render("  Enter: send  Esc: cancel") + "\n\n")
	}

	// Create wizard
	if m.creating {
		b.WriteString(m.renderCreateWizard())
		return b.String()
	}

	// Board
	b.WriteString(m.renderBoard())

	// Footer
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  n:new  N:backlog  Enter:attach  H/L:move card  d:archive  A:toggle archive view  D:delete  /:filter  ?:help  q:quit"))

	return b.String()
}

func (m Model) renderBoard() string {
	cols := m.visibleColumns()
	colWidth := m.width / len(cols)
	if colWidth < 20 {
		colWidth = 20
	}
	// Reserve space for card borders
	innerWidth := colWidth - 4

	var renderedCols []string
	for i, col := range cols {
		isFocused := i == m.colIdx
		renderedCols = append(renderedCols, m.renderColumn(col, isFocused, innerWidth, colWidth))
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, renderedCols...)
}

func (m Model) renderColumn(col store.Column, focused bool, innerWidth, totalWidth int) string {
	title := columnTitles[col]
	cards := m.cardsForColumn(col)
	selectedIdx := m.cardIdx[col]

	// Column header
	headerStr := headerStyle.Width(totalWidth).Render(
		fmt.Sprintf("%s (%d)", title, len(cards)),
	)

	var cardStrs []string
	cardStrs = append(cardStrs, headerStr)

	// Available height for cards (rough estimate)
	maxCards := (m.height - 8) / 4
	if maxCards < 3 {
		maxCards = 3
	}

	for i, card := range cards {
		if i >= maxCards {
			cardStrs = append(cardStrs, repoStyle.Render(
				fmt.Sprintf("  ... +%d more", len(cards)-maxCards),
			))
			break
		}

		isSelected := focused && i == selectedIdx
		cardStrs = append(cardStrs, m.renderCard(card, isSelected, innerWidth))
	}

	if len(cards) == 0 {
		cardStrs = append(cardStrs, repoStyle.Width(totalWidth).Align(lipgloss.Center).Render("(empty)"))
	}

	return strings.Join(cardStrs, "\n")
}

func (m Model) renderCard(card store.Card, selected bool, width int) string {
	icon := statusIcons[card.Status]
	style := statusStyles[card.Status]

	// Multi-select indicator
	selectPrefix := "  "
	if m.selected[card.ID] {
		selectPrefix = "* "
	}

	// First line: status icon + title + mute indicator
	muteIndicator := ""
	if card.NotificationsMuted {
		muteIndicator = " " + repoStyle.Render("🔇")
	}
	line1 := selectPrefix + style.Render(icon) + " " + truncate(card.Title, width-8) + muteIndicator

	// Second line: repo + time + cost
	ago := timeAgo(card.UpdatedAt)
	repoLabel := repoStyle.Render(card.RepoName)
	agoLabel := repoStyle.Render(ago)
	costLabel := ""
	if card.TotalCostUSD > 0 {
		costLabel = repoStyle.Render(fmt.Sprintf("$%.2f", card.TotalCostUSD))
	} else {
		costLabel = lipgloss.NewStyle().Foreground(colorGray).Render("$0.00")
	}
	line2 := repoLabel + " " + agoLabel + " " + costLabel

	content := line1 + "\n" + line2

	if selected {
		return selectedCardStyle.Width(width).Render(content)
	}
	return cardStyle.Width(width).Render(content)
}

func (m Model) renderCreateWizard() string {
	var b strings.Builder
	label := "New Session"
	if m.createBacklog {
		label = "New Backlog Card"
	}
	b.WriteString(titleStyle.Render("  "+label) + "\n\n")

	steps := []string{"Title", "Repo", "Branch", "Prompt (optional)"}

	for i, step := range steps {
		if i < m.createStep {
			// Completed step
			var val string
			switch i {
			case 0:
				val = m.createTitle
			case 1:
				val = m.createRepo
			case 2:
				val = m.createBranch
			}
			b.WriteString(fmt.Sprintf("  ✓ %s: %s\n", step, val))
		} else if i == m.createStep {
			// Current step
			b.WriteString(fmt.Sprintf("  → %s: %s\n", step, renderInputWithCursor(m.createInput, m.inputCursor)))
			if i == 1 && len(m.repoList) > 0 {
				b.WriteString("    (Tab to cycle repos)\n")
			}
		} else {
			b.WriteString(fmt.Sprintf("    %s:\n", step))
		}
	}

	b.WriteString("\n  Enter: next  Esc: cancel\n")
	return b.String()
}

func (m Model) renderHelp() string {
	help := `
  hive — Claude Code Session Kanban

  Navigation:
    j/k         Move up/down
    h/l         Move left/right (columns)
    /           Filter by repo name
    Esc         Clear filter

  Actions:
    n           New session (Active — starts immediately)
    N           New backlog card (no session until activated)
    Enter       Attach to session (Active) / Resume (Done)
    H/L         Move card left/right between columns
    d           Archive session (Active→Done, or Done→Archive hidden)
    A           Toggle Archive column visibility
    r           Resume a Done session
    D           Delete card (with confirmation)

  Detail view (i):
    c / Ctrl+C  Copy prompt to clipboard
    Ctrl+V      Paste in text inputs

  General:
    ?           Toggle this help
    q           Quit (sessions keep running)

  Press ? or q to close this help.
`
	return helpStyle.Render(help)
}

func (m Model) renderDetail() string {
	card := m.selectedCard()
	if card == nil {
		return "No card selected. Press Esc to close."
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("  Card Detail") + "\n\n")
	b.WriteString(fmt.Sprintf("  Title:    %s\n", card.Title))
	b.WriteString(fmt.Sprintf("  ID:       %s\n", card.ID))
	b.WriteString(fmt.Sprintf("  Repo:     %s\n", card.RepoName))
	b.WriteString(fmt.Sprintf("  Branch:   %s\n", card.Branch))
	b.WriteString(fmt.Sprintf("  Status:   %s %s\n", statusIcons[card.Status], card.Status))
	b.WriteString(fmt.Sprintf("  Column:   %s\n", card.ColumnID))
	b.WriteString(fmt.Sprintf("  Worktree: %s\n", card.WorktreePath))
	if card.TmuxSession != "" {
		b.WriteString(fmt.Sprintf("  Tmux:     %s\n", card.TmuxSession))
	}
	b.WriteString(fmt.Sprintf("  Created:  %s\n", timeAgo(card.CreatedAt)))
	if card.Prompt != "" {
		// Truncate long prompts for display
		prompt := card.Prompt
		if len(prompt) > 200 {
			prompt = prompt[:200] + "..."
		}
		b.WriteString(fmt.Sprintf("  Prompt:   %s\n", prompt))
	}
	b.WriteString("\n")

	// Token breakdown
	b.WriteString(titleStyle.Render("  Token Usage") + "\n\n")
	b.WriteString(fmt.Sprintf("  Input tokens:        %s\n", formatTokens(card.TotalInputTokens)))
	b.WriteString(fmt.Sprintf("  Output tokens:       %s\n", formatTokens(card.TotalOutputTokens)))
	b.WriteString(fmt.Sprintf("  Cache read tokens:   %s\n", formatTokens(card.TotalCacheReadTokens)))
	b.WriteString(fmt.Sprintf("  Cache write tokens:  %s\n", formatTokens(card.TotalCacheWriteTokens)))
	b.WriteString(fmt.Sprintf("  Total cost:          $%.4f\n", card.TotalCostUSD))
	if card.LastModelUsed != "" {
		b.WriteString(fmt.Sprintf("  Model:               %s\n", card.LastModelUsed))
	}

	// Token efficiency
	totalTokens := card.TotalInputTokens + card.TotalOutputTokens + card.TotalCacheReadTokens + card.TotalCacheWriteTokens
	if card.TotalCostUSD > 0 && totalTokens > 0 {
		tokPerDollar := float64(totalTokens) / card.TotalCostUSD
		b.WriteString(fmt.Sprintf("  Tokens/dollar:       %s\n", formatTokens(int64(tokPerDollar))))
	}

	if card.Prompt != "" {
		b.WriteString("\n  Press c to copy prompt · i or Esc to close.\n")
	} else {
		b.WriteString("\n  Press i or Esc to close.\n")
	}
	return helpStyle.Render(b.String())
}

func (m Model) renderSpend() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("  Aggregate Spend") + "\n\n")

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	weekStart := todayStart - 7*86400
	monthStart := todayStart - 30*86400

	var totalToday, totalWeek, totalMonth, totalLifetime float64
	repoSpend := make(map[string]float64)
	type cardCost struct {
		title string
		cost  float64
	}
	var topCards []cardCost

	for _, card := range m.cards {
		totalLifetime += card.TotalCostUSD
		if card.CreatedAt >= todayStart || card.UpdatedAt >= todayStart {
			totalToday += card.TotalCostUSD
		}
		if card.CreatedAt >= weekStart || card.UpdatedAt >= weekStart {
			totalWeek += card.TotalCostUSD
		}
		if card.CreatedAt >= monthStart || card.UpdatedAt >= monthStart {
			totalMonth += card.TotalCostUSD
		}
		repoSpend[card.RepoName] += card.TotalCostUSD
		if card.TotalCostUSD > 0 {
			topCards = append(topCards, cardCost{title: card.Title, cost: card.TotalCostUSD})
		}
	}

	b.WriteString(fmt.Sprintf("  Today:     $%.2f\n", totalToday))
	b.WriteString(fmt.Sprintf("  This week: $%.2f\n", totalWeek))
	b.WriteString(fmt.Sprintf("  This month:$%.2f\n", totalMonth))
	b.WriteString(fmt.Sprintf("  Lifetime:  $%.2f\n", totalLifetime))
	b.WriteString("\n")

	b.WriteString(titleStyle.Render("  By Repo") + "\n\n")
	for repo, spend := range repoSpend {
		if spend > 0 {
			b.WriteString(fmt.Sprintf("  %-20s $%.2f\n", repo, spend))
		}
	}
	b.WriteString("\n")

	// Top 5 most expensive
	b.WriteString(titleStyle.Render("  Top Sessions") + "\n\n")
	// Simple sort (top 5)
	for i := 0; i < len(topCards) && i < 5; i++ {
		for j := i + 1; j < len(topCards); j++ {
			if topCards[j].cost > topCards[i].cost {
				topCards[i], topCards[j] = topCards[j], topCards[i]
			}
		}
		b.WriteString(fmt.Sprintf("  %d. %-30s $%.2f\n", i+1, truncate(topCards[i].title, 30), topCards[i].cost))
	}

	b.WriteString("\n  Press $ or Esc to close.\n")
	return helpStyle.Render(b.String())
}

func formatTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// --- Helpers ---

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func timeAgo(unix int64) string {
	if unix == 0 {
		return ""
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
