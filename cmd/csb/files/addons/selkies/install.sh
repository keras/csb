#!/bin/bash
# csb:run-arg --shm-size=1g
#
# Publish the Selkies web port from the host:
#   csb --addon selkies --publish 8080                  # dynamic host port
#   csb --addon selkies --publish 127.0.0.1:8080:8080   # fixed host port
# Then run `selkies-start` inside the container; it prints the URL using
# $CSB_PUBLISH_8080 (the host port csb allocated) when --publish 8080
# was used, else falls back to 8080. Selkies serves the web client over
# plain HTTP on localhost, which is a browser "secure context", so the
# video and audio streams work without a TLS certificate.
#
# Selkies streams over its default WebSocket transport: one TCP port, no
# STUN/TURN relay, no UDP range to publish.
#
# The runtime scripts and X configs live in their own files next to this
# install.sh (selkies-start, selkies-stop, xorg.conf, Xwrapper.config,
# entrypoint.sh, help) and are placed with `install` below.

set -euo pipefail

SELKIES_VERSION=2.1.0

apt-get install -y --no-install-recommends \
    xfce4 xterm \
    pulseaudio \
    xserver-xorg-core xserver-xorg-video-dummy xserver-xorg-legacy \
    x11-utils x11-xkb-utils x11-xserver-utils xcvt xclip xdotool \
    procps curl ca-certificates jq \
    dbus-x11 fonts-dejavu-core

# The Debian package is a self-contained Python environment under
# /opt/selkies: the web client, the capture and audio extensions and both
# gamepad/webcam interposers travel inside it, and apt supplies only the
# shared libraries. The file name carries the distribution it was built in.
# shellcheck disable=SC1091
. /etc/os-release
_arch=$(dpkg --print-architecture)
_deb="selkies-${SELKIES_VERSION}-debian${VERSION_CODENAME}-${_arch}.deb"
_base_url="https://github.com/selkies-project/selkies/releases/download/${SELKIES_VERSION}"

_tmpdir=$(mktemp -d)
curl -fsSL "$_base_url/$_deb" -o "$_tmpdir/$_deb"
curl -fsSL "$_base_url/SHA256SUMS" -o "$_tmpdir/SHA256SUMS"
# The release has no package signature, so its checksum list is the only
# integrity check. --ignore-missing skips the other files it lists.
( cd "$_tmpdir" && sha256sum --check --ignore-missing SHA256SUMS ) \
    || { echo "selkies install: checksum mismatch for $_deb" >&2; exit 1; }
apt-get install -y --no-install-recommends "$_tmpdir/$_deb"
rm -rf "$_tmpdir"

command -v selkies >/dev/null \
    || { echo "selkies install: selkies not on PATH after installing $_deb" >&2; exit 1; }

# Selkies' resize shells out to `cvt`/`gtf` for the xrandr modeline when its
# native RandR call fails, and selkies-start uses `cvt` for the initial
# resolution. Both come from the `xcvt` package installed above. Fail the
# build loudly if it is ever missing rather than shipping a desktop that
# cannot resize.
command -v cvt >/dev/null \
    || { echo "selkies install: cvt not found (xcvt package missing); dynamic resize needs it" >&2; exit 1; }

# A HiDPI browser asks Selkies for a DEVICE-pixel resolution (window size x
# devicePixelRatio) and displays it scaled back down, so the desktop must
# raise its DPI to match or every label renders at half its physical size.
# Selkies applies that through xfconf-query, and only xfsettingsd — the
# XSETTINGS manager — turns the stored value into the Xft.dpi / Xcursor.size
# resources apps actually read. Both must exist. Guard them separately: with
# xfconf-query present but xfsettingsd missing, the query still exits 0, so
# selkies logs a successful DPI change while the desktop stays at 96 DPI.
# A silent lie is worse than a build failure.
command -v xfconf-query >/dev/null \
    || { echo "selkies install: xfconf-query not found; DPI scaling needs it" >&2; exit 1; }
command -v xfsettingsd >/dev/null \
    || { echo "selkies install: xfsettingsd not found; nothing would apply the DPI" >&2; exit 1; }

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
# usually runs NON-root. The Xorg binary is launched through the setuid
# Xorg.wrap (from xserver-xorg-legacy); allow any user to start it. Do NOT
# grant it root rights: the server would then run as root without
# CAP_IPC_OWNER, which containers drop, and could not attach the MIT-SHM
# segments Selkies creates as the sandbox user. Screen capture then fails with
# an Access error and the stream never starts. The dummy driver uses no GPU or
# KMS, so it needs no root rights.
install -D -m 0644 Xwrapper.config /etc/X11/Xwrapper.config

# xorg.conf: baked into the system path so Xorg auto-loads it with no
# -config flag — a non-root user cannot write a system config at runtime.
# Its Virtual size is the MAX resolution Selkies' dynamic resize can grow to
# (5K); depth is fixed at 24 (CSB_SELKIES_GEOMETRY still sets the initial
# size at runtime, its depth field is not used by Xorg).
install -D -m 0644 xorg.conf /etc/X11/xorg.conf

install -D -m 0755 selkies-start /usr/local/bin/selkies-start
install -D -m 0755 selkies-stop  /usr/local/bin/selkies-stop
install -D -m 0755 entrypoint.sh /etc/csb/entrypoint.d/selkies.sh
install -D -m 0644 help          /etc/csb/help.d/selkies
