#!/bin/bash
set -euo pipefail

command -v mise >/dev/null
mise --version >/dev/null
test -n "${MISE_DATA_DIR:-}"
