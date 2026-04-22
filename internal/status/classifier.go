package status

import (
	"hash/fnv"
	"regexp"
	"time"
)

// Status represents the classified state of a Claude Code session.
type Status string

const (
	NeedsInput Status = "needs_input"
	Errored    Status = "errored"
	Working    Status = "working"
	Idle       Status = "idle"
	Unknown    Status = "unknown"
)

// cardState tracks the last observed state for a card.
type cardState struct {
	lastHash     uint64
	lastChangeTS time.Time
}

// Classifier classifies tmux pane content into a session status.
// It is stateful: it tracks content changes per card to distinguish working from idle.
type Classifier struct {
	states         map[string]*cardState
	idleThreshold  time.Duration
	needsInputPats []*regexp.Regexp
	erroredPats    []*regexp.Regexp
	idlePats       []*regexp.Regexp
}

// New creates a new Classifier with the given idle threshold.
func New(idleThreshold time.Duration) *Classifier {
	return &Classifier{
		states:         make(map[string]*cardState),
		idleThreshold:  idleThreshold,
		needsInputPats: compilePatterns(needsInputPatterns),
		erroredPats:    compilePatterns(erroredPatterns),
		idlePats:       compilePatterns(idlePatterns),
	}
}

// Classify determines the status of a card based on its pane content.
// Priority (first match wins):
//  1. needs_input — pane contains approval/confirmation prompts
//  2. errored — pane contains error indicators
//  3. working — content changed recently
//  4. idle — content stable and matches prompt indicator
//  5. unknown — fallback
func (c *Classifier) Classify(cardID string, content string, now time.Time) Status {
	// 1. Check needs_input (highest priority)
	for _, pat := range c.needsInputPats {
		if pat.MatchString(content) {
			c.updateState(cardID, content, now)
			return NeedsInput
		}
	}

	// 2. Check errored
	for _, pat := range c.erroredPats {
		if pat.MatchString(content) {
			c.updateState(cardID, content, now)
			return Errored
		}
	}

	// 3. Check working (content changed)
	hash := hashContent(content)
	state := c.getOrCreateState(cardID)
	contentChanged := hash != state.lastHash

	if contentChanged {
		state.lastHash = hash
		state.lastChangeTS = now
		return Working
	}

	// Content hasn't changed — check how long it's been stable
	elapsed := now.Sub(state.lastChangeTS)

	if elapsed < c.idleThreshold {
		// Recently changed, still probably working
		return Working
	}

	// 4. Check idle (stable content + prompt indicator)
	for _, pat := range c.idlePats {
		if pat.MatchString(content) {
			return Idle
		}
	}

	// 5. Fallback
	return Unknown
}

// Reset removes state for a card (e.g., when archived).
func (c *Classifier) Reset(cardID string) {
	delete(c.states, cardID)
}

func (c *Classifier) getOrCreateState(cardID string) *cardState {
	s, ok := c.states[cardID]
	if !ok {
		s = &cardState{}
		c.states[cardID] = s
	}
	return s
}

func (c *Classifier) updateState(cardID string, content string, now time.Time) {
	state := c.getOrCreateState(cardID)
	hash := hashContent(content)
	if hash != state.lastHash {
		state.lastHash = hash
		state.lastChangeTS = now
	}
}

func hashContent(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		compiled[i] = regexp.MustCompile(p)
	}
	return compiled
}

// --- Pattern definitions ---
// Keep all patterns here in one place for easy maintenance.

var needsInputPatterns = []string{
	`Do you want to proceed`,
	`\[y/N\]`,
	`\[Y/n\]`,
	`Allow this tool to run\?`,
	`Approve this (edit|command|action)\?`,
	`❯\s*1\.\s*Yes`,
	`\? Allow`,
	`\(y\)es`,
	`Press Enter to continue`,
	// Newer Claude Code permission model
	`Allow once`,
	`Allow always`,
	`Do you want to allow`,
	`Allow this action`,
	`Approve tool use`,
	// MCP server selection / interactive prompts
	`Space to select`,
	`Enter to confirm`,
	`Select any you wish to enable`,
	`Esc to reject`,
	`MCP server`,
	`trust.*\.mcp\.json`,
	`Do you want to trust`,
	`(?i)enable.*mcp`,
}

var erroredPatterns = []string{
	`Traceback \(most recent call last\)`,
	`claude: command not found`,
	`\[process exited with code [1-9]`,
	`Error:.*fatal`,
	`panic:`,
	// Rate limits
	`Rate limit exceeded`,
	`You've reached your usage limit`,
	`rate_limit_error`,
}

var idlePatterns = []string{
	`╭─+╮`,                        // Claude Code input box top border
	`(?m)>\s*$`,                   // bare > prompt (multiline so $ matches each line)
	`(?m)❯\s*$`,                   // Claude Code / fish / starship prompt
	`(?m)\$\s*$`,                  // bash prompt
	`What would you like to do\?`, // Claude Code idle prompt
	`Opus.*context\)`,             // Claude status bar visible = at idle prompt
}
