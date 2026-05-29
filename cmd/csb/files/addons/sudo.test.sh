#!/bin/bash
set -euo pipefail

command -v sudo >/dev/null
sudo -n true
test "$(sudo -n id -u)" = "0"
