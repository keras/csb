//go:build addons

// Black-box behavioral test for the podman addon's systemd-managed API
// service.
//
// The podman addon reuses Debian's packaged podman.socket/podman.service
// units rather than shipping its own (see the comment in
// cmd/csb/files/addons/podman/install.sh) and enables the socket via the same
// offline wants-symlink mechanism the systemd addon documents. That wiring is
// only load-bearing when the systemd addon is also enabled — test.sh (run by
// the generic TestAddons harness through the plain entrypoint-hook path)
// already covers the inert, no-systemd case: the packaged units exist and the
// wants-symlink points at the right one. This test is the other half: with
// systemd actually booted, does the API really answer on the rootful socket.
//
// Nested systemd DOES boot in this repo's own dev sandbox as long as the
// *rootful* podman addon is in play (the docker:dind --privileged recipe plus
// its cgroup v2 delegation) — verified 2026-07-25 via TestSystemd* and
// TestAddons/systemd. Only podman-rootless (no --privileged, no delegation)
// can't get systemd running (crun: mount proc: Operation not permitted). This
// test targets rootful podman, i.e. exactly the combination that works, so it
// is run for real rather than skipped.
//
// The packaged socket ships root:root, so this test checks both access
// paths the addon supports: through the sudo `podman` wrapper (always
// available) and directly against /usr/bin/podman with no sudo at all — the
// latter only works because podman/entrypoint.sh drops a per-boot
// socket-unit override chgrp-ing the socket to the sandbox user's group (see
// the comment there for why that doesn't hand out anything the sudoers rule
// doesn't already).
//
// This test's outer runtime matters: Runtime.HostIDs (internal/csb/runtime.go)
// maps the sandbox user to gid 0 whenever the outer runtime is *rootless*
// podman (its uid/gid remapping trick), and root can open a root:root socket
// regardless of whether the chgrp ran — so under a rootless outer runtime the
// no-sudo probe below would pass even if the entrypoint hook were completely
// broken. To avoid that false confidence, the test asserts the actual
// directory/socket ownership via `stat` (which fails honestly if the chgrp
// didn't happen) and only runs the no-sudo probe itself when the sandbox
// gid is non-zero; under gid 0 it skips that one probe with an explicit
// reason rather than silently reporting a pass that proves nothing.
package addons_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestPodmanSystemdAPIService(t *testing.T) {
	configDir := t.TempDir()
	t.Cleanup(func() { reapContainer(configDir) })

	// csb_launch forks this command *before* it execs systemd into PID 1 (see
	// systemd/launcher.sh), so for the first stretch of boot PID 1's comm is
	// still the entrypoint shell and /run/systemd/system doesn't exist yet —
	// systemd/test.sh hits the same window and polls through it rather than
	// racing it. Do the same here before trusting anything systemd-owned.
	//
	// Socket activation means podman.socket starts listening as soon as
	// sockets.target is reached, well before podman.service itself runs —
	// but a cold boot with both addons layered on (extra units, cgroup
	// delegation, mount --rshared) still takes real time, so poll rather
	// than assuming it's instant.
	//
	// The retry loop polls with `podman --remote` itself rather than
	// stat-ing the socket path directly: /run/podman is root-owned (root
	// systemd/podman.socket, root podman.service) and this command runs as
	// the unprivileged sandbox user, so a bare `[ -S ... ]` would report
	// false the whole time on EACCES, not just before the socket exists.
	// `podman --remote` goes through the sudo wrapper (/usr/local/bin/podman,
	// installed by this addon) the same way the rest of the addon is used,
	// and exercises the real REST client/server round trip (not just "the
	// file exists") — so a `version` with a populated Server section proves
	// the API actually answers.
	script := `set -euo pipefail
for i in $(seq 1 120); do
    [ "$(cat /proc/1/comm 2>/dev/null)" = systemd ] && [ -d /run/systemd/system ] && break
    sleep 0.5
done
test "$(cat /proc/1/comm)" = systemd
test -d /run/systemd/system

# The non-interactive command runs in parallel with the boot, often before
# the D-Bus system bus is even connectable (systemctl talks to systemd
# through it). Poll until systemctl gets *any* state answer — while the bus
# is unreachable it prints nothing — before asking it to wait; a bare
# is-system-running --wait issued too early returns empty immediately instead
# of actually waiting (systemd/test.sh hits this same trap).
for i in $(seq 1 120); do
    state=$(timeout 2 systemctl is-system-running 2>/dev/null) || true
    [ -n "$state" ] && break
    sleep 0.5
done

# Let the system settle; degraded is fine, some other addon's unit failing
# shouldn't fail this one.
state=$(timeout 90 systemctl is-system-running --wait) || true
case "$state" in
    running|degraded) ;;
    *) echo "unexpected system state: '$state'" >&2; exit 1 ;;
esac

out=""
for i in $(seq 1 120); do
    out=$(podman --remote --url unix:///run/podman/podman.sock version 2>&1) && break
    sleep 0.5
done
echo "--- via sudo wrapper ---"
echo "$out"
echo "$out" | grep -q '^Server:'

# Assert the entrypoint hook's chgrp actually landed, by observing the real
# filesystem state rather than grepping the override file's text (which would
# pass even if the unit never ran) or trusting a no-sudo probe blindly (which
# would pass under a rootless outer runtime regardless — see the package
# comment above). stat needs no special access to check this: reading an
# inode's own metadata only requires search permission on its *parent*
# directory, which /run itself already grants.
mygid=$(id -g)
dirstat=$(stat -c '%a %g' /run/podman)
sockgid=$(stat -c '%g' /run/podman/podman.sock)
echo "DIR_STAT=$dirstat SOCK_GID=$sockgid MYGID=$mygid"
[ "$dirstat" = "750 $mygid" ]
[ "$sockgid" = "$mygid" ]

# The entrypoint hook's socket-unit override (see podman/entrypoint.sh) grants
# the sandbox user's own group access to the socket, so the *real* binary,
# invoked directly (no sudo, no wrapper) must also reach it — this is what
# makes DOCKER_HOST/CONTAINER_HOST-style clients (docker-compose,
# testcontainers, ...) usable without going through the podman CLI wrapper.
# Skip this specific probe under gid 0 (rootless outer runtime): root opens a
# root:root socket regardless of the chgrp, so it can't tell the fix apart
# from ambient root — the stat checks above already cover that case honestly.
if [ "$mygid" = "0" ]; then
    echo "SKIP_NO_SUDO_PROBE=1 sandbox gid is 0 (rootless outer runtime), probe would prove nothing"
else
    echo "--- direct binary, no sudo ---"
    direct=$(/usr/bin/podman --url unix:///run/podman/podman.sock version 2>&1)
    echo "$direct"
    echo "$direct" | grep -q '^Server:'
fi

# The log driver and the events backend must stay a coherent pair. podman's
# default log driver is journald *whenever journald is reachable* — i.e.
# precisely when the systemd addon is enabled — while containers.conf pins
# events_logger to file for the no-systemd case. That mismatch is one podman
# rejects: following a container's logs dies with "using --follow with the
# journald --log-driver but without the journald --events-backend (file) is
# not supported", which breaks every API client that streams output back
# (the docker SDK's containers.run(), docker-compose up). containers.conf
# pins log_driver to compensate; assert the server actually reports the
# consistent pair, since only the systemd combination can regress it.
#
# Queried through the socket, not the CLI: the API service is a systemd unit
# and resolves its own defaults, and it was the service — not the CLI — that
# picked journald when log_driver was unset.
drv=$(/usr/bin/podman --url unix:///run/podman/podman.sock info --format '{{.Host.LogDriver}}/{{.Host.EventLogger}}' 2>&1)
echo "LOG_EVENT_PAIR=$drv"
[ "$drv" = "k8s-file/file" ]

# /var/run/docker.sock is opt-in ("podman docker-sock") and this run did not
# ask for it, so the path must stay clear — claiming it silently would make
# podman masquerade as the Docker daemon for every client in the container.
test ! -e /run/docker.sock
`
	cmd := exec.Command(csbBin,
		"--config-dir", configDir,
		"--addon", "systemd",
		"--addon", "podman",
		"--no-workspace",
		"--", "bash", "-c", script)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("podman API/socket-permission checks failed: %v\n--- output ---\n%s", err, out.String())
	}
	// Both the sudo-wrapper round trip and (when it ran) the no-sudo probe
	// print their own "Server:" line; there should be either one (gid 0,
	// wrapper only) or two (normal case) — either way at least one.
	if !strings.Contains(out.String(), "Server:") {
		t.Fatalf("no Server: section in podman version output — client reached the socket but got no server round trip\n--- output ---\n%s", out.String())
	}
	if strings.Contains(out.String(), "SKIP_NO_SUDO_PROBE=1") {
		t.Skip("sandbox gid is 0 (rootless outer runtime) — the no-sudo direct-socket probe can't distinguish the entrypoint fix from ambient root; stat-based ownership checks still ran and passed. Re-run under a rootful outer runtime (Docker, or rootful podman) to exercise the full probe.")
	}
}

// TestPodmanSystemdDockerSockOptIn covers the `podman docker-sock` argument,
// which additionally publishes the API at the Docker default path so clients
// given no configuration at all find it. The base test asserts the path stays
// clear without the argument; this one asserts the argument actually delivers
// it — the pair is what pins it as opt-in rather than incidental.
func TestPodmanSystemdDockerSockOptIn(t *testing.T) {
	configDir := t.TempDir()
	t.Cleanup(func() { reapContainer(configDir) })

	// The link must both point at the real socket and carry a live API behind
	// it: a dangling symlink would satisfy readlink alone, and that is exactly
	// the failure a client would hit as a connection refused.
	script := `set -euo pipefail
for i in $(seq 1 120); do
    [ "$(cat /proc/1/comm 2>/dev/null)" = systemd ] && [ -d /run/systemd/system ] && break
    sleep 0.5
done
for i in $(seq 1 120); do
    state=$(timeout 2 systemctl is-system-running 2>/dev/null) || true
    [ -n "$state" ] && break
    sleep 0.5
done
timeout 90 systemctl is-system-running --wait >/dev/null 2>&1 || true

for i in $(seq 1 120); do
    [ -e /run/docker.sock ] && break
    sleep 0.5
done
echo "DOCKER_SOCK_LINK=$(readlink /run/docker.sock)"
[ "$(readlink /run/docker.sock)" = /run/podman/podman.sock ]
/usr/bin/podman --url unix:///run/docker.sock version | grep -q '^Server:'
echo "DOCKER_SOCK_OK"
`
	cmd := exec.Command(csbBin,
		"--config-dir", configDir,
		"--addon", "systemd",
		"--addon", "podman docker-sock",
		"--no-workspace",
		"--", "bash", "-c", script)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker-sock opt-in failed: %v\n--- output ---\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "DOCKER_SOCK_OK") {
		t.Fatalf("docker-sock checks did not complete\n--- output ---\n%s", out.String())
	}
}
