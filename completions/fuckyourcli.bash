# fuckyourcli — bash completion (sourced by bash-completion).
# Installed by setup.sh / install.sh; keep in sync with them.
_fuckyourcli() {
    local cur prev
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    # Main top-level commands
    local main_commands="config auto serve sandbox status update models"
    local global_flags="--help --version --hash"

    # If we are at the first argument after 'fuckyourcli'
    if [ $COMP_CWORD -eq 1 ]; then
        COMPREPLY=( $(compgen -W "${main_commands} ${global_flags}" -- "$cur") )
        return 0
    fi

    # Flags that require a value: if we are immediately after one of these,
    # do not suggest other flags (allow value input).
    case "${prev}" in
        --addr|--token|--state-dir|--cors|--max-threads|--permissions|--interaction-timeout)
            return 0
            ;;
        --hitl|--interrupts)
            # Boolean flags: no specific completion.
            return 0
            ;;
    esac

    # If the subcommand is 'serve', always suggest serve flags,
    # regardless of how many have already been typed
    # (so 'serve' can have multiple flags simultaneously).
    if [[ "${COMP_WORDS[1]}" == "serve" ]]; then
        local serve_flags="--help --addr --token --state-dir --permissions --cors --max-threads --hitl --interrupts --interaction-timeout"
        COMPREPLY=( $(compgen -W "${serve_flags}" -- "$cur") )
        return 0
    fi

    # Subcommand management based on the previous word (prev)
    case "${prev}" in
        config)
            # Subcommands for 'fuckyourcli config <TAB>'
            COMPREPLY=( $(compgen -W "show set reset" -- "$cur") )
            return 0
            ;;
        auto)
            # Subcommands for 'fuckyourcli auto <TAB>'
            COMPREPLY=( $(compgen -W "enable disable status" -- "$cur") )
            return 0
            ;;
        sandbox)
            # Subcommands for 'fuckyourcli sandbox <TAB>'
            COMPREPLY=( $(compgen -W "setup status reset" -- "$cur") )
            return 0
            ;;
        models)
            # Subcommands for 'fuckyourcli models <TAB>'
            COMPREPLY=( $(compgen -W "update" -- "$cur") )
            return 0
            ;;
    esac
}
complete -F _fuckyourcli fuckyourcli
