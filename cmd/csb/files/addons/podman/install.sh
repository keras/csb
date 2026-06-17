#!/bin/bash
# csb:run-arg --device /dev/fuse
# csb:run-arg --device /dev/net/tun
# csb:run-arg --security-opt seccomp=unconfined
# csb:run-arg --security-opt apparmor=unconfined
# csb:run-arg --cap-add SYS_ADMIN
# csb:run-arg --cap-add NET_ADMIN

set -euo pipefail

apt-get install -y fuse-overlayfs podman uidmap

mkdir -p /etc/containers

cat > /etc/containers/policy.json <<'EOF'
{"default":[{"type":"insecureAcceptAnything"}]}
EOF

cat > /etc/containers/registries.conf <<'EOF'
[registries.search]
registries = ["docker.io"]
EOF

cat > /etc/containers/storage.conf <<'EOF'
[storage]
driver = "overlay"
runroot = "/run/containers/storage"
graphroot = "/var/lib/containers/storage"

[storage.options.overlay]
mount_program = "/usr/bin/fuse-overlayfs"
EOF

cat > /etc/containers/containers.conf <<'EOF'
[containers]
# Docker bind-mounts /proc/sys read-only so crun cannot set sysctls.
default_sysctls = []
# Sharing the outer PID namespace avoids crun needing to mount a new proc
# inside a nested user+mount namespace, which Docker prevents.
pidns = "host"
# slirp4netns sets accept_dad before the inner mount namespace is active,
# hitting the outer read-only /proc/sys; disabling IPv6 skips that sysctl.
network_cmd_options = ["enable_ipv6=false"]
EOF

cat > /etc/csb/entrypoint.d/podman.sh <<'EOF'
# Promote root mount to shared propagation so Podman can propagate mounts
# across its own mount namespaces (Docker defaults to private propagation).
mount --make-rshared /
# subuid/subgid ranges required by rootless Podman for uid mapping
echo "sandbox:100000:65536" >> /etc/subuid
echo "sandbox:100000:65536" >> /etc/subgid
# XDG_RUNTIME_DIR is required by rootless Podman for its socket
export XDG_RUNTIME_DIR="/run/user/${HOST_UID}"
mkdir -p "${XDG_RUNTIME_DIR}"
chmod 700 "${XDG_RUNTIME_DIR}"
chown "${HOST_UID}:${HOST_GID}" "${XDG_RUNTIME_DIR}"
# Make /proc/sys writable so nested container runtimes can configure
# network namespaces. Requires SYS_ADMIN + seccomp=unconfined.
mount -o remount,rw /proc/sys 2>/dev/null || true
EOF
chmod +x /etc/csb/entrypoint.d/podman.sh

cat <<'EOT' > /etc/csb/help.d/podman
podman — rootless containers inside the sandbox

  Run nested, rootless containers with podman:

    podman run --rm hello-world
    podman build -t myimage .

  Uses fuse-overlayfs storage under your home, so images and layers
  persist across csb runs. Requires the extra capabilities csb grants
  when this addon is enabled.
EOT
