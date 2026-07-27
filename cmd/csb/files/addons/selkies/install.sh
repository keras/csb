#!/bin/bash
# csb:run-arg --shm-size=1g
# csb:run-arg -p 127.0.0.1:3478:3478/tcp
#
# Publish the Selkies WebRTC web port from the host:
#   csb --addon selkies --publish 8080                  # dynamic host port
#   csb --addon selkies --publish 127.0.0.1:8080:8080   # fixed host port
# Then run `selkies-start` inside the container; it prints the URL using
# $CSB_PUBLISH_8080 (the host port csb allocated) when --publish 8080
# was used, else falls back to 8080. Selkies serves the web client over
# plain HTTP on localhost, which is a browser "secure context", so both
# the WebRTC video and audio streams work without a TLS certificate.
#
# A local coturn TURN relay is bundled and auto-published on TCP 3478
# (fixed 127.0.0.1:3478:3478 above). All WebRTC media is forced to relay
# through it (iceTransportPolicy=relay), so the connection works without
# any public STUN/TURN and regardless of network topology: both the host
# browser (via the published 3478) and the in-container webrtcbin (via
# 127.0.0.1:3478) reach the same relay. The fixed 3478:3478 mapping is
# what makes turn:127.0.0.1:3478 resolve from both sides.
#
# The runtime scripts and X configs live in their own files next to this
# install.sh (selkies-start, selkies-stop, xorg.conf, Xwrapper.config,
# entrypoint.sh, help) and are placed with `install` below.

set -euo pipefail

SELKIES_VERSION=1.6.2

apt-get install -y --no-install-recommends \
    openbox xterm \
    pulseaudio \
    coturn \
    xserver-xorg-core xserver-xorg-video-dummy xserver-xorg-legacy \
    x11-utils x11-xkb-utils x11-xserver-utils xcvt xsel \
    procps curl ca-certificates jq \
    dbus-x11 fonts-dejavu-core \
    python3 python3-pip python3-gi python3-setuptools \
    gcc python3-dev linux-libc-dev \
    gir1.2-gstreamer-1.0 gir1.2-gst-plugins-base-1.0 gir1.2-gst-plugins-bad-1.0 \
    python3-gst-1.0 \
    gstreamer1.0-tools gstreamer1.0-plugins-base \
    gstreamer1.0-plugins-good gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly \
    gstreamer1.0-libav gstreamer1.0-pulseaudio gstreamer1.0-nice

_arch=$(dpkg --print-architecture)

# The Python application and web client components are
# architecture-independent (pure Python / static JS); GStreamer itself
# comes from Debian's own packages above (trixie ships GStreamer 1.26,
# newer than what selkies needs). There is no prebuilt GStreamer/conda
# bundle for arm64, so we rely on the system GStreamer + its GI bindings.
_whl="selkies_gstreamer-${SELKIES_VERSION}-py3-none-any.whl"
_whl_url="https://github.com/selkies-project/selkies/releases/download/v${SELKIES_VERSION}/${_whl}"
_web_tgz="selkies-gstreamer-web_v${SELKIES_VERSION}.tar.gz"
_web_url="https://github.com/selkies-project/selkies/releases/download/v${SELKIES_VERSION}/${_web_tgz}"

_tmpdir=$(mktemp -d)
curl -fsSL "$_whl_url" -o "$_tmpdir/$_whl"
PIP_BREAK_SYSTEM_PACKAGES=1 pip3 install --no-cache-dir "$_tmpdir/$_whl"

# Force the in-container webrtcbin to relay-only ICE, matching the browser
# (rtc.json iceTransportPolicy=relay). selkies otherwise leaves webrtcbin's
# default policy "all", so it offers host/srflx candidates the browser
# cannot reach. Relay candidates are the LOWEST ICE priority, so the
# browser spends ~30s failing the higher-priority host/srflx pairs before
# it finally tries the relay pair — the slow connect. With both ends
# relay-only there is a single candidate pair and the stream connects
# promptly. (selkies exposes no setting for this, so patch the source.)
_sp=$(python3 -c "import selkies_gstreamer,os;print(os.path.dirname(selkies_gstreamer.__file__))")
if grep -q 'set_property("bundle-policy"' "$_sp/gstwebrtc_app.py" \
   && ! grep -q 'ice-transport-policy' "$_sp/gstwebrtc_app.py"; then
    sed -i '/self\.webrtcbin\.set_property("bundle-policy", "max-compat")/a\        self.webrtcbin.set_property("ice-transport-policy", "relay")' \
        "$_sp/gstwebrtc_app.py"
fi
grep -q 'ice-transport-policy' "$_sp/gstwebrtc_app.py" \
    || { echo "selkies install: failed to patch webrtcbin ice-transport-policy" >&2; exit 1; }

# Speed up (re)connects. selkies' "no peer yet" signalling retry (__main__.py)
# uses a blocking `time.sleep(2)` *inside an async error handler*, so each retry
# both waits 2s and stalls the asyncio loop that would otherwise notice the peer
# registering. The server (peer 0) only emits its WebRTC offer once it pairs
# with the browser (peer 1); until then the client sits on a black screen. On a
# reconnect to an already-running selkies the loop churns ~5 cycles (~10s)
# before the offer goes out, and even a first connect can eat a full 2s cycle
# depending on timing. Make the retry non-blocking and frequent so pairing lands
# in well under a second. Both matching lines (video + audio handlers) are the
# only `time.sleep(2)` in the file. (selkies exposes no setting for this.)
sed -i 's/^\( *\)time\.sleep(2)/\1await asyncio.sleep(0.25)/' "$_sp/__main__.py"
grep -q 'import asyncio' "$_sp/__main__.py" \
    && ! grep -q 'time\.sleep(2)' "$_sp/__main__.py" \
    && grep -q 'await asyncio\.sleep(0\.25)' "$_sp/__main__.py" \
    || { echo "selkies install: failed to patch signalling retry sleep" >&2; exit 1; }

# The coturn package ships an *enabled* coturn.service. Under the systemd addon
# it auto-starts turnserver on 3478 with the stock /etc/turnserver.conf (no
# realm=csb / selkies:csbturn credentials), stealing the port from selkies-start's
# own relay — which then can't bind 3478 and dies, leaving the browser with a
# TURN server that rejects its credentials, so WebRTC media never connects.
# selkies always runs its OWN turnserver from a generated config, so the packaged
# service must never run. Mask it offline (symlink to /dev/null): harmless when
# there is no systemd, authoritative when systemd is PID 1.
ln -sf /dev/null /etc/systemd/system/coturn.service
[ "$(readlink /etc/systemd/system/coturn.service)" = /dev/null ] \
    || { echo "selkies install: failed to mask coturn.service" >&2; exit 1; }

# Selkies' resize.py shells out to `cvt -r` to build the xrandr modeline for
# each new resolution. That binary ships in the `xcvt` package (installed
# above); without it the modeline comes back empty, `xrandr --newmode`
# fails, and dynamic resize is silently disabled. Fail the build loudly if
# it is ever missing rather than shipping a desktop that cannot resize.
command -v cvt >/dev/null \
    || { echo "selkies install: cvt not found (xcvt package missing); dynamic resize needs it" >&2; exit 1; }

mkdir -p /opt/gst-web
curl -fsSL "$_web_url" -o "$_tmpdir/$_web_tgz"
tar -xzf "$_tmpdir/$_web_tgz" -C /opt/gst-web --strip-components=1
rm -rf "$_tmpdir"

# Optional: joystick/gamepad interposer (arm64 build is available).
if [ "$_arch" = "arm64" ]; then
    _js_deb="selkies-js-interposer_v${SELKIES_VERSION}_ubuntu24.04_arm64.deb"
elif [ "$_arch" = "amd64" ]; then
    _js_deb="selkies-js-interposer_v${SELKIES_VERSION}_ubuntu24.04_amd64.deb"
else
    _js_deb=""
fi
if [ -n "$_js_deb" ]; then
    _js_url="https://github.com/selkies-project/selkies/releases/download/v${SELKIES_VERSION}/${_js_deb}"
    _tmpdeb=$(mktemp --suffix=.deb)
    if curl -fsSL "$_js_url" -o "$_tmpdeb"; then
        apt-get install -y --no-install-recommends "$_tmpdeb" || true
    fi
    rm -f "$_tmpdeb"
fi

# PulseAudio refuses to run as root (unless started in heavyweight
# --system mode), so when selkies-start runs as root it needs a dedicated
# unprivileged user to run the daemon under. Create it now; selkies-start
# only USES it in its root branch (in the normal non-root case the
# daemon just runs as the current user). The actual default.pa config
# (anonymous socket + null sink) is generated at runtime by selkies-start so
# the socket path can match whichever runtime dir the uid branch picks.
if ! id selkies-audio >/dev/null 2>&1; then
    useradd --system --create-home --home-dir /var/lib/selkies-audio --shell /usr/sbin/nologin selkies-audio
fi

# Place the bundled runtime scripts and X configs. install -D creates the
# parent dirs as needed.
#
# Xwrapper.config: csb normally maps the host user in, so selkies-start
# usually runs NON-root. The Xorg binary is the setuid Xorg.wrap (from
# xserver-xorg-legacy); allow any user to launch it and grant it the root
# rights the server needs. The dummy driver uses no GPU/KMS, so this is
# safe in the headless container.
install -D -m 0644 Xwrapper.config /etc/X11/Xwrapper.config

# xorg.conf: baked into the system path so Xorg auto-loads it with no
# -config flag — the setuid wrapper (used when a non-root user starts Xorg)
# rejects any -config that isn't a bare name in a trusted dir, and a
# non-root user cannot write a system config at runtime. Its Virtual size
# is the MAX resolution Selkies' dynamic resize can grow to (4K); depth is
# fixed at 24 (CSB_SELKIES_GEOMETRY still sets the initial size at runtime,
# its depth field is not used by Xorg).
install -D -m 0644 xorg.conf /etc/X11/xorg.conf

install -D -m 0755 selkies-start /usr/local/bin/selkies-start
install -D -m 0755 selkies-stop  /usr/local/bin/selkies-stop
install -D -m 0755 entrypoint.sh /etc/csb/entrypoint.d/selkies.sh
install -D -m 0644 help          /etc/csb/help.d/selkies
