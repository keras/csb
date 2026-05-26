#!/bin/bash

set -euo pipefail

apt-get install -y sudo

echo "sandbox ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/sandbox
chmod 0440 /etc/sudoers.d/sandbox
