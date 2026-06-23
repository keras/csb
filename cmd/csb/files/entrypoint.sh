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

# _apply_entrypoint_d sources the run-time addon hooks (env exports, mounts).
_apply_entrypoint_d() {
    for script in /etc/csb/entrypoint.d/*.sh; do
        [ -x "$script" ] && source "$script"
    done
}

# _run_user runs the user command as the sandbox uid. With "exec" it replaces
# the current process (the default, single-process path); with "noexec" it runs
# the command as a child and returns its exit code so the caller can act on it.
#
# gosu rewrites HOME/USER/LOGNAME from the target uid's passwd entry. When
# HOST_UID matches the uid we're already running as, skip gosu entirely —
# there's no privilege to drop, and skipping avoids the env rewrite.
_run_user() {
    local mode=$1; shift
    local pre=()
    if [ "${HOST_UID}:${HOST_GID}" != "$(id -u):$(id -g)" ]; then
        pre=(gosu "${HOST_UID}:${HOST_GID}")
    fi
    if [ "$mode" = exec ]; then
        exec "${pre[@]}" "$@"
    fi
    "${pre[@]}" "$@"
}

# csb_launch starts the workload. The default replaces this process with the
# user command (the single-process sandbox). An addon that provides an init
# system overrides csb_launch from an entrypoint.d hook to run that init as
# PID 1 instead; it can build on _run_user. There should be at most one init
# system, so a launcher registers itself with csb_register_launcher, which
# fails fast if two addons try to take over.
csb_launch() { _run_user exec "$@"; }

csb_register_launcher() {
    if [ -n "${_csb_launcher:-}" ]; then
        echo "csb: error: addons '$_csb_launcher' and '$1' both provide an init system; enable only one" >&2
        exit 1
    fi
    _csb_launcher=$1
}

# _run_user and friends are referenced by launcher overrides that fork a child
# shell; export them so that child inherits them.
export -f _apply_entrypoint_d _run_user
export HOST_UID HOST_GID

_apply_entrypoint_d   # a launcher-providing hook overrides csb_launch here
csb_launch "$@"
