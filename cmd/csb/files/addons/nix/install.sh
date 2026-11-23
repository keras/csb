#!/bin/bash
# Multi-user Nix: Debian's nix-bin with a root nix-daemon that the sandbox user
# (whatever uid it gets at container start) talks to over its socket.
#
# The store is on a named volume so installs survive container restarts. Docker
# and Podman seed an empty named volume from the image's /nix on first use; it
# is shared by every csb sandbox that enables this addon. Nix itself lives in
# /usr/bin (from the package, not the store), so rebuilding the image still
# upgrades it even though the volume hides the image's /nix.
# csb:run-arg -v csb-nix:/nix

set -euo pipefail

# Only nix-bin: nix-setup-systemd depends on libpam-systemd, which would pull
# systemd and systemd-sysv into every image. Its setup (build users, dirs,
# nix.conf; a daemon start in place of its units) is reproduced here in a
# container-friendly form.
apt-get install -y --no-install-recommends nix-bin ca-certificates

# Build users: the daemon runs builds as these unprivileged uids.
groupadd --force --system nixbld
for n in $(seq 1 10); do
    id "nixbld$n" >/dev/null 2>&1 || useradd --system \
        --comment "Nix build user $n" --home-dir /var/empty \
        --gid nixbld --groups nixbld --no-user-group \
        --shell /usr/sbin/nologin "nixbld$n"
done

install -D -m 0644 nix.conf   /etc/nix/nix.conf
install -D -m 0644 nix-env.sh /etc/csb/nix-env.sh
install -D -m 0644 help       /etc/csb/help.d/nix
printf '. /etc/csb/nix-env.sh\n' > /etc/profile.d/nix.sh

# The /nix skeleton. Created again on every start by the entrypoint hook, since
# an already-populated volume hides whatever the image has here.
install -d -m 0755 /nix/var/nix/daemon-socket
install -d -m 1777 /nix/var/nix/profiles/per-user /nix/var/nix/gcroots/per-user

# The daemon is started by the entrypoint hook, with or without the systemd
# addon (see entrypoint.sh for why not a socket-activated unit).
install -D -m 0755 entrypoint.sh /etc/csb/entrypoint.d/nix.sh
