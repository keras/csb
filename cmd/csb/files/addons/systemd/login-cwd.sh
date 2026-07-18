# Restore the container workdir in the systemd addon's interactive session.
# csb starts the container with `-w /workspace`, and the default (gosu) path
# simply inherits that cwd — but login(1), the interactive entry under this
# addon, chdir()s to the user's home before exec'ing the shell. The launcher
# exports CSB_LOGIN_CWD with the entrypoint's cwd (login -p preserves it into
# the session environment); jump there exactly once and unset the variable so
# later login shells inside the session don't teleport back. Sorts before
# csb-tmux.sh, so the tmux server starts in — and its windows inherit — the
# workdir.
[ -n "${CSB_LOGIN_CWD:-}" ] || return 0
_csb_cwd="$CSB_LOGIN_CWD"
unset CSB_LOGIN_CWD
[ -d "$_csb_cwd" ] && cd "$_csb_cwd"
unset _csb_cwd
