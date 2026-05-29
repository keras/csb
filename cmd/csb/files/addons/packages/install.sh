#!/bin/bash

set -euo pipefail

if [ "$#" -eq 0 ]; then
    exit 0
fi

apt-get install -y --no-install-recommends "$@"
