#!/bin/bash
set -euo pipefail

# Verify the rootful podman wiring is in place.
command -v podman fuse-overlayfs >/dev/null
test -f /etc/containers/storage.conf
test -f /etc/containers/registries.conf

# `podman` is the sudo wrapper; the real binary lives at /usr/bin/podman.
test -x /usr/local/bin/podman
test -f /etc/sudoers.d/podman
# Rootful uses native kernel overlay (no mount_program directive, so podman as
# root uses the kernel driver) with the graphroot on the persistent home volume.
grep -q storage-rootful /etc/containers/storage.conf
! grep -qE '^[[:space:]]*mount_program' /etc/containers/storage.conf

# Rootful shares the outer PID namespace to avoid a nested /proc mount.
grep -q 'pidns = "host"' /etc/containers/containers.conf

# The wrapper elevates via sudo; confirm it reaches a real root podman.
test "$(podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null)" = "false"

# API service: enabled via a wants-symlink to the packaged units (see
# install.sh) so systemd picks it up when the systemd addon is also present.
# Assert what holds regardless — this test runs the plain entrypoint hook
# path (no systemd, so nothing here actually starts); the behavioral check
# that the API answers over the socket lives in the Go addons_test suite,
# which boots systemd + podman together.
test -f /usr/lib/systemd/system/podman.socket
test -f /usr/lib/systemd/system/podman.service
test -L /etc/systemd/system/sockets.target.wants/podman.socket
[ "$(readlink /etc/systemd/system/sockets.target.wants/podman.socket)" = /usr/lib/systemd/system/podman.socket ]
# The packaged socket must still be the standard rootful path (the same root
# podman state the sudo wrapper reaches, just via the REST API instead of a
# local CLI invocation), or enabling it would expose a second, divergent API.
grep -q 'ListenStream=%t/podman/podman.sock' /usr/lib/systemd/system/podman.socket

# The entrypoint hook (runs regardless of systemd, see entrypoint.sh) drops a
# per-boot socket-unit override so the sandbox user's own group can reach the
# API socket directly, without going through the sudo wrapper. The override
# file existing is as far as the static/no-systemd path can check — whether
# it actually landed the right gid and made the socket traversable is a
# runtime behavior of a unit systemd never starts here, so that part is
# asserted from the Go addons_test suite (which boots systemd for real) via
# `stat`, not by grepping this file's text; see podman_systemd_test.go.
test -f /etc/systemd/system/podman.socket.d/csb-sandbox-access.conf

# The docker-sock drop-in is opt-in (`--addon "podman docker-sock"`); the
# harness installs this addon bare, so it must not be present. Guards against
# the opt-in silently becoming the default.
test ! -e /etc/systemd/system/podman.socket.d/csb-docker-sock.conf

# The API service and its no-sudo access path are user-facing behavior;
# help.d/podman must actually document them.
test -f /etc/csb/help.d/podman
grep -q 'CONTAINER_HOST' /etc/csb/help.d/podman
grep -q 'podman.sock' /etc/csb/help.d/podman
