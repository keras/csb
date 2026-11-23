# Sourced as root by entrypoint.sh, before the drop to the sandbox user.

. /etc/csb/nix-env.sh

# The store volume may predate this image (or this addon version), so make
# sure the directories the daemon and clients expect are there.
install -d -m 0755 /nix/var/nix/daemon-socket
install -d -m 1777 /nix/var/nix/profiles/per-user /nix/var/nix/gcroots/per-user

# Start the daemon here even under the systemd addon: an explicit `csb -- cmd`
# starts while systemd is still booting, so a socket-activated unit could not
# be relied on to be listening yet. Waiting for the socket below guarantees it
# is, in both modes.
#
# It gets a clean environment: this shell carries the sandbox user's HOME and
# csb's nss_wrapper LD_PRELOAD, neither of which belongs in a root daemon. The
# socket left by the previous container lives on the persistent volume, so
# remove it first — otherwise the wait below would see it at once and clients
# would get "connection refused" until the new daemon is up.
_nix_sock=/nix/var/nix/daemon-socket/socket
rm -f "$_nix_sock"
env -i HOME=/root PATH=/usr/sbin:/usr/bin:/sbin:/bin \
    nohup /usr/bin/nix-daemon --daemon >/var/log/nix-daemon.log 2>&1 &
for _ in $(seq 1 100); do
    [ -S "$_nix_sock" ] && break
    sleep 0.05
done
[ -S "$_nix_sock" ] || echo "csb: nix-daemon did not start; see /var/log/nix-daemon.log" >&2
unset _nix_sock
