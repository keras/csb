package main

import (
	_ "embed"
	"fmt"
	"os"

	"csb-host/internal/csb"
)

//go:embed files/entrypoint.sh
var entrypointSH []byte

//go:embed files/csb-persist.sh
var csbPersistSH []byte

//go:embed files/addons/mise.sh
var miseAddonSH []byte

func main() {
	cfg, err := csb.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb: error: %v\n", err)
		os.Exit(1)
	}

	csb.InitConfigDir(cfg.ConfigDir, miseAddonSH)

	rt := csb.NewRuntime(cfg.ContainerCLI())

	switch cfg.Subcommand {
	case "clean":
		if err := csb.RunClean(cfg, rt); err != nil {
			fmt.Fprintf(os.Stderr, "csb: error: %v\n", err)
			os.Exit(1)
		}
	case "config_edit":
		if err := csb.RunConfigEdit(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "csb: error: %v\n", err)
			os.Exit(1)
		}
	default: // "run"
		if err := csb.RunRun(cfg, rt, entrypointSH, csbPersistSH); err != nil {
			fmt.Fprintf(os.Stderr, "csb: error: %v\n", err)
			os.Exit(1)
		}
	}
}
