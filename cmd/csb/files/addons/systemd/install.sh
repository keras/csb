#!/bin/bash
# systemd boot-mode addon.
#
# Installs systemd and registers a launcher hook so systemd runs as PID 1 (the
# entrypoint stays init-agnostic; see the csb_launch seam in entrypoint.sh).
# With systemd as init, heavier addons (full desktop environments, daemons that
# ship their own unit files) can register real, supervised services instead of
# being hand-backgrounded from an entrypoint hook. Such addons just drop unit files
# under /etc/systemd/system and `systemctl enable` them at build time — boot
# order is then resolved by systemd, independent of addon install order.
#
# The run-args below are what systemd needs to boot inside a container. They are
# tuned for a cgroup v2 host with a reasonably modern Docker/Podman; sharing the
# host cgroup namespace + a writable cgroup mount is the broadly-compatible
# recipe. Some hosts may still require --privileged — see help.d/systemd.
# csb:run-arg --cgroupns=host
# csb:run-arg -v /sys/fs/cgroup:/sys/fs/cgroup:rw
# csb:run-arg --tmpfs /run
# csb:run-arg --tmpfs /run/lock
# csb:run-arg --stop-signal SIGRTMIN+3

set -euo pipefail

# systemd-sysv provides /sbin/init and the shutdown/poweroff helpers; dbus is
# needed by logind and anything desktop-flavoured layered on top.
apt-get install -y --no-install-recommends systemd systemd-sysv dbus

# Mask units that are pointless or actively harmful in a container: the console
# and serial gettys would fight the user session for the pty; the udev/modules
# units poke at hardware that isn't there; and tmp.mount would shadow the
# container's existing /tmp with a fresh tmpfs (hiding what csb writes there, and
# logging a "Directory /tmp to mount over is not empty" warning to the console).
# Masking via symlink-to-/dev/null works without a running systemd, so it is
# safe here at build time.
for unit in \
    getty.target \
    getty@tty1.service \
    console-getty.service \
    serial-getty@ttyS0.service \
    systemd-udevd.service \
    systemd-udev-trigger.service \
    systemd-modules-load.service \
    systemd-firstboot.service \
    tmp.mount \
    ; do
    ln -sf /dev/null "/etc/systemd/system/$unit"
done

# Place the bundled config, launcher hook, and help text. They live in their own
# files next to this install.sh; install -D creates parent dirs as needed.
#
# csb-container.conf tunes systemd for headless container use (quiet console,
# logs to the journal, bounded job timeouts). launcher.sh overrides csb_launch
# so systemd runs as PID 1 — the entrypoint stays init-agnostic and just sources
# this hook from entrypoint.d (0755 so it is sourced).
install -D -m 0644 csb-container.conf /etc/systemd/system.conf.d/csb-container.conf
install -D -m 0755 launcher.sh        /etc/csb/entrypoint.d/systemd.sh
install -D -m 0644 help               /etc/csb/help.d/systemd
