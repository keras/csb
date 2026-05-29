#!/bin/bash
set -euo pipefail

command -v Xvnc openbox websockify gui-start gui-stop curl pgrep >/dev/null

gui-start

ok=0
for _ in $(seq 1 30); do
    if curl -sf http://localhost:6080/vnc.html | grep -qi novnc; then
        ok=1
        break
    fi
    sleep 1
done

if [ "$ok" != "1" ]; then
    echo "=== Xvnc.log ==="; cat "$HOME/.vnc/Xvnc.log" 2>/dev/null || echo "(missing)"
    echo "=== openbox.log ==="; cat "$HOME/.vnc/openbox.log" 2>/dev/null || echo "(missing)"
    echo "=== novnc.log ==="; cat "$HOME/.vnc/novnc.log" 2>/dev/null || echo "(missing)"
    echo "=== processes ==="
    for f in "$HOME"/.vnc/*.pid; do
        [ -f "$f" ] || continue
        pid=$(cat "$f")
        echo "$f -> pid=$pid alive=$([ -d "/proc/$pid" ] && echo yes || echo no)"
    done
    exit 1
fi

pgrep -x Xvnc >/dev/null
gui-stop
