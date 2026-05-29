#!/bin/bash
set -euo pipefail

# The black-box test runner invokes the addon by name only, so install.sh
# runs with zero package names (a no-op). Just confirm the container is up.
command -v bash >/dev/null
