#!/bin/bash
set -euo pipefail

# This script is executed via `csb --addon systemd -- bash -c <script>` (see
# internal/addons/addons_test.go), i.e. the launcher's *non-interactive* branch
# — so it runs as a child of an actually booted systemd and can assert runtime
# behavior, not just installed files. The interactive/login/tmux branches need a
# pty and are covered from the host side in internal/addons/systemd_test.go.

# --- Boot behavior -----------------------------------------------------------

# systemd is the running init: PID 1 is systemd and its runtime dir exists.
test -d /run/systemd/system
test "$(cat /proc/1/comm)" = systemd

# The non-interactive command is launched in parallel with the boot, often
# before the D-Bus system bus is even connectable (unprivileged systemctl talks
# to systemd through it). Poll until systemctl gets *any* state answer — while
# the bus is unreachable it prints nothing — before asking it to wait.
for _ in $(seq 1 120); do
    state=$(timeout 2 systemctl is-system-running 2>/dev/null) || true
    [ -n "$state" ] && break
    sleep 0.5
done

# The system settles. Accept degraded — a failed leaf unit shouldn't fail the
# addon; the units the addon actually depends on are checked individually below.
state=$(timeout 90 systemctl is-system-running --wait) || true
case "$state" in
    running|degraded) ;;
    *) echo "unexpected system state: '$state'" >&2; exit 1 ;;
esac

# dbus and logind are up — the interactive login path gates on logind, and any
# desktop-flavoured addon layered on top needs dbus.
systemctl -q is-active dbus.service
systemctl -q is-active systemd-logind.service

# The journal is live (csb-container.conf routes logs there).
systemctl -q is-active systemd-journald.service

# The build-time masks held: no getty is up to fight the user session for the pty.
! systemctl -q is-active console-getty.service
! systemctl -q is-active getty@tty1.service

# --- Non-interactive session shape -------------------------------------------

# This path is the lightweight gosu drop, NOT a logind session: pam_systemd
# never ran, so no XDG_RUNTIME_DIR and no session is registered.
test -z "${XDG_RUNTIME_DIR:-}"
test -z "$(loginctl --no-legend list-sessions)"

# --- Installed files ---------------------------------------------------------

# systemd and its PID 1 binary are present.
command -v systemctl >/dev/null
test -x /lib/systemd/systemd

# Launcher hook is installed (and sourceable) and registers as an init provider.
test -x /etc/csb/entrypoint.d/systemd.sh
grep -q 'csb_register_launcher systemd' /etc/csb/entrypoint.d/systemd.sh

# Container-tuning config is in place.
test -f /etc/systemd/system.conf.d/csb-container.conf

# Console/serial gettys are masked so they can't grab the user's pty.
test "$(readlink /etc/systemd/system/getty.target)" = /dev/null
test "$(readlink /etc/systemd/system/console-getty.service)" = /dev/null

# The interactive session enters through login(1), and pam_systemd must be in the
# PAM session stack so logind registers a real session (XDG_RUNTIME_DIR, etc.).
command -v login >/dev/null
find /lib /usr/lib -name pam_systemd.so | grep -q .
# pam_systemd is wired into the stack /etc/pam.d/login pulls in (common-session).
grep -rq pam_systemd.so /etc/pam.d/

# The launcher routes the bare login shell through login(1).
grep -q 'login -p -f sandbox' /etc/csb/entrypoint.d/systemd.sh
