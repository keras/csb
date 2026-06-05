#!/bin/bash

set -euo pipefail

apt-get install -y sudo

echo "sandbox ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/sandbox
chmod 0440 /etc/sudoers.d/sandbox

cat <<'EOT' > /etc/csb/help.d/sudo
sudo — passwordless root inside the sandbox

  The sandbox user may run any command as root with no password:

    sudo apt-get install -y <pkg>

  Note: packages installed this way land on the container's root
  filesystem, which is NOT persistent — they vanish on the next run.
  For tools you want to keep, prefer mise or a packages addon entry.
EOT
