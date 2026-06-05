package csb

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// addonNames returns a comma-separated list of enabled addon names, or "none".
func addonNames(cfg *Config) string {
	if len(cfg.Addons) == 0 {
		return "none"
	}
	names := make([]string, 0, len(cfg.Addons))
	for _, spec := range cfg.Addons {
		name, _ := parseAddonSpec(spec)
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ",")
}

// RunClean interactively selects csb images and volumes to remove.
func RunClean(cfg *Config, rt *Runtime) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "csb clean: requires an interactive terminal")
		os.Exit(1)
	}

	images := rt.ListCSBImagesInfo()
	volumes := rt.ListCSBVolumesInfo()

	if len(images) == 0 && len(volumes) == 0 {
		fmt.Println("Nothing to remove.")
		return nil
	}

	var entries []selectorEntry

	if len(images) > 0 {
		entries = append(entries, selectorEntry{
			label:    fmt.Sprintf("Images (%d):", len(images)),
			isHeader: true,
		})
		maxRepo, maxSize, maxAge := 0, 0, 0
		for _, img := range images {
			repoTag := imageRepoTag(img)
			if len(repoTag) > maxRepo {
				maxRepo = len(repoTag)
			}
			if len(img.Size) > maxSize {
				maxSize = len(img.Size)
			}
			if len(img.Age) > maxAge {
				maxAge = len(img.Age)
			}
		}
		for _, img := range images {
			label := img.ID
			label += "  " + padRight(imageRepoTag(img), maxRepo)
			if img.Size != "" {
				label += "  " + padRight(img.Size, maxSize)
			}
			if img.Age != "" {
				label += "  " + padRight(img.Age, maxAge)
			}
			if cd := shortenHome(img.ConfigDir, cfg.Home); cd != "" {
				label += "  " + cd
			}
			entries = append(entries, selectorEntry{label: label, imageID: img.ID})
		}
	}

	if len(volumes) > 0 {
		if len(entries) > 0 {
			entries = append(entries, selectorEntry{isHeader: true})
		}
		entries = append(entries, selectorEntry{
			label:    fmt.Sprintf("Volumes (%d):", len(volumes)),
			isHeader: true,
		})
		maxAge := 0
		for _, vol := range volumes {
			if len(vol.Age) > maxAge {
				maxAge = len(vol.Age)
			}
		}
		for _, vol := range volumes {
			label := vol.Name
			if vol.Age != "" {
				label += "  " + padRight(vol.Age, maxAge)
			}
			if cd := shortenHome(vol.ConfigDir, cfg.Home); cd != "" {
				label += "  " + cd
			}
			if vol.Name == cfg.HomeVolume {
				label += "  ← current"
			}
			entries = append(entries, selectorEntry{label: label, volName: vol.Name})
		}
	}

	selected, ok := runSelector(entries)
	if !ok {
		fmt.Println("\nAborted.")
		return nil
	}
	if len(selected) == 0 {
		fmt.Println("\nNothing selected.")
		return nil
	}

	fmt.Println()
	var imageIDs []string
	for _, e := range selected {
		if e.imageID != "" {
			imageIDs = append(imageIDs, e.imageID)
		}
	}
	if len(imageIDs) > 0 {
		fmt.Printf("Removing %d image(s)...\n", len(imageIDs))
		rt.RemoveImages(imageIDs)
	}
	for _, e := range selected {
		if e.volName != "" {
			fmt.Printf("Removing volume %s...\n", e.volName)
			rt.RemoveVolume(e.volName)
		}
	}
	return nil
}

func imageRepoTag(img ImageInfo) string {
	repo := img.Repository
	if repo == "" || repo == "<none>" {
		repo = "<none>"
	}
	tag := img.Tag
	if tag == "" || tag == "<none>" {
		return repo
	}
	return repo + ":" + tag
}

func shortenHome(p, home string) string {
	if p == "" || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// RunConfigShow prints the fully resolved configuration as YAML, or — when
// invoked as `csb config show context` — a listing of the docker build
// context that would be sent for an image build.
func RunConfigShow(cfg *Config, entrypointContent, persistContent, hostRunTarXZ []byte) error {
	if cfg.ConfigShowTarget == "context" {
		return runConfigShowContext(cfg, entrypointContent, persistContent, hostRunTarXZ)
	}
	out, err := yaml.Marshal(cfg.Options)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	fmt.Printf("# csb resolved config\n# config dir: %s\n", cfg.ConfigDir)
	if cfg.Workspace != nil {
		fmt.Printf("# workspace:   %s\n", *cfg.Workspace)
	} else {
		fmt.Printf("# workspace:   (none)\n")
	}
	fmt.Println()
	fmt.Print(string(out))
	return nil
}

// runConfigShowContext outputs the docker build context. When stdout is a
// TTY it prints a human-readable per-entry listing (mode, size, path);
// when piped or redirected it streams the raw tar so it can be saved
// (`csb config show context > ctx.tar`) or piped (`| tar tvf -`,
// `| docker build -`).
func runConfigShowContext(cfg *Config, entrypointContent, persistContent, hostRunTarXZ []byte) error {
	contextTar, err := BuildContextTar(cfg, entrypointContent, persistContent, hostRunTarXZ)
	if err != nil {
		return fmt.Errorf("building context: %w", err)
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		_, err := os.Stdout.Write(contextTar)
		return err
	}
	tr := tar.NewReader(bytes.NewReader(contextTar))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading context tar: %w", err)
		}
		mode := fs.FileMode(hdr.Mode & 0777)
		if hdr.Typeflag == tar.TypeDir {
			mode |= fs.ModeDir
		}
		fmt.Printf("%s  %8d  %s\n", mode, hdr.Size, hdr.Name)
	}
	return nil
}

func RunConfigEdit(cfg *Config) error {
	var path string
	header := ""

	if cfg.ConfigEditTarget == "workdir" {
		if cfg.Workspace == nil {
			fmt.Fprintln(os.Stderr, "csb config edit workdir requires a workspace (not --no-workspace)")
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
func RunRun(cfg *Config, rt *Runtime, addonsFS fs.FS, entrypointContent, persistContent, hostRunTarXZ []byte) error {
	// Config summary
	logInfo("config dir", "path", cfg.ConfigDir, "runtime", cfg.ContainerCLI())

	// Verbose nudge when shipped resources have drifted from this binary.
	if embedded, err := managedEmbeddedFiles(addonsFS); err == nil {
		if n := pendingUpdateCount(cfg.ConfigDir, embedded); n > 0 {
			logInfo("shipped resources", "differ", n, "hint", "csb config status")
		}
	}
	if cfg.Workspace != nil {
		logInfo("workspace", "host", *cfg.Workspace, "container", cfg.Workdir())
	} else {
		logInfo("workspace", "mode", "none")
	}
	userConfigPath := filepath.Join(cfg.ConfigDir, "config.yaml")
	if _, err := os.Stat(userConfigPath); err == nil {
		logInfo("user config", "path", userConfigPath)
	}
	if wcp := cfg.WorkdirConfigPath(); wcp != "" {
		if _, err := os.Stat(wcp); err == nil {
			logInfo("workdir config", "path", wcp)
		}
	}

	// Validate addons exist
	for _, spec := range cfg.Addons {
		name, _ := parseAddonSpec(spec)
		if name == "" {
			continue
		}
		addonPath := filepath.Join(cfg.ConfigDir, "addons", name, "install.sh")
		if _, err := os.Stat(addonPath); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "csb: error: addon not found: %s\n", name)
			os.Exit(2)
		}
	}
	logInfo("addons", "enabled", addonNames(cfg))

	imgName := ImageName(cfg, string(entrypointContent), string(persistContent), hostRunTarXZ)

	if cfg.Rebuild || !rt.ImageExists(imgName) {
		reason := "not found locally"
		if cfg.Rebuild {
			reason = "--rebuild flag"
		}
		fmt.Fprintf(os.Stderr, "Building %s...\n", imgName)
		logInfo("building image", "image", imgName, "reason", reason)
		contextTar, err := BuildContextTar(cfg, entrypointContent, persistContent, hostRunTarXZ)
		if err != nil {
			return fmt.Errorf("building context: %w", err)
		}
		if err := rt.BuildImage(imgName, contextTar, ImageLabels(cfg), "linux/"+cfg.Arch, !cfg.Verbose); err != nil {
			return fmt.Errorf("building image: %w", err)
		}
	} else {
		logInfo("image up to date", "image", imgName)
	}

	created, err := rt.EnsureVolume(cfg.HomeVolume, VolumeLabels(cfg))
	if err != nil {
		return fmt.Errorf("ensuring volume: %w", err)
	}
	if created {
		logInfo("volume created", "name", cfg.HomeVolume)
	} else {
		logInfo("volume ready", "name", cfg.HomeVolume)
	}

	var brokerProc *exec.Cmd
	var brokerURL, brokerToken string

	if cfg.HostExecEnabled {
		logInfo("starting host exec broker", "bind", cfg.HostExecBind, "allow", strings.Join(cfg.HostExecAllow, ","))
		brokerProc, brokerURL, brokerToken, err = StartHostExec(cfg.HostExecAllow, cfg.HostExecBind, cfg.ContainerCLI())
		if err != nil {
			return fmt.Errorf("starting host exec: %w", err)
		}
	}

	mounts := ResolveMounts(cfg)
	for _, m := range mounts {
		mode := "rw"
		if m.Readonly {
			mode = "ro"
		}
		logInfo("mount", "host", m.Src, "container", m.Dst, "mode", mode)
	}

	env := ResolveEnv(cfg, brokerURL, brokerToken)
	cmd, err := BuildRunCommand(cfg, mounts, env, imgName)
	if err != nil {
		return err
	}

	logInfo("launching", "command", shJoin(cmd))

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
