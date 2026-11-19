# devenv zsh init - restore ZDOTDIR and source user's .zshrc

# Remember whether this shell was spawned by the devenv hook, then unset the
# exported marker immediately so it cannot leak into further descendants.
# The cd-out handler itself is installed after the user's zsh configuration
# below, so their precmd hook setup cannot discard it.
if [ -n "$_DEVENV_HOOK_DIR" ]; then
    unset _DEVENV_HOOK_DIR
    __devenv_enable_exit_on_cd_out=1
fi

if [ -n "$_DEVENV_REAL_ZDOTDIR" ]; then
    ZDOTDIR="$_DEVENV_REAL_ZDOTDIR"
    unset _DEVENV_REAL_ZDOTDIR
    [ -f "$ZDOTDIR/.zshenv" ] && source "$ZDOTDIR/.zshenv"
    [ -f "$ZDOTDIR/.zshrc" ] && source "$ZDOTDIR/.zshrc"
else
    unset ZDOTDIR
    [ -f "$HOME/.zshenv" ] && source "$HOME/.zshenv"
    [ -f "$HOME/.zshrc" ] && source "$HOME/.zshrc"
fi

# Restore devenv PATH after user's .zshrc may have modified it
export PATH="$_DEVENV_PATH"

# A hook-spawned shell must exit when the user leaves the project so its
# parent shell can follow. Install this after the user's zsh configuration so
# their precmd hook setup cannot discard the handler.
if [ -n "$__devenv_enable_exit_on_cd_out" ]; then
    __devenv_exit_on_cd_out() {
        case "$PWD" in
            "$DEVENV_ROOT"|"$DEVENV_ROOT"/*) ;;
            *)
                printf '%s' "$PWD" > "$DEVENV_ROOT/.devenv/exit-dir"
                exit
                ;;
        esac
    }
    autoload -Uz add-zsh-hook
    add-zsh-hook precmd __devenv_exit_on_cd_out
    unset __devenv_enable_exit_on_cd_out
fi

# Set devenv prompt prefix
PROMPT="(devenv) ${PROMPT}"

# Hot-reload hook

autoload -Uz add-zsh-hook

__devenv_reload_apply() {
    # Source new environment if a reload is pending
    if [ -f "/tmp/devenv-reload-7404.sh" ]; then
        # Shell out to bash to handle the env diff (bash syntax)
        local reload_output
        reload_output=$(bash -c '
            
# Environment diff helpers (inspired by direnv)
# Diff is stored in _DEVENV_DIFF env var (not a file) so each shell has its own state
# Uses gzip+base64 encoding for compact storage

# Variables to ignore in diff (shell internals that change dynamically)
__devenv_ignored_var() {
    case "$1" in
        _*|PWD|OLDPWD|SHLVL|SHELL|SHELLOPTS|BASHOPTS|BASH_*|HISTCMD|HISTFILE)
            return 0 ;;
        PS1|PS2|PS3|PS4|PROMPT|PROMPT_COMMAND|PROMPT_DIRTRIM)
            return 0 ;;
        COMP_*|READLINE_*|MAILCHECK|COLUMNS|LINES|RANDOM|SECONDS|LINENO|EPOCHSECONDS|EPOCHREALTIME|SRANDOM)
            return 0 ;;
        STARSHIP_*|__fish*|DIRENV_*|nix_saved_*)
            return 0 ;;
        *)
            return 1 ;;
    esac
}

__devenv_capture_env() {
    # Capture exported variables using declare -p for proper escaping
    declare -p -x 2>/dev/null | LC_ALL=C sort
}

__devenv_serialize_diff() {
    # Serialize diff (stdin) to base64-encoded gzip
    gzip -c | base64 -w0
}

__devenv_deserialize_diff() {
    # Deserialize diff from base64-encoded gzip to stdout
    echo "$1" | base64 -d | gzip -d 2>/dev/null
}

__devenv_compute_diff() {
    # Compare before ($1) and current env, return diff via _DEVENV_DIFF env var
    local before_file="$1"

    # Create temp files
    local after_file diff_content
    after_file=$(mktemp)
    diff_content=$(mktemp)
    __devenv_capture_env > "$after_file"

    # Build associative arrays for before/after
    local -A before_vars after_vars
    while IFS= read -r line; do
        [[ "$line" != declare\ -x\ * ]] && continue
        local vardef="${line#declare -x }"
        local var="${vardef%%=*}"
        [[ -z "$var" ]] && continue
        __devenv_ignored_var "$var" && continue
        before_vars["$var"]="$line"
    done < "$before_file"

    while IFS= read -r line; do
        [[ "$line" != declare\ -x\ * ]] && continue
        local vardef="${line#declare -x }"
        local var="${vardef%%=*}"
        [[ -z "$var" ]] && continue
        __devenv_ignored_var "$var" && continue
        after_vars["$var"]="$line"
    done < "$after_file"

    # Find PREV entries (vars that were modified or removed)
    for var in "${!before_vars[@]}"; do
        if [[ "${after_vars[$var]}" != "${before_vars[$var]}" ]]; then
            echo "P:${before_vars[$var]}" >> "$diff_content"
        fi
    done

    # Find NEXT entries (vars that were added or modified)
    for var in "${!after_vars[@]}"; do
        if [[ -z "${before_vars[$var]+x}" ]]; then
            echo "N:$var" >> "$diff_content"
        elif [[ "${after_vars[$var]}" != "${before_vars[$var]}" ]]; then
            echo "N:$var" >> "$diff_content"
        fi
    done

    # Serialize and store in env var
    _DEVENV_DIFF=$(__devenv_serialize_diff < "$diff_content")
    export _DEVENV_DIFF

    rm -f "$after_file" "$diff_content"
}

__devenv_apply_reverse_diff() {
    # Reverse the diff: restore PREV values, unset NEXT-only vars
    [[ -z "$_DEVENV_DIFF" ]] && return

    local -A prev_vars
    local diff_content
    diff_content=$(__devenv_deserialize_diff "$_DEVENV_DIFF")

    # First pass: collect and restore PREV declarations
    while IFS= read -r line; do
        if [[ "$line" == P:declare\ * ]]; then
            local decl="${line#P:}"
            local var="${decl#declare -x }"
            var="${var%%=*}"
            prev_vars["$var"]=1
            # Use export instead of evaluating the declare statement directly,
            # because declare -x inside a function creates a local variable
            # in bash 5.0+.
            eval "export ${decl#declare -x }" 2>/dev/null
        fi
    done <<< "$diff_content"

    # Second pass: unset NEXT vars that were not in PREV (added vars)
    while IFS= read -r line; do
        if [[ "$line" == N:* ]]; then
            local var="${line#N:}"
            if [[ -z "${prev_vars[$var]+x}" ]]; then
                unset "$var"
            fi
        fi
    done <<< "$diff_content"
}


# Reverse previous diff
__devenv_apply_reverse_diff

# Capture env before sourcing new devenv
_before=$(mktemp)
__devenv_capture_env > "$_before"

# Send enterShell output to the terminal instead of the captured stdout.
# Probe by actually opening /dev/tty: it can exist with writable permission
# bits yet fail to open (ENXIO) when there is no controlling terminal.
if { : >/dev/tty; } 2>/dev/null; then
    _devenv_reload_out=/dev/tty
else
    _devenv_reload_out=/dev/null
fi

# Source new devenv environment
source "/tmp/devenv-reload-7404.sh" >"$_devenv_reload_out" 2>"$_devenv_reload_out"
rm -f "/tmp/devenv-reload-7404.sh"
unset _devenv_reload_out

# Compute new diff
__devenv_compute_diff "$_before"
rm -f "$_before"

# Output current environment for the calling shell to parse
export -p
        ' 2>/dev/null)

        # Apply the environment changes
        if [ -n "$reload_output" ]; then
            eval "$reload_output"
        fi

        # Update saved PATH
        _DEVENV_PATH="$PATH"
    fi
}

__devenv_restore_path() {
    # Restore devenv PATH (in case direnv or other tools modified it)
    export PATH="$_DEVENV_PATH"
}

__devenv_precmd_hook() {
    __devenv_reload_apply
    __devenv_restore_path
}
add-zsh-hook precmd __devenv_precmd_hook

# Keybinding for manual reload
__devenv_reload_widget() {
    __devenv_reload_apply
    zle reset-prompt
}
zle -N __devenv_reload_widget
bindkey "${DEVENV_RELOAD_KEYBIND:-\e\C-r}" __devenv_reload_widget


