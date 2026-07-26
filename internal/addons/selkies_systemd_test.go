//go:build addons

// Black-box regression test for the selkies + systemd addon combination.
//
// Booting systemd (the systemd addon) gives the sandbox user a real logind
// session with a `systemd --user` manager, which socket-activates the distro's
// stock PulseAudio. That daemon owns the well-known D-Bus name org.PulseAudio1
// on the user session bus (DBUS_SESSION_BUS_ADDRESS=/run/user/<uid>/bus).
//
// selkies-start runs its OWN PulseAudio (private XDG_RUNTIME_DIR + anonymous
// socket) to publish the csb-audio null sink. Even with a private runtime dir
// that daemon still connects to the inherited user bus (found via
// $XDG_RUNTIME_DIR/bus even when DBUS_SESSION_BUS_ADDRESS is unset) and tries
// to claim org.PulseAudio1 — which is already taken — so it aborts with "Daemon
// startup failed" and selkies-start exits 1. The failure only surfaces when a
// per-user D-Bus bus exists, i.e. under the systemd addon's logind session
// with dbus-user-session present; the plain selkies test.sh (gosu path, no
// user bus) never catches it.
//
// dbus-user-session is only Recommended (never Depended-on) by pulseaudio /
// libpam-systemd / podman, and every addon installs with --no-install-
// recommends, so a bare selkies+systemd image has no user bus and would pass
// vacuously. This test pulls it in explicitly via the packages addon so the
// collision condition — the one real users hit once any recommends-carrying
// package lands it — is deterministic.
//
// The test drives an interactive selkies+systemd logind session under a pty,
// runs selkies-start, and asserts it comes up with the csb-audio sink.
package addons_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// runToFile types a guarded, backgrounded script into the interactive session
// and returns what it wrote to /mnt/csb-home/<name>. The lock guard makes the
// re-sends (needed because early pty keystrokes can be dropped) idempotent even
// for a long-running command like selkies-start: only the first send runs it;
// later sends are no-ops until the result file appears.
func (s *systemdSession) runToFile(name, script string) string {
	s.t.Helper()
	outFile := filepath.Join(s.configDir, "home", name)
	lock := "/mnt/csb-home/." + name + ".lock"
	tmp := "/mnt/csb-home/." + name + ".tmp"
	dst := "/mnt/csb-home/" + name
	line := "if [ ! -e " + lock + " ]; then ( touch " + lock + "; { " + script +
		"; } > " + tmp + " 2>&1; mv " + tmp + " " + dst + " ) & fi"

	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		s.sendLine(line)
		time.Sleep(3 * time.Second)
		if b, err := os.ReadFile(outFile); err == nil {
			return string(b)
		}
		select {
		case <-s.done:
			s.t.Fatalf("csb exited before %q completed: %v\n--- terminal ---\n%s", name, s.waitErr, s.terminal())
		default:
		}
	}
	s.t.Fatalf("%q never produced output\n--- terminal ---\n%s", name, s.terminal())
	return ""
}

func TestSelkiesSystemdPulseAudio(t *testing.T) {
	s := startSystemdSession(t, "--addon", "selkies", "--addon", "packages dbus-user-session")

	// Confirm the collision precondition holds before trusting the result:
	// a user D-Bus bus must exist (at $XDG_RUNTIME_DIR/bus — the env var may be
	// unset while the socket is present, and PulseAudio finds it either way)
	// and the stock systemd --user PulseAudio must own it. Without that there'd
	// be nothing for selkies' PulseAudio to collide with and the test would
	// pass vacuously.
	pre := s.runToFile("pre.txt", `echo "userbus=$([ -S "${XDG_RUNTIME_DIR:-/nonexistent}/bus" ] && echo yes || echo no)"; `+
		`echo "upa=$(systemctl --user is-active pulseaudio.service pulseaudio.socket 2>/dev/null | paste -sd, -)"`)
	if ub := probeField(t, pre, "userbus"); ub != "yes" {
		t.Fatalf("no user D-Bus bus in the logind session — precondition for the bug is absent\n%s", pre)
	}

	out := s.runToFile("selkies.txt", `selkies-start; rc=$?; echo "rc=$rc"; `+
		`echo "sink=$(pactl list short sinks 2>/dev/null | grep -c csb-audio)"; `+
		`echo "palog=$(tail -1 "$HOME/.selkies/pulseaudio.log" 2>/dev/null)"`)

	if rc := probeField(t, out, "rc"); rc != "0" {
		t.Fatalf("selkies-start exited %s under the systemd addon (PulseAudio D-Bus name collision)\n--- pre ---\n%s\n--- probe ---\n%s\n--- terminal ---\n%s",
			rc, pre, out, s.terminal())
	}
	if sink := probeField(t, out, "sink"); sink == "0" {
		t.Fatalf("csb-audio sink absent after selkies-start\n--- probe ---\n%s", out)
	}
}
