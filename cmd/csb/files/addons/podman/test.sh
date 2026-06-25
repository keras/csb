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
