#!/bin/bash
# csb:run-arg --device /dev/fuse
# csb:run-arg --device /dev/net/tun
# csb:run-arg --security-opt seccomp=unconfined
# csb:run-arg --security-opt apparmor=unconfined
# csb:run-arg --cap-add SYS_ADMIN
# csb:run-arg --cap-add NET_ADMIN

set -euo pipefail

apt-get install -y fuse-overlayfs podman uidmap

# The bundled resources live next to this install.sh; install -D creates parent
# dirs as needed.
install -D -m 0644 policy.json     /etc/containers/policy.json
install -D -m 0644 registries.conf /etc/containers/registries.conf
install -D -m 0644 storage.conf    /etc/containers/storage.conf
install -D -m 0644 containers.conf /etc/containers/containers.conf
install -D -m 0755 entrypoint.sh   /etc/csb/entrypoint.d/podman.sh
install -D -m 0644 help            /etc/csb/help.d/podman
