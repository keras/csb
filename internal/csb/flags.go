package csb

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// portRE validates publish spec: [[host_ip:]host_port:]container_port[/tcp|udp|sctp]
var portRE = regexp.MustCompile(
	`^(?:` +
		`(?:\d{1,3}(?:\.\d{1,3}){3}|\[[0-9a-fA-F:]+\]):(?:\d+(?:-\d+)?)?:` +
		`|(?:\d+(?:-\d+)?):` +
		`)?` +
		`\d+(?:-\d+)?` +
		`(?:/(?:tcp|udp|sctp))?$`,
)

// detectSubcommand scans argv left-to-right for the first non-flag token.
// Returns (subcommand, remainingArgv).
func detectSubcommand(argv []string) (string, []string) {
	for i, token := range argv {
		if token == "--" {
			break
		}
		if !strings.HasPrefix(token, "-") {
			switch token {
			case "clean":
				return "clean", append(argv[:i:i], argv[i+1:]...)
			case "config-edit":
				return "config_edit", append(argv[:i:i], argv[i+1:]...)
			case "run":
				return "run", append(argv[:i:i], argv[i+1:]...)
			default:
				return "run", argv
			}
		}
	}
	return "run", argv
}

// ParseArgs parses CLI arguments and returns a Config.
func ParseArgs(argv []string) (*Config, error) {
	subcommand, subArgv := detectSubcommand(argv)

	// Pre-parse to resolve config_dir and workspace for YAML loading.
	configDir, preWorkspace, err := preParse(subArgv)
	if err != nil {
		return nil, err
	}

	// Load YAML configs.
	userYAML, err := loadYAMLConfig(configDir)
	if err != nil {
		return nil, fmt.Errorf("loading user config: %w", err)
	}
	workdirYAML := map[string]interface{}{}
	if preWorkspace != nil {
		workdirYAML, err = loadWorkdirYAMLConfig(configDir, *preWorkspace)
		if err != nil {
			return nil, fmt.Errorf("loading workdir config: %w", err)
		}
	}
	// Merge: workdir overrides user
	yamlCfg := mergeMaps(userYAML, workdirYAML)

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()

	switch subcommand {
	case "clean":
		return parseCleanArgs(subArgv, configDir, preWorkspace, yamlCfg, cwd, home)
	case "config_edit":
		return parseConfigEditArgs(subArgv, configDir, preWorkspace, cwd, home)
	default:
		return parseRunArgs(subArgv, configDir, preWorkspace, yamlCfg, cwd, home)
	}
}

func preParse(argv []string) (string, *string, error) {
	// Simple scan for --config-dir, --workspace, --no-workspace
	var configDirFlag, workspaceFlag string
	noWorkspace := false

	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--config-dir" && i+1 < len(argv):
			i++
			configDirFlag = argv[i]
		case strings.HasPrefix(arg, "--config-dir="):
			configDirFlag = strings.TrimPrefix(arg, "--config-dir=")
		case arg == "--workspace" && i+1 < len(argv):
			i++
			workspaceFlag = argv[i]
		case strings.HasPrefix(arg, "--workspace="):
			workspaceFlag = strings.TrimPrefix(arg, "--workspace=")
		case arg == "--no-workspace":
			noWorkspace = true
		}
	}

	// Resolve config dir
	if configDirFlag == "" {
		configDirFlag = os.Getenv("CSB_CONFIG_DIR")
	}
	if configDirFlag == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, err
		}
		configDirFlag = filepath.Join(home, ".config", "csb")
	}
	configDir, err := filepath.Abs(expandUser(configDirFlag))
	if err != nil {
		return "", nil, err
	}

	// Resolve workspace
	var workspace *string
	if noWorkspace {
		workspace = nil
	} else if workspaceFlag != "" {
		abs, err := filepath.Abs(workspaceFlag)
		if err != nil {
			return "", nil, err
		}
		workspace = &abs
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		workspace = &cwd
	}

	return configDir, workspace, nil
}

func expandUser(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	} else if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}
	return path
}

func loadYAMLConfig(configDir string) (map[string]interface{}, error) {
	path := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]interface{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]interface{}{}, nil
	}
	return m, nil
}

func loadWorkdirYAMLConfig(configDir, workspace string) (map[string]interface{}, error) {
	path := workdirConfigPath(configDir, workspace)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]interface{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]interface{}{}, nil
	}
	return m, nil
}

func mergeMaps(base, override map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{}
	for k, v := range base {
		result[k] = v
	}
	for k, v := range override {
		result[k] = v
	}
	return result
}

// yamlString extracts a string from the YAML map, or returns def.
func yamlString(m map[string]interface{}, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// yamlBool extracts a bool from the YAML map, or returns (def, false).
func yamlBool(m map[string]interface{}, key string) (bool, bool) {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b, true
		}
	}
	return false, false
}

// yamlStringList extracts a string list from the YAML map.
func yamlStringList(m map[string]interface{}, key string) ([]string, bool) {
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	switch val := v.(type) {
	case []interface{}:
		result := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result, true
	case string:
		return []string{val}, true
	}
	return nil, false
}

// resolveOptions resolves all option values with CLI > env > yaml > default precedence.
type optionValues struct {
	// CLI flags (pointer = not set)
	useTmux       *bool
	useTTY        *bool
	mounts        []string
	runtime       string
	baseImage     string
	nestedPodman  *bool
	addons        []string
	envForward    []string
	envInject     []string
	publish       []string
	hostNetwork   *bool
	hostExecEnabled *bool
	hostExecAllow []string
}

type resolvedOptions struct {
	UseTmux         bool
	UseTTY          bool
	Mount           []Mount
	Runtime         string
	BaseImage       string
	NestedPodman    bool
	Addons          []string
	HomeVolume      string
	Image           string
	EnvForward      []string
	EnvInject       []string
	Publish         []string
	HostNetwork     bool
	HostExecEnabled bool
	HostExecAllow   []string
	HostExecBind    string
}

func resolveAllOptions(opts optionValues, yaml map[string]interface{}) (resolvedOptions, error) {
	var r resolvedOptions

	// use_tmux
	if opts.useTmux != nil {
		r.UseTmux = *opts.useTmux
	} else if v, ok := yamlBool(yaml, "tmux"); ok {
		r.UseTmux = v
	} else {
		r.UseTmux = false
	}

	// use_tty
	if opts.useTTY != nil {
		r.UseTTY = *opts.useTTY
	} else if v, ok := yamlBool(yaml, "tty"); ok {
		r.UseTTY = v
	} else {
		r.UseTTY = term.IsTerminal(int(os.Stdin.Fd()))
	}

	// mount
	var mountSpecs []string
	if len(opts.mounts) > 0 {
		mountSpecs = opts.mounts
	} else if list, ok := yamlStringList(yaml, "mount"); ok {
		mountSpecs = list
	}
	for _, spec := range mountSpecs {
		m, err := ParseMount(spec)
		if err != nil {
			return r, fmt.Errorf("invalid mount %q: %w", spec, err)
		}
		r.Mount = append(r.Mount, m)
	}
	if r.Mount == nil {
		r.Mount = []Mount{}
	}

	// runtime
	if opts.runtime != "" {
		r.Runtime = opts.runtime
	} else if env := os.Getenv("CSB_RUNTIME"); env != "" {
		r.Runtime = env
	} else if v := yamlString(yaml, "runtime", ""); v != "" {
		r.Runtime = v
	} else {
		r.Runtime = "auto"
	}

	// base_image
	if opts.baseImage != "" {
		r.BaseImage = opts.baseImage
	} else if env := os.Getenv("CSB_BASE_IMAGE"); env != "" {
		r.BaseImage = env
	} else if v := yamlString(yaml, "base_image", ""); v != "" {
		r.BaseImage = v
	} else {
		r.BaseImage = "debian:stable-slim"
	}

	// nested_podman
	if opts.nestedPodman != nil {
		r.NestedPodman = *opts.nestedPodman
	} else if env := os.Getenv("CSB_NESTED_PODMAN"); env != "" {
		r.NestedPodman = BoolFromEnv(env)
	} else if v, ok := yamlBool(yaml, "nested_podman"); ok {
		r.NestedPodman = v
	} else {
		r.NestedPodman = false
	}

	// addons
	if len(opts.addons) > 0 {
		r.Addons = opts.addons
	} else if list, ok := yamlStringList(yaml, "addons"); ok {
		r.Addons = list
	} else {
		r.Addons = []string{"mise"}
	}

	// home_volume
	if env := os.Getenv("CSB_HOME_VOLUME"); env != "" {
		r.HomeVolume = env
	} else if v := yamlString(yaml, "home_volume", ""); v != "" {
		r.HomeVolume = v
	} else {
		r.HomeVolume = "csb-home"
	}

	// image
	if env := os.Getenv("CSB_IMAGE"); env != "" {
		r.Image = env
	} else if v := yamlString(yaml, "image", ""); v != "" {
		r.Image = v
	} else {
		r.Image = ""
	}

	// env_forward
	if len(opts.envForward) > 0 {
		r.EnvForward = opts.envForward
	} else if env := os.Getenv("CSB_ENV_FORWARD"); env != "" {
		r.EnvForward = strings.Fields(env)
	} else if list, ok := yamlStringList(yaml, "env_forward"); ok {
		r.EnvForward = list
	} else {
		r.EnvForward = []string{}
	}

	// env_inject
	if len(opts.envInject) > 0 {
		r.EnvInject = opts.envInject
	} else if env := os.Getenv("CSB_ENV"); env != "" {
		r.EnvInject = strings.Fields(env)
	} else if list, ok := yamlStringList(yaml, "env"); ok {
		r.EnvInject = list
	} else {
		r.EnvInject = []string{}
	}

	// publish
	if len(opts.publish) > 0 {
		r.Publish = opts.publish
	} else if env := os.Getenv("CSB_PUBLISH"); env != "" {
		r.Publish = strings.Fields(env)
	} else if list, ok := yamlStringList(yaml, "publish"); ok {
		r.Publish = list
	} else {
		r.Publish = []string{}
	}
	for _, spec := range r.Publish {
		if !portRE.MatchString(spec) {
			return r, fmt.Errorf("invalid publish spec %q; expected [[host_ip:]host_port:]container_port[/tcp|udp|sctp]", spec)
		}
	}

	// host_network
	if opts.hostNetwork != nil {
		r.HostNetwork = *opts.hostNetwork
	} else if env := os.Getenv("CSB_HOST_NETWORK"); env != "" {
		r.HostNetwork = BoolFromEnv(env)
	} else if v, ok := yamlBool(yaml, "host_network"); ok {
		r.HostNetwork = v
	} else {
		r.HostNetwork = false
	}

	// host_exec_enabled
	if opts.hostExecEnabled != nil {
		r.HostExecEnabled = *opts.hostExecEnabled
	} else if env := os.Getenv("CSB_HOST_EXEC"); env != "" {
		r.HostExecEnabled = BoolFromEnv(env)
	} else if v, ok := yamlBool(yaml, "host_exec_enabled"); ok {
		r.HostExecEnabled = v
	} else {
		r.HostExecEnabled = false
	}

	// host_exec_allow
	if len(opts.hostExecAllow) > 0 {
		r.HostExecAllow = opts.hostExecAllow
	} else if list, ok := yamlStringList(yaml, "host_exec_allow"); ok {
		r.HostExecAllow = list
	} else {
		r.HostExecAllow = []string{}
	}

	// host_exec_bind
	if v := yamlString(yaml, "host_exec_bind", ""); v != "" {
		r.HostExecBind = v
	} else {
		r.HostExecBind = "0.0.0.0:0"
	}

	return r, nil
}

func parseRunArgs(argv []string, configDir string, preWorkspace *string, yamlCfg map[string]interface{}, cwd, home string) (*Config, error) {
	var (
		workspaceFlag   string
		noWorkspace     bool
		rebuild         bool
		resetHome       bool
		verbose         bool
		configDirFlag   string
		// option flags
		useTmux      *bool
		useTTY       *bool
		mounts       []string
		runtimeFlag  string
		baseImage    string
		nestedPodman *bool
		addons       []string
		envForward   []string
		envInject    []string
		publish      []string
		hostNetwork  *bool
		hostExecEnabled *bool
		hostExecAllow []string
	)

	// Parse argv manually to handle bool pairs (--tmux/--no-tmux) and appending flags.
	var remaining []string
	passthroughStart := -1

	for i := 0; i < len(argv); i++ {
		arg := argv[i]

		if arg == "--" {
			passthroughStart = i + 1
			break
		}

		getNext := func() (string, error) {
			i++
			if i >= len(argv) {
				return "", fmt.Errorf("flag %s requires an argument", arg)
			}
			return argv[i], nil
		}

		switch {
		case arg == "--workspace":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			workspaceFlag = v
		case strings.HasPrefix(arg, "--workspace="):
			workspaceFlag = strings.TrimPrefix(arg, "--workspace=")
		case arg == "--no-workspace":
			noWorkspace = true
		case arg == "--rebuild":
			rebuild = true
		case arg == "--reset-home":
			resetHome = true
		case arg == "-v", arg == "--verbose":
			verbose = true
		case arg == "--config-dir":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			configDirFlag = v
		case strings.HasPrefix(arg, "--config-dir="):
			configDirFlag = strings.TrimPrefix(arg, "--config-dir=")
		case arg == "--tmux":
			t := true
			useTmux = &t
		case arg == "--no-tmux":
			f := false
			useTmux = &f
		case arg == "--tty":
			t := true
			useTTY = &t
		case arg == "--no-tty":
			f := false
			useTTY = &f
		case arg == "--mount":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			mounts = append(mounts, v)
		case strings.HasPrefix(arg, "--mount="):
			mounts = append(mounts, strings.TrimPrefix(arg, "--mount="))
		case arg == "--runtime":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			runtimeFlag = v
		case strings.HasPrefix(arg, "--runtime="):
			runtimeFlag = strings.TrimPrefix(arg, "--runtime=")
		case arg == "--base-image":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			baseImage = v
		case strings.HasPrefix(arg, "--base-image="):
			baseImage = strings.TrimPrefix(arg, "--base-image=")
		case arg == "--nested-podman":
			t := true
			nestedPodman = &t
		case arg == "--no-nested-podman":
			f := false
			nestedPodman = &f
		case arg == "--addon":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			addons = append(addons, v)
		case strings.HasPrefix(arg, "--addon="):
			addons = append(addons, strings.TrimPrefix(arg, "--addon="))
		case arg == "--env-forward":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			envForward = append(envForward, v)
		case strings.HasPrefix(arg, "--env-forward="):
			envForward = append(envForward, strings.TrimPrefix(arg, "--env-forward="))
		case arg == "--env":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			envInject = append(envInject, v)
		case strings.HasPrefix(arg, "--env="):
			envInject = append(envInject, strings.TrimPrefix(arg, "--env="))
		case arg == "--publish":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			publish = append(publish, v)
		case strings.HasPrefix(arg, "--publish="):
			publish = append(publish, strings.TrimPrefix(arg, "--publish="))
		case arg == "--host-network":
			t := true
			hostNetwork = &t
		case arg == "--no-host-network":
			f := false
			hostNetwork = &f
		case arg == "--host-exec":
			t := true
			hostExecEnabled = &t
		case arg == "--no-host-exec":
			f := false
			hostExecEnabled = &f
		case arg == "--host-exec-allow":
			v, err := getNext()
			if err != nil {
				return nil, err
			}
			hostExecAllow = append(hostExecAllow, v)
		case strings.HasPrefix(arg, "--host-exec-allow="):
			hostExecAllow = append(hostExecAllow, strings.TrimPrefix(arg, "--host-exec-allow="))
		case arg == "--help-full":
			fmt.Print(formatHelpFull())
			os.Exit(0)
		case arg == "-h", arg == "--help":
			fmt.Print(formatHelp())
			os.Exit(0)
		case strings.HasPrefix(arg, "-"):
			return nil, fmt.Errorf("unknown flag: %s", arg)
		default:
			remaining = append(remaining, arg)
		}
	}

	_ = configDirFlag // already resolved in preParse

	var passthrough []string
	if passthroughStart >= 0 {
		passthrough = argv[passthroughStart:]
	} else {
		passthrough = remaining
	}

	// Resolve workspace
	var workspace *string
	if noWorkspace {
		workspace = nil
	} else if workspaceFlag != "" {
		abs, err := filepath.Abs(workspaceFlag)
		if err != nil {
			return nil, err
		}
		workspace = &abs
	} else {
		workspace = preWorkspace
	}

	opts := optionValues{
		useTmux:         useTmux,
		useTTY:          useTTY,
		mounts:          mounts,
		runtime:         runtimeFlag,
		baseImage:       baseImage,
		nestedPodman:    nestedPodman,
		addons:          addons,
		envForward:      envForward,
		envInject:       envInject,
		publish:         publish,
		hostNetwork:     hostNetwork,
		hostExecEnabled: hostExecEnabled,
		hostExecAllow:   hostExecAllow,
	}

	resolved, err := resolveAllOptions(opts, yamlCfg)
	if err != nil {
		return nil, err
	}

	return &Config{
		CWD:             cwd,
		Home:            home,
		ConfigDir:       configDir,
		Workspace:       workspace,
		Subcommand:      "run",
		Rebuild:         rebuild,
		ResetHome:       resetHome,
		Verbose:         verbose,
		PassthroughArgs: passthrough,
		UseTmux:         resolved.UseTmux,
		UseTTY:          resolved.UseTTY,
		Mount:           resolved.Mount,
		Runtime:         resolved.Runtime,
		BaseImage:       resolved.BaseImage,
		NestedPodman:    resolved.NestedPodman,
		Addons:          resolved.Addons,
		HomeVolume:      resolved.HomeVolume,
		Image:           resolved.Image,
		EnvForward:      resolved.EnvForward,
		EnvInject:       resolved.EnvInject,
		Publish:         resolved.Publish,
		HostNetwork:     resolved.HostNetwork,
		HostExecEnabled: resolved.HostExecEnabled,
		HostExecAllow:   resolved.HostExecAllow,
		HostExecBind:    resolved.HostExecBind,
	}, nil
}

func parseCleanArgs(argv []string, configDir string, preWorkspace *string, yamlCfg map[string]interface{}, cwd, home string) (*Config, error) {
	verbose := false
	for _, arg := range argv {
		if arg == "-v" || arg == "--verbose" {
			verbose = true
		}
	}

	// Resolve options with fallback to defaults on error
	resolved := resolvedOptions{
		Runtime:    "auto",
		HomeVolume: "csb-home",
		Addons:     []string{"mise"},
	}
	if r, err := resolveAllOptions(optionValues{}, yamlCfg); err == nil {
		resolved = r
	}

	return &Config{
		CWD:        cwd,
		Home:       home,
		ConfigDir:  configDir,
		Workspace:  preWorkspace,
		Subcommand: "clean",
		Verbose:    verbose,
		Runtime:    resolved.Runtime,
		HomeVolume: resolved.HomeVolume,
		Addons:     resolved.Addons,
	}, nil
}

func parseConfigEditArgs(argv []string, configDir string, preWorkspace *string, cwd, home string) (*Config, error) {
	verbose := false
	var workspaceFlag string
	noWorkspace := false
	var target string

	var positional []string

	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "-v", arg == "--verbose":
			verbose = true
		case arg == "--workspace" && i+1 < len(argv):
			i++
			workspaceFlag = argv[i]
		case strings.HasPrefix(arg, "--workspace="):
			workspaceFlag = strings.TrimPrefix(arg, "--workspace=")
		case arg == "--no-workspace":
			noWorkspace = true
		case arg == "--config-dir" && i+1 < len(argv):
			i++ // already resolved
		case strings.HasPrefix(arg, "--config-dir="):
			// already resolved
		case strings.HasPrefix(arg, "-"):
			// ignore unknown flags
		default:
			positional = append(positional, arg)
		}
	}

	if len(positional) > 0 {
		target = positional[0]
	}
	if target == "" {
		target = "user"
	}
	if target != "user" && target != "workdir" {
		return nil, fmt.Errorf("config-edit target must be 'user' or 'workdir', got %q", target)
	}

	var workspace *string
	if noWorkspace {
		workspace = nil
	} else if workspaceFlag != "" {
		abs, err := filepath.Abs(workspaceFlag)
		if err != nil {
			return nil, err
		}
		workspace = &abs
	} else {
		workspace = preWorkspace
	}

	return &Config{
		CWD:              cwd,
		Home:             home,
		ConfigDir:        configDir,
		Workspace:        workspace,
		Subcommand:       "config_edit",
		ConfigEditTarget: target,
		Verbose:          verbose,
	}, nil
}

func formatHelp() string {
	return `Usage: csb [flags] [subcommand] [-- args...]

Subcommands:
  run          Run a command in an isolated container (default)
  clean        Remove all csb images and labeled volumes
  config-edit  Open the user or workdir config file in $VISUAL/$EDITOR/vi

Flags:
  --workspace PATH          host directory to mount as the workspace (default: CWD)
  --no-workspace            ephemeral workspace, no host directory mounted
  --rebuild                 force a full image rebuild
  --reset-home              remove and recreate the home volume
  -v, --verbose             print the run command before executing
  --config-dir PATH         host directory for csb config (default: ~/.config/csb)
  --tmux / --no-tmux        run inside tmux
  --tty / --no-tty          allocate a TTY (default: auto-detect)
  --mount SRC:DST[:MODE]    extra bind mounts
  --runtime auto|docker|podman  container runtime (default: auto)
  --base-image IMAGE        base image (default: debian:stable-slim)
  --nested-podman           install podman inside the container
  --addon NAME              addon to install (repeatable; default: mise)
  --env-forward NAME        host env var to forward into the container
  --env KEY=VALUE           env var to inject into the container
  --publish SPEC            publish a container port to the host
  --host-network            use host networking
  --host-exec               start host exec broker
  --host-exec-allow RULE    allowed host command pattern
  --help-full               show all config options, env vars, and example YAML
`
}

func formatHelpFull() string {
	lines := []string{
		"csb — full configuration reference",
		"",
		"OPTIONS",
		"",
	}

	type optDoc struct {
		flag    string
		envVar  string
		yamlKey string
		help    string
	}
	docs := []optDoc{
		{"--tmux / --no-tmux", "(none)", "tmux", "run inside tmux"},
		{"--tty / --no-tty", "(none)", "tty", "allocate a TTY (default: auto-detect from stdin)"},
		{"--mount VALUE  (repeatable)", "(none)", "mount", "extra bind mounts (format: src:dst[:mode])"},
		{"--runtime {auto|docker|podman}", "CSB_RUNTIME", "runtime", "container runtime to use"},
		{"--base-image BASE_IMAGE", "CSB_BASE_IMAGE", "base_image", "base image for the container (default: debian:stable-slim)"},
		{"--nested-podman / --no-nested-podman", "CSB_NESTED_PODMAN", "nested_podman", "install and configure podman inside the container"},
		{"--addon NAME  (repeatable)", "(none)", "addons", "addon to install (repeatable; default: mise)"},
		{"(no CLI flag)", "CSB_HOME_VOLUME", "home_volume", "named volume for the container home (default: csb-home)"},
		{"(no CLI flag)", "CSB_IMAGE", "image", "override the image name/tag"},
		{"--env-forward VALUE  (repeatable)", "CSB_ENV_FORWARD  (space-separated)", "env_forward", "host env var names to forward into the container"},
		{"--env VALUE  (repeatable)", "CSB_ENV  (space-separated)", "env", "KEY=VALUE pairs to inject into the container environment"},
		{"--publish VALUE  (repeatable)", "CSB_PUBLISH  (space-separated)", "publish", "publish a container port to the host"},
		{"--host-network / --no-host-network", "CSB_HOST_NETWORK", "host_network", "use host networking"},
		{"--host-exec / --no-host-exec", "CSB_HOST_EXEC", "host_exec_enabled", "start host exec broker"},
		{"--host-exec-allow VALUE  (repeatable)", "(none)", "host_exec_allow", "allowed host command pattern"},
		{"(no CLI flag)", "(none)", "host_exec_bind", "host exec broker listen address"},
	}

	for _, d := range docs {
		lines = append(lines, "  "+d.flag)
		lines = append(lines, "    env : "+d.envVar)
		lines = append(lines, "    yaml: "+d.yamlKey)
		if d.help != "" {
			lines = append(lines, "    "+d.help)
		}
		lines = append(lines, "")
	}

	lines = append(lines, "EXAMPLE config.yaml", "")
	for _, line := range strings.Split(renderConfigTemplate(), "\n") {
		lines = append(lines, "  "+line)
	}
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}
