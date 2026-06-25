#!/bin/bash
set -euo pipefail

# Verify the rootless podman wiring is in place.
command -v podman fuse-overlayfs >/dev/null
test -f /etc/containers/storage.conf
test -f /etc/containers/registries.conf

# Rootless uses fuse-overlayfs storage (no privileged container, no sudo wrapper).
grep -q fuse-overlayfs /etc/containers/storage.conf
test ! -e /usr/local/bin/podman
test ! -e /etc/sudoers.d/podman

podman --version >/dev/null
