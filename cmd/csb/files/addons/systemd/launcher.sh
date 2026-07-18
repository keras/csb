# Launcher hook: override the entrypoint's csb_launch to run systemd as PID 1.
# The entrypoint owns no systemd knowledge — it just calls whatever csb_launch
# an addon leaves behind (see the csb_launch seam in entrypoint.sh). We fork the
# user session *first* so it inherits the pty Docker wired to our stdio, then
# exec systemd with its stdio on /dev/null so the two never contend for the
# terminal. When the session exits it asks PID 1 to exit, stopping the container
# (and `--rm` reaps it). The session's exit code becomes the container exit code
# on the non-interactive branch; the interactive branch always reports 0,
# because util-linux login(1) exits 0 regardless of the shell's status.
#
# The forked session enters one of two ways:
#
#   * Bare interactive session (CSB_LOGIN_SHELL=1, set by csb when no command was
#     given — with or without tmux) on a tty: through login(1), so it becomes a
#     real *logind* session — XDG_RUNTIME_DIR, a per-user `systemd --user`
#     manager, and `loginctl` visibility. gosu (the default drop) bypasses PAM, so
#     logind never sees the session; login runs pam_systemd and registers it. -f
#     skips auth (we are the trusted entrypoint, and the sandbox user's password
#     is locked), -p preserves most of the environment csb injected (broker creds,
#     env_inject, nss_wrapper) that login would otherwise scrub — except PATH,
#     which login always regenerates from /etc/login.defs; core re-adds csb's
#     ~/.local/bin prefix for login shells (/etc/profile.d/csb-path.sh). We gate
#     on systemd-logind being up so the session can actually register; logind
#     resolves the sandbox user from the real /etc/passwd entry the entrypoint
#     appended (gosu/nss_wrapper aren't in logind's process). login runs a login
#     shell, not csb's tmux command, so with tmux a profile.d snippet starts it
#     inside the session (CSB_TMUX / csb-tmux.sh), keeping the tmux server in the
#     logind session.
#
#   * Anything else (explicit `csb -- cmd`, or no tty): the existing lightweight
#     gosu drop. A batch command needs no login session, and login(1) can't carry
#     an arbitrary command anyway.

# _csb_stop_container asks PID 1 (systemd) to exit so the container stops (and
# --rm reaps it), passing the session's exit code through as the container exit
# code. It retries because a fast command can finish before systemd has its
# control socket up, and an exit request sent that early is silently dropped —
# which would leave the container running forever (very likely with heavier addon
# sets that lengthen boot, e.g. podman). Only fall back to poweroff if `exit`
# never took (e.g. verb unavailable); running poweroff *after* a successful exit
# would override the code we just set (and in a private PID namespace poweroff's
# reboot(2) signals init, surfacing as exit 130).
_csb_stop_container() {
    local rc=$1 _
    for _ in $(seq 1 300); do
        systemctl exit "$rc" >/dev/null 2>&1 && return
        sleep 0.1
    done
    systemctl poweroff >/dev/null 2>&1 || true
}
export -f _csb_stop_container

csb_register_launcher systemd
csb_launch() {
    # Preserve the pty so the backgrounded session can reconnect to it; a `&`
    # job would otherwise inherit /dev/null on stdin.
    exec {tin}<&0 {tout}>&1 {terr}>&2
    # With a pty, --ctty makes it the session's controlling terminal so job
    # control and Ctrl-C work in the user shell. Without one (csb -- cmd), drop
    # --ctty since there is no terminal to claim.
    #
    # Acquiring the ctty is a TIOCSCTTY steal, which the kernel denies (EPERM)
    # when the pty is already the controlling terminal of an outer session — e.g.
    # a csb running inside another container (nested rootful podman). That denial
    # is non-fatal: the shell still works, only job control degrades. So *probe*
    # the steal with a throwaway setsid and only pass --ctty when it succeeds,
    # rather than letting setsid abort the entire launch.
    local ctty=()
    if [ -t "$tin" ] && setsid --ctty true <&"$tin" 2>/dev/null; then
        ctty=(--ctty)
    fi

    if [ "${CSB_LOGIN_SHELL:-}" = 1 ] && [ -t "$tin" ]; then
        # Carry the container workdir (`-w /workspace`) across login(1), which
        # chdir()s to $HOME before exec'ing the shell. login -p preserves the
        # environment, and /etc/profile.d/csb-login-cwd.sh (installed by this
        # addon) jumps back once and unsets the variable.
        export CSB_LOGIN_CWD="$PWD"
        # Interactive login shell. login(1) must be the *session leader* and the
        # terminal's foreground process group: it drives the tty (TIOCSCTTY,
        # termios, vhangup) directly, and if it runs in a background process group
        # the kernel hits it with SIGTTOU and stops it (T state) before it ever
        # spawns the shell — the session just hangs. Running it under the wrapper
        # bash (as for the gosu path) is exactly that broken shape. So launch
        # login *as* the session leader with `setsid -w`: it runs login in a new
        # session, making login the foreground group, and waits so we can stop
        # the container when the session ends.
        {
            # systemd calls release_terminal() (TIOCNOTTY) in its early boot,
            # which sends SIGHUP to the foreground process group (pgrp 1). This
            # fork lives in pgrp 1 because bash doesn't create a new pgrp for &
            # jobs in non-interactive mode. SIG_IGN is inherited across exec(), so
            # sleep/timeout/login children also ignore the SIGHUP, and login's own
            # vhangup() SIGHUP (sent to login's new session's foreground pgrp) is
            # harmless too — login inherits SIG_IGN and simply continues to exec
            # the shell.
            trap '' HUP
            # Wait (bounded ~30s) for logind so the session can register; if it
            # never comes up we still log in, just without a tracked session.
            # On Docker Desktop on macOS, systemd boots slower (large image +
            # VirtioFS) than on native Linux, so 10 s was not always enough.
            # Each systemctl call is capped at 0.5 s: if the D-Bus socket exists
            # but the daemon is still initializing, a bare `systemctl is-active`
            # can block for the full D-Bus method timeout (90 s by default).
            for _ in $(seq 1 300); do
                timeout 0.5 systemctl -q is-active systemd-logind.service 2>/dev/null && break
                sleep 0.1
            done
            # Do NOT pass --ctty here: login acquires the controlling terminal
            # itself via TIOCSCTTY internally (TIOCNOTTY in systemd's early boot
            # releases session 1's ctty, so the non-force steal succeeds). login's
            # vhangup() sends SIGHUP to its own foreground pgrp, but SIG_IGN
            # inherited above means login ignores it and proceeds to exec the shell.
            setsid -w login -p -f sandbox <&"$tin" >&"$tout" 2>&"$terr"
            _csb_stop_container "$?"
        } &
    else
        # Non-interactive run or explicit command: the lightweight gosu drop. The
        # command runs in the wrapper's foreground process group, so no SIGTTOU.
        setsid "${ctty[@]}" bash -c '
            _run_user noexec "$@"
            _csb_stop_container "$?"
        ' bash "$@" <&"$tin" >&"$tout" 2>&"$terr" &
    fi

    # Hand the pty entirely to the session, then become PID 1 on /dev/null.
    # --show-status=no keeps systemd's boot status off /dev/console (the pty);
    # ShowStatus=no in system.conf is the authoritative knob, this is a backstop.
    #
    # SYSTEMD_LOG_TARGET=null: whenever the journal is unreachable — before
    # journald is up (the version banner, "Detected virtualization ...",
    # "Queued start job ...") and again during late shutdown ("Sending SIGTERM
    # to remaining processes ...") — systemd's logging falls back to
    # /dev/console, which under `-t` is the user's pty. A container has no
    # writable /dev/kmsg to absorb the fallback, and no CAP_SYS_ADMIN to divert
    # the console node, so instead PID 1 boots with its logging off entirely
    # and log-target.service flips it to the journal for exactly the window
    # journald is alive. Must be the environment variable: it is read before
    # argv parsing (a --log-target flag misses the banner), and it survives
    # system.conf parsing (env wins over config for PID 1's log settings).
    exec {tin}<&- {tout}>&- {terr}>&-
    exec </dev/null >/dev/null 2>&1
    exec env SYSTEMD_LOG_TARGET=null /lib/systemd/systemd --show-status=no
}
