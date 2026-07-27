package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/keras/csb/internal/client"
)

func main() {
	brokerURL := os.Getenv("CSB_HOST_EXEC_URL")
	brokerToken := os.Getenv("CSB_HOST_EXEC_TOKEN")

	if brokerURL == "" {
		fmt.Fprintln(os.Stderr, "csb-host-run: host exec not enabled in this sandbox (CSB_HOST_EXEC_URL not set)")
		os.Exit(1)
	}

	args := os.Args[1:]
	ttyMode := client.TTYAuto
parse:
	for len(args) > 0 {
		switch args[0] {
		case "-t":
			ttyMode = client.TTYForce
			args = args[1:]
		case "-T":
			ttyMode = client.TTYNever
			args = args[1:]
		case "--":
			args = args[1:]
			break parse
		default:
			break parse
		}
	}

	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: csb-host-run [-t|-T] [--] <cmd> [args...]")
		os.Exit(1)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	sigC := make(chan os.Signal, 4)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)

	exitCode, err := client.Run(brokerURL, brokerToken, cmd, cmdArgs, sigC, os.Stdin, os.Stdout, os.Stderr, ttyMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb-host-run: %v\n", err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}
