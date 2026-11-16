package csb

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCLI writes an executable stand-in for docker/podman. Every invocation is
// appended to a log (one space-joined line per call) before body, a /bin/sh
// fragment, decides the response. body gets the scratch dir so it can keep state
// in files. Named "docker" so Runtime.HostIDs doesn't try to query podman.
func fakeCLI(t *testing.T, body func(dir string) string) (cli string, calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logFile + "'\n" + body(dir) + "\n"
	cli = filepath.Join(dir, "docker")
	require.NoError(t, os.WriteFile(cli, []byte(script), 0755))
	return cli, func() []string {
		data, err := os.ReadFile(logFile)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// ── #4: full container IDs ───────────────────────────────────────────────────

func TestMatchSessionTarget_FullContainerID(t *testing.T) {
	// ps reports 12 characters; `inspect` and users' scripts have all 64.
	full := "aaa111222333" + strings.Repeat("f", 52)
	got := matchSessionTarget(testSessions, full)
	if assert.Len(t, got, 1) {
		assert.Equal(t, "csb-proj-aaaaaa", got[0].Name)
	}
	// A long target that merely shares a short prefix with nothing must not match.
	assert.Empty(t, matchSessionTarget(testSessions, "ddd"+strings.Repeat("f", 61)))
	assert.False(t, idMatches("", "abc"))
	assert.False(t, idMatches("abc", ""))
}

// ── #6: lookup failures are errors, not "no sessions" ────────────────────────

const psTwoSessions = `printf 'aaa111222333\tcsb-p-aaaaaa\tUp 1 minute\nbbb444555666\tcsb-p-bbbbbb\tUp 2 minutes\n'`

func TestListSessions_PsFailureIsAnError(t *testing.T) {
	cli, _ := fakeCLI(t, func(string) string { return `echo "Cannot connect to the daemon" >&2; exit 1` })

	sessions, err := NewRuntime(cli).ListSessions()
	require.Error(t, err)
	assert.Nil(t, sessions)
	assert.Contains(t, err.Error(), "Cannot connect to the daemon", "the runtime's own message should reach the user")
}

func TestListSessions_InspectFailureIsAnError(t *testing.T) {
	cli, _ := fakeCLI(t, func(string) string {
		return `case "$1" in ps) ` + psTwoSessions + `;; *) echo "boom" >&2; exit 1;; esac`
	})

	_, err := NewRuntime(cli).ListSessions()
	require.Error(t, err, "a failed label lookup must not silently yield sessions with empty workspaces")
	assert.Contains(t, err.Error(), "boom")
}

func TestListSessions_ReadsLabels(t *testing.T) {
	cli, _ := fakeCLI(t, func(string) string {
		return `case "$1" in ps) ` + psTwoSessions + `;; *) printf '%s' '[` +
			`{"Id":"aaa111222333ffff","Config":{"Labels":{"csb.workspace":"/w/a","csb.config-dir":"/c"}}},` +
			`{"Id":"bbb444555666ffff","Config":{"Labels":{"csb.workspace":"/w/b","csb.config-dir":"/c"}}}]';; esac`
	})

	sessions, err := NewRuntime(cli).ListSessions()
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	assert.Equal(t, SessionInfo{ID: "aaa111222333", Name: "csb-p-aaaaaa", Status: "Up 1 minute", Workspace: "/w/a", ConfigDir: "/c"}, sessions[0])
	assert.Equal(t, "/w/b", sessions[1].Workspace)
}

// A session that ends between ps and inspect makes the runtime print the rest and
// exit non-zero. That is a race to tolerate, not a failure — and the vanished one
// must be dropped rather than reported with blank labels.
func TestListSessions_SessionVanishesBetweenPsAndInspect(t *testing.T) {
	cli, _ := fakeCLI(t, func(string) string {
		return `case "$1" in ps) ` + psTwoSessions + `;; *) ` +
			`printf '%s' '[{"Id":"aaa111222333ffff","Config":{"Labels":{"csb.workspace":"/w/a","csb.config-dir":"/c"}}}]'; ` +
			`echo "no such container" >&2; exit 1;; esac`
	})

	sessions, err := NewRuntime(cli).ListSessions()
	require.NoError(t, err)
	if assert.Len(t, sessions, 1) {
		assert.Equal(t, "csb-p-aaaaaa", sessions[0].Name)
	}
}

func TestListSessions_NoneRunning(t *testing.T) {
	cli, _ := fakeCLI(t, func(string) string { return `exit 0` })
	sessions, err := NewRuntime(cli).ListSessions()
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

// ── #5: interrupted while the session boots ──────────────────────────────────

// neverReady: the session never comes up, but the container stays alive.
func neverReady(string) string {
	return `case "$*" in
  *"inspect -f"*) echo true ;;
  run*) echo abc123def456 ;;
  *has-session*) exit 1 ;;
  *) exit 0 ;;
esac`
}

func TestWaitSessionReady_InterruptedBeforeStart(t *testing.T) {
	cli, _ := fakeCLI(t, neverReady)
	sigC := make(chan os.Signal, 1)
	sigC <- syscall.SIGINT

	err := waitSessionReady(NewRuntime(cli), "abc123def456", sigC)
	var interrupted *interruptedError
	require.True(t, errors.As(err, &interrupted), "got %v", err)
	assert.Equal(t, 130, interrupted.exitCode())
}

func TestWaitSessionReady_InterruptedMidWait(t *testing.T) {
	cli, _ := fakeCLI(t, neverReady)
	sigC := make(chan os.Signal, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		sigC <- syscall.SIGTERM
	}()

	start := time.Now()
	err := waitSessionReady(NewRuntime(cli), "abc123def456", sigC)
	var interrupted *interruptedError
	require.True(t, errors.As(err, &interrupted), "got %v", err)
	assert.Equal(t, 143, interrupted.exitCode())
	assert.Less(t, time.Since(start), 5*time.Second, "must return promptly, not wait out the 90s timeout")
}

func TestRunDetachable_InterruptRemovesContainer(t *testing.T) {
	cli, calls := fakeCLI(t, neverReady)
	sigC := make(chan os.Signal, 1)
	sigC <- syscall.SIGINT

	err := runDetachable(NewRuntime(cli), []string{cli, "run", "-d", "img"}, "csb-p-aaaaaa", sigC)

	var exitErr *ExitError
	require.True(t, errors.As(err, &exitErr), "got %v", err)
	assert.Equal(t, 130, exitErr.Code)
	assert.Contains(t, exitErr.Error(), "removed it")
	assert.Contains(t, calls(), "rm -f abc123def456", "the half-started container must not be left running")
}

func TestRunDetachable_ContainerDiesBeforeReady(t *testing.T) {
	cli, calls := fakeCLI(t, func(string) string {
		return `case "$*" in
  *"inspect -f"*) exit 1 ;;
  run*) echo abc123def456 ;;
  *has-session*) exit 1 ;;
  *) exit 0 ;;
esac`
	})

	err := runDetachable(NewRuntime(cli), []string{cli, "run", "-d", "img"}, "csb-p-aaaaaa", make(chan os.Signal))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited before the session started")
	assert.Contains(t, calls(), "rm -f abc123def456")
}

// ── #1: the "still running" hint tracks the tmux session ─────────────────────

// `exec ... has-session` succeeds only while <dir>/alive exists; the attach
// itself always returns 0, like a user pressing prefix-d or typing `exit`.
func attachCLI(t *testing.T, alive bool) string {
	cli, _ := fakeCLI(t, func(dir string) string {
		if alive {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "alive"), nil, 0644))
		}
		return `case "$*" in
  *has-session*) [ -f '` + dir + `/alive' ] ;;
  *) exit 0 ;;
esac`
	})
	return cli
}

func TestAttachSession_HintWhenSessionStillRunning(t *testing.T) {
	cli := attachCLI(t, true)
	out := captureStderr(t, func() {
		require.NoError(t, attachSession(NewRuntime(cli), "abc123def456", "csb-p-aaaaaa"))
	})
	assert.Contains(t, out, "still running; reattach with: csb attach csb-p-aaaaaa")
}

// The container can linger for ~1s after the last window closes; the session is
// what matters, and it is gone — so no hint pointing at an unattachable session.
func TestAttachSession_NoHintWhenSessionEnded(t *testing.T) {
	cli := attachCLI(t, false)
	out := captureStderr(t, func() {
		require.NoError(t, attachSession(NewRuntime(cli), "abc123def456", "csb-p-aaaaaa"))
	})
	assert.NotContains(t, out, "reattach")
}
