#!/bin/bash
set -euo pipefail

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
