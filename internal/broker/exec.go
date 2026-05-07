package broker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"csb-host/internal/proto"
	"github.com/creack/pty"
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

func runCommand(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, f startFrame) {
	if f.tty {
		runCommandTTY(ctx, cancel, conn, f)
	} else {
		runCommandPipes(ctx, cancel, conn, f)
	}
}

type startFrame struct {
	cmd  string
	args []string
	tty  bool
	cols uint16
	rows uint16
}

func runCommandPipes(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, f startFrame) {
	proc := exec.CommandContext(ctx, f.cmd, f.args...)
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
	slog.Info("command started", "cmd", f.cmd, "args", f.args, "pid", proc.Process.Pid)

	var wmu sync.Mutex
	sendLocked := func(fr proto.Frame) {
		wmu.Lock()
		defer wmu.Unlock()
		sendFrame(ctx, conn, fr)
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
				cancel()
				return
			}
			fr, err := proto.Decode(data)
			if err != nil {
				continue
			}
			switch fr.Type {
			case "stdin":
				if len(fr.Data) == 0 {
					return
				}
				remaining := fr.Data
				for len(remaining) > 0 {
					n, err := stdinPipe.Write(remaining)
					if err != nil {
						return
					}
					remaining = remaining[n:]
				}
			case "signal":
				if proc.Process != nil {
					forwardSignal(proc.Process, fr.Name)
				}
			}
		}
	}()

	wg.Wait()

	exitCode := 0
	if err := proc.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}

	slog.Info("command exited", "cmd", f.cmd, "args", f.args, "exit_code", exitCode)

	wmu.Lock()
	sendFrame(ctx, conn, proto.NewExit(exitCode))
	wmu.Unlock()

	conn.Close(websocket.StatusNormalClosure, "")
}

func runCommandTTY(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, f startFrame) {
	proc := exec.CommandContext(ctx, f.cmd, f.args...)
	proc.Env = scrubEnv()

	cols, rows := f.cols, f.rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	ptmx, err := pty.StartWithSize(proc, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		sendFrame(ctx, conn, proto.NewError("exec_failed", err.Error()))
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	defer ptmx.Close()

	slog.Info("command started (tty)", "cmd", f.cmd, "args", f.args, "pid", proc.Process.Pid, "cols", cols, "rows", rows)

	var wmu sync.Mutex
	sendLocked := func(fr proto.Frame) {
		wmu.Lock()
		defer wmu.Unlock()
		sendFrame(ctx, conn, fr)
	}

	// PTY master → stdout frames
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				sendLocked(proto.NewStdout(cp))
			}
			if err != nil {
				return
			}
		}
	}()

	// WS → PTY stdin + resize + signal forwarding
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			fr, err := proto.Decode(data)
			if err != nil {
				continue
			}
			switch fr.Type {
			case "stdin":
				if len(fr.Data) == 0 {
					ptmx.Write([]byte{4}) // Ctrl-D = EOF
					return
				}
				ptmx.Write(fr.Data)
			case "resize":
				pty.Setsize(ptmx, &pty.Winsize{Cols: fr.Cols, Rows: fr.Rows})
			case "signal":
				if proc.Process != nil {
					forwardSignal(proc.Process, fr.Name)
				}
			}
		}
	}()

	wg.Wait()

	exitCode := 0
	if err := proc.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}

	slog.Info("command exited", "cmd", f.cmd, "args", f.args, "exit_code", exitCode)

	wmu.Lock()
	sendFrame(ctx, conn, proto.NewExit(exitCode))
	wmu.Unlock()

	conn.Close(websocket.StatusNormalClosure, "")
}

func forwardSignal(proc *os.Process, name string) {
	slog.Info("forwarding signal", "signal", name, "pid", proc.Pid)
	switch name {
	case "SIGINT":
		proc.Signal(syscall.SIGINT)
	case "SIGTERM":
		proc.Signal(syscall.SIGTERM)
	case "SIGKILL":
		proc.Kill()
	}
}
