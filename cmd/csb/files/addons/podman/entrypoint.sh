# Promote root mount to shared propagation so Podman can propagate mounts
# across its own mount namespaces (Docker defaults to private propagation).
mount --make-rshared / 2>/dev/null || true

# cgroup v2 delegation (docker:dind recipe). Even a privileged container starts
# with the controllers listed in cgroup.controllers but NOT enabled in the
# root's cgroup.subtree_control, so podman can't enable them for the cgroups it
# creates (crun fails: "controller pids is not available"). cgroup v2 forbids
# enabling subtree_control on a cgroup that has processes directly in it, so
# first move our own processes into a child ("init") cgroup, then push every
# available controller into the root's subtree_control for children to inherit.
if [ -e /sys/fs/cgroup/cgroup.controllers ] && [ -w /sys/fs/cgroup/cgroup.subtree_control ]; then
    mkdir -p /sys/fs/cgroup/init
    while read -r _pid; do
        echo "$_pid" > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null || true
    done < /sys/fs/cgroup/cgroup.procs
    for _ctrl in $(cat /sys/fs/cgroup/cgroup.controllers); do
        echo "+$_ctrl" > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
    done
fi

# Make /proc/sys writable so netavark can configure network namespaces.
mount -o remount,rw /proc/sys 2>/dev/null || true

# When the systemd addon is also enabled, let the sandbox user reach the
# packaged podman.socket API directly (unix:///run/podman/podman.sock), not
# only through the sudo `podman` wrapper. Debian's unit ships SocketMode=0660
# on the socket itself, but /run/podman is created root:root 0700 by
# /usr/lib/tmpfiles.d/podman.conf ("D! /run/podman 0700 root root") — not by
# the socket unit, whose own default parent-dir mode (DirectoryMode=) is
# 0755. Whichever created it, a client can't even traverse into %t/podman to
# reach the socket, so a bare `DOCKER_HOST=unix://...` client
# (docker-compose, testcontainers, ...) gets EACCES even though the sudo
# wrapper works fine. Granting the sandbox user's own uid's *group* direct
# access doesn't hand that user anything the sudoers rule (this addon's
# /etc/sudoers.d/podman) doesn't already: that rule is NOPASSWD for the same
# root /usr/bin/podman, so the sandbox user is already one `sudo` away from
# everything the socket exposes. (Sudo authorizes by username, the socket by
# gid, so in principle a *different* uid sharing HOST_GID would gain the
# socket without gaining sudo — nothing in this container is in that shape
# today, and HOST_GID landing on a packaged system group, e.g. `dialout`
# here, is harmless since those groups ship with no members.)
#
# `SocketGroup=` doesn't help here: it sets ownership of the socket inode,
# not the directory's, so it wouldn't fix the actual blocker (%t/podman
# itself being untraversable) and chgrp on the directory would still be
# needed regardless. So a single ExecStartPost= mechanism handles both,
# rather than splitting across a static directive and a script. It has to be
# a *runtime*-written override either way, since HOST_GID itself is only
# known at container start (the entrypoint appends it to /etc/passwd right
# before this hook runs) — a build-time drop-in could not name the right
# group. ExecStartPost= runs after the listening socket is created and
# bound, so both paths are guaranteed to exist by the time it fires. This
# file is written on every container start (the GID can differ run to run)
# and, like the rest of this addon's systemd wiring, is inert when the
# systemd addon isn't installed.
mkdir -p /etc/systemd/system/podman.socket.d
cat > /etc/systemd/system/podman.socket.d/csb-sandbox-access.conf <<EOF
[Socket]
ExecStartPost=-/bin/chmod 0750 %t/podman
ExecStartPost=/bin/chgrp $HOST_GID %t/podman
ExecStartPost=/bin/chgrp $HOST_GID %t/podman/podman.sock
EOF
