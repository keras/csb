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

	"csb-host/internal/proto"
	"nhooyr.io/websocket"
	"golang.org/x/term"
)

// Run connects to the broker, sends cmd+args, wires stdio, and returns the exit code.
// signals should be a channel receiving os.Signal values (SIGINT, SIGTERM) to forward.
func Run(
	brokerURL, token, cmd string,
	args []string,
	signals <-chan os.Signal,
	stdin io.Reader,
	stdout, stderr io.Writer,
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

	// Detect if stdin is a real terminal and set up TTY mode.
	var startFrame proto.Frame
	stdinFd := -1
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		stdinFd = int(f.Fd())
	}

	if stdinFd >= 0 {
		cols, rows, err := term.GetSize(stdinFd)
		if err != nil || cols == 0 || rows == 0 {
			cols, rows = 80, 24
		}
		startFrame = proto.NewStartTTY(cmd, args, uint16(cols), uint16(rows))
	} else {
		startFrame = proto.NewStart(cmd, args)
	}

	if err := sendLocked(startFrame); err != nil {
		return 1, fmt.Errorf("send start: %w", err)
	}

	// Put local terminal in raw mode so the host PTY drives it.
	if stdinFd >= 0 {
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
					if stdinFd < 0 {
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
	if stdinFd >= 0 {
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
	case "unknown_command", "exec_failed":
		return 127
	default:
		return 1
	}
}
