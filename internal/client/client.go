package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"syscall"

	"csb-host/internal/proto"
	"nhooyr.io/websocket"
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

	if err := sendLocked(proto.NewStart(cmd, args)); err != nil {
		return 1, fmt.Errorf("send start: %w", err)
	}

	// stdin pump: read from stdin, send stdin frames; empty data = EOF.
	// Guards each send with a non-blocking done check so writes don't race
	// with conn.Close in the main loop below.
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

	// signal pump: forward SIGINT/SIGTERM to the host process
	if signals != nil {
		go func() {
			for sig := range signals {
				var name string
				switch sig {
				case syscall.SIGINT:
					name = "SIGINT"
				case syscall.SIGTERM:
					name = "SIGTERM"
				default:
					continue
				}
				sendLocked(proto.NewSignal(name))
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
