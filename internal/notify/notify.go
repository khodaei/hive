package notify

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/khodaei/hive/internal/config"
)

// Notifier handles notification delivery with batching and DND support.
type Notifier struct {
	cfg config.Notifications

	mu          sync.Mutex
	recentCount int
	recentReset time.Time
	batchWindow time.Duration
}

// New creates a new Notifier.
func New(cfg config.Notifications) *Notifier {
	return &Notifier{
		cfg:         cfg,
		batchWindow: 10 * time.Second,
	}
}

// Send sends a notification, respecting batching and DND rules.
func (n *Notifier) Send(title, body, actionURL string) {
	if !n.cfg.Enabled {
		return
	}

	// Check quiet hours
	if n.isQuietTime() {
		return
	}

	n.mu.Lock()
	now := time.Now()

	// Reset batch counter if window expired
	if now.After(n.recentReset) {
		n.recentCount = 0
		n.recentReset = now.Add(n.batchWindow)
	}

	n.recentCount++

	// If 3+ notifications in window, batch them
	if n.recentCount == 3 {
		n.mu.Unlock()
		sendNotification(
			"hive",
			fmt.Sprintf("%d agents need attention", n.recentCount),
			"hive://filter/needs-input",
		)
		return
	}
	if n.recentCount > 3 {
		n.mu.Unlock()
		return // suppress individual notifications after batch fires
	}
	n.mu.Unlock()

	sendNotification(title, body, actionURL)
}

func (n *Notifier) isQuietTime() bool {
	qh := n.cfg.QuietHours
	if qh.Start == "" || qh.End == "" {
		return false
	}

	now := time.Now()
	startH, startM := parseTime(qh.Start)
	endH, endM := parseTime(qh.End)

	nowMinutes := now.Hour()*60 + now.Minute()
	startMinutes := startH*60 + startM
	endMinutes := endH*60 + endM

	if startMinutes < endMinutes {
		// Same day range (e.g., 09:00 - 17:00)
		return nowMinutes >= startMinutes && nowMinutes < endMinutes
	}
	// Overnight range (e.g., 22:00 - 07:00)
	return nowMinutes >= startMinutes || nowMinutes < endMinutes
}

func parseTime(s string) (int, int) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h, m
}

func sendNotification(title, body, actionURL string) {
	// Prefer terminal-notifier if available (supports click actions)
	if path, err := exec.LookPath("terminal-notifier"); err == nil {
		args := []string{"-title", title, "-message", body, "-group", "hive"}
		if actionURL != "" {
			args = append(args, "-open", actionURL)
		}
		exec.Command(path, args...).Run()
		return
	}

	// Fallback to osascript
	script := fmt.Sprintf(`display notification %q with title %q`, body, title)
	exec.Command("osascript", "-e", script).Run()
}

// Legacy function for backward compatibility
func Notify(title, body string) error {
	sendNotification(title, body, "")
	return nil
}
