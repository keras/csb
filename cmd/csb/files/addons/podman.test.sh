#!/bin/bash
set -euo pipefail

command -v podman fuse-overlayfs >/dev/null
test -f /etc/containers/storage.conf
test -f /etc/containers/registries.conf
podman --version >/dev/null
