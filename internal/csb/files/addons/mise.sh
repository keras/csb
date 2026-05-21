#!/bin/bash

set -euo pipefail

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
