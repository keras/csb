#!/bin/bash
set -euo pipefail

# NOTE: the `go test -tags addons` harness runs this script as ROOT
# (uid 0, via `csb --no-workspace`), so it exercises selkies-start's ROOT
# audio path (PulseAudio as the selkies-audio user, socket under /run).
# Normal csb usage maps the host user in and runs NON-root, which takes
# selkies-start's other branch (PulseAudio as the current user, socket under
# $HOME/.selkies/pulse). The non-root path is NOT covered here and must
# be verified out-of-band, e.g.:
#   csb-host-run go run ./cmd/csb --addon "+selkies" --no-tmux --no-tty \
#       bash -c 'selkies-start; PULSE_SERVER=unix:$HOME/.selkies/pulse/native \
#       parec -d csb-audio.monitor | head -c 1000 | wc -c; selkies-stop'

command -v Xorg xfce4-session xfwm4 xfsettingsd xfconf-query xfce4-panel xfdesktop dbus-daemon xrdb xprop \
    selkies selkies-start selkies-stop curl pgrep pactl parec paplay xclip xdotool \
    xrandr cvt >/dev/null

# The entrypoint must export PULSE_SERVER so apps play into the csb-audio
# sink selkies streams; otherwise audio is silent (0 kbps) even though the
# pipeline is up. Capture what the entrypoint provided before any check
# overrides it.
entry_pulse="${PULSE_SERVER:-}"
[ -n "$entry_pulse" ] || { echo "FAIL: entrypoint did not export PULSE_SERVER"; exit 1; }

# Wait until the web server answers with a usable HTTP status. Basic auth
# is disabled so 200 is expected; accept any non-error status rather than
# body content.
wait_web() {
    for _ in $(seq 1 60); do
        code=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/ || true)
        case "$code" in
            200|301|302|401) return 0 ;;
        esac
        sleep 1
    done
    return 1
}

dump_logs() {
    echo "=== Xorg.log ==="; cat "$HOME/.selkies/Xorg.log" 2>/dev/null || echo "(missing)"
    for _l in dbus xfce4-session pulseaudio selkies; do
        echo "=== ${_l}.log ==="; cat "$HOME/.selkies/${_l}.log" 2>/dev/null || echo "(missing)"
    done
    echo "=== processes ==="
    for f in "$HOME"/.selkies/*.pid; do
        [ -f "$f" ] || continue
        pid=$(cat "$f")
        echo "$f -> pid=$pid alive=$([ -d "/proc/$pid" ] && echo yes || echo no)"
    done
}

selkies-start

ok=0
wait_web && ok=1

if [ "$ok" != "1" ]; then
    dump_logs
    exit 1
fi

pgrep -x Xorg >/dev/null
pgrep -x xfce4-session >/dev/null

# The desktop components come up asynchronously under xfce4-session; give
# them a moment before asserting on them.
wait_proc() {
    for _ in $(seq 1 100); do
        pgrep -x "$1" >/dev/null && return 0
        sleep 0.1
    done
    return 1
}

# ── Dynamic resolution ────────────────────────────────────────────────
# Selkies resizes the X display to match the browser window by ADDING RANDR
# modes at runtime. This needs an X server that honours RRCreateMode — Xvfb
# does NOT, so we use Xorg+dummy — and a framebuffer envelope big enough for
# the sizes a browser asks for. Drive Selkies' own `selkies resize` rather
# than a copy of its xrandr calls, so the test breaks when the real path does.
export DISPLAY=:1
# Selkies' DPI code reads the declared desktop from its own environment;
# selkies-start exports the same value to the streamer.
export XDG_CURRENT_DESKTOP=XFCE

resize_fail() {
    echo "RESIZE CHECK FAILED: $1"
    echo "=== xrandr ==="; xrandr 2>&1 || true
    echo "=== Xorg.log ==="; cat "$HOME/.selkies/Xorg.log" 2>/dev/null || echo "(missing)"
    selkies-stop || true
    exit 1
}

# Resize must be enabled in the streamer.
grep -q 'SELKIES_ENABLE_RESIZE=true' /usr/local/bin/selkies-start \
    || resize_fail "selkies-start does not enable SELKIES_ENABLE_RESIZE"

current_res() {
    xrandr 2>/dev/null | sed -n 's/.*current \([0-9]*\) x \([0-9]*\).*/\1x\2/p'
}

selkies resize 1920x1080 >/dev/null 2>&1 \
    || resize_fail "selkies resize could not grow the display to 1920x1080"
_cur=$(current_res)
echo "current resolution after resize: ${_cur}"
[ "$_cur" = "1920x1080" ] || resize_fail "display did not resize to 1920x1080 (got '${_cur}')"

# HiDPI envelope. A 2x client asks for twice its window size, so an ordinary
# 2560x1440 browser window requests 5120x2880 — past what a 4K-sized
# framebuffer can hold. That failure is not graceful: RRSetScreenSize returns
# BadMatch and leaves the display with no current mode at all. xrandr's
# reported "maximum" does not reveal the limit (it reads 32767x32767), so the
# only honest check is to actually switch to it.
selkies resize 5120x2880 >/dev/null 2>&1 \
    || resize_fail "selkies resize could not reach 5120x2880 (VideoRam too small for HiDPI?)"
_cur=$(current_res)
[ "$_cur" = "5120x2880" ] || resize_fail "display did not resize to 5120x2880 (got '${_cur}')"
echo "HiDPI envelope ok: reached ${_cur}"
selkies resize 1920x1080 >/dev/null 2>&1 || true

# ── HiDPI / DPI scaling ───────────────────────────────────────────────
# A HiDPI browser renders the desktop at device pixels and scales it back
# down, so text is half-size unless the desktop raises its DPI to match.
# Selkies does that through set_dpi(), which needs xfconf-query, a live
# xfce4-session to find the session bus, and a live xfsettingsd to push the
# value into RESOURCE_MANAGER. Drive the real function rather than a
# reimplementation of it — set_dpi returning True while nothing changes is
# exactly the failure this guards.
dpi_fail() {
    echo "DPI CHECK FAILED: $1"
    echo "=== xrdb ==="; xrdb -query 2>&1 || true
    echo "=== xsettings ==="; xfconf-query -c xsettings -l -v 2>&1 || true
    for _l in dbus xfce4-session; do
        echo "=== ${_l}.log ==="; cat "$HOME/.selkies/${_l}.log" 2>/dev/null || echo "(missing)"
    done
    selkies-stop || true
    exit 1
}

wait_proc xfsettingsd || dpi_fail "xfsettingsd not running"
wait_proc xfwm4 || dpi_fail "xfwm4 not running"

# The window manager must have claimed the display, or nothing is decorated.
_wmwin=""
for _ in $(seq 1 50); do
    _wmwin=$(xprop -root _NET_SUPPORTING_WM_CHECK 2>/dev/null | grep -o '0x[0-9a-f]*' || true)
    [ -n "$_wmwin" ] && break
    sleep 0.1
done
[ -n "$_wmwin" ] || dpi_fail "no window manager registered on :1"
xprop -id "$_wmwin" _NET_WM_NAME 2>/dev/null | grep -qi xfwm4 \
    || dpi_fail "window manager on :1 is not xfwm4"

# set_dpi finds the session bus by reading the environment of the running
# xfce4-session; run it with the bundled interpreter so it sees the same
# modules the streamer does.
set_dpi() {
    /opt/selkies/bin/python -c "
import asyncio, sys
from selkies.display_utils import set_dpi
sys.exit(0 if asyncio.run(set_dpi($1)) else 1)
"
}

# DPI is applied through the session's xfconf store, which only exists once
# the session finished loading. Retry rather than racing it.
_dpi_ok=
for _ in $(seq 1 30); do
    if set_dpi 192 2>/dev/null; then _dpi_ok=1; break; fi
    sleep 1
done
[ -n "$_dpi_ok" ] || dpi_fail "selkies' own set_dpi() reported failure"

# The value has to reach RESOURCE_MANAGER, which is what X clients read.
# xfconf storing it is not enough — that is the silent-success mode.
for _ in $(seq 1 50); do
    xrdb -query 2>/dev/null | grep -qE '^Xft\.dpi:[[:space:]]*192' && break
    sleep 0.1
done
xrdb -query 2>/dev/null | grep -qE '^Xft\.dpi:[[:space:]]*192' \
    || dpi_fail "set_dpi(192) did not reach RESOURCE_MANAGER as Xft.dpi"
echo "DPI scaling ok: Xft.dpi=$(xrdb -query | awk '/^Xft.dpi:/{print $2}')"

# xterm must use an Xft face; a core bitmap font ignores Xft.dpi outright,
# so the shipped terminal would not scale with everything else.
xrdb -query 2>/dev/null | grep -qi '^xterm\*faceName:' \
    || dpi_fail "xterm has no Xft faceName; it would ignore Xft.dpi"

set_dpi 96 || dpi_fail "could not restore DPI to 96"

# Audio path must actually work — this is the whole point of the addon.
# Connect to PulseAudio over the same anonymous socket selkies-start uses;
# the socket path depends on the uid branch selkies-start took.
if [ "$(id -u)" -eq 0 ]; then
    export PULSE_SERVER="unix:/run/selkies-audio/native"
else
    export PULSE_SERVER="unix:${HOME}/.selkies/pulse/native"
fi

audio_fail() {
    echo "AUDIO CHECK FAILED: $1"
    echo "=== pulseaudio.log ==="; cat "$HOME/.selkies/pulseaudio.log" 2>/dev/null || echo "(missing)"
    echo "=== selkies.log ==="; cat "$HOME/.selkies/selkies.log" 2>/dev/null || echo "(missing)"
    echo "=== sinks ==="; pactl list short sinks 2>&1 || true
    selkies-stop || true
    exit 1
}

# 1. The capture sink must exist server-side, and be the one selkies reads.
pactl list short sinks 2>&1 | grep -q csb-audio || audio_fail "csb-audio sink missing"
pactl list short sources 2>&1 | grep -q csb-audio.monitor || audio_fail "csb-audio.monitor source missing"
grep -q 'SELKIES_AUDIO_DEVICE_NAME=csb-audio.monitor' /usr/local/bin/selkies-start \
    || audio_fail "selkies-start does not point selkies at csb-audio.monitor"
# Selkies must not have fallen back to a second sink of its own.
pactl list short sinks 2>&1 | grep -qE '[[:space:]]output[[:space:]]' \
    && audio_fail "selkies created its own 'output' sink instead of capturing csb-audio"

# 2. A real capture probe must succeed. parec exits 0 on a clean stop, so
#    count the bytes it read instead of trusting the exit code alone.
probe_bytes=$(timeout 5 parec -d csb-audio.monitor --raw --format=s16le --rate=48000 --channels=2 2>/dev/null \
    | head -c 4000 | wc -c || true)
[ "${probe_bytes:-0}" -gt 0 ] || audio_fail "capture probe read no audio from csb-audio.monitor"

# 3. An app using ONLY the entrypoint-provided PULSE_SERVER (not a manually
#    set one) must be able to play into csb-audio — this is what makes the
#    user's apps audible. Play a short silent clip and assert it shows up
#    as a sink-input.
head -c 1000000 /dev/zero \
    | PULSE_SERVER="$entry_pulse" timeout 5 paplay --raw --rate=44100 --channels=2 --format=s16le >/dev/null 2>&1 &
_tone=$!
sleep 1
if ! PULSE_SERVER="$entry_pulse" pactl list short sink-inputs 2>/dev/null | grep -q .; then
    kill "$_tone" 2>/dev/null
    audio_fail "app using entrypoint PULSE_SERVER ($entry_pulse) could not play into csb-audio"
fi
kill "$_tone" 2>/dev/null || true

# ── Transport ──────────────────────────────────────────────────────────
# Selkies streams over one WebSocket port. Nothing else should be needed.
stream_fail() {
    echo "STREAM CHECK FAILED: $1"
    echo "=== selkies.log ==="; cat "$HOME/.selkies/selkies.log" 2>/dev/null || echo "(missing)"
    selkies-stop || true
    exit 1
}

# Selkies logs this line when it brings up the transport it was told to use.
grep -q 'Starting service: websockets' "$HOME/.selkies/selkies.log" \
    || stream_fail "selkies is not running its WebSocket transport"

# A WebSocket upgrade on the data endpoint must succeed. A bare GET answers
# 101 only when the upgrade headers are valid, which proves the server is
# really serving the stream and not just static files.
_ws_code=$(curl -s -o /dev/null -m 5 -w '%{http_code}' \
    -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
    -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
    http://localhost:8080/api/websockets || true)
[ "$_ws_code" = "101" ] || stream_fail "WebSocket upgrade on /api/websockets returned '${_ws_code}', want 101"

# ── Idempotency: a second selkies-start must not relaunch daemons.
#    Assert the recorded pids are unchanged. ───────────────────────────
pids() {
    for _n in selkies Xorg dbus xfce4-session; do
        printf '%s=%s ' "$_n" "$(cat "$HOME/.selkies/${_n}.pid" 2>/dev/null)"
    done
}
_before=$(pids)
selkies-start
_after=$(pids)
if [ "$_before" != "$_after" ]; then
    echo "IDEMPOTENCY FAILED: pids changed on 2nd start"
    echo "  before: $_before"
    echo "  after:  $_after"
    selkies-stop || true; exit 1
fi

# ── Restart cycle: stop then start must come back cleanly. This catches a
#    stale /tmp/.X1-lock blocking Xorg (broken display), session processes
#    that outlive selkies-stop, and any non-idempotent restart of selkies on
#    the fixed port. ──────────────────────────────────────────────────
selkies-stop
sleep 1
for _n in xfwm4 xfsettingsd xfce4-panel xfdesktop xfce4-session; do
    pgrep -u "$(id -u)" -x "$_n" >/dev/null \
        && { echo "STOP FAILED: $_n still running after selkies-stop"; exit 1; }
done
selkies-start
if ! wait_web; then
    echo "RESTART FAILED: web server did not come back after stop/start"
    dump_logs
    selkies-stop || true; exit 1
fi
pgrep -x Xorg >/dev/null || { echo "RESTART FAILED: Xorg not running after restart"; selkies-stop || true; exit 1; }
wait_proc xfwm4 || { echo "RESTART FAILED: xfwm4 not running after restart"; selkies-stop || true; exit 1; }

selkies-stop
