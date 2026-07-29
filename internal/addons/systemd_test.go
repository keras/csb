//go:build addons

// Black-box behavioral tests for the systemd addon's launch paths.
//
// The addon's launcher (cmd/csb/files/addons/systemd/launcher.sh) has four
// meaningfully different shapes:
//
//   - non-interactive `csb -- cmd`: gosu drop alongside systemd; the command's
//     exit code must ride through `systemctl exit` to the csb exit code
//   - interactive, no tmux: entered through login(1) so it becomes a real
//     logind session (XDG_RUNTIME_DIR, `systemd --user`, loginctl visibility)
//   - interactive with --tmux: same login session, but tmux is started from a
//     profile.d snippet so its *server* lives inside the logind session
//   - the console must stay clean: systemd boots with its status/log output
//     routed away from the pty (csb-container.conf + --show-status=no)
//
// The non-interactive boot behavior itself (PID 1 is systemd, units settle,
// logind up, masks held) is asserted by the addon's test.sh, which the generic
// TestAddons harness already runs through the non-interactive branch. The tests
// here cover what test.sh cannot: exit-code plumbing and the pty-requiring
// interactive branches.
//
// Interactive tests drive csb under a pty (creack/pty). Rather than scraping
// the terminal (fragile under tmux redraws), the typed probe writes its
// findings to /mnt/csb-home — a bind mount of <config-dir>/home — and the test
// reads the file from the host. The probe line is re-sent until the file
// appears: login(1)'s vhangup can flush pty input queued before the shell is
// up, and under --tmux early keystrokes can land before tmux takes over, so a
// single send is not reliable. The probe is idempotent, so re-sends are
// harmless.
package addons_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// probeCmd is typed into the interactive shell. It records the session shape
// and atomically publishes it to /mnt/csb-home for the host side to read.
// Single line (typed via pty), double quotes only where expansion is wanted.
const probeCmd = `{ echo "xdg=${XDG_RUNTIME_DIR:-}"; echo "tmux=${TMUX:-}"; echo "path=$PATH"; echo "pwd=$(pwd)"; echo "sessions=$(loginctl --no-legend list-sessions 2>/dev/null | wc -l)"; sid=$(loginctl --no-legend list-sessions 2>/dev/null | awk '{print $1; exit}'); echo "--- session-status ---"; loginctl session-status "$sid" 2>&1; } > /mnt/csb-home/.probe.tmp 2>&1; mv /mnt/csb-home/.probe.tmp /mnt/csb-home/probe.txt`

// systemdSession drives an interactive `csb --addon systemd` under a pty.
type systemdSession struct {
	t         *testing.T
	cmd       *exec.Cmd
	ptmx      *os.File
	configDir string
	done      chan struct{} // closed when csb exits; waitErr is then valid
	waitErr   error

	mu  sync.Mutex
	out bytes.Buffer // everything the container wrote to the pty, for diagnostics
}

func startSystemdSession(t *testing.T, extra ...string) *systemdSession {
	t.Helper()
	configDir := t.TempDir()
	// Pre-create <config-dir>/home so csb bind-mounts it at /mnt/csb-home —
	// the channel the probe uses to report back to the host.
	if err := os.MkdirAll(filepath.Join(configDir, "home"), 0o755); err != nil {
		t.Fatal(err)
	}

	args := []string{"--config-dir", configDir, "--addon", "systemd", "--no-workspace"}
	args = append(args, extra...)
	cmd := exec.Command(csbBin, args...)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		t.Fatalf("start csb under pty: %v", err)
	}

	s := &systemdSession{t: t, cmd: cmd, ptmx: ptmx, configDir: configDir, done: make(chan struct{})}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.out.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { s.waitErr = cmd.Wait(); close(s.done) }()

	t.Cleanup(func() {
		select {
		case <-s.done:
		default:
			_ = cmd.Process.Kill()
			<-s.done
		}
		_ = ptmx.Close()
		reapContainer(configDir)
	})
	return s
}

func (s *systemdSession) sendLine(line string) {
	s.t.Helper()
	if _, err := s.ptmx.Write([]byte(line + "\r")); err != nil {
		s.t.Fatalf("write to pty: %v", err)
	}
}

func (s *systemdSession) terminal() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// probe types probeCmd into the session until the result file shows up on the
// host, then returns its contents. The long deadline covers a cold systemd
// boot plus the launcher's logind wait plus (with --tmux) tmux startup.
func (s *systemdSession) probe() string {
	s.t.Helper()
	probeFile := filepath.Join(s.configDir, "home", "probe.txt")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		s.sendLine(probeCmd)
		time.Sleep(2 * time.Second)
		if b, err := os.ReadFile(probeFile); err == nil {
			return string(b)
		}
		select {
		case <-s.done:
			s.t.Fatalf("csb exited before probe completed: %v\n--- terminal ---\n%s", s.waitErr, s.terminal())
		default:
		}
	}
	s.t.Fatalf("probe result never appeared\n--- terminal ---\n%s", s.terminal())
	return ""
}

// waitExit waits for csb to exit and returns its exit code.
func (s *systemdSession) waitExit(timeout time.Duration) int {
	s.t.Helper()
	select {
	case <-s.done:
		if s.waitErr == nil {
			return 0
		}
		if ee, ok := s.waitErr.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		s.t.Fatalf("csb failed: %v\n--- terminal ---\n%s", s.waitErr, s.terminal())
	case <-time.After(timeout):
		s.t.Fatalf("csb did not exit within %v\n--- terminal ---\n%s", timeout, s.terminal())
	}
	return -1
}

// probeField extracts the value of a "key=value" line from probe output.
func probeField(t *testing.T, probe, key string) string {
	t.Helper()
	for _, line := range strings.Split(probe, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	t.Fatalf("probe output has no %q line:\n%s", key, probe)
	return ""
}

// TestSystemdNonInteractiveExitCode: a fast command's exit code must propagate
// through `systemctl exit` to the csb exit code. "Fast" is the interesting
// part — it exercises the launcher's retry against the race where the command
// finishes before systemd's control socket is up (see _csb_stop_container).
func TestSystemdNonInteractiveExitCode(t *testing.T) {
	cmd := exec.Command(csbBin,
		"--config-dir", t.TempDir(),
		"--addon", "systemd",
		"--no-workspace",
		"--", "sh", "-c", "exit 7")
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("want exit error with code 7, got %v\n--- output ---\n%s", err, out)
	}
	if code := ee.ExitCode(); code != 7 {
		t.Fatalf("exit code = %d, want 7\n--- output ---\n%s", code, out)
	}
}

// TestSystemdNonInteractiveCleanConsole: systemd's boot chatter must stay off
// the command's stdio (ShowStatus=no / LogTarget=journal / --show-status=no).
func TestSystemdNonInteractiveCleanConsole(t *testing.T) {
	cmd := exec.Command(csbBin,
		"--config-dir", t.TempDir(),
		"--addon", "systemd",
		"--no-workspace",
		"--", "echo", "hello-from-csb")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("csb run failed: %v\n--- output ---\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("hello-from-csb")) {
		t.Fatalf("command output missing\n--- output ---\n%s", out)
	}
	for _, noise := range []string{"[  OK  ]", "Welcome to", "Reached target"} {
		if bytes.Contains(out, []byte(noise)) {
			t.Fatalf("systemd boot output %q leaked to the console\n--- output ---\n%s", noise, out)
		}
	}
}

// TestSystemdInteractiveLoginSession: the bare interactive shell must be a
// real logind session — XDG_RUNTIME_DIR set by pam_systemd, the session
// visible to loginctl, csb's ~/.local/bin PATH prefix restored after login(1)
// scrubbed it — and must NOT be inside tmux. Exiting the shell must stop the
// container; the container exits 0 regardless of the shell's status, because
// util-linux login(1) always exits 0 (the exit-code passthrough in
// _csb_stop_container only carries codes on the non-interactive branch).
func TestSystemdInteractiveLoginSession(t *testing.T) {
	s := startSystemdSession(t)
	probe := s.probe()

	if xdg := probeField(t, probe, "xdg"); !strings.HasPrefix(xdg, "/run/user/") {
		t.Errorf("XDG_RUNTIME_DIR = %q, want /run/user/<uid> (pam_systemd did not run?)\n%s", xdg, probe)
	}
	if tm := probeField(t, probe, "tmux"); tm != "" {
		t.Errorf("TMUX = %q, want unset (no --tmux was given)\n%s", tm, probe)
	}
	if p := probeField(t, probe, "path"); !strings.Contains(p, "/.local/bin") {
		t.Errorf("PATH %q lost the ~/.local/bin prefix (csb-path.sh not applied?)\n%s", p, probe)
	}
	if n := probeField(t, probe, "sessions"); n == "0" {
		t.Errorf("no logind session registered\n%s", probe)
	}
	// csb runs the container with `-w /workspace` and the shell must open
	// there, exactly like the non-systemd (gosu) interactive path does.
	// login(1) chdir()s to the user's home, so the launcher has to restore
	// the workdir afterwards.
	if p := probeField(t, probe, "pwd"); p != "/workspace" {
		t.Errorf("interactive shell cwd = %q, want /workspace (the container workdir)\n%s", p, probe)
	}

	// `exit 7` still yields container exit 0: login(1) swallows the shell's
	// status. This assertion documents that shape — if it starts failing,
	// either login's behavior changed or the launcher grew real passthrough.
	s.sendLine("exit 7")
	if code := s.waitExit(2 * time.Minute); code != 0 {
		t.Errorf("exit code = %d, want 0 (login(1) exits 0 regardless of shell status)\n--- terminal ---\n%s", code, s.terminal())
	}

	// systemd's early-boot output and systemd-shutdown's teardown messages
	// must not reach the pty: PID 1 boots with SYSTEMD_LOG_TARGET=null and
	// csb-log-target.service confines its logging to the journal's lifetime,
	// so the console fallback (= this pty) never fires.
	for _, noise := range []string{
		"running in system mode",
		"Detected virtualization",
		"Queued start job",
		"Sending SIGTERM to remaining processes",
	} {
		if strings.Contains(s.terminal(), noise) {
			t.Errorf("systemd log output %q leaked to the interactive pty\n--- terminal ---\n%s", noise, s.terminal())
		}
	}
}

// TestSystemdInteractiveTmux: with --tmux the shell must be inside tmux, still
// carry the logind session env, and the tmux *server* must live inside the
// session (visible in loginctl session-status's process tree) — that is the
// point of starting tmux from within the login session instead of as the
// container command.
func TestSystemdInteractiveTmux(t *testing.T) {
	s := startSystemdSession(t, "--tmux")
	probe := s.probe()

	if tm := probeField(t, probe, "tmux"); tm == "" {
		t.Errorf("TMUX unset — csb-tmux.sh did not start tmux\n%s", probe)
	}
	if xdg := probeField(t, probe, "xdg"); !strings.HasPrefix(xdg, "/run/user/") {
		t.Errorf("XDG_RUNTIME_DIR = %q inside tmux, want /run/user/<uid>\n%s", xdg, probe)
	}
	if n := probeField(t, probe, "sessions"); n == "0" {
		t.Errorf("no logind session registered\n%s", probe)
	}
	// The session-status process tree must contain the tmux server.
	if _, status, ok := strings.Cut(probe, "--- session-status ---"); !ok || !strings.Contains(status, "tmux") {
		t.Errorf("tmux server not inside the logind session\n%s", probe)
	}
	// The tmux window shell must open in the container workdir, like the
	// non-systemd tmux path does (login(1) chdir()s to home; the workdir has
	// to be restored before tmux starts so the server inherits it).
	if p := probeField(t, probe, "pwd"); p != "/workspace" {
		t.Errorf("tmux shell cwd = %q, want /workspace (the container workdir)\n%s", p, probe)
	}

	// Exiting the (only) tmux window ends the server, which ends the exec'd
	// login shell and stops the container. tmux does not forward the window
	// shell's exit status — the container exits 0; assert that shape.
	s.sendLine("exit")
	if code := s.waitExit(2 * time.Minute); code != 0 {
		t.Errorf("exit code = %d, want 0\n--- terminal ---\n%s", code, s.terminal())
	}
}
