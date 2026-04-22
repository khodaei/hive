package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/khodaei/hive/internal/cli"
	"github.com/khodaei/hive/internal/config"
	gitpkg "github.com/khodaei/hive/internal/git"
	"github.com/khodaei/hive/internal/logrotate"
	"github.com/khodaei/hive/internal/poller"
	"github.com/khodaei/hive/internal/recovery"
	"github.com/khodaei/hive/internal/status"
	"github.com/khodaei/hive/internal/store"
	"github.com/khodaei/hive/internal/templates"
	"github.com/khodaei/hive/internal/tmux"
	"github.com/khodaei/hive/internal/transcripts"
	"github.com/khodaei/hive/internal/tui"
)

const version = "0.1.0"

// sentinelRepoNone is the repo name attached to ad-hoc sessions — those
// started by `hive new` in a directory that doesn't match any configured
// repo. Ensures cards always have a valid repos.name FK target and gives
// the user something to filter on (`hive ls -r '(none)'`).
const sentinelRepoNone = "(none)"

// Exit codes (documented contract):
//
//	0 success
//	1 general error
//	2 query ambiguous / picker required in non-interactive context
//	3 dependency missing (tmux, git, ...)
//	4 config invalid
const (
	exitGeneral        = 1
	exitQueryAmbiguous = 2
	exitDepMissing     = 3
	exitConfigInvalid  = 4
)

// exitWith prints a message to stderr and exits with the given code.
func exitWith(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(code)
}

// resolveCardOrExit wraps cli.ResolveOrPick with the CLI's exit-code contract.
//   - User cancels the picker: returns (_, false); caller usually just returns.
//   - Non-TTY context with no unambiguous resolution: exit 2 (lists candidates).
//   - Other errors: exit 1.
func resolveCardOrExit(cards []store.Card, query, verb string) (store.Card, bool) {
	card, err := cli.ResolveOrPick(cards, query)
	if err == nil {
		return card, true
	}
	if errors.Is(err, cli.ErrPickerCancelled) {
		return store.Card{}, false
	}
	if errors.Is(err, cli.ErrNoTTY) {
		matches := cli.Matches(cards, query)
		if query == "" {
			exitWith(exitQueryAmbiguous, "%s: no query and not a TTY — pass a query or run interactively", verb)
		}
		if len(matches) == 0 {
			exitWith(exitQueryAmbiguous, "%s: no card matching %q", verb, query)
		}
		fmt.Fprintf(os.Stderr, "%s: ambiguous query %q — matches:\n", verb, query)
		for _, m := range matches {
			fmt.Fprintf(os.Stderr, "  %s  %s  [%s]\n", m.ID, m.Title, m.RepoName)
		}
		os.Exit(exitQueryAmbiguous)
	}
	exitWith(exitGeneral, "%s: %v", verb, err)
	return store.Card{}, false
}

func main() {
	// Pre-flight checks
	checkDependency("tmux", "tmux is required. Install with: brew install tmux")
	checkDependency("git", "git is required. Install with: xcode-select --install")

	if len(os.Args) < 2 {
		runPickerOrHelp()
		return
	}

	switch os.Args[1] {
	case "new":
		runNew(os.Args[2:])
	case "tui":
		runTUI()
	case "daemon":
		runDaemon()
	case "list", "ls":
		runList(os.Args[2:])
	case "archived":
		// Thin alias for `hive ls -s archived` — lets users type `hive archived`
		// to browse everything they've archived (covers both col=Done and
		// col=Archived since both have status=archived after `hive done`).
		runList(append([]string{"-s", "archived"}, os.Args[2:]...))
	case "watch":
		runWatch(os.Args[2:])
	case "attach", "a":
		runAttach(os.Args[2:])
	case "last", "-":
		runLast()
	case "peek":
		runPeek(os.Args[2:])
	case "card":
		runCard(os.Args[2:])
	case "summarize":
		runSummarize(os.Args[2:])
	case "done":
		runDone(os.Args[2:])
	case "resume":
		runResume(os.Args[2:])
	case "rm":
		runDelete(os.Args[2:], "rm")
	case "kill":
		runDelete(os.Args[2:], "kill")
	case "import":
		runImport()
	case "open":
		runOpen(os.Args[2:])
	case "cd":
		runCd(os.Args[2:])
	case "tail":
		runTail(os.Args[2:])
	case "search":
		runSearch(os.Args[2:])
	case "doctor":
		runDoctor()
	case "status":
		runStatus(os.Args[2:])
	case "send":
		runSend(os.Args[2:])
	case "config":
		runConfigCheck()
	case "template":
		runTemplate()
	case "version":
		fmt.Printf("hive %s\n", version)
	case "--help", "-h", "help":
		if len(os.Args) >= 3 {
			printVerbHelp(os.Args[2])
			return
		}
		printUsage()
	default:
		// Bare positional args ("hive \"fix auth bug\"", "hive ." etc.) are
		// treated as `hive new <those args>` so the CLI's primary create verb
		// is reachable with one fewer keystroke.
		runNew(os.Args[1:])
	}
}

// runPickerOrHelp is the no-arg entry point. It loops a fuzzy picker over
// live (non-archived) cards; Enter attaches and exits, Right archives the
// selection in place, Left restores the most-recently archived card.
// Without any cards — prints usage. Without a TTY — prints usage too.
func runPickerOrHelp() {
	cfg, s := mustLoadConfigAndStore()
	defer s.Close()

	for {
		cards := visiblePickCards(s)
		if len(cards) == 0 {
			printUsage()
			return
		}
		card, action, err := cli.PickWithActions(cards, "")
		if err != nil {
			if errors.Is(err, cli.ErrPickerCancelled) {
				return
			}
			if errors.Is(err, cli.ErrNoTTY) {
				printUsage()
				return
			}
			log.Fatalf("picker: %v", err)
		}

		switch action {
		case cli.ActionArchive:
			archiveInPlace(s, cfg, card)
			continue
		case cli.ActionUnarchive:
			if !unarchiveLatest(s, cfg) {
				// Tiny hint written to stderr so the user knows why the
				// list didn't change.
				fmt.Fprintln(os.Stderr, "hive: nothing to restore (no archived cards).")
			}
			continue
		default:
			attachToCard(s, cfg, card)
			return
		}
	}
}

// visiblePickCards returns cards eligible for the default picker view —
// everything except the Archived column and cards whose status is
// StatusArchived. Matches the filter runPickerOrHelp used before the
// loop-with-actions rewrite.
func visiblePickCards(s *store.Store) []store.Card {
	all, err := s.ListCards()
	if err != nil {
		return nil
	}
	out := make([]store.Card, 0, len(all))
	for _, c := range all {
		if c.ColumnID == store.ColumnArchived || c.Status == store.StatusArchived {
			continue
		}
		out = append(out, c)
	}
	return out
}

// archiveInPlace performs the same DB + tmux state changes as `hive done -y`
// but without the interactive undo window or per-archive output. Worktree
// removal follows config.ArchiveBehavior (delete / keep / prompt-as-keep).
func archiveInPlace(s *store.Store, cfg config.Config, card store.Card) {
	if err := s.UpdateCardColumn(card.ID, store.ColumnDone); err != nil {
		log.Printf("archive: %v", err)
		return
	}
	if err := s.UpdateCardStatus(card.ID, store.StatusArchived); err != nil {
		log.Printf("archive: %v", err)
		return
	}
	finalizeArchive(s, card, cfg.ArchiveBehavior == "delete")
}

// unarchiveLatest restores the most-recently-archived card: flips it back to
// Active/Working and re-creates the tmux session (via the same logic used by
// `hive resume`). Returns false if there's nothing to restore.
func unarchiveLatest(s *store.Store, cfg config.Config) bool {
	card, err := s.FindLastArchivedCard()
	if err != nil {
		return false
	}

	// Resume the card: re-create tmux if needed, re-launch claude --resume.
	tmuxName := cfg.TmuxPrefix + card.ID
	if !tmux.HasSession(tmuxName) {
		if card.WorktreePath == "" {
			log.Printf("unarchive: card %s has no worktree", card.ID)
			return false
		}
		if err := tmux.NewSession(tmuxName, card.WorktreePath); err != nil {
			log.Printf("unarchive: new tmux: %v", err)
			return false
		}
		claudeCmd := cfg.ClaudeCmd
		if card.ClaudeSessionID != "" {
			claudeCmd = fmt.Sprintf("%s --resume %s", cfg.ClaudeCmd, card.ClaudeSessionID)
		}
		if err := tmux.SendKeys(tmuxName, claudeCmd); err != nil {
			log.Printf("unarchive: launch claude: %v", err)
		}
	}
	s.UpdateCardColumn(card.ID, store.ColumnActive)
	s.UpdateCardStatus(card.ID, store.StatusWorking)
	s.UpdateCardTmuxSession(card.ID, tmuxName)
	return true
}

// attachToCard attaches the current terminal to a card's tmux session, stamps
// last_attached_at, and handles the inside-tmux (switch-client) case.
// If the card has no live tmux session (paused / done / tmux died), this
// transparently resumes it first — creating a new tmux session, running
// `claude --resume <id>` (or just `claude` if no session ID), and flipping
// the card back to Active / Working.
func attachToCard(s *store.Store, cfg config.Config, c store.Card) {
	// Archived-column cards are deliberately out of reach: there's no
	// tmux session and we don't auto-resume them without an explicit verb.
	if c.ColumnID == store.ColumnArchived {
		log.Fatalf("card %s (%s) is in the Archived column — use `hive resume %s` instead", c.ID, c.Title, c.ID)
	}

	// If there's no live tmux session, auto-resume first.
	if c.TmuxSession == "" || !tmux.HasSession(c.TmuxSession) {
		resumed, err := resumeSession(s, cfg, c)
		if err != nil {
			log.Fatalf("resume %s: %v", c.ID, err)
		}
		c = resumed
	}

	if err := s.UpdateCardLastAttached(c.ID); err != nil {
		log.Printf("stamp last_attached_at: %v", err)
	}
	if os.Getenv("TMUX") != "" {
		if err := exec.Command("tmux", "switch-client", "-t", c.TmuxSession).Run(); err != nil {
			log.Fatalf("tmux switch-client: %v", err)
		}
		return
	}
	cmd := tmux.AttachCommand(c.TmuxSession)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("tmux attach: %v", err)
	}
}

// resumeSession re-creates a tmux session for a card that doesn't have a live
// one, runs `claude --resume <session_id>` (or just `claude` when no Claude
// session ID is known), and flips the card back to col=Active / status=Working.
// Used both by `hive resume` and transparently by attachToCard when the user
// hits Enter on a paused / done card in the picker.
func resumeSession(s *store.Store, cfg config.Config, card store.Card) (store.Card, error) {
	if card.WorktreePath == "" {
		return card, fmt.Errorf("card has no worktree path")
	}
	tmuxName := cfg.TmuxPrefix + card.ID
	if !tmux.HasSession(tmuxName) {
		if err := tmux.NewSession(tmuxName, card.WorktreePath); err != nil {
			return card, fmt.Errorf("create tmux session: %w", err)
		}
		claudeCmd := cfg.ClaudeCmd
		if card.ClaudeSessionID != "" {
			claudeCmd = fmt.Sprintf("%s --resume %s", cfg.ClaudeCmd, card.ClaudeSessionID)
		}
		// Prefix an explicit `cd` so the resume happens in the worktree even
		// if a login shell's init scripts chdir'd to $HOME (common on macOS
		// setups). Claude Code scopes transcripts by cwd — `claude --resume
		// <uuid>` from the wrong dir can't find the session.
		fullCmd := fmt.Sprintf("cd %q && %s", card.WorktreePath, claudeCmd)
		if err := tmux.SendKeys(tmuxName, fullCmd); err != nil {
			return card, fmt.Errorf("launch claude: %w", err)
		}
	}
	s.UpdateCardColumn(card.ID, store.ColumnActive)
	s.UpdateCardStatus(card.ID, store.StatusWorking)
	s.UpdateCardTmuxSession(card.ID, tmuxName)

	card.TmuxSession = tmuxName
	card.ColumnID = store.ColumnActive
	card.Status = store.StatusWorking
	return card, nil
}

// --- TUI ---

func runTUI() {
	cfg, s := mustLoadConfigAndStore()
	defer s.Close()

	syncRepos(cfg, s)

	// Reconcile DB with tmux state on startup
	result := recovery.Reconcile(s, cfg.TmuxPrefix)
	if result.DeadSessions > 0 {
		fmt.Fprintf(os.Stderr, "hive: %d session(s) disappeared since last run (marked errored)\n", result.DeadSessions)
	}
	if len(result.OrphanedSessions) > 0 {
		fmt.Fprintf(os.Stderr, "hive: %d orphaned tmux session(s) found\n", len(result.OrphanedSessions))
	}

	m := tui.NewModel(s, cfg)
	p := tea.NewProgram(m, tea.WithAltScreen())

	// Handle SIGTERM/SIGINT gracefully
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		// Send quit to Bubble Tea which will trigger cleanup
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		log.Fatalf("TUI error: %v", err)
	}
}

// --- Daemon ---

func runDaemon() {
	cfg, s := mustLoadConfigAndStore()
	defer s.Close()

	syncRepos(cfg, s)

	// Set up logging with rotation
	dir, _ := config.Dir()
	logWriter, err := logrotate.New(filepath.Join(dir, "daemon.log"), cfg.LogMaxSizeMB)
	if err != nil {
		log.Fatalf("open log file: %v", err)
	}
	defer logWriter.Close()
	log.SetOutput(logWriter)

	classifier := status.New(time.Duration(cfg.IdleThresholdSec) * time.Second)
	p := poller.New(s, classifier, cfg, func(sc poller.StatusChange) {
		log.Printf("status change: card=%s %s -> %s", sc.CardID, sc.OldStatus, sc.NewStatus)
	})

	log.Println("hive daemon started")
	p.Start()

	// Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("hive daemon stopping")
	p.Stop()
}

// --- List ---

// runList backs `hive list` / `hive ls`. Flags: --repo, --status, --column,
// --since <dur>, --json, --format table|tsv|json.
func runList(args []string) {
	var filters cli.ListFilters
	format := cli.FormatTable
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo", "-r":
			if i+1 < len(args) {
				filters.Repo = args[i+1]
				i++
			}
		case "--status", "-s":
			if i+1 < len(args) {
				filters.Status = args[i+1]
				i++
			}
		case "--column", "-c":
			if i+1 < len(args) {
				filters.Column = args[i+1]
				i++
			}
		case "--since":
			if i+1 < len(args) {
				d, err := cli.ParseDuration(args[i+1])
				if err != nil {
					log.Fatalf("--since: %v", err)
				}
				filters.Since = d
				i++
			}
		case "--json":
			format = cli.FormatJSON
		case "--format", "-f":
			if i+1 < len(args) {
				switch args[i+1] {
				case "json":
					format = cli.FormatJSON
				case "tsv":
					format = cli.FormatTSV
				case "table":
					format = cli.FormatTable
				default:
					log.Fatalf("--format: unknown value %q (table|tsv|json)", args[i+1])
				}
				i++
			}
		default:
			log.Fatalf("ls: unknown flag %q", args[i])
		}
	}

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	all, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	cards := cli.ApplyFilters(all, filters)

	if len(cards) == 0 && format == cli.FormatTable {
		fmt.Println("No cards.")
		return
	}
	if err := cli.WriteCards(os.Stdout, cards, format); err != nil {
		log.Fatalf("write: %v", err)
	}
}

// --- New (create + attach) ---

// runNew is `hive new [title] [-p prompt] [-r repo] [-b branch] [-w worktree]`.
//   - Creates a new tmux session running the configured claude command, then
//     attaches (switch-client if already inside tmux).
//   - If a title is provided, creates a card: auto-detects repo from cwd
//     unless overridden with -r, uses cwd as worktree when it's inside one,
//     otherwise creates a new worktree.
//   - Without a title, runs a cardless Claude session rooted in cwd.
//
// Bare positional forms (`hive "fix auth"`, `hive .`) dispatch here too.
func runNew(args []string) {
	var title, prompt, repoName, branchOverride, worktreeOverride string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--prompt":
			if i+1 < len(args) {
				prompt = args[i+1]
				i++
			}
		case "-r", "--repo":
			if i+1 < len(args) {
				repoName = args[i+1]
				i++
			}
		case "-b", "--branch":
			if i+1 < len(args) {
				branchOverride = args[i+1]
				i++
			}
		case "-w", "--worktree":
			if i+1 < len(args) {
				worktreeOverride = args[i+1]
				i++
			}
		case "-t", "--title":
			if i+1 < len(args) {
				title = args[i+1]
				i++
			}
		case ".":
			// `hive new .` / `hive .` means "use cwd as worktree".
			cwd, _ := os.Getwd()
			worktreeOverride = cwd
		default:
			if !strings.HasPrefix(args[i], "-") && title == "" {
				title = args[i]
			}
		}
	}
	if title == "" && worktreeOverride != "" {
		title = filepath.Base(worktreeOverride)
	}
	newCreate(title, prompt, repoName, branchOverride, worktreeOverride)
}

// newCreate is the shared create-and-attach implementation.
func newCreate(title, prompt, repoName, branchOverride, worktreeOverride string) {
	cfg, s := mustLoadConfigAndStore()
	defer s.Close()

	cwd, _ := os.Getwd()
	inTmux := os.Getenv("TMUX") != ""

	var sessionName, wtPath string
	var card *store.Card

	if title == "" && worktreeOverride == "" {
		// Standalone Claude session (no card), run in cwd.
		sessionName = fmt.Sprintf("claude_%d", time.Now().Unix())
		wtPath = cwd
	} else {
		syncRepos(cfg, s)

		var repoConfig *config.Repo
		if repoName != "" {
			for _, r := range cfg.Repos {
				r := r
				if r.Name == repoName {
					repoConfig = &r
					break
				}
			}
			if repoConfig == nil {
				fmt.Fprintf(os.Stderr, "Repo %q not found. Configured repos:\n", repoName)
				for _, r := range cfg.Repos {
					fmt.Fprintf(os.Stderr, "  %s (%s)\n", r.Name, r.Path)
				}
				os.Exit(1)
			}
		} else {
			// Auto-detect from cwd. Prefer the longest matching repo path so
			// nested repos resolve to the closest one.
			absCwd, _ := filepath.Abs(cwd)
			var bestLen int
			for _, r := range cfg.Repos {
				r := r
				absRepo, _ := filepath.Abs(r.Path)
				if absCwd == absRepo || strings.HasPrefix(absCwd, absRepo+string(filepath.Separator)) {
					if len(absRepo) > bestLen {
						bestLen = len(absRepo)
						repoConfig = &r
					}
				}
			}
		}
		// No configured repo matched cwd and none was given via -r. Track it
		// anyway as an "(none)" sentinel-repo card anchored at cwd so the
		// rest of hive (ls/attach/card/done/resume/summary) all work.
		adhoc := repoConfig == nil
		if adhoc {
			repoConfig = &config.Repo{Name: sentinelRepoNone, Path: "", DefaultBranch: ""}
		}

		if title == "" {
			// Derive a sensible default title if the caller only gave us `.`
			// or a worktree without a title.
			if worktreeOverride != "" {
				title = filepath.Base(worktreeOverride)
			} else {
				title = filepath.Base(cwd)
			}
		}
		branch := branchOverride
		switch {
		case adhoc:
			// Ad-hoc: the "worktree" is just cwd. Skip git worktree creation.
			wtPath = cwd
			if branch == "" {
				branch = currentBranchAt(wtPath) // empty for non-git dirs
			}
			fmt.Printf("Ad-hoc session in %s", wtPath)
			if branch != "" {
				fmt.Printf(" (branch: %s)", branch)
			}
			fmt.Println()
		case worktreeOverride != "":
			wtPath = worktreeOverride
			if branch == "" {
				branch = currentBranchAt(wtPath)
				if branch == "" {
					branch = repoConfig.DefaultBranch
				}
			}
			fmt.Printf("Using worktree: %s (branch: %s)\n", wtPath, branch)
		default:
			detectedWT, detectedBranch := detectCurrentWorktree(cwd, repoConfig)
			if detectedWT != "" {
				wtPath = detectedWT
				if branch == "" {
					branch = detectedBranch
					if branch == "" {
						branch = repoConfig.DefaultBranch
					}
				}
				fmt.Printf("Using existing worktree: %s (branch: %s)\n", wtPath, branch)
			} else {
				if branch == "" {
					branch = strings.ReplaceAll(strings.ToLower(title), " ", "-")
				}
				fmt.Printf("Creating new worktree in %s (branch: %s)...\n", repoConfig.Name, branch)
				gitpkg.Fetch(repoConfig.Path)
				var err error
				wtPath, err = gitpkg.CreateWorktree(repoConfig.Path, branch, repoConfig.DefaultBranch)
				if err != nil {
					log.Fatalf("create worktree: %v", err)
				}
			}
		}

		cardID := tui.GenerateID()
		sessionName = cfg.TmuxPrefix + cardID
		now := time.Now().Unix()
		card = &store.Card{
			ID: cardID, Title: title, Prompt: prompt,
			RepoName: repoConfig.Name, Branch: branch, WorktreePath: wtPath,
			ColumnID: store.ColumnActive, Status: store.StatusWorking,
			TmuxSession: sessionName, CreatedAt: now, UpdatedAt: now,
		}
		if prompt != "" {
			card.PendingPrompt = prompt
		}
	}

	if err := tmux.NewSession(sessionName, wtPath); err != nil {
		log.Fatalf("create tmux session: %v", err)
	}
	if err := tmux.SendKeys(sessionName, cfg.ClaudeCmd); err != nil {
		log.Fatalf("send claude command: %v", err)
	}

	if card != nil {
		if err := s.InsertCard(*card); err != nil {
			log.Fatalf("insert card: %v", err)
		}
		fmt.Printf("Created card: %s (tmux: %s)\n", card.ID, sessionName)
	}

	promptDone := make(chan struct{})
	if prompt != "" {
		cardID := ""
		if card != nil {
			cardID = card.ID
		}
		go func(session, text, cid string) {
			defer close(promptDone)
			// Fixed 12-second delay gives Claude time to boot; settings
			// (enabledMcpjsonServers) should prevent MCP dialogs from blocking.
			// Clear the card's PendingPrompt on success so a concurrent daemon
			// won't re-send.
			time.Sleep(12 * time.Second)
			if err := tmux.SendKeysLiteral(session, text); err != nil {
				log.Printf("send prompt: %v", err)
				return
			}
			time.Sleep(500 * time.Millisecond)
			if err := exec.Command("tmux", "send-keys", "-t", session, "Enter").Run(); err != nil {
				log.Printf("send prompt enter: %v", err)
				return
			}
			if cid != "" {
				s.UpdateCardPendingPrompt(cid, "")
			}
		}(sessionName, prompt, cardID)
	} else {
		close(promptDone)
	}

	if inTmux {
		if err := exec.Command("tmux", "switch-client", "-t", sessionName).Run(); err != nil {
			fmt.Printf("Started session %s — attach with: tmux attach -t %s\n", sessionName, sessionName)
		}
		<-promptDone
		return
	}

	attach := tmux.AttachCommand(sessionName)
	attach.Stdin = os.Stdin
	attach.Stdout = os.Stdout
	attach.Stderr = os.Stderr
	if err := attach.Run(); err != nil {
		log.Fatalf("tmux attach: %v", err)
	}
	<-promptDone
}

// currentBranchAt returns the branch of a worktree path, or "".
func currentBranchAt(path string) string {
	out, err := exec.Command("git", "-C", path, "branch", "--show-current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectCurrentWorktree returns the worktree path and branch for the cwd if
// cwd is inside a valid git working tree that belongs to the given repo.
// Returns empty strings if cwd is outside a working tree, is the bare-repo
// phantom root, or belongs to a different repo than repoConfig.
func detectCurrentWorktree(cwd string, repoConfig *config.Repo) (string, string) {
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", ""
	}
	topLevel := strings.TrimSpace(string(out))
	if topLevel == "" {
		return "", ""
	}

	absTop, _ := filepath.Abs(topLevel)
	absRepo, _ := filepath.Abs(repoConfig.Path)

	// The detected worktree must belong to this repo. Allow either an exact
	// match with repoConfig.Path (normal repo) or a path inside it (bare repo
	// worktrees sit inside the repo root alongside .bare/).
	if absTop != absRepo && !strings.HasPrefix(absTop, absRepo+string(filepath.Separator)) {
		return "", ""
	}

	// For bare repo layout, the repo root contains .bare/ and a .git link file,
	// but isn't a useful working tree — skip it and create a new worktree.
	if absTop == absRepo && gitpkg.DetectLayout(repoConfig.Path) == gitpkg.LayoutBare {
		return "", ""
	}

	branchOut, _ := exec.Command("git", "-C", topLevel, "branch", "--show-current").Output()
	branch := strings.TrimSpace(string(branchOut))
	return topLevel, branch
}


// --- Attach ---

// runAttach backs `hive attach` and `hive a`. With a query, resolves it via
// the shared precedence rules; on ambiguity or no query, opens the picker.
func runAttach(args []string) {
	query := ""
	if len(args) > 0 {
		query = strings.Join(args, " ")
	}
	cfg, s := mustLoadConfigAndStore()
	defer s.Close()

	cards := livePickCards(s)
	if len(cards) == 0 {
		log.Fatalf("no active cards to attach to. Create one with: hive new <title>")
	}

	card, ok := resolveCardOrExit(cards, query, "attach")
	if !ok {
		return
	}
	attachToCard(s, cfg, card)
}

// livePickCards returns cards eligible for the picker/resolve path. Archived
// cards are hidden since they can't be attached to directly.
func livePickCards(s *store.Store) []store.Card {
	all, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	live := make([]store.Card, 0, len(all))
	for _, c := range all {
		if c.ColumnID == store.ColumnArchived || c.Status == store.StatusArchived {
			continue
		}
		live = append(live, c)
	}
	return live
}

// runLast backs `hive last` / `hive -`: attach the most-recently-attached card.
func runLast() {
	cfg, s := mustLoadConfigAndStore()
	defer s.Close()
	card, err := s.FindLastAttachedCard()
	if err != nil {
		log.Fatalf("no previously-attached card on record. Use `hive` to pick one.")
	}
	attachToCard(s, cfg, card)
}

// --- Peek ---

// runPeek backs `hive peek [query] [-n N]` — print the last N lines of the
// target card's tmux pane without attaching. Defaults to 40 lines.
func runPeek(args []string) {
	n := 40
	var queryParts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n", "--lines":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil || v <= 0 {
					log.Fatalf("bad -n value %q", args[i+1])
				}
				n = v
				i++
			}
		default:
			queryParts = append(queryParts, args[i])
		}
	}
	query := strings.Join(queryParts, " ")

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	cards := livePickCards(s)
	if len(cards) == 0 {
		log.Fatalf("no active cards.")
	}
	card, ok := resolveCardOrExit(cards, query, "peek")
	if !ok {
		return
	}
	if card.TmuxSession == "" || !tmux.HasSession(card.TmuxSession) {
		log.Fatalf("card %s (%s) has no live tmux session", card.ID, card.Title)
	}
	out, err := tmux.CapturePane(card.TmuxSession)
	if err != nil {
		log.Fatalf("capture pane: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		fmt.Println(l)
	}
}

// --- Summary (local LLM via Ollama) ---

// runSummarize backs `hive summarize [query] [--all] [--force]`.
//
// Regenerates the LLM summary for one or more cards by calling the Ollama
// instance configured under `summary:` in config.yaml. Skips cards whose
// cached summary is already fresh unless --force is passed. Ollama must be
// reachable; on error, prints the message and exits non-zero. The picker
// preview and `hive card` degrade gracefully (falling back to raw Recent
// turns) when no cached summary exists, so this verb is never required for
// the rest of the CLI to work.
func runSummarize(args []string) {
	all := false
	force := false
	var positional []string
	for _, a := range args {
		switch a {
		case "--all", "-a":
			all = true
		case "--force", "-f":
			force = true
		default:
			positional = append(positional, a)
		}
	}
	query := strings.Join(positional, " ")

	cfg, s := mustLoadConfigAndStore()
	defer s.Close()

	if !cfg.Summary.Enabled {
		exitWith(exitConfigInvalid, "summary.enabled is false in config.yaml — set it to true to generate summaries")
	}

	cards, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	if len(cards) == 0 {
		log.Fatalf("no cards.")
	}

	var targets []store.Card
	if all {
		for _, c := range cards {
			if c.WorktreePath == "" {
				continue
			}
			targets = append(targets, c)
		}
	} else {
		card, ok := resolveCardOrExit(cards, query, "summarize")
		if !ok {
			return
		}
		targets = []store.Card{card}
	}

	for _, c := range targets {
		if err := generateSummary(s, c, cfg.Summary, force); err != nil {
			if errors.Is(err, transcripts.ErrNoTranscript) {
				fmt.Fprintf(os.Stderr, "%s: skipped (no transcript yet)\n", shortIDFmt(c.ID))
				continue
			}
			fmt.Fprintf(os.Stderr, "%s: %v\n", shortIDFmt(c.ID), err)
			if !all {
				os.Exit(exitGeneral)
			}
			continue
		}
		fmt.Printf("%s  %s  summary regenerated\n", shortIDFmt(c.ID), c.Title)
	}
}

// generateSummary is the shared path used by `hive summarize` (synchronous
// from the CLI) and the poller (from a goroutine on status transitions).
// Returns nil (silent no-op) when the cache is already up to date unless
// force is true. Returns ErrNoTranscript when there's no Claude .jsonl yet.
func generateSummary(s *store.Store, c store.Card, cfg config.Summary, force bool) error {
	if c.WorktreePath == "" {
		return transcripts.ErrNoTranscript
	}
	paths, err := transcripts.ListTranscripts(c.WorktreePath)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return transcripts.ErrNoTranscript
	}
	info, err := os.Stat(paths[0])
	if err != nil {
		return err
	}
	mtime := info.ModTime().Unix()

	// Skip if cache is up to date.
	if !force && c.SummaryTranscriptMtime >= mtime && c.Summary != "" {
		return nil
	}

	res, err := transcripts.Summarize(context.Background(), c.WorktreePath, transcripts.SummaryOpts{
		OllamaURL:   cfg.OllamaURL,
		Model:       cfg.OllamaModel,
		TurnsWindow: cfg.TurnsWindow,
		MaxTokens:   cfg.MaxTokens,
		Timeout:     time.Duration(cfg.TimeoutSec) * time.Second,
	})
	if err != nil {
		return err
	}
	return s.UpdateCardSummary(c.ID, res.Text, res.TranscriptMtime)
}

// --- Card detail ---

// runCard backs `hive card [query] [--json]` — print a formatted summary of
// one card's metadata. Used standalone and as the fzf picker --preview source.
// Resolves against all cards (including archived) so users can inspect any
// card in the DB.
func runCard(args []string) {
	asJSON := false
	var positional []string
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		default:
			positional = append(positional, a)
		}
	}
	query := strings.Join(positional, " ")

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	all, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	if len(all) == 0 {
		log.Fatalf("no cards.")
	}
	card, ok := resolveCardOrExit(all, query, "card")
	if !ok {
		return
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(cardSummary(card))
		return
	}

	printCardSummary(os.Stdout, card)
}

// cardSummary assembles a plain struct for --json output.
func cardSummary(c store.Card) map[string]any {
	return map[string]any{
		"id":               c.ID,
		"title":            c.Title,
		"repo":             c.RepoName,
		"branch":           c.Branch,
		"column":           string(c.ColumnID),
		"status":           string(c.Status),
		"worktree":         c.WorktreePath,
		"tmux_session":     c.TmuxSession,
		"claude_session":   c.ClaudeSessionID,
		"created_at":       c.CreatedAt,
		"updated_at":       c.UpdatedAt,
		"last_attached_at": c.LastAttachedAt,
		"cost_usd":         c.TotalCostUSD,
		"input_tokens":     c.TotalInputTokens,
		"output_tokens":    c.TotalOutputTokens,
		"cache_read":       c.TotalCacheReadTokens,
		"cache_write":      c.TotalCacheWriteTokens,
		"model":            c.LastModelUsed,
		"pending_prompt":   c.PendingPrompt,
		"pr_url":           c.PRURL,
		"notifications_muted": c.NotificationsMuted,
	}
}

// printCardSummary writes a human-readable summary block.
// Kept compact (<= ~20 lines) so it fits comfortably in fzf's preview window.
func printCardSummary(w io.Writer, c store.Card) {
	// ANSI codes — matched to the picker's palette.
	const (
		reset   = "\x1b[0m"
		bold    = "\x1b[1m"
		dim     = "\x1b[2m"
		cyan    = "\x1b[36m"
		yellow  = "\x1b[33m"
		green   = "\x1b[32m"
		magenta = "\x1b[35m"
		red     = "\x1b[31m"
	)

	// Status color matching the picker.
	statusColor := ""
	switch c.Status {
	case store.StatusWorking:
		statusColor = yellow
	case store.StatusIdle:
		statusColor = green
	case store.StatusNeedsInput:
		statusColor = magenta + bold
	case store.StatusErrored:
		statusColor = red + bold
	case store.StatusPaused, store.StatusArchived:
		statusColor = dim
	}

	label := func(k string) string { return dim + fmt.Sprintf("%-14s", k) + reset }
	row := func(k, v string) { fmt.Fprintln(w, label(k)+v) }

	fmt.Fprintf(w, "%s%s%s  %s%s%s\n", bold, c.Title, reset, dim, c.ID, reset)
	fmt.Fprintln(w, dim+strings.Repeat("─", 48)+reset)

	row("Repo:", cyan+emptyOrDash(c.RepoName)+reset)
	row("Branch:", emptyOrDash(c.Branch))
	row("Column:", string(c.ColumnID))
	row("Status:", statusColor+cliStatusIcon(c.Status)+" "+string(c.Status)+reset)
	row("Worktree:", truncatePath(c.WorktreePath, 60))
	row("Tmux:", emptyOrDash(c.TmuxSession))
	if c.ClaudeSessionID != "" {
		row("Claude ID:", c.ClaudeSessionID)
	}
	row("Created:", humanTime(c.CreatedAt))
	row("Updated:", humanTime(c.UpdatedAt))
	if c.LastAttachedAt > 0 {
		row("Last attach:", humanTime(c.LastAttachedAt))
	}
	if c.TotalCostUSD > 0 || c.TotalInputTokens > 0 {
		row("Cost:", fmt.Sprintf("$%.2f  %sin %s · out %s · cache r/w %s/%s%s",
			c.TotalCostUSD, dim,
			humanCount(c.TotalInputTokens),
			humanCount(c.TotalOutputTokens),
			humanCount(c.TotalCacheReadTokens),
			humanCount(c.TotalCacheWriteTokens), reset))
	}
	if c.LastModelUsed != "" {
		row("Model:", c.LastModelUsed)
	}
	if c.PendingPrompt != "" {
		row("Pending:", truncateString(c.PendingPrompt, 80))
	}
	if c.PRURL != "" {
		row("PR:", c.PRURL)
	}
	if c.NotificationsMuted {
		row("Muted:", "yes")
	}

	// Cached LLM summary — shown above Recent turns when present. Fully
	// fail-safe: if the cache is empty (Ollama down, feature off, or
	// not-yet-generated), we skip this block and fall through to Recent
	// turns below.
	if c.Summary != "" {
		fmt.Fprintln(w, dim+strings.Repeat("─", 48)+reset)
		ageStr := humanAgeMain(c.SummaryGeneratedAt)
		fmt.Fprintln(w, dim+"Summary"+reset+"  "+dim+"("+ageStr+" ago)"+reset)
		renderSummary(w, c.Summary)
	}

	// Recent transcript turns — the fallback when there's no cached summary.
	// If a summary is present, skip this block: the two panels overlap in
	// purpose and the summary alone is easier to read at a glance.
	if c.Summary == "" && c.WorktreePath != "" {
		turns, err := transcripts.LastTurns(c.WorktreePath, 4)
		if err == nil && len(turns) > 0 {
			fmt.Fprintln(w, dim+strings.Repeat("─", 48)+reset)
			fmt.Fprintln(w, dim+"Recent turns:"+reset)
			const (
				bodyWidth = 44 // visible chars on the wrapped body lines
				maxChars  = 320
			)
			for i, t := range turns {
				if i > 0 {
					fmt.Fprintln(w)
				}
				tag, tagColor := "you", cyan
				if t.Role == "assistant" {
					tag, tagColor = "claude", bold
				}
				fmt.Fprintf(w, "  %s%s%s\n", tagColor, tag, reset)
				text := truncateString(t.Text, maxChars)
				for _, line := range wordWrap(text, bodyWidth) {
					fmt.Fprintf(w, "    %s\n", line)
				}
			}
		}
	}
}

// renderSummary prints a tidy version of the model's structured summary to w.
// It understands the "Goal: / Progress: / Next:" shape buildPrompt asks for,
// bolds the section labels, indents Progress bullets, drops empty "label:"
// lines (so a naked "Claude's progress:" bullet from a less-obedient model
// doesn't waste a row), highlights `backticks` in cyan, and word-wraps to
// 48 runes.
//
// Works fine when the model deviates from the format — unrecognised lines
// are treated as free text, bullets are rendered as bullets, everything gets
// the same wrap treatment.
func renderSummary(w io.Writer, summary string) {
	const bodyWidth = 48
	const (
		reset  = "\x1b[0m"
		bold   = "\x1b[1m"
		dim    = "\x1b[2m"
		cyan   = "\x1b[36m"
		yellow = "\x1b[33m"
		green  = "\x1b[32m"
		blue   = "\x1b[34m"
	)

	// Each section gets its own accent color — the label itself is
	// bold + colored, and the bullet marker inherits the same color so the
	// eye can group "which section is this bullet part of" at a glance.
	// Backticked spans stay cyan regardless of section so `code` stands out.
	colorFor := func(label string) string {
		switch label {
		case "Goal":
			return yellow
		case "Progress":
			return green
		case "Next":
			return blue
		}
		return bold // unknown label: fall back to plain bold
	}

	currentColor := bold // color used by any bullet outside a known section
	inProgress := false
	printedSection := false // used to emit a blank line between sections
	for _, raw := range strings.Split(strings.TrimSpace(summary), "\n") {
		line := strings.TrimRight(raw, " ")
		if line == "" {
			continue
		}

		// Section label like "Goal:", "Progress:", "Next:".
		if label, rest, ok := splitSummaryLabel(line); ok {
			if printedSection {
				// Breathing room between Goal / Progress / Next.
				fmt.Fprintln(w)
			}
			printedSection = true
			inProgress = label == "Progress"
			accent := colorFor(label)
			currentColor = accent
			body := strings.TrimSpace(rest)
			if body == "" {
				// Naked "Progress:" header — render as accent-bold label.
				fmt.Fprintf(w, "  %s%s%s:%s\n", bold, accent, label, reset)
				continue
			}
			// Inline body after the label: "Goal: <text>".
			first := true
			for _, ln := range wordWrap(body, bodyWidth-len(label)-2) {
				colored := colorizeBackticks(ln, cyan, reset)
				if first {
					fmt.Fprintf(w, "  %s%s%s:%s %s\n", bold, accent, label, reset, colored)
					first = false
				} else {
					fmt.Fprintf(w, "    %s\n", colored)
				}
			}
			continue
		}

		// Bullet lines ("- foo"). If we're under a Progress section, indent
		// them one level deeper than top-level bullets.
		if strings.HasPrefix(line, "- ") {
			text := strings.TrimSpace(strings.TrimPrefix(line, "-"))
			// Drop naked-label bullets like "- Claude's progress:" (end with
			// ':' and nothing else). They're artifacts of a less-structured
			// response and waste a row.
			if strings.HasSuffix(text, ":") && !strings.Contains(text[:len(text)-1], " ") {
				continue
			}
			indent := "  "
			cont := "    "
			if inProgress {
				indent = "    "
				cont = "      "
			}
			first := true
			for _, ln := range wordWrap(text, bodyWidth-len(indent)) {
				out := colorizeBackticks(ln, cyan, reset)
				if first {
					// Colored "- " marker so the eye can track which section
					// a bullet belongs to without re-reading the label.
					fmt.Fprintf(w, "%s%s- %s%s\n", indent, currentColor, reset, out)
					first = false
				} else {
					fmt.Fprintf(w, "%s%s\n", cont, out)
				}
			}
			continue
		}

		// Free-text fallback — wrap and indent like inline section text.
		for i, ln := range wordWrap(line, bodyWidth) {
			out := colorizeBackticks(ln, cyan, reset)
			if i == 0 {
				fmt.Fprintf(w, "  %s\n", out)
			} else {
				fmt.Fprintf(w, "    %s\n", out)
			}
		}
	}
}

// splitSummaryLabel matches a leading "Goal:", "Progress:", or "Next:" label
// and returns (label, rest, true). Comparison is case-sensitive but accepts
// any of the three canonical names; anything else is returned with ok=false.
func splitSummaryLabel(s string) (label, rest string, ok bool) {
	for _, known := range []string{"Goal", "Progress", "Next", "Summary"} {
		prefix := known + ":"
		if strings.HasPrefix(s, prefix) {
			return known, s[len(prefix):], true
		}
	}
	return "", "", false
}

// colorizeBackticks wraps every `...` substring in ANSI color codes so
// inline code is visually distinct in the preview. Unbalanced backticks
// are left alone.
func colorizeBackticks(s, color, reset string) string {
	if !strings.Contains(s, "`") {
		return s
	}
	var b strings.Builder
	inTick := false
	for _, r := range s {
		if r == '`' {
			if inTick {
				b.WriteString(reset)
				inTick = false
			} else {
				b.WriteString(color)
				inTick = true
			}
			continue
		}
		b.WriteRune(r)
	}
	// If we opened but didn't close, restore formatting defensively.
	if inTick {
		b.WriteString(reset)
	}
	return b.String()
}

// wordWrap splits s into lines of at most width runes, breaking only at
// whitespace. Never splits a word in the middle — a word longer than width
// lives on its own line. Returns a nil slice for empty input.
func wordWrap(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	cur := words[0]
	curLen := runeCount(cur)
	for _, w := range words[1:] {
		wl := runeCount(w)
		if curLen+1+wl > width {
			lines = append(lines, cur)
			cur = w
			curLen = wl
			continue
		}
		cur += " " + w
		curLen += 1 + wl
	}
	return append(lines, cur)
}

func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func emptyOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// humanTime formats a Unix timestamp as "2006-01-02 15:04 (<age>)".
func humanTime(ts int64) string {
	if ts == 0 {
		return "-"
	}
	t := time.Unix(ts, 0)
	return t.Format("2006-01-02 15:04") + " (" + humanAgeMain(ts) + " ago)"
}

// humanAgeMain mirrors cli.humanAge for main.go so runCard stays independent.
func humanAgeMain(ts int64) string {
	if ts == 0 {
		return "-"
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// humanCount formats an integer with K / M suffixes.
func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// truncatePath keeps the last n chars of a path prefixed with "…/".
func truncatePath(p string, n int) string {
	if p == "" {
		return "-"
	}
	if len(p) <= n {
		return p
	}
	return "…" + p[len(p)-n+1:]
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// cliStatusIcon is a local mirror of cli.StatusIcon to avoid pulling the
// picker package into every main.go path.
func cliStatusIcon(s store.Status) string {
	return cli.StatusIcon(s)
}

// --- Done / Resume / Rm ---

// runDone backs `hive done [query] [--yes] [--delete-worktree] [--keep-worktree]`.
// Archives the card: moves to the Done column, status=archived, and kills the
// tmux session. A 3-second undo window lets the user press Ctrl-C to restore
// state; skipped when --yes is passed or stdin isn't a TTY.
func runDone(args []string) {
	yes := false
	deleteWT := false
	keepWT := false
	var positional []string
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			yes = true
		case "--delete-worktree":
			deleteWT = true
		case "--keep-worktree":
			keepWT = true
		default:
			positional = append(positional, a)
		}
	}
	if deleteWT && keepWT {
		log.Fatalf("--delete-worktree and --keep-worktree are mutually exclusive")
	}

	query := strings.Join(positional, " ")

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	// `hive done` can target active AND done cards (re-archive). Only skip
	// the already-archived column which has nothing left to do.
	all, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	candidates := make([]store.Card, 0, len(all))
	for _, c := range all {
		if c.ColumnID == store.ColumnArchived {
			continue
		}
		candidates = append(candidates, c)
	}
	if len(candidates) == 0 {
		log.Fatalf("no cards to archive.")
	}

	card, ok := resolveCardOrExit(candidates, query, "done")
	if !ok {
		return
	}

	// If the card is already "done" (col=Done, status=archived), treat a
	// second `hive done` as "move it out of the default view" — same as the
	// TUI's second-press behavior.
	if card.ColumnID == store.ColumnDone && card.Status == store.StatusArchived {
		if err := s.UpdateCardColumn(card.ID, store.ColumnArchived); err != nil {
			log.Fatalf("move to archived: %v", err)
		}
		fmt.Printf("Moved %q (%s) to the Archived column.\n", card.Title, card.ID)
		return
	}

	prevCol := card.ColumnID
	prevStatus := card.Status

	// Phase 1: move to Done/archived in the DB so status is reflected
	// immediately in listings and other consumers.
	if err := s.UpdateCardColumn(card.ID, store.ColumnDone); err != nil {
		log.Fatalf("update column: %v", err)
	}
	if err := s.UpdateCardStatus(card.ID, store.StatusArchived); err != nil {
		log.Fatalf("update status: %v", err)
	}

	interactive := cli.IsInteractive() && !yes
	if interactive {
		fmt.Printf("Archiving %q (%s). Ctrl-C within 3s to undo...\n", card.Title, card.ID)
		if undone := runUndoWindow(3 * time.Second); undone {
			if err := s.UpdateCardColumn(card.ID, prevCol); err == nil {
				s.UpdateCardStatus(card.ID, prevStatus)
			}
			fmt.Println("Undone.")
			return
		}
	}

	finalizeArchive(s, card, resolveWorktreeDeletion(deleteWT, keepWT))
	fmt.Printf("Archived %q (%s).\n", card.Title, card.ID)
}

// resolveWorktreeDeletion picks the delete-worktree policy for an archive.
// Explicit flags win; otherwise fall back to a safe default (do not delete).
// CLI skips the TUI's interactive "prompt" behavior — that's what the --yes /
// --delete-worktree flags are for.
func resolveWorktreeDeletion(deleteWT, keepWT bool) bool {
	if deleteWT {
		return true
	}
	if keepWT {
		return false
	}
	return false
}

// finalizeArchive does the destructive half of archive: captures the Claude
// session ID, kills tmux, and optionally removes the worktree.
func finalizeArchive(s *store.Store, card store.Card, deleteWorktree bool) {
	if card.WorktreePath != "" {
		if sid, _ := transcripts.FindSessionID(card.WorktreePath); sid != "" {
			s.UpdateCardClaudeSession(card.ID, sid)
		}
	}
	if card.TmuxSession != "" {
		if err := tmux.KillSession(card.TmuxSession); err != nil {
			log.Printf("kill tmux %s: %v", card.TmuxSession, err)
		}
	}
	if deleteWorktree && card.WorktreePath != "" {
		if err := gitpkg.RemoveWorktree(card.WorktreePath, card.WorktreePath); err != nil {
			log.Printf("remove worktree %s: %v", card.WorktreePath, err)
		} else {
			fmt.Printf("Removed worktree %s\n", card.WorktreePath)
		}
	}
}

// runUndoWindow blocks for up to d, returning true if the user interrupts
// (SIGINT / Ctrl-C) before the timeout. Returns false on natural timeout.
func runUndoWindow(d time.Duration) bool {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	select {
	case <-sigCh:
		return true
	case <-time.After(d):
		return false
	}
}

// runResume backs `hive resume [query]`. Targets cards in the Done or
// Archived columns; recreates a tmux session running `claude --resume <id>`
// in the stored worktree and attaches.
func runResume(args []string) {
	query := strings.Join(args, " ")

	cfg, s := mustLoadConfigAndStore()
	defer s.Close()

	all, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	// Resumable = anything without a live tmux session. That includes:
	//   - Done / Archived cards (archived via `hive done`).
	//   - Active + Paused cards (imported via `hive import --session-id`,
	//     or cards whose tmux died but whose status the poller correctly
	//     flipped to Paused).
	candidates := make([]store.Card, 0, len(all))
	liveActiveCount := 0
	for _, c := range all {
		resumable := c.ColumnID == store.ColumnDone ||
			c.ColumnID == store.ColumnArchived ||
			c.Status == store.StatusPaused
		if resumable {
			candidates = append(candidates, c)
			continue
		}
		if c.ColumnID == store.ColumnActive {
			liveActiveCount++
		}
	}
	if len(candidates) == 0 {
		if liveActiveCount > 0 {
			log.Fatalf("nothing to resume. (You have %d live active cards — use `hive attach` or `hive` to pick one.)", liveActiveCount)
		}
		log.Fatalf("nothing to resume.")
	}

	card, ok := resolveCardOrExit(candidates, query, "resume")
	if !ok {
		return
	}
	// attachToCard handles both paths transparently now: if there's no live
	// tmux it resumes, otherwise it just attaches. Use that one entry point.
	attachToCard(s, cfg, card)
}

// runDelete backs `hive rm` and `hive kill` (aliases). Hard-deletes the card
// from the DB and kills its tmux session. Optionally removes the worktree.
// Prompts for confirmation unless --yes or stdin isn't a TTY (in which case
// --yes is required for safety).
func runDelete(args []string, verbName string) {
	yes := false
	deleteWT := false
	var positional []string
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			yes = true
		case "--delete-worktree":
			deleteWT = true
		default:
			positional = append(positional, a)
		}
	}
	query := strings.Join(positional, " ")

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	all, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	if len(all) == 0 {
		log.Fatalf("no cards.")
	}

	card, ok := resolveCardOrExit(all, query, verbName)
	if !ok {
		return
	}

	if !yes {
		if !cli.IsInteractive() {
			log.Fatalf("%s deletes card %s permanently. Re-run with --yes to confirm (non-TTY).", verbName, card.ID)
		}
		fmt.Printf("Delete %q (%s)", card.Title, card.ID)
		if deleteWT && card.WorktreePath != "" {
			fmt.Printf(" AND worktree %s", card.WorktreePath)
		}
		fmt.Printf("? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("Aborted.")
			return
		}
	}

	if card.TmuxSession != "" {
		if err := tmux.KillSession(card.TmuxSession); err != nil {
			log.Printf("kill tmux %s: %v", card.TmuxSession, err)
		}
	}
	if deleteWT && card.WorktreePath != "" {
		if err := gitpkg.RemoveWorktree(card.WorktreePath, card.WorktreePath); err != nil {
			log.Printf("remove worktree %s: %v", card.WorktreePath, err)
		}
	}
	if err := s.DeleteCard(card.ID); err != nil {
		log.Fatalf("delete card: %v", err)
	}
	fmt.Printf("Deleted %q (%s).\n", card.Title, card.ID)
}

// --- Templates ---

func runTemplate() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: hive template list | show <name> | create <name>\n")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "list":
		tmpls, err := templates.List()
		if err != nil {
			log.Fatalf("list templates: %v", err)
		}
		if len(tmpls) == 0 {
			fmt.Println("No templates. Create one with: hive template create <name>")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tREPO\tPROMPT")
		for _, t := range tmpls {
			prompt := t.InitialPrompt
			if len(prompt) > 50 {
				prompt = prompt[:47] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, t.RepoName, prompt)
		}
		w.Flush()

	case "show":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: hive template show <name>\n")
			os.Exit(1)
		}
		t, err := templates.Get(os.Args[3])
		if err != nil {
			log.Fatalf("get template: %v", err)
		}
		fmt.Printf("Name:    %s\n", t.Name)
		fmt.Printf("Repo:    %s\n", t.RepoName)
		if t.BranchFrom != "" {
			fmt.Printf("Branch:  %s\n", t.BranchFrom)
		}
		fmt.Printf("Prompt:  %s\n", t.InitialPrompt)
		if t.SetupScript != "" {
			fmt.Printf("Setup:   %s\n", t.SetupScript)
		}

	case "create":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: hive template create <name>\n")
			os.Exit(1)
		}
		t := templates.Template{Name: os.Args[3]}
		// Parse remaining flags
		args := os.Args[4:]
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--repo":
				if i+1 < len(args) {
					t.RepoName = args[i+1]
					i++
				}
			case "--prompt":
				if i+1 < len(args) {
					t.InitialPrompt = args[i+1]
					i++
				}
			case "--branch":
				if i+1 < len(args) {
					t.BranchFrom = args[i+1]
					i++
				}
			}
		}
		if err := templates.Save(t); err != nil {
			log.Fatalf("save template: %v", err)
		}
		fmt.Printf("Template %q saved.\n", t.Name)

	default:
		fmt.Fprintf(os.Stderr, "Unknown template command: %s\n", os.Args[2])
		os.Exit(1)
	}
}

// --- Open ---

// runOpen backs `hive open [query]` — open the card's worktree in the user's
// editor ($EDITOR, or `code` fallback). Use --finder on macOS to open in
// Finder instead.
func runOpen(args []string) {
	finder := false
	var positional []string
	for _, a := range args {
		switch a {
		case "--finder":
			finder = true
		default:
			positional = append(positional, a)
		}
	}
	query := strings.Join(positional, " ")

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	all, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	if len(all) == 0 {
		log.Fatalf("no cards.")
	}
	card, ok := resolveCardOrExit(all, query, "open")
	if !ok {
		return
	}
	if card.WorktreePath == "" {
		log.Fatalf("card %s has no worktree path", card.ID)
	}

	if finder {
		if err := exec.Command("open", card.WorktreePath).Run(); err != nil {
			log.Fatalf("open: %v", err)
		}
		return
	}
	cmd := openEditorCmd(card.WorktreePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("editor: %v", err)
	}
}

// openEditorCmd returns the best-guess editor command for a path:
// $VISUAL, $EDITOR (with args support), `code`, then `open` as last resort.
func openEditorCmd(path string) *exec.Cmd {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed != "" {
		parts := strings.Fields(ed)
		return exec.Command(parts[0], append(parts[1:], path)...)
	}
	if _, err := exec.LookPath("code"); err == nil {
		return exec.Command("code", path)
	}
	return exec.Command("open", path)
}

// runCd backs `hive cd [query]` — print the worktree path to stdout so shell
// aliases can `cd $(hive cd)`. Picker UI (if any) writes to stderr.
func runCd(args []string) {
	query := strings.Join(args, " ")

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	all, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	if len(all) == 0 {
		log.Fatalf("no cards.")
	}
	card, ok := resolveCardOrExit(all, query, "cd")
	if !ok {
		// Cancelled picker: exit non-zero so shell aliases `cd $(hive cd)`
		// don't accidentally take the user to $HOME.
		os.Exit(exitGeneral)
	}
	if card.WorktreePath == "" {
		log.Fatalf("card %s has no worktree path", card.ID)
	}
	fmt.Println(card.WorktreePath)
}

// runTail backs `hive tail [query]` — live-stream the last lines of a tmux
// pane. Refreshes twice per second. Ctrl-C exits.
func runTail(args []string) {
	n := 20
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-n", "--lines":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil || v <= 0 {
					log.Fatalf("bad -n value %q", args[i+1])
				}
				n = v
				i++
			}
		default:
			positional = append(positional, args[i])
		}
	}
	query := strings.Join(positional, " ")

	if !cli.IsInteractive() {
		log.Fatalf("tail: requires a TTY")
	}

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	cards := livePickCards(s)
	if len(cards) == 0 {
		log.Fatalf("no active cards.")
	}
	card, ok := resolveCardOrExit(cards, query, "tail")
	if !ok {
		return
	}
	if card.TmuxSession == "" {
		log.Fatalf("card %s has no tmux session", card.ID)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()

	draw := func() {
		if !tmux.HasSession(card.TmuxSession) {
			fmt.Fprintln(os.Stderr, "tmux session gone — exiting.")
			os.Exit(1)
		}
		out, err := tmux.CapturePane(card.TmuxSession)
		if err != nil {
			fmt.Fprintf(os.Stderr, "capture: %v\n", err)
			return
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		fmt.Print("\033[2J\033[H")
		fmt.Printf("hive tail %s (%s) — Ctrl-C to exit\n\n", card.ID, card.Title)
		for _, l := range lines {
			fmt.Println(l)
		}
	}
	draw()
	for {
		select {
		case <-sigCh:
			fmt.Println()
			return
		case <-tick.C:
			draw()
		}
	}
}

// runSearch backs `hive search <term> [--cards] [--transcripts] [-n N]`.
// Greps live pane captures and/or Claude transcripts for a term; prints
// matches in a scriptable format.
func runSearch(args []string) {
	includeCards := true
	includeTranscripts := true
	maxPerCard := 5
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cards-only":
			includeTranscripts = false
		case "--transcripts-only":
			includeCards = false
		case "-n", "--limit":
			if i+1 < len(args) {
				v, err := strconv.Atoi(args[i+1])
				if err != nil || v <= 0 {
					log.Fatalf("bad -n value %q", args[i+1])
				}
				maxPerCard = v
				i++
			}
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: hive search <term>\n")
		os.Exit(1)
	}
	term := strings.Join(positional, " ")
	termLower := strings.ToLower(term)

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	cards, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}

	hits := 0
	for _, c := range cards {
		cardHits := 0
		if includeCards && c.TmuxSession != "" && tmux.HasSession(c.TmuxSession) {
			if out, err := tmux.CapturePaneFull(c.TmuxSession); err == nil {
				for _, line := range strings.Split(out, "\n") {
					if strings.Contains(strings.ToLower(line), termLower) {
						fmt.Printf("%s  %s  pane: %s\n", shortIDFmt(c.ID), c.Title, strings.TrimSpace(line))
						cardHits++
						hits++
						if cardHits >= maxPerCard {
							break
						}
					}
				}
			}
		}
		if cardHits >= maxPerCard {
			continue
		}
		if !includeTranscripts || c.WorktreePath == "" {
			continue
		}
		transcriptPaths, _ := transcripts.ListTranscripts(c.WorktreePath)
		for _, tp := range transcriptPaths {
			if cardHits >= maxPerCard {
				break
			}
			f, err := os.Open(tp)
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
			for scanner.Scan() && cardHits < maxPerCard {
				line := scanner.Text()
				if strings.Contains(strings.ToLower(line), termLower) {
					// Trim the JSONL match to something human-readable.
					snippet := line
					if len(snippet) > 200 {
						snippet = snippet[:200] + "…"
					}
					fmt.Printf("%s  %s  transcript: %s\n", shortIDFmt(c.ID), c.Title, snippet)
					cardHits++
					hits++
				}
			}
			f.Close()
		}
	}
	if hits == 0 {
		fmt.Fprintf(os.Stderr, "No matches for %q.\n", term)
		os.Exit(1)
	}
}

func shortIDFmt(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// runDoctor backs `hive doctor` — environment sanity checks.
func runDoctor() {
	type check struct {
		name string
		ok   bool
		msg  string
	}
	var checks []check

	record := func(name string, ok bool, msg string) {
		checks = append(checks, check{name: name, ok: ok, msg: msg})
	}

	// PATH tools.
	for _, bin := range []string{"tmux", "git", "claude", "fzf"} {
		if _, err := exec.LookPath(bin); err == nil {
			record(bin, true, "on $PATH")
		} else {
			optional := bin == "fzf" || bin == "claude"
			if optional {
				record(bin, false, "not found (optional)")
			} else {
				record(bin, false, "not found — required")
			}
		}
	}

	// Config.
	cfg, err := config.Load()
	if err != nil {
		record("config", false, err.Error())
	} else {
		record("config", true, fmt.Sprintf("%d repo(s)", len(cfg.Repos)))
		for _, e := range config.ValidateRepos(cfg) {
			record("repo", false, e.Error())
		}
	}

	// DB writable.
	dir, err := config.Dir()
	if err != nil {
		record("db-dir", false, err.Error())
	} else {
		dbPath := filepath.Join(dir, "hive.db")
		st, err := store.Open(dbPath)
		if err != nil {
			record("db", false, err.Error())
		} else {
			record("db", true, dbPath)

			// Orphaned tmux sessions (matching the configured prefix but not
			// recorded in the DB) and stale worktrees (DB-referenced dir gone).
			cards, _ := st.ListCards()
			known := make(map[string]bool)
			for _, c := range cards {
				if c.TmuxSession != "" {
					known[c.TmuxSession] = true
				}
			}
			sessions, _ := tmux.ListSessions()
			orphans := 0
			prefix := cfg.TmuxPrefix
			if prefix == "" {
				prefix = "hv_"
			}
			for _, sess := range sessions {
				if strings.HasPrefix(sess, prefix) && !known[sess] {
					orphans++
				}
			}
			if orphans > 0 {
				record("orphans", false, fmt.Sprintf("%d tmux session(s) with prefix %q not in DB (use `hive import --tmux <name>`)", orphans, prefix))
			} else {
				record("orphans", true, "none")
			}
			stale := 0
			for _, c := range cards {
				if c.ColumnID == store.ColumnArchived {
					continue
				}
				if c.WorktreePath == "" {
					continue
				}
				if info, err := os.Stat(c.WorktreePath); err != nil || !info.IsDir() {
					stale++
				}
			}
			if stale > 0 {
				record("worktrees", false, fmt.Sprintf("%d card(s) reference a missing worktree dir", stale))
			} else {
				record("worktrees", true, "all present")
			}
			st.Close()
		}
	}

	// Print.
	fails := 0
	for _, c := range checks {
		mark := "ok"
		if !c.ok {
			mark = "FAIL"
			fails++
		}
		fmt.Printf("  [%s] %-12s %s\n", mark, c.name, c.msg)
	}
	if fails > 0 {
		os.Exit(1)
	}
}

// --- Status ---

// runStatus backs `hive status [--short] [--json]`.
// Default: multi-line human summary.
// --short: one-line PS1-friendly output ("3⚙ 1❓ $2.47").
// --json: a StatusSummary JSON object.
func runStatus(args []string) {
	short := false
	asJSON := false
	for _, a := range args {
		switch a {
		case "--short", "-s":
			short = true
		case "--json":
			asJSON = true
		default:
			log.Fatalf("status: unknown flag %q", a)
		}
	}

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	cards, err := s.ListCards()
	if err != nil {
		log.Fatalf("list cards: %v", err)
	}
	summary := cli.SummarizeCards(cards)

	switch {
	case asJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(summary); err != nil {
			log.Fatalf("encode: %v", err)
		}
	case short:
		fmt.Println(cli.ShortStatusLine(summary))
	default:
		fmt.Printf("%d active", summary.ActiveCount)
		if summary.NeedsInputCount > 0 {
			fmt.Printf(", %d needs input", summary.NeedsInputCount)
		}
		if summary.ErroredCount > 0 {
			fmt.Printf(", %d errored", summary.ErroredCount)
		}
		fmt.Printf(", $%.2f total cost\n", summary.TotalCostUSD)
	}
}

// --- Watch ---

// runWatch backs `hive watch [--interval 2s]` — redraws `hive ls` (with any
// passed filters) every tick. Accepts the same filter flags as `hive ls`.
// Exits on SIGINT.
func runWatch(args []string) {
	interval := 2 * time.Second
	listArgs := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--interval", "-i":
			if i+1 < len(args) {
				d, err := cli.ParseDuration(args[i+1])
				if err != nil || d < 250*time.Millisecond {
					log.Fatalf("--interval: must be >= 250ms (got %q)", args[i+1])
				}
				interval = d
				i++
			}
		default:
			listArgs = append(listArgs, args[i])
		}
	}

	if !cli.IsInteractive() {
		log.Fatalf("watch: stdout must be a TTY")
	}

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Parse filters once; the interval loop just re-reads the store.
	var filters cli.ListFilters
	for i := 0; i < len(listArgs); i++ {
		switch listArgs[i] {
		case "--repo", "-r":
			if i+1 < len(listArgs) {
				filters.Repo = listArgs[i+1]
				i++
			}
		case "--status":
			if i+1 < len(listArgs) {
				filters.Status = listArgs[i+1]
				i++
			}
		case "--column", "-c":
			if i+1 < len(listArgs) {
				filters.Column = listArgs[i+1]
				i++
			}
		case "--since":
			if i+1 < len(listArgs) {
				d, err := cli.ParseDuration(listArgs[i+1])
				if err != nil {
					log.Fatalf("--since: %v", err)
				}
				filters.Since = d
				i++
			}
		default:
			log.Fatalf("watch: unknown flag %q", listArgs[i])
		}
	}

	tick := time.NewTicker(interval)
	defer tick.Stop()
	draw := func() {
		all, err := s.ListCards()
		if err != nil {
			fmt.Fprintf(os.Stderr, "watch: %v\n", err)
			return
		}
		cards := cli.ApplyFilters(all, filters)
		// ANSI clear-screen + home.
		fmt.Print("\033[2J\033[H")
		fmt.Printf("hive watch (every %s, Ctrl-C to quit)\n\n", interval)
		if len(cards) == 0 {
			fmt.Println("No cards.")
			return
		}
		if err := cli.WriteCards(os.Stdout, cards, cli.FormatTable); err != nil {
			fmt.Fprintf(os.Stderr, "watch: %v\n", err)
		}
	}
	draw()
	for {
		select {
		case <-sigCh:
			fmt.Println()
			return
		case <-tick.C:
			draw()
		}
	}
}

// --- Send ---

// runSend backs `hive send [query] <msg>`.
//
// Message sources (first-match wins):
//   - `-e` / `--edit` flag: open $EDITOR on a tempfile.
//   - Positional `-`: read from stdin.
//   - Positional words: joined with spaces.
//   - No message given: open $EDITOR.
func runSend(args []string) {
	editor := false
	positional := []string{}
	for _, a := range args {
		switch a {
		case "-e", "--edit":
			editor = true
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: hive send [query] <message> | - | -e\n")
		os.Exit(1)
	}
	query := positional[0]
	msg := strings.Join(positional[1:], " ")

	message, err := cli.ReadMessage(msg, editor)
	if err != nil {
		log.Fatalf("message: %v", err)
	}
	if message == "" {
		log.Fatalf("empty message — aborted")
	}

	_, s := mustLoadConfigAndStore()
	defer s.Close()

	cards := livePickCards(s)
	if len(cards) == 0 {
		log.Fatalf("no active cards.")
	}
	card, ok := resolveCardOrExit(cards, query, "send")
	if !ok {
		return
	}
	if card.TmuxSession == "" || !tmux.HasSession(card.TmuxSession) {
		log.Fatalf("no active tmux session for card %s", card.ID)
	}

	if err := tmux.SendKeys(card.TmuxSession, message); err != nil {
		log.Fatalf("send: %v", err)
	}

	s.InsertStatusEvent(store.StatusEvent{
		CardID:     card.ID,
		Status:     "user_input_sent",
		Detail:     message,
		ObservedAt: time.Now().Unix(),
	})

	fmt.Printf("Sent to %s (%s)\n", card.Title, card.TmuxSession)
}

// --- Import ---

func runImport() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, `Usage:
  hive import --tmux <session-name> --repo <repo> --title <title>
    Import a running tmux session as a card

  hive import --session-id <claude-session-id> --repo <repo> --title <title> [--cwd <path>]
    Import a closed Claude session for resume
`)
		os.Exit(1)
	}

	var tmuxSession, sessionID, repoName, title, cwd string
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tmux":
			if i+1 < len(args) {
				tmuxSession = args[i+1]
				i++
			}
		case "--session-id", "-s":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--repo", "-r":
			if i+1 < len(args) {
				repoName = args[i+1]
				i++
			}
		case "--title", "-t":
			if i+1 < len(args) {
				title = args[i+1]
				i++
			}
		case "--cwd", "-w":
			if i+1 < len(args) {
				cwd = args[i+1]
				i++
			}
		}
	}

	if repoName == "" || title == "" {
		fmt.Fprintf(os.Stderr, "Error: --repo and --title are required\n")
		os.Exit(1)
	}
	if tmuxSession == "" && sessionID == "" {
		fmt.Fprintf(os.Stderr, "Error: either --tmux or --session-id is required\n")
		os.Exit(1)
	}

	cfg, s := mustLoadConfigAndStore()
	defer s.Close()
	syncRepos(cfg, s)

	cardID := tui.GenerateID()
	now := time.Now().Unix()

	// Determine worktree path
	wtPath := cwd
	if wtPath == "" && tmuxSession != "" {
		// Try to get CWD from tmux session
		if detected, err := tmux.SessionCWD(tmuxSession); err == nil && detected != "" {
			wtPath = detected
		}
	}

	// Validate worktree path exists if provided
	if wtPath != "" {
		if info, err := os.Stat(wtPath); err != nil || !info.IsDir() {
			log.Fatalf("worktree path %q does not exist or is not a directory", wtPath)
		}
	}

	col := store.ColumnActive
	status := store.StatusPaused // Paused: has session ID, no tmux, ready to resume

	if tmuxSession != "" {
		// Importing a live tmux session
		if !tmux.HasSession(tmuxSession) {
			log.Fatalf("tmux session %q not found", tmuxSession)
		}
		col = store.ColumnActive
		status = store.StatusWorking
	}

	// Auto-detect branch from worktree
	branch := ""
	if wtPath != "" {
		cmd := exec.Command("git", "-C", wtPath, "branch", "--show-current")
		if out, err := cmd.Output(); err == nil {
			branch = strings.TrimSpace(string(out))
		}
	}

	card := store.Card{
		ID:              cardID,
		Title:           title,
		RepoName:        repoName,
		Branch:          branch,
		WorktreePath:    wtPath,
		ColumnID:        col,
		Status:          status,
		TmuxSession:     tmuxSession,
		ClaudeSessionID: sessionID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := s.InsertCard(card); err != nil {
		log.Fatalf("insert card: %v", err)
	}

	if tmuxSession != "" {
		fmt.Printf("Imported live tmux session %q as card %s (Active)\n", tmuxSession, cardID)
	} else {
		fmt.Printf("Imported closed session %s as card %s (Active — Paused, click Resume to continue)\n", sessionID, cardID)
	}
}

// --- Config Check ---

func runConfigCheck() {
	if len(os.Args) > 2 && os.Args[2] == "check" {
		// Explicit check subcommand
	} else if len(os.Args) == 2 {
		// Just "hive config" — treat as check
	} else {
		exitWith(exitGeneral, "Usage: hive config check")
	}

	cfg, err := config.Load()
	if err != nil {
		exitWith(exitConfigInvalid, "Config error: %v", err)
	}

	fmt.Printf("Config loaded from ~/.hive/config.yaml\n")
	fmt.Printf("  Repos: %d\n", len(cfg.Repos))
	fmt.Printf("  Poll interval: %ds\n", cfg.PollIntervalSec)
	fmt.Printf("  Claude cmd: %s\n", cfg.ClaudeCmd)
	fmt.Printf("  Archive behavior: %s\n", cfg.ArchiveBehavior)
	fmt.Printf("  Log max size: %dMB\n", cfg.LogMaxSizeMB)
	fmt.Println()

	errs := config.ValidateRepos(cfg)
	if len(errs) == 0 {
		fmt.Printf("All %d repos validated successfully.\n", len(cfg.Repos))
	} else {
		fmt.Fprintf(os.Stderr, "Repo validation errors:\n")
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e.Error())
		}
		os.Exit(exitConfigInvalid)
	}
}

// --- Shared helpers ---

func mustLoadConfigAndStore() (config.Config, *store.Store) {
	cfg, err := config.Load()
	if err != nil {
		exitWith(exitConfigInvalid, "load config: %v", err)
	}

	dir, err := config.Dir()
	if err != nil {
		log.Fatalf("config dir: %v", err)
	}

	dbPath := filepath.Join(dir, "hive.db")
	s, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}

	return cfg, s
}

func syncRepos(cfg config.Config, s *store.Store) {
	for _, r := range cfg.Repos {
		s.UpsertRepo(store.Repo{
			Name:          r.Name,
			Path:          r.Path,
			DefaultBranch: r.DefaultBranch,
			SetupScript:   r.SetupScript,
		})
	}
	// Sentinel repo used by ad-hoc (non-configured-repo) sessions so the
	// repos.name FK always has a valid target. Idempotent via UpsertRepo.
	s.UpsertRepo(store.Repo{Name: sentinelRepoNone})
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `hive %s — Claude Code session manager (CLI-first)

Quick start:
  hive                               Open fuzzy picker → Enter to attach.
  hive new "fix auth bug"            Create a session (auto-detects repo) and attach.
  hive a fix-au                      Attach (fuzzy resolve). Ambiguous → picker.
  hive last                          Attach the most-recently-attached session.
  hive send fix-au "keep going"      Send text without attaching.
  hive peek fix-au                   Snapshot a pane without attaching.
  hive done fix-au                   Archive (3s Ctrl-C undo window).

Create / attach:
  new [title] [-p prompt] [-r repo] [-b branch] [-w worktree] [-t title]
                        Primary create verb. With a title, creates a card;
                        without one, runs a cardless Claude session in cwd.
  attach, a [query]     Resolve + attach. With no query, opens the picker.
  last, -               Attach the most-recently-attached live session.

Inspect / interact:
  ls, list [flags]      Filtered listing (-r repo, -s status, -c column,
                        --since <dur>, --json, -f {table|tsv|json}).
  archived [ls flags]   Alias for 'ls -s archived' — the archived card view.
  status [--short|--json]
                        One-line (PS1) or structured summary.
  watch [--interval 2s] [ls flags]
                        Live-redraw the card list. Ctrl-C exits.
  card [query] [--json] Detailed summary of one card (also the picker's
                        preview source). Resolves against all cards.
  summarize [query] [--all] [--force]
                        Regenerate the local-LLM summary via Ollama.
                        Cached on the card; used by 'hive card'. Safe to
                        skip — 'hive card' falls back to Recent turns
                        when no cached summary exists.
  peek [query] [-n N]   Last N lines of a pane (no attach).
  tail [query] [-n N]   Live pane stream. Ctrl-C exits.
  send [query] <msg>    Send text. <msg> may be "-" (stdin) or -e (editor).
  search <term>         Grep live panes + Claude transcripts.
                        --cards-only, --transcripts-only, -n <limit>.
  cd [query]            Print worktree path (for 'cd $(hive cd)').
  open [query]          Open worktree in $EDITOR (--finder for Finder).

Lifecycle:
  done [query]          Archive with 3s undo window. -y / --yes skips.
                        --delete-worktree / --keep-worktree override default.
  resume [query]        Re-launch a Done/Archived card with claude --resume.
  rm, kill [query]      Hard-delete a card. -y / --yes skips confirmation.
                        --delete-worktree also removes the git worktree.

Admin:
  doctor                Check tmux/git/claude/fzf, config, orphans, worktrees.
  config check          Validate config and repo paths.
  repo (see config)     Managed via ~/.hive/config.yaml.
  template list|show|create
                        Manage session templates.
  import                Import existing session (--tmux / --session-id).

Long-running processes (required for background status polling + summaries):
  tui                   Bubble Tea kanban board.
  daemon                Headless poller + notifications.

Other:
  version               Print version.

Exit codes:
  0  success
  1  general error
  2  ambiguous query in a non-interactive context (picker unavailable)
  3  required dependency missing (tmux, git)
  4  config invalid

Per-command help:
  hive help <command>   e.g. 'hive help new', 'hive help ls', 'hive help done'.

Shell completions:
  zsh:   install completions/_hive to a dir on $fpath, then compinit.
  bash:  source completions/hive.bash from ~/.bashrc.
  fish:  copy completions/hive.fish to ~/.config/fish/completions/.
`, version)
}

// verbHelp holds the long-form help text for each verb. Keep entries short
// (6–14 lines) and include a usage line + flag reference + an example.
var verbHelp = map[string]string{
	"new": `hive new [title] [flags]

  Create a session and attach. With a title, creates a card (auto-detects repo
  from cwd unless -r overrides); without a title, starts a cardless Claude
  session in cwd.

  -r, --repo <name>     Repo name (from config). Auto-detected by default.
  -t, --title <name>    Card title (alternative to the positional arg).
  -p, --prompt <text>   Initial prompt; sent after Claude boots.
  -b, --branch <name>   Branch name (default: derived from the title).
  -w, --worktree <path> Use an existing worktree path instead of creating one.
  .                     Shorthand for '-w $PWD'.

  Examples:
    hive new "fix auth bug"                 create + attach, new worktree
    hive new "fix auth" -p "investigate"    create with a prompt
    hive new .                              reuse cwd as the worktree
    hive new                                cardless Claude in cwd
`,
	"attach": `hive attach, hive a [query]

  Attach to a session. With an exact ID or a unique prefix/title substring,
  attaches directly. With 0 or many matches, opens the picker pre-filtered
  with the query. Inside tmux, uses switch-client.

  Examples:
    hive                   open the picker
    hive a                 open the picker
    hive a fix-au          attach if 'fix-au' uniquely resolves, else picker
    hive attach a3f1b2c9   attach by full ID
`,
	"a":    "See 'hive help attach'.",
	"last": "hive last, hive -\n\n  Attach the most-recently-attached live session.\n",
	"summarize": `hive summarize [query] [--all] [--force]

  Regenerate the local-LLM summary (via Ollama) for one or more cards.
  Summary lines are cached on the card row and shown by 'hive card'.

  Requires summary.enabled=true in config.yaml and a running Ollama
  instance with the configured model pulled.

  --all / -a     Iterate every card with a worktree.
  --force / -f   Regenerate even when the cache is already fresh.

  Fail-safe: if Ollama is unreachable, this verb errors out but every
  other part of hive keeps working — 'hive card' falls back to showing
  the raw Recent turns block when no cached summary exists.
`,
	"card": `hive card [query] [--json]

  Print a formatted summary of one card's metadata: title, repo, branch,
  worktree path, tmux/Claude session IDs, created/updated/last-attached
  timestamps, cost + token breakdown, model, pending prompt, PR URL, and
  notifications-muted state. Also the source the picker uses for its
  --preview sidebar.

  --json   Emit the same fields as a JSON object instead of a text block.
`,
	"peek": `hive peek [query] [-n N]

  Print the last N lines (default 40) of a session's tmux pane without
  attaching. Useful for quick "what's Claude up to?" checks.

  -n, --lines N    Number of trailing lines to print (default 40).

  Example:
    hive peek fix-au -n 80
`,
	"tail": `hive tail [query] [-n N]

  Live-stream a session's tmux pane. Refreshes 2×/s. Ctrl-C exits. The window
  shows the last N lines (default 20).

  -n, --lines N    Lines to keep on screen (default 20).
`,
	"send": `hive send [query] <msg>

  Send text to a session without attaching.

  <msg> can be:
    literal text    e.g. hive send fix-au "keep going"
    -               read stdin:     echo hi | hive send fix-au -
    (omit) or -e    open $EDITOR:   hive send fix-au
                                    hive send fix-au --edit

  -e, --edit        Open $EDITOR on a tempfile for multi-line input.
`,
	"done": `hive done [query] [flags]

  Archive a card: move it to the Done column (status=archived) and kill its
  tmux session. A 3-second Ctrl-C window lets you undo; skipped when --yes
  is passed or stdin is not a TTY.

  -y, --yes            Skip the undo window.
  --delete-worktree    Remove the git worktree as part of archiving.
  --keep-worktree      Keep the worktree (override delete-configured setups).

  Running 'hive done' on a card that's already Done+archived moves it to the
  hidden Archived column.
`,
	"resume": `hive resume [query]

  Re-launch a Done or Archived card: create a new tmux session in the stored
  worktree running 'claude --resume <id>' (if a session ID was captured on
  archive; otherwise just 'claude'), mark the card Active, and attach.
`,
	"rm": `hive rm, hive kill [query] [flags]

  Hard-delete a card and kill its tmux session. Prompts for confirmation
  unless --yes is passed. Non-TTY invocations require --yes.

  -y, --yes            Skip the confirmation prompt.
  --delete-worktree    Also 'git worktree remove' the card's worktree.
`,
	"kill": "See 'hive help rm'.",
	"ls": `hive ls, hive list [flags]

  List cards with filters and a chosen output format.

  -r, --repo <name>     Filter by repo.
  -s, --status <st>     Filter by live status (working|idle|needs_input|
                        errored|paused|archived|unknown).
  -c, --column <col>    Filter by column (active|done|archived|backlog).
  --since <dur>         Updated within duration. Accepts 'd' suffix (e.g. 7d,
                        2d12h), plus standard Go durations (1h, 30m, 250ms).
  --json                JSON array of sanitized card fields.
  -f, --format <fmt>    table (default) | tsv | json.

  Example:
    hive ls --repo flowrida --status needs_input --since 1h
`,
	"list":     "See 'hive help ls'.",
	"archived": "hive archived [ls flags]\n\n  Alias for 'hive ls -s archived'. Shows every card with status=archived\n  (covers both col=Done and col=Archived). Accepts the same flags as ls:\n  --repo, --since, --json, -f, etc.\n",
	"watch":  "hive watch [--interval 2s] [ls flags]\n\n  Live-redraw the card list. Accepts every filter flag 'hive ls' accepts.\n  --interval, -i <dur>    Refresh interval (>= 250ms). Default 2s.\n",
	"status": "hive status [--short|--json]\n\n  Summarize cards:\n    default: multi-line human summary\n    -s, --short    one-line PS1 ('3⚙ 1❓ $2.47')\n    --json         StatusSummary object for scripting\n",
	"search": `hive search <term> [flags]

  Grep live tmux panes and Claude transcript JSONL files for a term. Output
  is one match per line: '<short_id>  <title>  (pane|transcript): <match>'.

  --cards-only         Skip transcripts.
  --transcripts-only   Skip pane captures.
  -n, --limit N        Max matches per card (default 5).

  Example:
    hive search "flaky test"
`,
	"cd":     "hive cd [query]\n\n  Print the worktree path on stdout (picker UI goes to stderr). Intended\n  for shell aliases:\n\n    alias hc='cd $(hive cd)'\n\n  Exits non-zero when the picker is cancelled so the surrounding 'cd'\n  doesn't take you home unintentionally.\n",
	"open":   "hive open [query] [--finder]\n\n  Open a card's worktree in your editor (\\$VISUAL → \\$EDITOR → 'code' →\n  'open'). With --finder, opens in Finder (macOS).\n",
	"doctor": "hive doctor\n\n  Sanity-check the environment: tmux/git/claude/fzf on $PATH, config load,\n  repo path validation, DB writability, orphaned tmux sessions, and stale\n  worktree paths. Exits non-zero on any failure.\n",
	"tui":    "hive tui\n\n  Launch the Bubble Tea kanban board. Sessions persist after quit.\n",
	"import": `hive import [flags]

  Import an existing tmux or Claude session as a hive card.

  --tmux <name>         Running tmux session name.
  --session-id <id>     Claude session (UUID or name like 'eng-cli').
  -r, --repo <name>     Repo name (required).
  -t, --title <name>    Card title (required).
  --cwd <path>          Worktree path (defaults: the tmux session's cwd).
`,
	"config": "hive config check\n\n  Load ~/.hive/config.yaml, print a summary, and validate every repo path.\n  Exits 4 on config problems.\n",
	"template": `hive template list | show <name> | create <name> [flags]

  Manage reusable session templates (YAML files under ~/.hive/templates/).

  create flags:
    --repo <name>      Repo name.
    --prompt <text>    Initial prompt.
    --branch <name>    Base branch.
`,
	"daemon":  "hive daemon\n\n  Run the poller in the foreground, no TUI. Logs to ~/.hive/daemon.log.\n",
	"version": "hive version\n\n  Print the build version.\n",
}

// printVerbHelp writes verb-specific help or falls back to the top-level usage.
func printVerbHelp(verb string) {
	// Normalize common aliases so `hive help last` shows the full help, etc.
	switch verb {
	case "-":
		verb = "last"
	}
	if text, ok := verbHelp[verb]; ok {
		fmt.Fprintln(os.Stderr, text)
		return
	}
	fmt.Fprintf(os.Stderr, "No help for %q. Run 'hive help' for a command list.\n", verb)
	os.Exit(exitGeneral)
}

func checkDependency(name, msg string) {
	if _, err := exec.LookPath(name); err != nil {
		exitWith(exitDepMissing, "Error: %s", msg)
	}
}
