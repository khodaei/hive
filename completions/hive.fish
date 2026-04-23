# fish completions for hive
# Install: copy into ~/.config/fish/completions/hive.fish

function __hive_commands
    echo -e "new\tCreate a session and attach"
    echo -e "pr-review\tReview a pull request in a new worktree"
    echo -e "attach\tAttach to a session"
    echo -e "a\tAttach (alias)"
    echo -e "last\tAttach the most-recently-attached session"
    echo -e "peek\tSnapshot a pane without attaching"
    echo -e "card\tFormatted summary of a card"
    echo -e "tail\tLive-stream a pane"
    echo -e "send\tSend text without attaching"
    echo -e "done\tArchive a card (10s undo)"
    echo -e "resume\tRe-launch a Done/Archived card"
    echo -e "rm\tHard-delete a card"
    echo -e "kill\tHard-delete a card (alias)"
    echo -e "list\tList all cards"
    echo -e "ls\tList all cards (alias)"
    echo -e "archived\tList archived cards"
    echo -e "watch\tLive-update the card list"
    echo -e "status\tSummary"
    echo -e "search\tGrep panes + transcripts"
    echo -e "cd\tPrint a card worktree path"
    echo -e "open\tOpen worktree in editor"
    echo -e "doctor\tEnvironment sanity checks"
    echo -e "tui\tLaunch the TUI"
    echo -e "daemon\tBackground poller"
    echo -e "import\tImport existing session"
    echo -e "config\tConfig check"
    echo -e "template\tManage templates"
    echo -e "version\tPrint version"
    echo -e "help\tShow help"
end

function __hive_card_ids
    hive ls --json 2>/dev/null |
      python3 -c 'import json,sys
for c in json.load(sys.stdin):
    print(c["id"][:8] + "\t" + c["title"])
' 2>/dev/null
end

set -l __hive_subs new pr-review attach a last peek card tail send done resume rm kill list ls archived watch status search cd open doctor tui daemon import config template version help
complete -c hive -n "not __fish_seen_subcommand_from $__hive_subs" -f -a "(__hive_commands)"

# Subcommand: attach-style cards
for sub in attach a peek card tail send done resume rm kill cd open
    complete -c hive -n "__fish_seen_subcommand_from $sub" -f -a "(__hive_card_ids)"
end

complete -c hive -n "__fish_seen_subcommand_from card" -l json -d "JSON output"

# Shared flags
complete -c hive -n "__fish_seen_subcommand_from new" -l repo -s r -d "Repo name"
complete -c hive -n "__fish_seen_subcommand_from new" -l title -s t -d "Title"
complete -c hive -n "__fish_seen_subcommand_from new" -l prompt -s p -d "Initial prompt"
complete -c hive -n "__fish_seen_subcommand_from new" -l branch -s b -d "Branch"
complete -c hive -n "__fish_seen_subcommand_from new" -l worktree -s w -d "Worktree"
complete -c hive -n "__fish_seen_subcommand_from new" -l bg -s d -d "Background — skip attach"

complete -c hive -n "__fish_seen_subcommand_from pr-review" -l bg -s d -d "Background — skip attach"
complete -c hive -n "__fish_seen_subcommand_from pr-review" -l prompt -s p -d "Prompt override"
complete -c hive -n "__fish_seen_subcommand_from pr-review" -l repo -s r -d "Repo override"

complete -c hive -n "__fish_seen_subcommand_from ls list" -l repo -s r -d "Filter by repo"
complete -c hive -n "__fish_seen_subcommand_from ls list" -l status -s s -d "Filter by status"
complete -c hive -n "__fish_seen_subcommand_from ls list" -l column -s c -d "Filter by column"
complete -c hive -n "__fish_seen_subcommand_from ls list" -l since -d "Updated since duration"
complete -c hive -n "__fish_seen_subcommand_from ls list" -l json -d "JSON output"
complete -c hive -n "__fish_seen_subcommand_from ls list" -l format -s f -d "table|tsv|json"

complete -c hive -n "__fish_seen_subcommand_from status" -l short -s s -d "PS1 one-liner"
complete -c hive -n "__fish_seen_subcommand_from status" -l json -d "Structured output"

complete -c hive -n "__fish_seen_subcommand_from done rm kill" -l yes -s y -d "Skip confirmation"
complete -c hive -n "__fish_seen_subcommand_from done rm kill" -l delete-worktree -d "Also remove worktree"
complete -c hive -n "__fish_seen_subcommand_from done" -l keep-worktree -d "Keep worktree"

complete -c hive -n "__fish_seen_subcommand_from send" -l edit -s e -d "Compose in $EDITOR"

complete -c hive -n "__fish_seen_subcommand_from open" -l finder -d "Open in Finder (macOS)"

complete -c hive -n "__fish_seen_subcommand_from search" -l cards-only -d "Skip transcripts"
complete -c hive -n "__fish_seen_subcommand_from search" -l transcripts-only -d "Skip panes"
complete -c hive -n "__fish_seen_subcommand_from search" -l limit -s n -d "Max matches per card"

complete -c hive -n "__fish_seen_subcommand_from peek tail" -l lines -s n -d "Number of lines"

complete -c hive -n "__fish_seen_subcommand_from watch" -l interval -s i -d "Refresh interval (e.g. 2s)"
complete -c hive -n "__fish_seen_subcommand_from watch" -l repo -s r -d "Filter by repo"
complete -c hive -n "__fish_seen_subcommand_from watch" -l status -d "Filter by status"
complete -c hive -n "__fish_seen_subcommand_from watch" -l column -s c -d "Filter by column"
complete -c hive -n "__fish_seen_subcommand_from watch" -l since -d "Updated since duration"

complete -c hive -n "__fish_seen_subcommand_from import" -l tmux -d "Tmux session name"
complete -c hive -n "__fish_seen_subcommand_from import" -l session-id -s s -d "Claude session UUID"
complete -c hive -n "__fish_seen_subcommand_from import" -l repo -s r -d "Repo"
complete -c hive -n "__fish_seen_subcommand_from import" -l title -s t -d "Title"
complete -c hive -n "__fish_seen_subcommand_from import" -l cwd -s w -d "Working dir"


complete -c hive -n "__fish_seen_subcommand_from config" -f -a "check" -d "Validate config"

complete -c hive -n "__fish_seen_subcommand_from template" -f -a "list show create"

complete -c hive -n "__fish_seen_subcommand_from help" -f -a "new attach send done resume rm ls status watch search doctor"
