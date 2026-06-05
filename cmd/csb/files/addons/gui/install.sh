#!/bin/bash
# csb:run-arg --shm-size=512m
#
# Publish the noVNC port from the host:
#   csb --addon gui --publish 6080                  # dynamic host port
#   csb --addon gui --publish 127.0.0.1:6080:6080   # fixed host port
# Then run `gui-start` inside the container; it prints the URL using
# $CSB_PUBLISH_6080 (the host port csb allocated) when --publish 6080
# was used, else falls back to 6080.

set -euo pipefail

apt-get install -y --no-install-recommends \
    tigervnc-standalone-server tigervnc-common \
    openbox xterm \
    novnc websockify \
    procps curl ca-certificates \
    dbus-x11 fonts-dejavu-core

install -m 0755 /dev/stdin /usr/local/bin/gui-start <<'EOF'
#!/bin/bash
set -euo pipefail
: "${CSB_GUI_GEOMETRY:=1280x800x24}"
_geom=${CSB_GUI_GEOMETRY%x*}
_depth=${CSB_GUI_GEOMETRY##*x}
mkdir -p "$HOME/.vnc"
Xvnc :1 -geometry "$_geom" -depth "$_depth" \
    -SecurityTypes None -localhost yes -rfbport 5901 \
    > "$HOME/.vnc/Xvnc.log" 2>&1 &
echo $! > "$HOME/.vnc/Xvnc.pid"
DISPLAY=:1 openbox > "$HOME/.vnc/openbox.log" 2>&1 &
echo $! > "$HOME/.vnc/openbox.pid"
websockify --web=/usr/share/novnc 6080 localhost:5901 \
    > "$HOME/.vnc/novnc.log" 2>&1 &
echo $! > "$HOME/.vnc/novnc.pid"
echo "GUI ready: http://localhost:${CSB_PUBLISH_6080:-6080}/vnc.html"
EOF

install -m 0755 /dev/stdin /usr/local/bin/gui-stop <<'EOF'
#!/bin/bash
for _name in Xvnc openbox novnc; do
    _pidfile="$HOME/.vnc/${_name}.pid"
    if [ -f "$_pidfile" ]; then
        kill "$(cat "$_pidfile")" 2>/dev/null || true
        rm -f "$_pidfile"
    fi
done
EOF

cat > /etc/csb/entrypoint.d/gui.sh <<'EOT'
export DISPLAY=:1
EOT
chmod +x /etc/csb/entrypoint.d/gui.sh

cat <<'EOT' > /etc/csb/help.d/gui
gui — lightweight desktop over noVNC

Run a VNC desktop (Xvnc + openbox) reachable from your browser:

    gui-start      start the desktop + noVNC, prints the URL
    gui-stop       tear it down

The desktop listens on port 6080 inside the sandbox. To reach it from
the host the port must be PUBLISHED when you start csb — it is not
exposed automatically:

    csb --addon gui --publish 6080                 # dynamic host port
    csb --addon gui --publish 127.0.0.1:6080:6080  # fixed host port

gui-start prints the correct URL using the host port csb allocated
($CSB_PUBLISH_6080); without --publish it falls back to 6080.

Set the resolution/depth before gui-start via CSB_GUI_GEOMETRY (default
1280x800x24):

    CSB_GUI_GEOMETRY=1920x1080x24 gui-start
EOT
