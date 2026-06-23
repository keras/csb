# Launcher hook: override the entrypoint's csb_launch to run systemd as PID 1.
# The entrypoint owns no systemd knowledge — it just calls whatever csb_launch
# an addon leaves behind (see the csb_launch seam in entrypoint.sh). We fork the
# user session *first* so it inherits the pty Docker wired to our stdio, then
# exec systemd with its stdio on /dev/null so the two never contend for the
# terminal. When the session exits it asks PID 1 to exit, stopping the container
# (and `--rm` reaps it).
csb_register_launcher systemd
csb_launch() {
    # Preserve the pty so the backgrounded session can reconnect to it; a `&`
    # job would otherwise inherit /dev/null on stdin.
    exec {tin}<&0 {tout}>&1 {terr}>&2
    # With a pty, --ctty makes it the session's controlling terminal so job
    # control and Ctrl-C work in the user shell. Without one (csb -- cmd), drop
    # --ctty since there is no terminal to claim.
    local ctty=()
    [ -t "$tin" ] && ctty=(--ctty)
    setsid "${ctty[@]}" bash -c '
        _run_user noexec "$@"
        systemctl exit "$?" 2>/dev/null || systemctl poweroff 2>/dev/null || true
    ' bash "$@" <&"$tin" >&"$tout" 2>&"$terr" &
    # Hand the pty entirely to the session, then become PID 1 on /dev/null.
    # --show-status=no keeps systemd's boot status off /dev/console (the pty);
    # ShowStatus=no in system.conf is the authoritative knob, this is a backstop.
    exec {tin}<&- {tout}>&- {terr}>&-
    exec </dev/null >/dev/null 2>&1
    exec /lib/systemd/systemd --show-status=no
}
