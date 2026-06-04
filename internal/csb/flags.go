package csb

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// detectSubcommand scans argv left-to-right for the first non-flag token.
// Returns (subcommand, remainingArgv).
// For flags that take a value (non-bool Options flags), the value token is
// skipped whether the flag appears as --flag value or --flag=value.
// Unknown flags are treated as not taking a value.
func detectSubcommand(argv []string) (string, []string) {
	flagIdx := buildFlagIndex(reflect.TypeOf(Options{}))
	t := reflect.TypeOf(Options{})

	for i := 0; i < len(argv); i++ {
		token := argv[i]
		if token == "--" {
			break
		}
		if strings.HasPrefix(token, "-") {
			// Determine the flag name (strip =value if present).
			flagName := token
			if eq := strings.IndexByte(token, '='); eq >= 0 && strings.HasPrefix(token, "--") {
				flagName = token[:eq]
				// Value is embedded; no next token to skip.
				continue
			}
			// Two-token form: check if this flag takes a value and skip next token.
			// Also skip values for structural flags not in the Options flag index.
			if idx, ok := flagIdx[flagName]; ok {
				kind := t.Field(idx).Type.Kind()
				if kind != reflect.Bool {
					i++ // skip value token
				}
			} else if flagName == "--config-dir" || flagName == "--workspace" {
				i++ // skip value token
			}
			continue
		}
		switch token {
		case "clean":
			return "clean", append(argv[:i:i], argv[i+1:]...)
		case "config", "config-edit":
			return "config", append(argv[:i:i], argv[i+1:]...)
		case "run":
			return "run", append(argv[:i:i], argv[i+1:]...)
		default:
			return "run", argv
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
	// Pass layers low→high: user first, then workdir.
	yamlLayers := []map[string]interface{}{userYAML, workdirYAML}

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()

	switch subcommand {
	case "clean":
		return parseCleanArgs(subArgv, configDir, preWorkspace, yamlLayers, cwd, home)
	case "config":
		return parseConfigArgs(subArgv, configDir, preWorkspace, yamlLayers, cwd, home)
	default:
		return parseRunArgs(subArgv, configDir, preWorkspace, yamlLayers, cwd, home)
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

func parseRunArgs(argv []string, configDir string, preWorkspace *string, yamlLayers []map[string]interface{}, cwd, home string) (*Config, error) {
	var workspaceFlag string
	var noWorkspace, rebuild, verbose bool

	parser := newOptParser()
	passthroughStart := -1

argLoop:
	for i := 0; i < len(argv); i++ {
		arg := argv[i]

		if arg == "--" {
			passthroughStart = i + 1
			break
		}

		switch {
		case arg == "--workspace":
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("flag --workspace requires an argument")
			}
			i++
			workspaceFlag = argv[i]
		case strings.HasPrefix(arg, "--workspace="):
			workspaceFlag = strings.TrimPrefix(arg, "--workspace=")
		case arg == "--no-workspace":
			noWorkspace = true
		case arg == "--rebuild":
			rebuild = true
		case arg == "-v", arg == "--verbose":
			verbose = true
		case arg == "--config-dir":
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("flag --config-dir requires an argument")
			}
			i++ // already resolved in preParse
		case strings.HasPrefix(arg, "--config-dir="):
			// already resolved in preParse
		case arg == "--help-full":
			fmt.Print(formatHelpFull())
			os.Exit(0)
		case arg == "-h", arg == "--help":
			fmt.Print(formatHelp())
			os.Exit(0)
		default:
			newI, handled, err := parser.handle(arg, argv, i)
			if err != nil {
				return nil, err
			}
			i = newI
			if !handled {
				if strings.HasPrefix(arg, "-") {
					return nil, fmt.Errorf("unknown flag: %s", arg)
				}
				// First positional ends csb flag parsing; rest is the command.
				passthroughStart = i
				break argLoop
			}
		}
	}

	var passthrough []string
	if passthroughStart >= 0 {
		passthrough = argv[passthroughStart:]
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

	resolved, err := resolveOptions(parser.values, yamlLayers...)
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
		Verbose:         verbose,
		PassthroughArgs: passthrough,
		Options:         resolved,
	}, nil
}

func parseCleanArgs(argv []string, configDir string, preWorkspace *string, yamlLayers []map[string]interface{}, cwd, home string) (*Config, error) {
	verbose := false
	for _, arg := range argv {
		if arg == "-v" || arg == "--verbose" {
			verbose = true
		}
	}

	resolved, err := resolveOptions(nil, yamlLayers...)
	if err != nil {
		return nil, err
	}

	return &Config{
		CWD:        cwd,
		Home:       home,
		ConfigDir:  configDir,
		Workspace:  preWorkspace,
		Subcommand: "clean",
		Verbose:    verbose,
		Options:    resolved,
	}, nil
}

func parseConfigArgs(argv []string, configDir string, preWorkspace *string, yamlLayers []map[string]interface{}, cwd, home string) (*Config, error) {
	verbose := false
	var workspaceFlag string
	noWorkspace := false

	var positional []string
	parser := newOptParser()

	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "-h", arg == "--help":
			fmt.Print(formatConfigHelp())
			os.Exit(0)
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
		default:
			newI, handled, err := parser.handle(arg, argv, i)
			if err != nil {
				return nil, err
			}
			i = newI
			if !handled {
				if strings.HasPrefix(arg, "-") {
					// ignore unknown flags
				} else {
					positional = append(positional, arg)
				}
			}
		}
	}

	if len(positional) == 0 {
		fmt.Print(formatConfigHelp())
		os.Exit(0)
	}

	action := positional[0]
	positional = positional[1:]

	editTarget := "user"
	showTarget := "config"
	switch action {
	case "show":
		if len(positional) > 0 {
			showTarget = positional[0]
		}
		if showTarget != "config" && showTarget != "context" {
			return nil, fmt.Errorf("config show target must be 'config' or 'context', got %q", showTarget)
		}
	case "edit":
		if len(positional) > 0 {
			editTarget = positional[0]
		}
		if editTarget != "user" && editTarget != "workdir" {
			return nil, fmt.Errorf("config edit target must be 'user' or 'workdir', got %q", editTarget)
		}
	default:
		return nil, fmt.Errorf("unknown config action %q; expected 'show' or 'edit'", action)
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

	resolved, err := resolveOptions(parser.values, yamlLayers...)
	if err != nil {
		return nil, err
	}

	return &Config{
		CWD:              cwd,
		Home:             home,
		ConfigDir:        configDir,
		Workspace:        workspace,
		Subcommand:       "config",
		ConfigAction:     action,
		ConfigEditTarget: editTarget,
		ConfigShowTarget: showTarget,
		Verbose:          verbose,
		Options:          resolved,
	}, nil
}

func formatConfigHelp() string {
	return `Usage: csb config <action> [options]

Actions:
  show [config|context]   Print resolved config YAML, or list the docker
                          build context (default: config). When the
                          context listing is piped, the raw tar is sent.
  edit [user|workdir]     Open a config file in $VISUAL/$EDITOR/vi (default: user)

Options:
  --workspace PATH        workspace directory (default: CWD)
  --no-workspace          no workspace
  --config-dir PATH       csb config directory (default: ~/.config/csb)
`
}

func formatHelp() string {
	fixed := `Usage: csb [flags] [subcommand] [-- args...]

Subcommands:
  run               Run a command in an isolated container (default)
  clean             Interactively select csb images and volumes to remove
  config show       Print the fully resolved configuration as YAML
  config show context  List the docker build context that would be sent
  config edit       Open the user or workdir config file in $VISUAL/$EDITOR/vi
  config edit workdir  Edit the per-workspace config file

Flags:
  --workspace PATH          host directory to mount as the workspace (default: CWD)
  --no-workspace            ephemeral workspace, no host directory mounted
  --rebuild                 force a full image rebuild
  -v, --verbose             print the run command before executing
  --config-dir PATH         host directory for csb config (default: ~/.config/csb)
`
	optLines := strings.Join(optionHelpLines(), "\n")
	return fixed + optLines + "\n  --help-full               show all config options, env vars, and example YAML\n"
}

func formatHelpFull() string {
	lines := []string{
		"csb — full configuration reference",
		"",
		"OPTIONS",
		"",
	}
	lines = append(lines, optionHelpFullLines()...)
	lines = append(lines, "EXAMPLE config.yaml", "")
	for _, line := range strings.Split(renderConfigTemplate(), "\n") {
		lines = append(lines, "  "+line)
	}
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}
