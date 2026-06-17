#!/bin/bash

set -euo pipefail

apt-get install -y --no-install-recommends ca-certificates curl

curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh

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
