package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"csb-host/internal/client"
)

func main() {
	brokerURL := os.Getenv("CSB_HOST_EXEC_URL")
	brokerToken := os.Getenv("CSB_HOST_EXEC_TOKEN")

	if brokerURL == "" {
		fmt.Fprintln(os.Stderr, "csb-host-run: host exec not enabled in this sandbox (CSB_HOST_EXEC_URL not set)")
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: csb-host-run <cmd> [args...]")
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	sigC := make(chan os.Signal, 4)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)

	exitCode, err := client.Run(brokerURL, brokerToken, cmd, args, sigC, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb-host-run: %v\n", err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}
