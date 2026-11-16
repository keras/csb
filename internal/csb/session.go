package csb

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// How long to wait for a freshly started detachable container to bring its tmux
// session up. Generous: with the systemd addon this covers systemd boot plus the
// launcher's wait for logind.
const (
	sessionReadyTimeout = 90 * time.Second
	sessionReadyPoll    = 200 * time.Millisecond
)

// runForeground runs argv with csb's stdio, forwarding INT/TERM/HUP to it, and
// waits. A non-zero exit comes back as an *ExitError carrying the child's status.
// No Setpgid: the child must stay in csb's foreground group so the tty delivers
// Ctrl-C straight to it.
func runForeground(argv []string) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr

	// Notify before Start — a signal in between would hit the default
	// disposition and kill csb, orphaning the container and broker.
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer close(sigC) // LIFO: Stop first, then close ends the goroutine
	defer signal.Stop(sigC)
	if err := c.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", argv[0], err)
	}

	go func() {
		for s := range sigC {
			_ = c.Process.Signal(s)
		}
	}()

	if err := c.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// The child chose its status; not a csb error.
			return &ExitError{Code: exitErr.ExitCode()}
		}
		return fmt.Errorf("waiting for %s: %w", argv[0], err)
	}
	return nil
}

// startDetached runs the `<runtime> run -d ...` command and returns the new
// container's ID.
func startDetached(argv []string) (string, error) {
	c := exec.Command(argv[0], argv[1:]...)
	c.Stderr = os.Stderr
	out, err := c.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &ExitError{Code: exitErr.ExitCode(), Err: fmt.Errorf("%s run failed", argv[0])}
		}
		return "", fmt.Errorf("starting %s: %w", argv[0], err)
	}
	lines := strings.Fields(string(out))
	if len(lines) == 0 {
		return "", fmt.Errorf("%s run -d printed no container ID", argv[0])
	}
	return lines[len(lines)-1], nil
}

// interruptedError reports that csb was signalled while waiting for a session to
// come up, so the caller can clean up the half-started container.
type interruptedError struct{ sig os.Signal }

func (e *interruptedError) Error() string { return fmt.Sprintf("interrupted (%v)", e.sig) }

// exitCode is the conventional shell status for death by signal: 128 + signo.
func (e *interruptedError) exitCode() int {
	if s, ok := e.sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 130
}

// notifySignals registers for the signals that should abort a session start. It
// must be called before the container is started: a signal arriving earlier would
// hit the default disposition and kill csb, leaving the container behind.
func notifySignals() (<-chan os.Signal, func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return c, func() { signal.Stop(c) }
}

// waitSessionReady blocks until the container's tmux session exists, the
// container dies, a signal arrives on sigC, or the timeout expires.
func waitSessionReady(rt *Runtime, id string, sigC <-chan os.Signal) error {
	uid, gid := rt.HostIDs()
	deadline := time.Now().Add(sessionReadyTimeout)
	for {
		select {
		case s := <-sigC:
			return &interruptedError{s}
		default:
		}
		if rt.TmuxReady(id, uid, gid) {
			return nil
		}
		if !rt.ContainerRunning(id) {
			return errors.New("container exited before the session started")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for the tmux session to start", sessionReadyTimeout)
		}
		select {
		case s := <-sigC:
			return &interruptedError{s}
		case <-time.After(sessionReadyPoll):
		}
	}
}

// attachSession attaches this terminal to the container's tmux session. When the
// client returns — a detach, a closed terminal, or the session ending — it says
// whether the session is still running. The tmux client's own exit status means
// nothing here, so it is not propagated.
func attachSession(rt *Runtime, id, name string) error {
	uid, gid := rt.HostIDs()
	termName := os.Getenv("TERM")
	if termName == "" {
		termName = "xterm-256color"
	}
	argv := []string{rt.CLI, "exec", "-it", "-u", uid + ":" + gid, "-e", "TERM=" + termName}
	if ct := os.Getenv("COLORTERM"); ct != "" {
		argv = append(argv, "-e", "COLORTERM="+ct)
	}
	argv = append(argv, id, "tmux", "attach-session", "-t", tmuxSessionName)

	if err := runForeground(argv); err != nil {
		var exitErr *ExitError
		if !errors.As(err, &exitErr) {
			return err
		}
	}
	// Ask about the tmux session, not the container: after `exit` in the last
	// window the container's wait loop lingers for up to a second, and the session
	// is already gone by then.
	if rt.TmuxReady(id, uid, gid) {
		fmt.Fprintf(os.Stderr, "csb: detached — session %s is still running; reattach with: csb attach %s\n", name, name)
	}
	return nil
}

// sessionsForWorkspace filters sessions to those started from this workspace and
// config dir. Returns nil under --no-workspace: with nothing to identify the
// session by, it is never auto-selected.
func sessionsForWorkspace(sessions []SessionInfo, cfg *Config) []SessionInfo {
	if cfg.Workspace == nil {
		return nil
	}
	var out []SessionInfo
	for _, s := range sessions {
		if s.Workspace == *cfg.Workspace && s.ConfigDir == cfg.ConfigDir {
			out = append(out, s)
		}
	}
	return out
}

// matchSessionTarget resolves a user-supplied NAME or ID (full or prefix). An
// exact name wins outright; otherwise every session whose ID matches does.
func matchSessionTarget(sessions []SessionInfo, target string) []SessionInfo {
	for _, s := range sessions {
		if s.Name == target {
			return []SessionInfo{s}
		}
	}
	var out []SessionInfo
	for _, s := range sessions {
		if idMatches(s.ID, target) {
			out = append(out, s)
		}
	}
	return out
}

// idMatches reports whether target names the container with the given ID. ps
// reports a truncated ID, so a prefix match must work in both directions: a short
// target is a prefix of the ID, and a full 64-character ID has the short one as its
// prefix.
func idMatches(id, target string) bool {
	if id == "" || target == "" {
		return false
	}
	return strings.HasPrefix(id, target) || strings.HasPrefix(target, id)
}

// sessionTable renders sessions as aligned "name  workspace  status" lines.
func sessionTable(sessions []SessionInfo, home string) string {
	maxName, maxWS := 0, 0
	rows := make([][3]string, len(sessions))
	for i, s := range sessions {
		ws := shortenHome(s.Workspace, home)
		if ws == "" {
			ws = "(no workspace)"
		}
		rows[i] = [3]string{s.Name, ws, s.Status}
		if len(s.Name) > maxName {
			maxName = len(s.Name)
		}
		if len(ws) > maxWS {
			maxWS = len(ws)
		}
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s  %s  %s\n", padRight(r[0], maxName), padRight(r[1], maxWS), r[2])
	}
	return b.String()
}

// RunAttach reattaches to a running detachable session.
func RunAttach(cfg *Config, rt *Runtime) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("attach requires an interactive terminal")
	}

	sessions, err := rt.ListSessions()
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}
	if len(sessions) == 0 {
		return &ExitError{Code: 1, Err: errors.New("no running detachable sessions (start one with: csb --detachable)")}
	}

	var candidates []SessionInfo
	if cfg.AttachTarget != "" {
		candidates = matchSessionTarget(sessions, cfg.AttachTarget)
		switch len(candidates) {
		case 0:
			return &ExitError{Code: 1, Err: fmt.Errorf("no running session matches %q; running sessions:\n%s", cfg.AttachTarget, sessionTable(sessions, cfg.Home))}
		case 1:
		default:
			return &ExitError{Code: 2, Err: fmt.Errorf("%q is ambiguous; matching sessions:\n%s", cfg.AttachTarget, sessionTable(candidates, cfg.Home))}
		}
	} else {
		candidates = sessionsForWorkspace(sessions, cfg)
		if len(candidates) != 1 {
			what := "no session for this workspace"
			if len(candidates) > 1 {
				what = "several sessions for this workspace"
			}
			return &ExitError{Code: 2, Err: fmt.Errorf("%s; pick one with: csb attach NAME\n%s", what, sessionTable(sessions, cfg.Home))}
		}
	}

	s := candidates[0]
	return attachSession(rt, s.ID, s.Name)
}
