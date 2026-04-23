# bash completion for hive
# Install: source this file from your .bashrc, or symlink into /etc/bash_completion.d/

_hive_commands() {
  echo "new pr-review attach a last peek card tail send done resume rm kill list ls archived watch status search cd open doctor tui daemon import config template version help"
}

_hive_card_ids() {
  hive ls --json 2>/dev/null |
    python3 -c 'import json,sys
for c in json.load(sys.stdin):
    print(c["id"][:8])
' 2>/dev/null
}

_hive() {
  local cur prev words cword
  _init_completion || return

  if (( cword == 1 )); then
    COMPREPLY=($(compgen -W "$(_hive_commands)" -- "$cur"))
    return
  fi

  local cmd="${words[1]}"
  case "$cmd" in
    attach|a|resume|cd)
      COMPREPLY=($(compgen -W "$(_hive_card_ids)" -- "$cur"))
      ;;
    card)
      if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "--json" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$(_hive_card_ids)" -- "$cur"))
      fi
      ;;
    peek|tail)
      if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "-n --lines" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$(_hive_card_ids)" -- "$cur"))
      fi
      ;;
    send)
      if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "-e --edit" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$(_hive_card_ids)" -- "$cur"))
      fi
      ;;
    done)
      if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "-y --yes --delete-worktree --keep-worktree" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$(_hive_card_ids)" -- "$cur"))
      fi
      ;;
    rm|kill)
      if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "-y --yes --delete-worktree" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$(_hive_card_ids)" -- "$cur"))
      fi
      ;;
    open)
      if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "--finder" -- "$cur"))
      else
        COMPREPLY=($(compgen -W "$(_hive_card_ids)" -- "$cur"))
      fi
      ;;
    ls|list)
      COMPREPLY=($(compgen -W "--repo --status --column --since --json --format -r -s -c -f" -- "$cur"))
      ;;
    watch)
      COMPREPLY=($(compgen -W "--interval --repo --status --column --since -i -r -c" -- "$cur"))
      ;;
    status)
      COMPREPLY=($(compgen -W "--short --json -s" -- "$cur"))
      ;;
    new)
      COMPREPLY=($(compgen -W "--repo --title --prompt --branch --worktree --bg --detach -r -t -p -b -w -d" -- "$cur"))
      ;;
    pr-review)
      COMPREPLY=($(compgen -W "--bg --detach --prompt --repo -d -p -r" -- "$cur"))
      ;;
    template)
      COMPREPLY=($(compgen -W "list show create" -- "$cur"))
      ;;
    import)
      COMPREPLY=($(compgen -W "--tmux --session-id --repo --title --cwd -r -t -s -w" -- "$cur"))
      ;;
    search)
      COMPREPLY=($(compgen -W "--cards-only --transcripts-only --limit -n" -- "$cur"))
      ;;
    config)
      COMPREPLY=($(compgen -W "check" -- "$cur"))
      ;;
    help)
      COMPREPLY=($(compgen -W "$(_hive_commands)" -- "$cur"))
      ;;
  esac
}

complete -F _hive hive
