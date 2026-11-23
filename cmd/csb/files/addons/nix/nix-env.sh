# Nix client environment, for login shells (/etc/profile.d/nix.sh) and for
# `csb -- cmd` (the entrypoint hook). NIX_REMOTE=daemon makes even a root
# client (rootless podman maps the sandbox user to uid 0) go through the
# daemon instead of opening the store directly.
export NIX_REMOTE=daemon
export NIX_SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
case ":$PATH:" in
    *":/nix/var/nix/profiles/default/bin:"*) ;;
    *) export PATH="$HOME/.nix-profile/bin:/nix/var/nix/profiles/default/bin:$PATH" ;;
esac
