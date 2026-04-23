package poller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/khodaei/hive/internal/config"
	"github.com/khodaei/hive/internal/cost"
	"github.com/khodaei/hive/internal/notify"
	"github.com/khodaei/hive/internal/status"
	"github.com/khodaei/hive/internal/store"
	"github.com/khodaei/hive/internal/tmux"
	"github.com/khodaei/hive/internal/transcripts"
)

// StatusChange is emitted when a card's status changes.
type StatusChange struct {
	CardID    string
	OldStatus store.Status
	NewStatus store.Status
}

// Poller periodically checks tmux panes for all active sessions and updates statuses.
type Poller struct {
	store      *store.Store
	classifier *status.Classifier
	cfg        config.Config
	notifier   *notify.Notifier
	onChange   func(StatusChange) // callback for status changes
	stopCh     chan struct{}
	wg         sync.WaitGroup

	// ctx cancels on Stop(). Long-running background work (Ollama summary
	// calls in particular) respects it so shutdown is bounded.
	ctx    context.Context
	cancel context.CancelFunc

	// Idle-too-long tracking
	idleStartTimes map[string]time.Time // card ID -> when idle started
	idleNotified   map[string]bool      // card ID -> whether we already notified
	costAlerted    map[string]bool      // card ID -> whether budget alert already fired

	// Summary job serialization: at most one summary generation per card at
	// a time. Prevents overlapping slow LLM calls from stacking up when a
	// card flaps between statuses.
	summaryMu       sync.Mutex
	summaryInFlight map[string]bool
}

// New creates a new Poller.
func New(s *store.Store, classifier *status.Classifier, cfg config.Config, onChange func(StatusChange)) *Poller {
	ctx, cancel := context.WithCancel(context.Background())
	return &Poller{
		store:           s,
		classifier:      classifier,
		cfg:             cfg,
		notifier:        notify.New(cfg.Notifications),
		onChange:        onChange,
		stopCh:          make(chan struct{}),
		ctx:             ctx,
		cancel:          cancel,
		idleStartTimes:  make(map[string]time.Time),
		costAlerted:     make(map[string]bool),
		idleNotified:    make(map[string]bool),
		summaryInFlight: make(map[string]bool),
	}
}

// Start begins the polling loop in a goroutine.
func (p *Poller) Start() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(time.Duration(p.cfg.PollIntervalSec) * time.Second)
		defer ticker.Stop()

		// Do an immediate poll
		p.poll()

		for {
			select {
			case <-ticker.C:
				p.poll()
			case <-p.stopCh:
				return
			}
		}
	}()
}

// Stop stops the polling loop and waits for it — and every background
// goroutine tracked by p.wg (including in-flight summary jobs) — to finish.
// Cancels p.ctx first so long-running Ollama calls return promptly.
func (p *Poller) Stop() {
	p.cancel()
	close(p.stopCh)
	p.wg.Wait()
}

func (p *Poller) poll() {
	cards, err := p.store.ListCardsByColumn(store.ColumnActive)
	if err != nil {
		log.Printf("poller: list active cards: %v", err)
		return
	}

	// Bounded concurrency: max 20 concurrent pane captures
	sem := make(chan struct{}, 20)
	var wg sync.WaitGroup

	for _, card := range cards {
		if card.TmuxSession == "" {
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(c store.Card) {
			defer wg.Done()
			defer func() { <-sem }()
			p.checkCard(c)
		}(card)
	}

	wg.Wait()
}

func (p *Poller) checkCard(card store.Card) {
	// Check if tmux session still exists
	if !tmux.HasSession(card.TmuxSession) {
		// Session gone — mark as paused (not archived) so it stays on the board
		oldStatus := card.Status
		p.store.UpdateCardStatus(card.ID, store.StatusPaused)
		p.store.UpdateCardTmuxSession(card.ID, "") // clear stale session name
		// Clean up idle tracking
		delete(p.idleStartTimes, card.ID)
		delete(p.idleNotified, card.ID)
		if p.onChange != nil {
			p.onChange(StatusChange{CardID: card.ID, OldStatus: oldStatus, NewStatus: store.StatusPaused})
		}
		return
	}

	content, err := tmux.CapturePane(card.TmuxSession)
	if err != nil {
		log.Printf("poller: capture pane for %s: %v", card.ID, err)
		return
	}

	newStatus := p.classifier.Classify(card.ID, content, time.Now())
	storeStatus := classifierToStore(newStatus)

	if storeStatus != card.Status {
		oldStatus := card.Status

		if err := p.store.UpdateCardStatus(card.ID, storeStatus); err != nil {
			log.Printf("poller: update status for %s: %v", card.ID, err)
			return
		}

		// Record status event
		p.store.InsertStatusEvent(store.StatusEvent{
			CardID:     card.ID,
			Status:     storeStatus,
			ObservedAt: time.Now().Unix(),
		})

		if p.onChange != nil {
			p.onChange(StatusChange{CardID: card.ID, OldStatus: oldStatus, NewStatus: storeStatus})
		}

		// Fire notification if applicable (skip muted cards)
		if !card.NotificationsMuted {
			p.maybeNotify(card, storeStatus)
		}

		// Kick off a background LLM summary refresh if the new status is in
		// the configured trigger list. Non-blocking and fail-safe: a dead
		// Ollama endpoint just logs once — the rest of the poll tick is
		// unaffected and the card's cache stays as-is so the UI falls back
		// to "Recent turns".
		p.maybeSummarize(card, storeStatus)

		// Send pending prompt when Claude reaches idle for the first time.
		// Wait at least 10s after card creation to avoid sending into MCP/permission
		// prompts that appear during Claude Code startup.
		if storeStatus == store.StatusIdle && card.PendingPrompt != "" && card.TmuxSession != "" {
			startupElapsed := time.Since(time.Unix(card.CreatedAt, 0))
			if startupElapsed < 10*time.Second {
				log.Printf("poller: delaying pending prompt for %s (%.0fs since creation)", card.ID, startupElapsed.Seconds())
			} else {
				log.Printf("poller: sending pending prompt to %s", card.ID)
				// Use paste-buffer so multi-line prompts (e.g. the default
				// pr-review prompt) arrive intact instead of being split
				// on the first newline.
				if err := tmux.Paste(card.TmuxSession, card.PendingPrompt); err != nil {
					log.Printf("poller: send pending prompt: %v", err)
				} else {
					// Wait long enough for Claude to finish ingesting the
					// bracketed-paste block before we fire the submit — a
					// shorter delay caused the Enter to arrive during paste
					// processing and get eaten. C-m is the raw carriage
					// return keycode (what pressing Return sends); more
					// reliable across terminal/tmux configs than 'Enter'.
					time.Sleep(1500 * time.Millisecond)
					if err := exec.Command("tmux", "send-keys", "-t", card.TmuxSession, "C-m").Run(); err != nil {
						log.Printf("poller: submit prompt: %v", err)
					}
					p.store.UpdateCardPendingPrompt(card.ID, "")
					p.store.UpdateCardStatus(card.ID, store.StatusWorking)
				}
			}
		}
	}

	// Track cost from transcript
	p.updateCost(card)

	// Auto-detect PR URL from pane content
	if card.PRURL == "" {
		p.detectPR(card, content)
	}

	// Auto-detect branch from worktree if card branch is empty or generic
	if card.WorktreePath != "" && (card.Branch == "" || card.Branch == "main" || card.Branch == "master") {
		cmd := exec.Command("git", "-C", card.WorktreePath, "branch", "--show-current")
		if out, err := cmd.Output(); err == nil {
			branch := strings.TrimSpace(string(out))
			if branch != "" && branch != card.Branch {
				p.store.UpdateCardColumn(card.ID, card.ColumnID) // no-op to trigger updated_at
				// Use execWrite directly via a helper
				log.Printf("poller: auto-detected branch %s for %s", branch, card.ID)
				p.store.UpdateCardBranch(card.ID, branch)
			}
		}
	}
}

func (p *Poller) updateCost(card store.Card) {
	if card.WorktreePath == "" {
		return
	}

	usage, newOffset, err := transcripts.ReadUsageForWorktree(card.WorktreePath, card.TranscriptOffset)
	if err != nil {
		return // silently skip — transcript may not exist yet
	}

	if newOffset == card.TranscriptOffset {
		return // no new data
	}

	// Calculate incremental cost
	incrementalCost := float64(0)
	if usage.Model != "" {
		c, err := cost.Cost(usage.Model, usage)
		if err == nil {
			incrementalCost = c
		}
	}

	// Accumulate onto existing totals
	p.store.UpdateCardCost(card.ID,
		card.TotalInputTokens+usage.InputTokens,
		card.TotalOutputTokens+usage.OutputTokens,
		card.TotalCacheReadTokens+usage.CacheReadTokens,
		card.TotalCacheWriteTokens+usage.CacheCreationTokens,
		card.TotalCostUSD+incrementalCost,
		usage.Model, newOffset,
	)

	// Record cost snapshot with accumulated total
	newTotal := card.TotalCostUSD + incrementalCost
	if incrementalCost > 0 {
		p.store.InsertCostSnapshot(card.ID, newTotal, time.Now().Unix())
	}

	// Cost budget alert (fire once per card)
	if p.cfg.CostAlertsEnabled && p.cfg.MaxCostPerSession > 0 && newTotal >= p.cfg.MaxCostPerSession {
		if !card.NotificationsMuted && !p.costAlerted[card.ID] {
			p.costAlerted[card.ID] = true
			p.notifier.Send("hive",
				fmt.Sprintf("%s exceeded budget ($%.2f / $%.2f)", card.Title, newTotal, p.cfg.MaxCostPerSession),
				fmt.Sprintf("hive://focus/%s", card.ID),
			)
		}
	}
}

func (p *Poller) maybeNotify(card store.Card, newStatus store.Status) {
	actionURL := fmt.Sprintf("hive://focus/%s", card.ID)

	switch newStatus {
	case store.StatusNeedsInput:
		if p.cfg.Notifications.OnNeedsInput {
			p.notifier.Send("hive", card.Title+" needs input", actionURL)
		}
		// Reset idle tracking
		delete(p.idleStartTimes, card.ID)
		delete(p.idleNotified, card.ID)
	case store.StatusErrored:
		if p.cfg.Notifications.OnErrored {
			p.notifier.Send("hive", card.Title+" has errored", actionURL)
		}
		delete(p.idleStartTimes, card.ID)
		delete(p.idleNotified, card.ID)
	case store.StatusIdle:
		if p.cfg.Notifications.OnIdle {
			p.notifier.Send("hive", card.Title+" is idle", actionURL)
		}
		// Track idle-too-long
		if _, ok := p.idleStartTimes[card.ID]; !ok {
			p.idleStartTimes[card.ID] = time.Now()
		}
		p.checkIdleTooLong(card)
	case store.StatusWorking:
		// Reset idle tracking when working
		delete(p.idleStartTimes, card.ID)
		delete(p.idleNotified, card.ID)
	}
}

func (p *Poller) checkIdleTooLong(card store.Card) {
	threshold := p.cfg.Notifications.IdleTooLongMin
	if threshold <= 0 {
		return
	}
	if p.idleNotified[card.ID] {
		return // already notified for this idle stretch
	}
	start, ok := p.idleStartTimes[card.ID]
	if !ok {
		return
	}
	if time.Since(start) > time.Duration(threshold)*time.Minute {
		p.idleNotified[card.ID] = true
		p.notifier.Send("hive",
			fmt.Sprintf("%s has been idle for %d minutes", card.Title, threshold),
			fmt.Sprintf("hive://focus/%s", card.ID),
		)
	}
}

// prURLPattern matches GitHub/GHE PR URLs in pane content or transcripts.
var prURLPattern = regexp.MustCompile(`https?://[^\s"']+/pull/\d+`)

func (p *Poller) detectPR(card store.Card, content string) {
	match := prURLPattern.FindString(content)
	if match != "" {
		log.Printf("poller: auto-detected PR for %s: %s", card.ID, match)
		p.store.UpdateCardPRURL(card.ID, match)
	}
}

// maybeSummarize fires a background Ollama summary job for a card if the new
// status matches the configured AutoGenerateOn list. Never blocks polling.
func (p *Poller) maybeSummarize(card store.Card, newStatus store.Status) {
	if !p.cfg.Summary.Enabled {
		return
	}
	if card.WorktreePath == "" {
		return
	}
	if !statusInList(string(newStatus), p.cfg.Summary.AutoGenerateOn) {
		return
	}

	p.summaryMu.Lock()
	if p.summaryInFlight[card.ID] {
		p.summaryMu.Unlock()
		return
	}
	p.summaryInFlight[card.ID] = true
	p.summaryMu.Unlock()

	// Register the goroutine with the poller's waitgroup and the shutdown
	// context so Stop() blocks until the Ollama call returns (or timeout).
	p.wg.Add(1)
	go func(c store.Card) {
		defer p.wg.Done()
		defer func() {
			p.summaryMu.Lock()
			delete(p.summaryInFlight, c.ID)
			p.summaryMu.Unlock()
		}()

		// Bail early if shutdown fired between queueing and starting.
		if p.ctx.Err() != nil {
			return
		}

		// Skip if transcript hasn't moved since the last cached summary —
		// cheap metadata read, avoids hitting Ollama unnecessarily.
		paths, err := transcripts.ListTranscripts(c.WorktreePath)
		if err != nil || len(paths) == 0 {
			return
		}
		info, err := os.Stat(paths[0])
		if err != nil {
			return
		}
		if c.SummaryTranscriptMtime >= info.ModTime().Unix() && c.Summary != "" {
			return
		}

		res, err := transcripts.Summarize(p.ctx, c.WorktreePath, transcripts.SummaryOpts{
			OllamaURL:   p.cfg.Summary.OllamaURL,
			Model:       p.cfg.Summary.OllamaModel,
			TurnsWindow: p.cfg.Summary.TurnsWindow,
			MaxTokens:   p.cfg.Summary.MaxTokens,
			Timeout:     time.Duration(p.cfg.Summary.TimeoutSec) * time.Second,
		})
		if err != nil {
			// Context cancelled during shutdown is expected and quiet.
			if errors.Is(err, context.Canceled) || errors.Is(err, transcripts.ErrNoTranscript) {
				return
			}
			log.Printf("poller: summarize %s: %v", c.ID, err)
			return
		}
		// Re-check shutdown before writing: we don't want to race with
		// store.Close() in the daemon's shutdown path.
		if p.ctx.Err() != nil {
			return
		}
		if err := p.store.UpdateCardSummary(c.ID, res.Text, res.TranscriptMtime); err != nil {
			log.Printf("poller: cache summary for %s: %v", c.ID, err)
		}
	}(card)
}

func statusInList(status string, list []string) bool {
	for _, s := range list {
		if s == status {
			return true
		}
	}
	return false
}

func classifierToStore(s status.Status) store.Status {
	switch s {
	case status.NeedsInput:
		return store.StatusNeedsInput
	case status.Errored:
		return store.StatusErrored
	case status.Working:
		return store.StatusWorking
	case status.Idle:
		return store.StatusIdle
	default:
		return store.StatusUnknown
	}
}
