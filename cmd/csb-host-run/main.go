package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/keras/csb/internal/client"
)

const usage = "usage: csb-host-run [-t|-T] [-C DIR|--no-cwd] [--] <cmd> [args...]"

func main() {
	brokerURL := os.Getenv("CSB_HOST_EXEC_URL")
	brokerToken := os.Getenv("CSB_HOST_EXEC_TOKEN")

	if brokerURL == "" {
		fmt.Fprintln(os.Stderr, "csb-host-run: host exec not enabled in this sandbox (CSB_HOST_EXEC_URL not set)")
		os.Exit(1)
	}

	args := os.Args[1:]
	ttyMode := client.TTYAuto
	cwdDir := ""   // -C value; "" = mirror the current directory
	mirror := true // --no-cwd turns off both mirroring and -C
parse:
	for len(args) > 0 {
		switch args[0] {
		case "-t":
			ttyMode = client.TTYForce
			args = args[1:]
		case "-T":
			ttyMode = client.TTYNever
			args = args[1:]
		case "-C":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "csb-host-run: -C requires a directory\n"+usage)
				os.Exit(1)
			}
			cwdDir = args[1]
			mirror = true
			args = args[2:]
		case "--no-cwd":
			mirror = false
			args = args[1:]
		case "--":
			args = args[1:]
			break parse
		default:
			break parse
		}
	}

	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}

	cwd, err := resolveCwd(cwdDir, mirror)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb-host-run: %v\n", err)
		os.Exit(1)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	sigC := make(chan os.Signal, 4)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)

	exitCode, err := client.Run(brokerURL, brokerToken, cmd, cmdArgs, sigC, os.Stdin, os.Stdout, os.Stderr, ttyMode, cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb-host-run: %v\n", err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}

// resolveCwd picks the workspace-relative directory to ask the host for.
//
// With an explicit -C, a directory outside the workspace is an error. Without
// one we mirror the current directory, which only works while we are inside the
// workspace — anywhere else (e.g. $HOME) we stay silent and let the host command
// run in the broker's own directory, as it always did.
func resolveCwd(dir string, mirror bool) (string, error) {
	if !mirror {
		return "", nil
	}
	root := os.Getenv("CSB_WORKSPACE_DIR")
	explicit := dir != ""
	if !explicit {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		if explicit {
			return "", err
		}
		return "", nil
	}
	rel, err := client.RelCwd(root, abs)
	if err != nil {
		if explicit {
			return "", err
		}
		return "", nil
	}
	return rel, nil
}
