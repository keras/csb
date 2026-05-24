#!/bin/bash

_verbose_run() {
    t0=${EPOCHREALTIME/./}
    "$@"
    rc=$?
    if [ -n "${CSB_VERBOSE}" ]; then
        dt=$(( ${EPOCHREALTIME/./} - t0 ))
        printf '[csb][%s]: %d.%03ds\n' "$*" "$((dt / 1000000))" "$(( (dt % 1000000) / 1000 ))" >&2
    fi
    return $rc
}

HOST_UID="${HOST_UID:-0}"
HOST_GID="${HOST_GID:-0}"
_shell=$(command -v "${CSB_DEFAULT_SHELL:-bash}" 2>/dev/null)
_shell=${_shell:-"/bin/${CSB_DEFAULT_SHELL:-bash}"}
export SHELL="$_shell"
cp /etc/passwd /tmp/passwd
echo "sandbox:x:${HOST_UID}:${HOST_GID}::$CSB_HOME:$_shell" >> /tmp/passwd
echo "sandbox:x:${HOST_UID}:${HOST_GID}::$CSB_HOME:$_shell" >> /etc/passwd
# PAM account validation requires a shadow entry; ! = locked password (fine for NOPASSWD sudo)
echo "sandbox:!:19000:0:99999:7:::" >> /etc/shadow
export NSS_WRAPPER_PASSWD=/tmp/passwd
export NSS_WRAPPER_GROUP=/etc/group
export LD_PRELOAD=$(find /usr/lib -name 'libnss_wrapper.so' | head -1)
export HOME="$CSB_HOME"
export PATH="$HOME/.local/bin:$HOME/bin:$PATH"

# Fix ownership of home dir
chown "${HOST_UID}:${HOST_GID}" $HOME

# Symlink entries from /mnt/csb-home into $HOME.
# /mnt/csb-home is a bind mount of ~/.config/csb/home/ on the host;
# symlinking rather than bind-mounting lets csb-persist add new entries
# without a container restart.
if [ -d /mnt/csb-home ]; then
    _prev_opts=$(shopt -p dotglob nullglob)
    shopt -s dotglob nullglob
    for _entry in /mnt/csb-home/*; do
        _name=$(basename "$_entry")
        _target="$HOME/$_name"
        if [ -L "$_target" ] && [ "$(readlink "$_target")" = "$_entry" ]; then
            : # already correctly symlinked
        elif [ -e "$_target" ] || [ -L "$_target" ]; then
            printf '[csb] warning: %s exists in home volume and shadows ~/.config/csb/home/%s — delete the volume copy to use the host version\n' "$_name" "$_name" >&2
        else
            ln -s "$_entry" "$_target"
            chown -h "${HOST_UID}:${HOST_GID}" "$_target"
        fi
    done
    eval "$_prev_opts"
fi

if [ -n "${CSB_NESTED_PODMAN}" ]; then
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
fi

for script in /etc/csb/entrypoint.d/*.sh; do
    [ -x "$script" ] && source "$script"
done

# gosu rewrites HOME/USER/LOGNAME from the target uid's passwd entry. When
# HOST_UID matches the uid we're already running as, skip gosu entirely —
# there's no privilege to drop, and skipping avoids the env rewrite.
if [ "${HOST_UID}:${HOST_GID}" = "$(id -u):$(id -g)" ]; then
    exec "$@"
fi
exec gosu "${HOST_UID}:${HOST_GID}" "$@"
