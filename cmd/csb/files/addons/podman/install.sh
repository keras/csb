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

# When the systemd addon is also enabled, start the Podman API service (the
# REST/Docker-compatible API used by `podman --remote`, docker-compose
# clients, etc.) on the rootful socket. The Debian package already ships
# podman.service + podman.socket (system scope, listening on
# /run/podman/podman.sock, backed by the same root podman state the `podman`
# wrapper above reaches — the wrapper itself talks to no socket, it's a local
# `sudo /usr/bin/podman` invocation), so there is nothing to author: enable
# the packaged units rather than duplicating them. Socket-activation is the
# idiomatic form (and what the package's own [Install] targets) — only
# podman.socket needs enabling; systemd starts podman.service on first
# connection via the matching unit name, so no separate wants-symlink for the
# .service is needed. Same offline `systemctl enable` equivalent the systemd
# addon uses for its own units: a wants-symlink dropped at build time,
# resolved by systemd at boot regardless of addon install order. Without the
# systemd addon, /etc/systemd/system is just inert files on disk — harmless,
# and nothing here touches the entrypoint hook path used standalone.
mkdir -p /etc/systemd/system/sockets.target.wants
ln -sf /usr/lib/systemd/system/podman.socket /etc/systemd/system/sockets.target.wants/podman.socket

# Opt-in: `--addon "podman docker-sock"` also publishes the API at the Docker
# default path (/var/run/docker.sock), so Docker-API clients work with no
# configuration at all. Off by default because that path is a container-wide
# claim to *be* the Docker daemon — see the comment in docker-sock.conf. The
# drop-in needs no runtime values, so unlike the ownership drop-in (written per
# boot by entrypoint.sh, which can't see these args) it is a static file placed
# here at build time.
for _arg in "$@"; do
    case "$_arg" in
        docker-sock)
            install -D -m 0644 docker-sock.conf \
                /etc/systemd/system/podman.socket.d/csb-docker-sock.conf
            ;;
        *)
            echo "podman addon: unknown argument '$_arg' (expected: docker-sock)" >&2
            exit 1
            ;;
    esac
done
