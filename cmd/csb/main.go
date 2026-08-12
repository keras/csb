package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/keras/csb/internal/allowlist"
	"github.com/keras/csb/internal/broker"
	"github.com/keras/csb/internal/csb"
)

//go:embed files/Dockerfile
var dockerfile []byte

//go:embed files/entrypoint.sh
var entrypointSH []byte

//go:embed files/csb-persist.sh
var csbPersistSH []byte

//go:embed files/csb-help.sh
var csbHelpSH []byte

//go:embed all:files/addons
var addonsFS embed.FS

//go:embed files/csb-host-run.tar.xz
var hostRunTarXZ []byte

// exit is the only place csb sets a process exit status: an *csb.ExitError
// carries its own, anything else is a csb failure and exits 1.
func exit(err error) {
	var exitErr *csb.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.Err != nil {
			fmt.Fprintf(os.Stderr, "csb: error: %v\n", exitErr.Err)
		}
		os.Exit(exitErr.Code)
	}
	fmt.Fprintf(os.Stderr, "csb: error: %v\n", err)
	os.Exit(1)
}

func main() {
	if os.Getenv("CSB_HOST_BROKER_MODE") == "1" {
		runBroker()
		return
	}

	cfg, err := csb.ParseArgs(os.Args[1:])
	if err != nil {
		exit(err)
	}

	assets := csb.Assets{
		Dockerfile: dockerfile,
		Entrypoint: entrypointSH,
		Persist:    csbPersistSH,
		Help:       csbHelpSH,
		HostRun:    hostRunTarXZ,
		AddonsFS:   addonsFS,
	}

	csb.InitLogger(cfg.Verbose)
	csb.InitConfigDir(cfg.ConfigDir, assets.Dockerfile, assets.AddonsFS)

	rt := csb.NewRuntime(cfg.ContainerCLI())

	switch cfg.Subcommand {
	case "clean":
		if err := csb.RunClean(cfg, rt); err != nil {
			exit(err)
		}
	case "config":
		var err error
		switch cfg.ConfigAction {
		case "show":
			err = csb.RunConfigShow(cfg, assets)
		case "edit":
			err = csb.RunConfigEdit(cfg)
		case "status":
			err = csb.RunConfigStatus(cfg, assets)
		case "update":
			err = csb.RunConfigUpdate(cfg, assets)
		}
		if err != nil {
			exit(err)
		}
	default: // "run"
		if err := csb.RunRun(cfg, rt, assets); err != nil {
			exit(err)
		}
	}
}

type allowFlags []string

func (a *allowFlags) String() string { return fmt.Sprint([]string(*a)) }
func (a *allowFlags) Set(v string) error {
	*a = append(*a, v)
	return nil
}

func runBroker() {
	bind := flag.String("bind", "127.0.0.1:0", "listen address (port 0 = auto-assign)")
	workspace := flag.String("workspace", "", "host workspace root; clients may only pick a cwd under it")
	var allows allowFlags
	flag.Var(&allows, "allow", "allowed command pattern, repeatable: \"cmd arg1 **\"")
	flag.Parse()

	rules, err := allowlist.ParseAll([]string(allows))
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb-broker: %v\n", err)
		os.Exit(1)
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		fmt.Fprintf(os.Stderr, "csb-broker: generate token: %v\n", err)
		os.Exit(1)
	}
	token := hex.EncodeToString(tokenBytes)

	ln, err := net.Listen("tcp", *bind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb-broker: listen %s: %v\n", *bind, err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	info, _ := json.Marshal(map[string]any{"port": port, "token": token})
	fmt.Println(string(info))

	// Deadman switch: csb holds the write end of our stdin pipe for its whole
	// life, so EOF means it died without signaling us (e.g. SIGKILL). Gated on
	// stdin being a pipe — /dev/null and files read as instant EOF, which would
	// kill a manually-started broker on startup.
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeNamedPipe != 0 {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		}()
	} else {
		fmt.Fprintln(os.Stderr, "csb-broker: stdin is not a pipe; running without a deadman switch")
	}

	srv := broker.NewServer(token, rules, *workspace)
	if err := http.Serve(ln, srv); err != nil {
		fmt.Fprintf(os.Stderr, "csb-broker: serve: %v\n", err)
		os.Exit(1)
	}
}
