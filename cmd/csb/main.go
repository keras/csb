package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/keras/csb/internal/allowlist"
	"github.com/keras/csb/internal/broker"
	"github.com/keras/csb/internal/csb"
)

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

func main() {
	if os.Getenv("CSB_HOST_BROKER_MODE") == "1" {
		runBroker()
		return
	}

	cfg, err := csb.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb: error: %v\n", err)
		os.Exit(1)
	}

	assets := csb.Assets{
		Entrypoint: entrypointSH,
		Persist:    csbPersistSH,
		Help:       csbHelpSH,
		HostRun:    hostRunTarXZ,
		AddonsFS:   addonsFS,
	}

	csb.InitLogger(cfg.Verbose)
	csb.InitConfigDir(cfg.ConfigDir, assets.AddonsFS)

	rt := csb.NewRuntime(cfg.ContainerCLI())

	switch cfg.Subcommand {
	case "clean":
		if err := csb.RunClean(cfg, rt); err != nil {
			fmt.Fprintf(os.Stderr, "csb: error: %v\n", err)
			os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "csb: error: %v\n", err)
			os.Exit(1)
		}
	default: // "run"
		if err := csb.RunRun(cfg, rt, assets); err != nil {
			fmt.Fprintf(os.Stderr, "csb: error: %v\n", err)
			os.Exit(1)
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

	srv := broker.NewServer(token, rules)
	if err := http.Serve(ln, srv); err != nil {
		fmt.Fprintf(os.Stderr, "csb-broker: serve: %v\n", err)
		os.Exit(1)
	}
}
