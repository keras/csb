#!/bin/bash

set -euo pipefail

apt-get install -y --no-install-recommends ca-certificates curl

# The binary is kept in the build's download cache ($CSB_CACHE_DIR) and the
# installer is pointed at it: when the cached mise is already at (or past) the
# release the installer selects, it keeps it and skips the download; otherwise
# it fetches and checksum-verifies the new release in place. The installer
# itself is always fetched fresh, so new mise releases are still picked up.
# Keyed by architecture because the cache mount is shared across platforms.
if [ -n "${CSB_CACHE_DIR:-}" ]; then
    _cache="$CSB_CACHE_DIR/mise"
else
    _cache=$(mktemp -d)
    trap 'rm -rf "$_cache"' EXIT
fi
_bin="$_cache/mise-$(dpkg --print-architecture)"
mkdir -p "$_cache"
# A cached binary that can't report its version would make the installer abort
# instead of replacing it.
if [ -e "$_bin" ] && ! "$_bin" version >/dev/null 2>&1; then
    rm -f "$_bin"
fi
curl -fsSL https://mise.run | MISE_INSTALL_PATH="$_bin" sh
install -m 0755 "$_bin" /usr/local/bin/mise

printf '\neval "$(mise activate bash)"\n' >> /etc/bash.bashrc


cat <<'EOT' > /etc/csb/entrypoint.d/mise.sh
export MISE_DATA_DIR=$HOME/.local/share/mise
export MISE_STATE_DIR=$HOME/.local/state/mise
export MISE_CACHE_DIR=$HOME/.cache/mise
export PATH="$HOME/.local/share/mise/shims:$PATH"
export MISE_YES=1
EOT
chmod +x /etc/csb/entrypoint.d/mise.sh

cat <<'EOT' > /etc/profile.d/mise.sh
source /etc/csb/entrypoint.d/mise.sh
EOT

cat <<'EOT' > /etc/csb/help.d/mise
mise — runtime/tool version manager

  mise is activated in every shell. Use it to install language runtimes
  and CLI tools into your (persistent) sandbox home:

    mise use node@22       install + pin Node 22 for this directory
    mise use -g python@3.12 install Python 3.12 globally
    mise ls                list installed tools

  Installs live under ~/.local/share/mise, so they survive across runs.
EOT
