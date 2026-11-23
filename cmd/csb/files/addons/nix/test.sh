#!/bin/bash
set -euo pipefail

command -v nix nix-daemon >/dev/null
test "$NIX_REMOTE" = daemon

# The daemon answers.
nix store info >/dev/null

# A local derivation builds through the daemon as a nixbld user (builtins
# only: a bare derivation has an empty PATH). No network needed.
out=$(nix-build --no-out-link -E '
  derivation {
    name = "csb-nix-test";
    system = builtins.currentSystem;
    builder = "/bin/sh";
    args = [ "-c" "echo ok > $out" ];
  }')
test "$(cat "$out")" = ok
