#!/bin/bash
# csb-help — orientation box and topic docs for the csb sandbox.
#
#   csb-help            print the welcome box and the list of help topics.
#                       (This is what the MOTD shows on interactive login.)
#   csb-help <topic>    print the doc for <topic>. Core topics are built in;
#                       addons drop extra docs into /etc/csb/help.d/<name>.
set -euo pipefail

HELP_DIR=/etc/csb/help.d
ADDONS_FILE=/etc/csb/addons

# Styling only when writing to a terminal — keep box content itself plain
# ASCII so the width arithmetic in _box stays correct.
if [ -t 1 ]; then
    _accent=$'\e[36m'; _bold=$'\e[1m'; _rst=$'\e[0m'
else
    _accent=''; _bold=''; _rst=''
fi

# _join SEP — join stdin lines with SEP (handles multibyte separators, which
# `paste -d` does not).
_join() {
    local sep=$1 out='' line
    while IFS= read -r line; do
        [ -n "$out" ] && out+="$sep"
        out+="$line"
    done
    printf '%s' "$out"
}

# Built-in topics describing core sandbox features (not addon-specific).
_core_topics() { printf '%s\n' persistence addons host; }

# _addon_docs — names of addon-supplied docs in /etc/csb/help.d.
_addon_docs() {
    [ -d "$HELP_DIR" ] && find "$HELP_DIR" -maxdepth 1 -type f -printf '%f\n' 2>/dev/null
}

# _topics — core topics first, then addon-supplied docs, deduplicated.
_topics() { { _core_topics; _addon_docs; } | awk '!seen[$0]++'; }

# _box TITLE LINE... — draw a rounded box with TITLE set into the top border
# and sized to its widest line. Content lines must be plain ASCII (no escapes)
# so ${#line} reflects display width.
_box() {
    local title=$1; shift
    local lines=("$@") w=0 l
    for l in "${lines[@]}"; do (( ${#l} > w )) && w=${#l}; done
    # widen if needed so "─ TITLE " fits the top border (inner span is w+2)
    (( ${#title} + 1 > w )) && w=$(( ${#title} + 1 ))
    local bar tdashes
    bar=$(printf '─%.0s' $(seq 1 $((w + 2))))
    tdashes=$(printf '─%.0s' $(seq 1 $((w - ${#title} - 1))))
    printf '%s╭─ %s%s%s %s╮%s\n' "$_accent" "$_bold" "$title" "$_accent" "$tdashes" "$_rst"
    for l in "${lines[@]}"; do
        printf '%s│%s %s%*s %s│%s\n' \
            "$_accent" "$_rst" "$l" "$((w - ${#l}))" '' "$_accent" "$_rst"
    done
    printf '%s╰%s╯%s\n' "$_accent" "$bar" "$_rst"
}

_row() { printf '%-9s %s' "$1" "$2"; }

# _ws_mount — the bind-mount target under /workspace, or empty if none. The
# host CWD is mounted at a subdirectory mirroring its host path
# (e.g. /workspace/dev/my/csb), not at /workspace itself.
_ws_mount() { awk '$2 ~ /^\/workspace(\/|$)/ {print $2; exit}' /proc/mounts 2>/dev/null; }

_render_box() {
    local workspace addons topics ws_mount
    ws_mount=$(_ws_mount)
    if [ -n "$ws_mount" ]; then
        workspace="$ws_mount — live mount, edits hit the host"
    else
        workspace='/workspace — ephemeral, nothing reaches the host'
    fi
    if [ -s "$ADDONS_FILE" ]; then
        addons=$(_join ' · ' < "$ADDONS_FILE")
    else
        addons='(none)'
    fi
    topics=$(_topics | _join ', ')
    _box "csb sandbox" \
        "$(_row workspace "$workspace")" \
        "$(_row home "persists across runs · csb-help persistence")" \
        "$(_row addons "$addons")" \
        "$(_row help "csb-help <topic>  ($topics)")"
}

_topic_persistence() {
    cat <<EOF
${_bold}persistence — what sticks around${_rst}

  /workspace/...
                    Your host working directory, bind-mounted in under
                    /workspace (mirroring its path on the host). Edits here
                    change the real files immediately.

  ${HOME:-\$HOME}
                    Backed by a named volume (csb-home). Anything you
                    install or write here survives across csb runs, but
                    lives only inside the sandbox — it is not on the host.

  ~/.config/csb/home/   (on the host)
                    Files placed here are symlinked into your sandbox home
                    on every run. Use it for dotfiles shared between host
                    and sandbox.

Promote a sandbox home entry to that shared host overlay, no restart:

    csb-persist ~/.somedir

Everything else under your home that is not backed by the overlay is
sandbox-only: kept by the volume between runs, invisible to the host.
EOF
}

_topic_addons() {
    local enabled docs
    if [ -s "$ADDONS_FILE" ]; then enabled=$(_join ', ' < "$ADDONS_FILE"); else enabled='(none)'; fi
    docs=$(_addon_docs | _join ', ')
    cat <<EOF
${_bold}addons — optional tools baked into this sandbox${_rst}

Enabled here:  $enabled

Addons are chosen on the host (the 'addons:' list in config.yaml, or
--addon NAME) and baked into the image at build time — you cannot add
them from inside a running sandbox. Each addon can ship its own help:

    csb-help <addon>          e.g. csb-help gui

Addon docs available here: ${docs:-(none)}
EOF
}

_topic_host() {
    local status
    if [ -n "${CSB_HOST_EXEC_URL:-}" ]; then
        status='enabled in this session'
    else
        status='NOT enabled in this session'
    fi
    cat <<EOF
${_bold}host bridge — run allowlisted host commands from the sandbox${_rst}

Status:  $status

csb-host-run invokes a fixed allowlist of commands on the host, passing
stdin/stdout/stderr and the exit code straight through:

    csb-host-run make run
    echo hi | csb-host-run ./cmd

By default a PTY is allocated on the host only when both stdin and
stdout are terminals — so piping either side stays byte-exact (no
\n -> \r\n translation). Override with -t (force a PTY, e.g. for
colored output through a pipe) or -T (never allocate one):

    csb-host-run -t some-rich-cli | tee log
    csb-host-run -T make run

The host command runs in the directory matching your current one: a
sandbox cwd of \$CSB_WORKSPACE_DIR/cmd/csb runs in <workspace>/cmd/csb
on the host. Outside the workspace (e.g. \$HOME) the host side falls
back to the directory csb was started from. Pick another directory with
-C, or turn the mirroring off with --no-cwd:

    csb-host-run -C internal/broker go test .
    csb-host-run --no-cwd make run

-C only accepts directories inside the workspace; the host re-checks
this and refuses anything resolving outside it (exit 125).

The host enforces the allowlist and scrubs the environment before
spawning anything — env vars from the sandbox are not forwarded.

Enabling is a host-side decision (it cannot be changed from inside the
sandbox), e.g.:

    csb --host-exec --host-exec-allow "make run" --host-exec-allow "git **"

Exit codes: 125 = cwd outside the workspace, 126 = not allowlisted,
127 = not found on host, otherwise the command's own exit code.
EOF
}

_show_topic() {
    case $1 in
        persistence) _topic_persistence; return ;;
        addons)      _topic_addons; return ;;
        host)        _topic_host; return ;;
    esac
    if [ -f "$HELP_DIR/$1" ]; then
        cat "$HELP_DIR/$1"
        return
    fi
    printf 'csb-help: no such topic: %s\n\n' "$1" >&2
    printf 'Topics: %s\n' "$(_topics | _join ', ')" >&2
    return 1
}

case ${1:-} in
    ''|--motd|-h|--help) _render_box ;;
    *) _show_topic "$1" ;;
esac
