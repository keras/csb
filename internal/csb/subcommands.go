package csb

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RunClean removes all csb images and volumes after prompting for confirmation.
func RunClean(cfg *Config, rt *Runtime) error {
	imageIDs := rt.ListCSBImageIDs()
	volumes := rt.ListCSBVolumes()

	// Always include current home volume
	hasHome := false
	for _, v := range volumes {
		if v == cfg.HomeVolume {
			hasHome = true
			break
		}
	}
	if !hasHome {
		volumes = append(volumes, cfg.HomeVolume)
	}

	if len(imageIDs) == 0 && len(volumes) == 0 {
		fmt.Println("Nothing to remove.")
		return nil
	}

	if len(imageIDs) > 0 {
		fmt.Printf("Images (%d):\n", len(imageIDs))
		for _, id := range imageIDs {
			fmt.Printf("  %s\n", id)
		}
	}
	if len(volumes) > 0 {
		fmt.Printf("Volumes (%d):\n", len(volumes))
		for _, v := range volumes {
			fmt.Printf("  %s\n", v)
		}
	}

	fmt.Print("\nRemove all of the above? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println()
		os.Exit(1)
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" {
		fmt.Println("Aborted.")
		os.Exit(1)
	}

	if len(imageIDs) > 0 {
		fmt.Printf("Removing %d image(s)...\n", len(imageIDs))
		rt.RemoveImages(imageIDs)
	}
	for _, vol := range volumes {
		fmt.Printf("Removing volume %s...\n", vol)
		rt.RemoveVolume(vol)
	}
	return nil
}

// RunConfigEdit opens the target config file in the user's editor.
func RunConfigEdit(cfg *Config) error {
	var path string
	header := ""

	if cfg.ConfigEditTarget == "workdir" {
		if cfg.Workspace == nil {
			fmt.Fprintln(os.Stderr, "csb config-edit workdir requires a workspace (not --no-workspace)")
			os.Exit(1)
		}
		path = workdirConfigPath(cfg.ConfigDir, *cfg.Workspace)
		header = fmt.Sprintf("# workdir: %s\n", *cfg.Workspace)
	} else {
		path = filepath.Join(cfg.ConfigDir, "config.yaml")
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("creating config dir: %w", err)
		}
		content := header + renderConfigTemplate()
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}
	}

	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	result := cmd.Run()

	if result != nil {
		if exitErr, ok := result.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
	os.Exit(0)
	return nil // unreachable
}

// RunRun executes the main run subcommand.
func RunRun(cfg *Config, rt *Runtime, entrypointContent, persistContent []byte) error {
	// Validate addons exist
	for _, name := range cfg.Addons {
		addonPath := filepath.Join(cfg.ConfigDir, "addons", name+".sh")
		if _, err := os.Stat(addonPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "csb: error: addon not found: %s\n", name)
			os.Exit(2)
		}
	}

	if cfg.ResetHome {
		fmt.Printf("Removing home volume %s...\n", cfg.HomeVolume)
		rt.RemoveVolume(cfg.HomeVolume)
	}

	imgName := ImageName(cfg, string(entrypointContent), string(persistContent))

	if cfg.Rebuild || !rt.ImageExists(imgName) {
		contextTar, err := BuildContextTar(cfg, entrypointContent, persistContent)
		if err != nil {
			return fmt.Errorf("building context: %w", err)
		}
		if err := rt.BuildImage(imgName, contextTar, ImageLabels(cfg), !cfg.Verbose); err != nil {
			return fmt.Errorf("building image: %w", err)
		}
	}

	if err := rt.EnsureVolume(cfg.HomeVolume, VolumeLabels(cfg)); err != nil {
		return fmt.Errorf("ensuring volume: %w", err)
	}

	var brokerProc *exec.Cmd
	var brokerURL, brokerToken string

	if cfg.HostExecEnabled {
		var err error
		brokerProc, brokerURL, brokerToken, err = StartHostExec(cfg.HostExecAllow, cfg.HostExecBind, cfg.ContainerCLI())
		if err != nil {
			return fmt.Errorf("starting host exec: %w", err)
		}
	}

	mounts := ResolveMounts(cfg)
	env := ResolveEnv(cfg, brokerURL, brokerToken)
	cmd, err := BuildRunCommand(cfg, mounts, env, imgName)
	if err != nil {
		return err
	}

	if cfg.Verbose {
		fmt.Fprintln(os.Stderr, shJoin(cmd))
	}

	if brokerProc != nil {
		// Can't use exec when we need to clean up the broker after container exits.
		runCmd := exec.Command(cmd[0], cmd[1:]...)
		runCmd.Stdin = os.Stdin
		runCmd.Stdout = os.Stdout
		runCmd.Stderr = os.Stderr
		runErr := runCmd.Run()
		_ = brokerProc.Process.Signal(os.Interrupt)
		_ = brokerProc.Wait()
		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				os.Exit(exitErr.ExitCode())
			}
			os.Exit(1)
		}
		return nil
	}

	return rt.ExecRun(cmd)
}
