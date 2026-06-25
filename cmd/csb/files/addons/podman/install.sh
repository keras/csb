#!/bin/bash
# Rootful podman nested in an unprivileged container can't get cgroup v2
# controller delegation, mount a private /proc, or use fuse-overlayfs (its
# rootfs fails to exec on some kernels). Running the sandbox as a privileged
# container (the docker:dind recipe) clears all three. --privileged already
# implies the caps, devices, and seccomp/apparmor unconfined that nested podman
# needs, so it is the only run-arg required here.
# csb:run-arg --privileged

set -euo pipefail

apt-get install -y fuse-overlayfs podman uidmap sudo netavark aardvark-dns iptables

# Common policy + registry config. The bundled resources live next to this
# install.sh; install -D creates parent dirs as needed.
install -D -m 0644 policy.json     /etc/containers/policy.json
install -D -m 0644 registries.conf /etc/containers/registries.conf
install -D -m 0644 storage.conf    /etc/containers/storage.conf
install -D -m 0644 containers.conf /etc/containers/containers.conf
install -D -m 0755 entrypoint.sh   /etc/csb/entrypoint.d/podman.sh
install -D -m 0644 help            /etc/csb/help.d/podman

# The sandbox shell is non-root; reach a root podman through sudo. A wrapper
# makes the bare `podman` command elevate transparently (a bare invocation would
# otherwise silently fall back to the rootless code path).
install -D -m 0440 sudoers /etc/sudoers.d/podman
install -D -m 0755 podman  /usr/local/bin/podman
