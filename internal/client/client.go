package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/keras/csb/internal/proto"
	"golang.org/x/term"
	"nhooyr.io/websocket"
)

// TTYMode controls whether the host runs the command under a PTY.
type TTYMode int

const (
	// TTYAuto requests a PTY only when both stdin and stdout are real terminals,
	// so piping either side keeps the byte stream exact (no ONLCR translation).
	TTYAuto TTYMode = iota
	// TTYForce always requests a PTY (rich CLIs, colored output through a pipe).
	TTYForce
	// TTYNever never requests a PTY.
	TTYNever
)

// Run connects to the broker, sends cmd+args, wires stdio, and returns the exit code.
// signals should be a channel receiving os.Signal values (SIGINT, SIGTERM) to forward.
// cwd, when non-empty, is a workspace-relative directory the host process should
// run in; the broker rejects anything that resolves outside its workspace root.
func Run(
	brokerURL, token, cmd string,
	args []string,
	signals <-chan os.Signal,
	stdin io.Reader,
	stdout, stderr io.Writer,
	ttyMode TTYMode,
	cwd string,
) (int, error) {
	ctx := context.Background()

	conn, _, err := websocket.Dial(ctx, brokerURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		return 1, fmt.Errorf("connect to host broker: %w", err)
	}
	conn.SetReadLimit(32 << 20) // 32 MiB — accommodate large stdout/stderr frames

	done := make(chan struct{})
	defer close(done)

	var wmu sync.Mutex
	sendLocked := func(f proto.Frame) error {
		data, err := proto.Encode(f)
		if err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		return conn.Write(ctx, websocket.MessageText, data)
	}

	// Decide whether to request a PTY on the host. stdinFd is the raw-mode /
	// resize source and is only valid when stdin is a genuine terminal.
	stdinFd := -1
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		stdinFd = int(f.Fd())
	}
	stdoutIsTTY := false
	if f, ok := stdout.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		stdoutIsTTY = true
	}

	useTTY := false
	switch ttyMode {
	case TTYForce:
		useTTY = true
	case TTYNever:
		useTTY = false
	default:
		useTTY = stdinFd >= 0 && stdoutIsTTY
	}

	var startFrame proto.Frame
	if useTTY {
		cols, rows := uint16(80), uint16(24)
		if stdinFd >= 0 {
			if c, r, err := term.GetSize(stdinFd); err == nil && c > 0 && r > 0 {
				cols, rows = uint16(c), uint16(r)
			}
		}
		startFrame = proto.NewStartTTY(cmd, args, cols, rows)
	} else {
		startFrame = proto.NewStart(cmd, args)
	}
	startFrame.Cwd = cwd

	if err := sendLocked(startFrame); err != nil {
		return 1, fmt.Errorf("send start: %w", err)
	}

	// Put local terminal in raw mode so the host PTY drives it.
	if useTTY && stdinFd >= 0 {
		oldState, err := term.MakeRaw(stdinFd)
		if err == nil {
			defer term.Restore(stdinFd, oldState)
		}
	}

	// stdin pump
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				select {
				case <-done:
					return
				default:
					sendLocked(proto.NewStdin(cp))
				}
			}
			if err != nil {
				select {
				case <-done:
				default:
					sendLocked(proto.NewStdin(nil)) // signal EOF to broker
				}
				return
			}
		}
	}()

	// signal pump: forward SIGINT/SIGTERM; handle SIGWINCH for TTY resize
	if signals != nil {
		go func() {
			for sig := range signals {
				switch sig {
				case syscall.SIGINT:
					sendLocked(proto.NewSignal("SIGINT"))
				case syscall.SIGTERM:
					sendLocked(proto.NewSignal("SIGTERM"))
				case syscall.SIGWINCH:
					if !useTTY || stdinFd < 0 {
						continue
					}
					cols, rows, err := term.GetSize(stdinFd)
					if err == nil && cols > 0 && rows > 0 {
						sendLocked(proto.NewResize(uint16(cols), uint16(rows)))
					}
				}
			}
		}()
	}

	// If TTY mode, also watch SIGWINCH independently in case the caller didn't subscribe.
	if useTTY && stdinFd >= 0 {
		winchC := make(chan os.Signal, 1)
		signal.Notify(winchC, syscall.SIGWINCH)
		go func() {
			defer signal.Stop(winchC)
			for {
				select {
				case <-done:
					return
				case <-winchC:
					cols, rows, err := term.GetSize(stdinFd)
					if err == nil && cols > 0 && rows > 0 {
						sendLocked(proto.NewResize(uint16(cols), uint16(rows)))
					}
				}
			}
		}()
	}

	// read frames from broker until exit or error
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return 1, fmt.Errorf("read from broker: %w", err)
		}

		f, err := proto.Decode(data)
		if err != nil {
			return 1, fmt.Errorf("decode frame: %w", err)
		}

		switch f.Type {
		case "stdout":
			stdout.Write(f.Data)
		case "stderr":
			stderr.Write(f.Data)
		case "exit":
			code := 0
			if f.ExitCode != nil {
				code = *f.ExitCode
			}
			conn.Close(websocket.StatusNormalClosure, "")
			return code, nil
		case "error":
			fmt.Fprintf(stderr, "csb-host-run: %s: %s\n", f.ErrCode, f.Message)
			conn.Close(websocket.StatusNormalClosure, "")
			return exitCodeForError(f.ErrCode), nil
		}
	}
}

func exitCodeForError(errCode string) int {
	switch errCode {
	case "not_whitelisted":
		return 126
	case "invalid_cwd":
		return 125
	case "unknown_command", "exec_failed":
		return 127
	default:
		return 1
	}
}
