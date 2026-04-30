package broker

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"csb-host/internal/proto"
	"nhooyr.io/websocket"
)

// scrubEnv returns a minimal, safe environment for the spawned host process.
// No env vars from the sandbox are forwarded to prevent injection via GIT_SSH_COMMAND etc.
func scrubEnv() []string {
	keep := []string{"PATH", "HOME", "USER", "LANG", "TERM"}
	env := make([]string, 0, len(keep))
	for _, k := range keep {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func runCommand(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, cmd string, args []string) {
	proc := exec.CommandContext(ctx, cmd, args...)
	proc.Env = scrubEnv()

	stdinPipe, err := proc.StdinPipe()
	if err != nil {
		sendFrame(ctx, conn, proto.NewError("exec_failed", err.Error()))
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	stdoutPipe, err := proc.StdoutPipe()
	if err != nil {
		sendFrame(ctx, conn, proto.NewError("exec_failed", err.Error()))
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	stderrPipe, err := proc.StderrPipe()
	if err != nil {
		sendFrame(ctx, conn, proto.NewError("exec_failed", err.Error()))
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}

	if err := proc.Start(); err != nil {
		sendFrame(ctx, conn, proto.NewError("exec_failed", err.Error()))
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}

	var wmu sync.Mutex
	sendLocked := func(f proto.Frame) {
		wmu.Lock()
		defer wmu.Unlock()
		sendFrame(ctx, conn, f)
	}

	var wg sync.WaitGroup
	pipeToWS := func(r io.Reader, newFrame func([]byte) proto.Frame) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 4096)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					cp := make([]byte, n)
					copy(cp, buf[:n])
					sendLocked(newFrame(cp))
				}
				if err != nil {
					return
				}
			}
		}()
	}

	pipeToWS(stdoutPipe, proto.NewStdout)
	pipeToWS(stderrPipe, proto.NewStderr)

	// WS → stdin + signal forwarding
	go func() {
		defer stdinPipe.Close()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				cancel() // client disconnected unexpectedly — kill the host process
				return
			}
			f, err := proto.Decode(data)
			if err != nil {
				continue
			}
			switch f.Type {
			case "stdin":
				if len(f.Data) == 0 {
					return // empty data = EOF on stdin
				}
				remaining := f.Data
				for len(remaining) > 0 {
					n, err := stdinPipe.Write(remaining)
					if err != nil {
						return
					}
					remaining = remaining[n:]
				}
			case "signal":
				if proc.Process != nil {
					forwardSignal(proc.Process, f.Name)
				}
			}
		}
	}()

	wg.Wait() // wait until all stdout/stderr is flushed

	exitCode := 0
	if err := proc.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}

	wmu.Lock()
	sendFrame(ctx, conn, proto.NewExit(exitCode))
	wmu.Unlock()

	conn.Close(websocket.StatusNormalClosure, "")
}

func forwardSignal(proc *os.Process, name string) {
	switch name {
	case "SIGINT":
		proc.Signal(syscall.SIGINT)
	case "SIGTERM":
		proc.Signal(syscall.SIGTERM)
	case "SIGKILL":
		proc.Kill()
	}
}
