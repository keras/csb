package csb

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// bootParams are the locator flags that must be resolved before any YAML is
// loaded, because they determine where the YAML lives (config dir) and which
// per-workdir overlay applies (workspace). They flow through the same struct-tag
// machinery as Options, but carry no yaml: tag and resolve from CLI > env >
// default only — putting either in YAML would be circular or meaningless.
type bootParams struct {
	ConfigDir   string `flag:"config-dir"   env:"CSB_CONFIG_DIR" default:"@defaultConfigDir" help:"host directory for csb config (default: ~/.config/csb)" metavar:"PATH"`
	Workspace   string `flag:"workspace"    env:"CSB_WORKSPACE"  default:"@cwd"             help:"host directory to mount as the workspace (default: CWD)" metavar:"PATH"`
	NoWorkspace bool   `flag:"no-workspace"                                                 help:"ephemeral workspace, no host directory mounted"`
}

// runFlags are execution-control flags for the run subcommand. They map to
// Config (not Options/YAML), but use the same flag: machinery so each flag's
// name and help text live in exactly one place. -v/--verbose stays bespoke
// because it carries a short alias the parser does not model.
type runFlags struct {
	Rebuild bool `flag:"rebuild" help:"force a full image rebuild"`
}

func init() {
	// Default funcs backing bootParams' @defaults. Registered here (not in the
	// options.go literal) so the locator machinery stays self-contained.
	defaultFuncs["cwd"] = func() any {
		cwd, _ := os.Getwd()
		return cwd
	}
	defaultFuncs["defaultConfigDir"] = func() any {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".config", "csb")
	}
	for _, t := range []reflect.Type{reflect.TypeOf(bootParams{}), reflect.TypeOf(runFlags{})} {
		if err := validateTags(t); err != nil {
			panic("csb flags: " + err.Error())
		}
	}
}

// flagTakesValue reports, for a "--name" token, whether the flag consumes the
// following token as its value. It consults every struct that defines CLI flags
// so the subcommand scan never hard-codes which flags take values.
func flagTakesValue(flagName string) bool {
	for _, t := range []reflect.Type{reflect.TypeOf(bootParams{}), reflect.TypeOf(runFlags{}), reflect.TypeOf(Options{})} {
		for i := range t.NumField() {
			f := t.Field(i)
			if f.Tag.Get("flag") != "" && "--"+f.Tag.Get("flag") == flagName {
				return f.Type.Kind() != reflect.Bool
			}
		}
	}
	return false
}

// detectSubcommand scans argv left-to-right for the first non-flag token.
// Returns (subcommand, remainingArgv). Value tokens of value-taking flags are
// skipped (both --flag value and --flag=value forms). Unknown flags are treated
// as not taking a value.
func detectSubcommand(argv []string) (string, []string) {
	for i := 0; i < len(argv); i++ {
		token := argv[i]
		if token == "--" {
			break
		}
		if strings.HasPrefix(token, "-") {
			if eq := strings.IndexByte(token, '='); eq >= 0 && strings.HasPrefix(token, "--") {
				continue // value embedded; nothing to skip
			}
			if flagTakesValue(token) {
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

// parsedFlags is the result of a single walk over argv: collected CLI values for
// each flag struct plus the structural bits and command boundary.
type parsedFlags struct {
	bootVals    map[int]any
	runVals     map[int]any
	optVals     map[int]any
	verbose     bool
	positional  []string // config subcommand: action + targets
	passthrough []string // run subcommand: the command after csb's flags
}

// parseFlags walks argv once, classifying every token as a bootstrap flag, a
// run-control flag, an Options flag, a structural flag, a positional, or the
// start of the passthrough command. Positional and unknown-flag handling depend
// on the already-detected subcommand. Locator flags after the command boundary
// (-- or the first run positional) are left for the inner command, consistent
// with all other flags. run-control flags are only recognized for run.
func parseFlags(argv []string, subcommand string) (parsedFlags, error) {
	boot := newOptParserFor(reflect.TypeOf(bootParams{}))
	run := newOptParserFor(reflect.TypeOf(runFlags{}))
	opt := newOptParser()
	pf := parsedFlags{bootVals: boot.values, runVals: run.values, optVals: opt.values}
	passthroughStart := -1

loop:
	for i := 0; i < len(argv); i++ {
		arg := argv[i]

		switch {
		case arg == "--":
			passthroughStart = i + 1
			break loop
		case arg == "-v" || arg == "--verbose":
			pf.verbose = true
			continue
		case arg == "--help-config":
			if subcommand == "run" {
				fmt.Print(formatHelpFull())
				os.Exit(0)
			}
			continue
		case arg == "-h" || arg == "--help":
			switch subcommand {
			case "config":
				fmt.Print(formatConfigHelp())
				os.Exit(0)
			case "run":
				fmt.Print(formatHelp())
				os.Exit(0)
			}
			continue // clean has no dedicated help screen
		}

		if ni, handled, err := boot.handle(arg, argv, i); err != nil {
			return pf, err
		} else if handled {
			i = ni
			continue
		}
		// run-control flags apply only to run; elsewhere they fall through to the
		// unknown-flag path and are ignored.
		if subcommand == "run" {
			if ni, handled, err := run.handle(arg, argv, i); err != nil {
				return pf, err
			} else if handled {
				i = ni
				continue
			}
		}
		if ni, handled, err := opt.handle(arg, argv, i); err != nil {
			return pf, err
		} else if handled {
			i = ni
			continue
		}

		if strings.HasPrefix(arg, "-") {
			if subcommand == "run" {
				return pf, fmt.Errorf("unknown flag: %s", arg)
			}
			continue // config/clean ignore unknown flags
		}

		// First positional.
		switch subcommand {
		case "run":
			passthroughStart = i
			break loop
		case "config":
			pf.positional = append(pf.positional, arg)
		}
		// clean ignores positionals
	}

	if passthroughStart >= 0 {
		pf.passthrough = argv[passthroughStart:]
	}
	return pf, nil
}

// ParseArgs parses CLI arguments and returns a Config.
func ParseArgs(argv []string) (*Config, error) {
	subcommand, subArgv := detectSubcommand(argv)

	pf, err := parseFlags(subArgv, subcommand)
	if err != nil {
		return nil, err
	}

	// Resolve the locator flags (CLI > env > default; no YAML) so we know where
	// to load the YAML from and which workdir overlay applies.
	var boot bootParams
	if err := resolveInto(&boot, pf.bootVals); err != nil {
		return nil, err
	}

	configDir, err := filepath.Abs(expandUser(boot.ConfigDir))
	if err != nil {
		return nil, err
	}

	var workspace *string
	if !boot.NoWorkspace {
		abs, err := filepath.Abs(boot.Workspace)
		if err != nil {
			return nil, err
		}
		workspace = &abs
	}

	// Load YAML configs.
	userYAML, err := loadYAMLConfig(configDir)
	if err != nil {
		return nil, fmt.Errorf("loading user config: %w", err)
	}
	workdirYAML := map[string]interface{}{}
	if workspace != nil {
		workdirYAML, err = loadWorkdirYAMLConfig(configDir, *workspace)
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
		return buildCleanConfig(pf, configDir, workspace, yamlLayers, cwd, home)
	case "config":
		return buildConfigConfig(pf, configDir, workspace, yamlLayers, cwd, home)
	default:
		return buildRunConfig(pf, configDir, workspace, yamlLayers, cwd, home)
	}
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

func buildRunConfig(pf parsedFlags, configDir string, workspace *string, yamlLayers []map[string]interface{}, cwd, home string) (*Config, error) {
	resolved, err := resolveOptions(pf.optVals, yamlLayers...)
	if err != nil {
		return nil, err
	}

	var rf runFlags
	if err := resolveInto(&rf, pf.runVals); err != nil {
		return nil, err
	}

	return &Config{
		CWD:             cwd,
		Home:            home,
		ConfigDir:       configDir,
		Workspace:       workspace,
		Subcommand:      "run",
		Rebuild:         rf.Rebuild,
		Verbose:         pf.verbose,
		PassthroughArgs: pf.passthrough,
		Options:         resolved,
	}, nil
}

func buildCleanConfig(pf parsedFlags, configDir string, workspace *string, yamlLayers []map[string]interface{}, cwd, home string) (*Config, error) {
	// clean ignores CLI Options flags; its Options come from yaml/env/default.
	resolved, err := resolveOptions(nil, yamlLayers...)
	if err != nil {
		return nil, err
	}

	return &Config{
		CWD:        cwd,
		Home:       home,
		ConfigDir:  configDir,
		Workspace:  workspace,
		Subcommand: "clean",
		Verbose:    pf.verbose,
		Options:    resolved,
	}, nil
}

func buildConfigConfig(pf parsedFlags, configDir string, workspace *string, yamlLayers []map[string]interface{}, cwd, home string) (*Config, error) {
	if len(pf.positional) == 0 {
		fmt.Print(formatConfigHelp())
		os.Exit(0)
	}

	action := pf.positional[0]
	targets := pf.positional[1:]

	editTarget := "user"
	showTarget := "config"
	switch action {
	case "show":
		if len(targets) > 0 {
			showTarget = targets[0]
		}
		if showTarget != "config" && showTarget != "context" {
			return nil, fmt.Errorf("config show target must be 'config' or 'context', got %q", showTarget)
		}
	case "edit":
		if len(targets) > 0 {
			editTarget = targets[0]
		}
		if editTarget != "user" && editTarget != "workdir" {
			return nil, fmt.Errorf("config edit target must be 'user' or 'workdir', got %q", editTarget)
		}
	case "status", "update":
		// No targets or flags.
	default:
		return nil, fmt.Errorf("unknown config action %q; expected 'show', 'edit', 'status', or 'update'", action)
	}

	resolved, err := resolveOptions(pf.optVals, yamlLayers...)
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
		Verbose:          pf.verbose,
		Options:          resolved,
	}, nil
}

func formatConfigHelp() string {
	header := `Usage: csb config <action> [options]

Actions:
  show [config|context]   Print resolved config YAML, or list the docker
                          build context (default: config). When the
                          context listing is piped, the raw tar is sent.
  edit [user|workdir]     Open a config file in $VISUAL/$EDITOR/vi (default: user)
  status                  Show which shipped resources (Dockerfile, addons)
                          differ from this binary's versions
  update                  Interactively overwrite shipped resources with this
                          binary's versions (edited files are backed up to .bak)

Options:
`
	rows := renderRows(optionRows(reflect.TypeOf(bootParams{}), false))
	return header + strings.Join(rows, "\n") + "\n"
}

func formatHelp() string {
	header := `Usage: csb [flags] [subcommand] [-- args...]

Subcommands:
  run               Run a command in an isolated container (default)
  clean             Interactively select csb images and volumes to remove
  config show       Print the fully resolved configuration as YAML
  config show context  List the docker build context that would be sent
  config edit       Open the user or workdir config file in $VISUAL/$EDITOR/vi
  config edit workdir  Edit the per-workspace config file
  config status     Show which shipped resources differ from this binary
  config update     Interactively update shipped resources (Dockerfile, addons)

Flags:
`
	// Locator flags + run-control flags + the bespoke -v + Options flags +
	// --help-config, rendered together so the column alignment is shared.
	rows := optionRows(reflect.TypeOf(bootParams{}), false)
	rows = append(rows, optionRows(reflect.TypeOf(runFlags{}), false)...)
	rows = append(rows, helpRow{"-v, --verbose", "verbose logging"})
	rows = append(rows, optionRows(reflect.TypeOf(Options{}), false)...)
	rows = append(rows, helpRow{"--help-config", "show all config options, env vars, and example YAML"})
	return header + strings.Join(renderRows(rows), "\n") + "\n"
}

func formatHelpFull() string {
	lines := []string{
		"csb — full configuration reference",
		"",
		"OPTIONS",
		"",
	}
	lines = append(lines, optionFullLines(reflect.TypeOf(bootParams{}))...)
	lines = append(lines, optionFullLines(reflect.TypeOf(runFlags{}))...)
	lines = append(lines, optionHelpFullLines()...)
	lines = append(lines, "EXAMPLE config.yaml", "")
	for _, line := range strings.Split(renderConfigTemplate(), "\n") {
		lines = append(lines, "  "+line)
	}
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}
