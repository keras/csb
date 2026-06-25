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
