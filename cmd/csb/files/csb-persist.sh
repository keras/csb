#!/bin/bash
set -euo pipefail

CSB_HOST_HOME=/mnt/csb-home

usage() {
    printf 'Usage: csb-persist <path>\n\n' >&2
    printf '  Move <path> to %s and symlink it back,\n' "$CSB_HOST_HOME" >&2
    printf '  making it persistent in ~/.config/csb/home/ on the host.\n' >&2
    exit 1
}

[ $# -ne 1 ] && usage

src=$(realpath -sm "$1")
name=$(basename "$src")
dst="$CSB_HOST_HOME/$name"

[ ! -d "$CSB_HOST_HOME" ] && { printf '%s is not mounted\n' "$CSB_HOST_HOME" >&2; exit 1; }
[ ! -e "$src" ] && { printf 'no such file or directory: %s\n' "$1" >&2; exit 1; }
[ "$(dirname "$src")" != "$HOME" ] && { printf 'path must be directly under $HOME: %s\n' "$src" >&2; exit 1; }

if [ -L "$src" ] && [ "$(readlink "$src")" = "$dst" ]; then
    printf '%s is already persisted\n' "$name" >&2
    exit 0
fi
[ -L "$src" ] && { printf '%s is already a symlink\n' "$src" >&2; exit 1; }
[ -e "$dst" ] && { printf '%s already exists in %s\n' "$name" "$CSB_HOST_HOME" >&2; exit 1; }

mv "$src" "$dst"
ln -s "$dst" "$src"
printf 'Persisted: %s -> %s\n' "$src" "$dst"
