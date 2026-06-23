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
#       gst-launch-1.0 -q pulsesrc device=csb-audio.monitor ! \
#       audio/x-raw ! audioconvert ! fakesink num-buffers=10; selkies-stop'

command -v Xorg openbox selkies-gstreamer selkies-start selkies-stop curl pgrep pactl gst-launch-1.0 xsel \
    xrandr cvt turnserver turnutils_uclient turnutils_peer >/dev/null

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

selkies-start

ok=0
wait_web && ok=1

if [ "$ok" != "1" ]; then
    echo "=== Xorg.log ==="; cat "$HOME/.selkies/Xorg.log" 2>/dev/null || echo "(missing)"
    echo "=== openbox.log ==="; cat "$HOME/.selkies/openbox.log" 2>/dev/null || echo "(missing)"
    echo "=== pulseaudio.log ==="; cat "$HOME/.selkies/pulseaudio.log" 2>/dev/null || echo "(missing)"
    echo "=== selkies.log ==="; cat "$HOME/.selkies/selkies.log" 2>/dev/null || echo "(missing)"
    echo "=== processes ==="
    for f in "$HOME"/.selkies/*.pid; do
        [ -f "$f" ] || continue
        pid=$(cat "$f")
        echo "$f -> pid=$pid alive=$([ -d "/proc/$pid" ] && echo yes || echo no)"
    done
    exit 1
fi

pgrep -x Xorg >/dev/null
pgrep -f selkies-gstreamer >/dev/null

# ── Dynamic resolution ────────────────────────────────────────────────
# Selkies resizes the X display to match the browser window by ADDING RANDR
# modes at runtime (xrandr --newmode/--addmode/--output). This needs (a) an
# X server that honours RRCreateMode — Xvfb does NOT, so we use Xorg+dummy —
# and (b) the `cvt` tool Selkies' resize.py shells out to (from the xcvt
# package). Prove the display can actually be enlarged past its initial
# geometry, exercising the exact RANDR path Selkies uses on a browser resize.
export DISPLAY=:1

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

# The framebuffer envelope (Virtual / maximum) must exceed the 1280x800
# default initial so there is room to grow.
_max=$(xrandr 2>/dev/null | sed -n 's/.*maximum \([0-9]*\) x \([0-9]*\).*/\1x\2/p')
echo "framebuffer maximum: ${_max:-unknown}"
_envw=${_max%x*}; _envh=${_max#*x}
{ [ -n "$_envw" ] && [ "$_envw" -ge 1920 ] && [ "$_envh" -ge 1080 ]; } \
    || resize_fail "framebuffer envelope '${_max}' too small for dynamic resize"

# Actually grow the display past its initial size to 1920x1080, using the
# same newmode/addmode/output sequence (and cvt modeline) Selkies uses.
_out=$(xrandr --query 2>/dev/null | awk '/ connected/{print $1; exit}')
[ -n "$_out" ] || resize_fail "no connected RANDR output on :1"
_ml=$(cvt -r 1920 1080 60 | sed -n 's/^[[:space:]]*Modeline[[:space:]]*"[^"]*"[[:space:]]*//p')
[ -n "$_ml" ] || resize_fail "cvt produced no modeline for 1920x1080"
xrandr --newmode 1920x1080 $_ml 2>/dev/null || true
xrandr --addmode "$_out" 1920x1080 2>/dev/null \
    || resize_fail "xrandr could not add mode 1920x1080 (server ignores RRCreateMode?)"
xrandr --output "$_out" --mode 1920x1080 2>/dev/null \
    || resize_fail "could not switch $_out to 1920x1080"
_cur=$(xrandr 2>/dev/null | sed -n 's/.*current \([0-9]*\) x \([0-9]*\).*/\1x\2/p')
echo "current resolution after resize: ${_cur}"
[ "$_cur" = "1920x1080" ] || resize_fail "display did not resize to 1920x1080 (got '${_cur}')"

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

# 1. The capture sink must exist server-side.
pactl list short sinks 2>&1 | grep -q csb-audio || audio_fail "csb-audio sink missing"

# 2. A real capture probe must succeed. gst-launch-1.0 can exit 0 even on
#    a connection error, so check stderr for the auth/connect failures too
#    rather than trusting the exit code alone.
probe_out=$(timeout 15 gst-launch-1.0 -q \
    pulsesrc device=csb-audio.monitor ! audio/x-raw ! audioconvert ! fakesink num-buffers=10 2>&1)
probe_rc=$?
echo "$probe_out"
if [ "$probe_rc" -ne 0 ]; then
    audio_fail "capture probe exited $probe_rc"
fi
if echo "$probe_out" | grep -qiE 'Access denied|Failed to connect|Connection refused|ERROR'; then
    audio_fail "capture probe reported a connection/auth error"
fi

# An app using ONLY the entrypoint-provided PULSE_SERVER (not a manually
# set one) must be able to play into csb-audio — this is what makes the
# user's apps audible. Play a short tone and assert it shows up as a
# sink-input on csb-audio.
PULSE_SERVER="$entry_pulse" timeout 5 gst-launch-1.0 -q audiotestsrc num-buffers=100 ! audioconvert ! pulsesink >/dev/null 2>&1 &
_tone=$!
sleep 1
if ! PULSE_SERVER="$entry_pulse" pactl list short sink-inputs 2>/dev/null | grep -q .; then
    kill "$_tone" 2>/dev/null
    audio_fail "app using entrypoint PULSE_SERVER ($entry_pulse) could not play into csb-audio"
fi
kill "$_tone" 2>/dev/null || true

# ── Local coturn TURN relay ────────────────────────────────────────────
# WebRTC media is relayed through a local coturn so it connects without
# any public STUN/TURN. Assert the relay is up, configured for relay-only
# ICE, and can actually relay a packet round-trip.
turn_fail() {
    echo "TURN CHECK FAILED: $1"
    echo "=== coturn.log ==="; cat "$HOME/.selkies/coturn.log" 2>/dev/null || echo "(missing)"
    echo "=== turnserver.conf ==="; cat "$HOME/.selkies/turnserver.conf" 2>/dev/null || echo "(missing)"
    echo "=== rtc.json ==="; cat "$HOME/.selkies/rtc.json" 2>/dev/null || echo "(missing)"
    selkies-stop || true
    exit 1
}

pgrep -x turnserver >/dev/null || turn_fail "turnserver process not alive"

# 3478 must be listening (TCP).
(exec 3<>/dev/tcp/127.0.0.1/3478) 2>/dev/null && exec 3>&- || turn_fail "3478 not listening"

# rtc.json must force relay through the local turn server.
grep -q '"iceTransportPolicy": "relay"' "$HOME/.selkies/rtc.json" || turn_fail "rtc.json missing relay policy"
grep -q 'turn:127.0.0.1:3478' "$HOME/.selkies/rtc.json" || turn_fail "rtc.json missing turn url"

# Strongest check: a real relayed round-trip through coturn over TCP with
# the configured credentials. turnutils_peer listens on UDP 3480; the
# client allocates on coturn, binds a channel to that peer, and exchanges
# packets. A clean run reports send/recv counts with zero lost packets.
turnutils_peer > "$HOME/.selkies/turn_peer.log" 2>&1 &
_peer_pid=$!
sleep 1
relay_out=$(timeout 30 turnutils_uclient -t -u selkies -w csbturn -p 3478 \
    -n 5 -c -y 127.0.0.1 -e 127.0.0.1 2>&1)
relay_rc=$?
kill "$_peer_pid" 2>/dev/null || true
echo "=== turnutils relay round-trip ==="
echo "$relay_out"
if [ "$relay_rc" -ne 0 ]; then
    turn_fail "turnutils_uclient exited $relay_rc"
fi
# Require evidence of a successful relayed exchange (messages received,
# zero lost). turnutils_uclient can exit 0 even when nothing relayed, so
# assert on the reported counters.
echo "$relay_out" | grep -q 'tot_recv_msgs=10' || turn_fail "no relayed packets received"
echo "$relay_out" | grep -q 'Total lost packets 0' || turn_fail "relayed packets were lost"

# The server (webrtcbin) must offer ONLY relay ICE candidates, and the
# relay address must NOT be loopback. Rationale:
#  - host/srflx candidates make the relay-only browser waste ~30s failing
#    higher-priority pairs before falling back to relay (slow connect).
#  - a 127.0.0.1 relay candidate is dropped by Firefox (it discards
#    loopback candidates) → ICE fails in Firefox though Chrome is lenient.
# Connect as a signalling peer and inspect the candidates it sends. Output
# is "<types>|<loopback-relay?>".
cand=$(python3 - <<'PY'
import asyncio, websockets, base64, json
async def main():
    types=set(); loop_relay=False
    try:
        ws=await websockets.connect("ws://localhost:8080/webrtc/signalling/", open_timeout=5)
        await ws.send("HELLO 1 "+base64.b64encode(b"{}").decode())
        await asyncio.wait_for(ws.recv(), 5)
        try:
            while True:
                m=await asyncio.wait_for(ws.recv(), 6)
                c=json.loads(m).get("ice",{}).get("candidate","") if m.startswith("{") else ""
                if " typ " not in c: continue
                f=c.split(); typ=c.split(" typ ",1)[1].split()[0]; types.add(typ)
                # candidate:... <component> <proto> <prio> <ADDR> <port> typ <typ> ...
                if typ=="relay" and len(f)>=5 and f[4].startswith("127."): loop_relay=True
        except asyncio.TimeoutError:
            pass
    except Exception as e:
        print("ERR:"+type(e).__name__); return
    print((",".join(sorted(types)) or "NONE")+"|"+("yes" if loop_relay else "no"))
asyncio.run(main())
PY
)
cand_types=${cand%|*}; loop_relay=${cand#*|}
echo "offered ICE candidate types: ${cand_types} (loopback-relay: ${loop_relay})"
echo "$cand_types" | grep -q relay || turn_fail "no relay candidate offered (cand=$cand_types)"
if echo "$cand_types" | grep -qE 'host|srflx'; then
    turn_fail "webrtcbin offered non-relay candidates ($cand_types); ice-transport-policy=relay not applied"
fi
[ "$loop_relay" = "no" ] || turn_fail "relay candidate uses loopback (127.0.0.1) address; Firefox will drop it"

# ── Idempotency: a second selkies-start must not relaunch daemons. coturn
#    legitimately forks several worker processes, so assert the recorded
#    pids are unchanged rather than counting processes. ─────────────────
_p1=$(cat "$HOME/.selkies/coturn.pid"); _s1=$(cat "$HOME/.selkies/selkies.pid"); _x1=$(cat "$HOME/.selkies/Xorg.pid")
selkies-start
_p2=$(cat "$HOME/.selkies/coturn.pid"); _s2=$(cat "$HOME/.selkies/selkies.pid"); _x2=$(cat "$HOME/.selkies/Xorg.pid")
if [ "$_p1" != "$_p2" ] || [ "$_s1" != "$_s2" ] || [ "$_x1" != "$_x2" ]; then
    echo "IDEMPOTENCY FAILED: pids changed on 2nd start (coturn $_p1->$_p2, selkies $_s1->$_s2, Xorg $_x1->$_x2)"
    selkies-stop || true; exit 1
fi

# ── Restart cycle: stop then start must come back cleanly. This catches a
#    stale /tmp/.X1-lock blocking Xorg (broken display) and any non-
#    idempotent restart of coturn/selkies on the fixed port. ───────────
selkies-stop
sleep 1
selkies-start
if ! wait_web; then
    echo "RESTART FAILED: web server did not come back after stop/start"
    echo "=== Xorg.log ==="; cat "$HOME/.selkies/Xorg.log" 2>/dev/null || echo "(missing)"
    echo "=== selkies.log ==="; cat "$HOME/.selkies/selkies.log" 2>/dev/null || echo "(missing)"
    selkies-stop || true; exit 1
fi
pgrep -x Xorg >/dev/null || { echo "RESTART FAILED: Xorg not running after restart"; selkies-stop || true; exit 1; }
pgrep -x turnserver >/dev/null || { echo "RESTART FAILED: turnserver not running after restart"; selkies-stop || true; exit 1; }

selkies-stop
