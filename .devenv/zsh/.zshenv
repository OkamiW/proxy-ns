# devenv zsh .zshenv - runs before /etc/zshrc.
# Prepend devenv profile site-functions so the system compinit (often
# called from /etc/zshrc on nix-darwin, Debian, etc.) picks them up.
if [ -n "$DEVENV_PROFILE" ] && [ -d "$DEVENV_PROFILE/share/zsh/site-functions" ]; then
    fpath=("$DEVENV_PROFILE/share/zsh/site-functions" $fpath)
fi
