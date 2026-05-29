package csb

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"strings"

	"golang.org/x/term"
)

// portRE validates a publish spec: [[host_ip:]host_port:]container_port[/tcp|udp|sctp]
var portRE = regexp.MustCompile(
	`^(?:` +
		`(?:\d{1,3}(?:\.\d{1,3}){3}|\[[0-9a-fA-F:]+\]):(?:\d+(?:-\d+)?)?:` +
		`|(?:\d+(?:-\d+)?):` +
		`)?` +
		`\d+(?:-\d+)?` +
		`(?:/(?:tcp|udp|sctp))?$`,
)

// Options holds all user-configurable container options.
// Struct tags drive CLI flag parsing, env lookup, YAML loading, defaults,
// validation, and help text — each option is defined exactly once.
//
// Tag reference:
//
//	flag:"name"      CLI flag name. bool → --name/--no-name pair. string/slice → --name VALUE.
//	                 Slice flags are repeatable. Omit for env/yaml-only options.
//	env:"VAR"        Environment variable. bool → BoolFromEnv. string → raw. slice → see envsep.
//	envsep:"fields"  Split env value on whitespace before assigning to a slice field.
//	yaml:"key"       YAML config key.
//	default:"value"  Literal default (string/slice only), or "@funcName" to call a registered defaultFunc.
//	                 For slices, a literal default is a single-element list.
//	parse:"name"     Element-parse func for []T fields (e.g. []Mount). Must register in parseFuncs.
//	validate:"name"  Post-resolution validation func. Must register in validateFuncs.
//	help:"text"      One-line description for help output.
//	metavar:"NAME"   Placeholder shown in short help for string/slice flags (default: "VALUE").
//	example:"text"   Example value for the generated config template. Use literal \n as line separator.
type Options struct {
	UseTmux      bool     `flag:"tmux"            yaml:"tmux"             example:"true"                                 help:"run inside tmux"`
	UseTTY       bool     `flag:"tty"             yaml:"tty"              default:"@autoTTY"    example:"true"         help:"allocate a TTY (default: auto-detect from stdin)"`
	DefaultShell string   `flag:"shell"           yaml:"default_shell"    default:"bash"        example:"zsh"          help:"shell for new tmux windows, $SHELL, and default startup command"`
	DefaultCmd   []string `yaml:"default_cmd"                             example:"[vim]"       help:"startup command (default: <default_shell> -l; overridden by positional args)"`
	Addons       []string `flag:"addon"           yaml:"addons"           default:"mise sudo"   example:"[mise, sudo, gui]" help:"addon to install (NAME [ARGS...])"     metavar:"SPEC"`
	Arch         string   `flag:"arch"            env:"CSB_ARCH"          yaml:"arch"             default:"@hostArch"   example:"arm64"        validate:"arch"   help:"container arch (amd64|arm64); requires QEMU/binfmt on host when not host's arch"  metavar:"ARCH"`
	Mount        []Mount  `flag:"mount"           yaml:"mount"            parse:"mount"         example:"\n- ~/.ssh:~/.ssh:ro"  help:"extra bind mounts"                        metavar:"SRC:DST[:MODE]"`
	EnvForward   []string `flag:"env-forward"     env:"CSB_ENV_FORWARD"   envsep:"fields"       yaml:"env_forward"     example:"[MY_TOKEN, OTHER_VAR]"  help:"host env var names to forward into the container"  metavar:"NAME"`
	EnvInject    []string `flag:"env"             env:"CSB_ENV"           envsep:"fields"       yaml:"env"             example:"[MY_VAR=hello, DEBUG=1]"  help:"KEY=VALUE pairs to inject into the container"  metavar:"KEY=VALUE"`
	Publish      []string `flag:"publish"         env:"CSB_PUBLISH"       envsep:"fields"       yaml:"publish"         validate:"publish"     example:"\n- 8080:8080\n- 127.0.0.1:5432:5432"  help:"publish a container port to the host"  metavar:"SPEC"`

	HomeVolume      string   `env:"CSB_HOME_VOLUME"  yaml:"home_volume"      default:"csb-home"    example:"csb-home"     help:"named volume for the container home"`
	Image           string   `env:"CSB_IMAGE"        yaml:"image"            example:"my-custom:latest"                    help:"override the image name/tag"`
	Runtime         string   `flag:"runtime"         env:"CSB_RUNTIME"       yaml:"runtime"        default:"auto"         example:"auto"         help:"container runtime to use"                 metavar:"auto|docker|podman"`
	HostNetwork     bool     `flag:"host-network"    env:"CSB_HOST_NETWORK"  yaml:"host_network"   example:"false"        help:"use host networking"`
	HostExecEnabled bool     `flag:"host-exec"       env:"CSB_HOST_EXEC"     yaml:"host_exec_enabled"  example:"false"    help:"start host exec broker"`
	HostExecAllow   []string `flag:"host-exec-allow" yaml:"host_exec_allow"  example:"\n- open *\n- git log **"            help:"allowed host command pattern"              metavar:"RULE"`
	HostExecBind    string   `yaml:"host_exec_bind"  default:"0.0.0.0:0"     example:"0.0.0.0:0"   help:"host exec broker listen address"`
}

// defaultFuncs backs "@name" defaults in struct tags.
var defaultFuncs = map[string]func() any{
	"autoTTY":  func() any { return term.IsTerminal(int(os.Stdin.Fd())) },
	"hostArch": func() any { return runtime.GOARCH },
}

// parseFuncs backs parse:"name" tags; each func parses a raw string into the slice element type.
var parseFuncs = map[string]func(string) (any, error){
	"mount": func(s string) (any, error) { return ParseMount(s) },
}

// validateFuncs backs validate:"name" tags; called after all sources are resolved.
var validateFuncs = map[string]func(any) error{
	"publish": func(v any) error {
		for _, spec := range v.([]string) {
			if !portRE.MatchString(spec) {
				return fmt.Errorf("invalid publish spec %q; expected [[host_ip:]host_port:]container_port[/tcp|udp|sctp]", spec)
			}
		}
		return nil
	},
	"arch": func(v any) error {
		s := v.(string)
		if s != "amd64" && s != "arm64" {
			return fmt.Errorf("invalid arch %q; expected amd64 or arm64", s)
		}
		return nil
	},
}

// validateTags checks that every tag reference in t resolves and that tag/kind combinations
// are valid. Returns an error describing the first problem found.
func validateTags(t reflect.Type) error {
	for i := range t.NumField() {
		f := t.Field(i)
		kind := f.Type.Kind()

		if def := f.Tag.Get("default"); def != "" {
			if strings.HasPrefix(def, "@") {
				if _, ok := defaultFuncs[def[1:]]; !ok {
					return fmt.Errorf("field %s: unknown @default %q", f.Name, def)
				}
			} else {
				// Literal defaults are only meaningful for string and slice fields.
				if kind != reflect.String && kind != reflect.Slice {
					return fmt.Errorf("field %s: literal default %q not supported for kind %s; use @funcName", f.Name, def, kind)
				}
			}
		}

		if name := f.Tag.Get("parse"); name != "" {
			if kind != reflect.Slice {
				return fmt.Errorf("field %s: parse: tag requires a slice field, got %s", f.Name, kind)
			}
			if _, ok := parseFuncs[name]; !ok {
				return fmt.Errorf("field %s: unknown parse %q", f.Name, name)
			}
		}

		if name := f.Tag.Get("validate"); name != "" {
			if _, ok := validateFuncs[name]; !ok {
				return fmt.Errorf("field %s: unknown validate %q", f.Name, name)
			}
		}

		if f.Tag.Get("envsep") != "" {
			if kind != reflect.Slice {
				return fmt.Errorf("field %s: envsep: tag requires a slice field, got %s", f.Name, kind)
			}
			if f.Tag.Get("env") == "" {
				return fmt.Errorf("field %s: envsep: tag requires an env: tag", f.Name)
			}
		}
	}
	return nil
}

func init() {
	if err := validateTags(reflect.TypeOf(Options{})); err != nil {
		panic("options: " + err.Error())
	}
}

// optParser parses CLI argv for Options flags, accumulating values by field index.
type optParser struct {
	t       reflect.Type
	flagIdx map[string]int
	values  map[int]any
}

func newOptParser() *optParser {
	t := reflect.TypeOf(Options{})
	return &optParser{
		t:       t,
		flagIdx: buildFlagIndex(t),
		values:  make(map[int]any),
	}
}

// handle processes one CLI argument at position i in argv.
// Returns (updated i, handled, error). handled=false means arg is not an options flag.
// All CLI slice values are stored as []string regardless of element type;
// resolveOptions applies any parse: func during resolution.
func (p *optParser) handle(arg string, argv []string, i int) (int, bool, error) {
	// --no-X negation for bool flags. Reject --no-X=value.
	if strings.HasPrefix(arg, "--no-") {
		if eqIdx := strings.IndexByte(arg, '='); eqIdx >= 0 {
			return i, true, fmt.Errorf("flag %s does not take a value", arg[:eqIdx])
		}
		base := "--" + arg[5:]
		if idx, ok := p.flagIdx[base]; ok && p.t.Field(idx).Type.Kind() == reflect.Bool {
			p.values[idx] = false
			return i, true, nil
		}
	}

	// Split --flag=value.
	flagName, eqVal, hasEq := arg, "", false
	if eq := strings.IndexByte(arg, '='); eq >= 0 && strings.HasPrefix(arg, "--") {
		flagName, eqVal, hasEq = arg[:eq], arg[eq+1:], true
	}

	idx, ok := p.flagIdx[flagName]
	if !ok {
		return i, false, nil
	}

	field := p.t.Field(idx)
	switch field.Type.Kind() {
	case reflect.Bool:
		p.values[idx] = true
		return i, true, nil
	case reflect.String:
		val, newI, err := nextArg(arg, argv, i, eqVal, hasEq)
		if err != nil {
			return i, true, err
		}
		p.values[idx] = val
		return newI, true, nil
	case reflect.Slice:
		val, newI, err := nextArg(arg, argv, i, eqVal, hasEq)
		if err != nil {
			return i, true, err
		}
		existing, _ := p.values[idx].([]string)
		p.values[idx] = append(existing, val)
		return newI, true, nil
	}
	return i, false, nil
}

// buildFlagIndex maps "--flag-name" to the corresponding Options field index.
func buildFlagIndex(t reflect.Type) map[string]int {
	m := make(map[string]int, t.NumField())
	for i := range t.NumField() {
		if tag := t.Field(i).Tag.Get("flag"); tag != "" {
			m["--"+tag] = i
		}
	}
	return m
}

// nextArg returns the value for a flag, advancing i past the value token if needed.
func nextArg(flag string, argv []string, i int, eqVal string, hasEq bool) (string, int, error) {
	if hasEq {
		return eqVal, i, nil
	}
	if i+1 >= len(argv) {
		return "", i, fmt.Errorf("flag %s requires an argument", flag)
	}
	return argv[i+1], i + 1, nil
}

// resolveOptions resolves final option values from CLI, environment, YAML config, and defaults.
// Precedence: CLI > env > yaml > default. cliValues maps Options field index to CLI-supplied value(s).
// cliValues may be nil (e.g. for subcommands that skip CLI parsing).
func resolveOptions(cliValues map[int]any, yamlCfg map[string]any) (Options, error) {
	var opts Options
	t := reflect.TypeOf(opts)
	v := reflect.ValueOf(&opts).Elem()

	for i := range t.NumField() {
		field := t.Field(i)
		fv := v.Field(i)

		envTag := field.Tag.Get("env")
		envsepTag := field.Tag.Get("envsep")
		yamlTag := field.Tag.Get("yaml")
		defaultTag := field.Tag.Get("default")
		parseTag := field.Tag.Get("parse")
		validateTag := field.Tag.Get("validate")

		set := false

		// 1. CLI
		if cliVal, ok := cliValues[i]; ok {
			set = true
			switch field.Type.Kind() {
			case reflect.Bool:
				fv.SetBool(cliVal.(bool))
			case reflect.String:
				fv.SetString(cliVal.(string))
			case reflect.Slice:
				if err := setSliceField(fv, field, cliVal.([]string), parseTag); err != nil {
					return opts, err
				}
			}
		}

		// 2. Environment variable
		if !set && envTag != "" {
			if env := os.Getenv(envTag); env != "" {
				set = true
				switch field.Type.Kind() {
				case reflect.Bool:
					fv.SetBool(BoolFromEnv(env))
				case reflect.String:
					fv.SetString(env)
				case reflect.Slice:
					var vals []string
					if envsepTag == "fields" {
						vals = strings.Fields(env)
					} else {
						vals = []string{env}
					}
					if err := setSliceField(fv, field, vals, parseTag); err != nil {
						return opts, err
					}
				}
			}
		}

		// 3. YAML config
		// Note: yamlString returns "" for missing or explicitly-empty keys, so
		// `image: ""` in yaml is treated as unset — consistent with pre-refactor behaviour.
		if !set && yamlTag != "" {
			switch field.Type.Kind() {
			case reflect.Bool:
				if val, ok := yamlBool(yamlCfg, yamlTag); ok {
					fv.SetBool(val)
					set = true
				}
			case reflect.String:
				if val := yamlString(yamlCfg, yamlTag, ""); val != "" {
					fv.SetString(val)
					set = true
				}
			case reflect.Slice:
				if specs, ok := yamlStringList(yamlCfg, yamlTag); ok {
					if err := setSliceField(fv, field, specs, parseTag); err != nil {
						return opts, err
					}
					set = true
				}
			}
		}

		// 4. Default
		if !set && defaultTag != "" {
			if err := applyDefault(fv, field, defaultTag, parseTag); err != nil {
				return opts, err
			}
		}

		// Ensure slices are never nil.
		// For []string fields without a parse: tag, setSliceField sets reflect.ValueOf(specs)
		// which is nil when specs is nil; the guard below catches that case.
		if field.Type.Kind() == reflect.Slice && fv.IsNil() {
			fv.Set(reflect.MakeSlice(field.Type, 0, 0))
		}

		// 5. Validate
		if validateTag != "" {
			if err := validateFuncs[validateTag](fv.Interface()); err != nil {
				return opts, err
			}
		}
	}

	return opts, nil
}

func setSliceField(fv reflect.Value, field reflect.StructField, specs []string, parseTag string) error {
	if parseTag == "" {
		fv.Set(reflect.ValueOf(specs))
		return nil
	}
	fn := parseFuncs[parseTag]
	result := reflect.MakeSlice(field.Type, 0, len(specs))
	for _, spec := range specs {
		val, err := fn(spec)
		if err != nil {
			return fmt.Errorf("invalid %s %q: %w", parseTag, spec, err)
		}
		result = reflect.Append(result, reflect.ValueOf(val))
	}
	fv.Set(result)
	return nil
}

func applyDefault(fv reflect.Value, field reflect.StructField, defaultTag, parseTag string) error {
	if strings.HasPrefix(defaultTag, "@") {
		val := defaultFuncs[defaultTag[1:]]()
		switch field.Type.Kind() {
		case reflect.Bool:
			fv.SetBool(val.(bool))
		case reflect.String:
			fv.SetString(val.(string))
		}
		return nil
	}
	switch field.Type.Kind() {
	case reflect.String:
		fv.SetString(defaultTag)
	case reflect.Slice:
		return setSliceField(fv, field, strings.Fields(defaultTag), parseTag)
	}
	return nil
}

// formatFlagStr returns the flag string for a field as shown in help output.
// When full is true, slice flags include "(repeatable)".
func formatFlagStr(f reflect.StructField, full bool) string {
	flagTag := f.Tag.Get("flag")
	if flagTag == "" {
		return "(no CLI flag)"
	}
	metavar := f.Tag.Get("metavar")
	if metavar == "" {
		metavar = "VALUE"
	}
	switch f.Type.Kind() {
	case reflect.Bool:
		return fmt.Sprintf("--%s / --no-%s", flagTag, flagTag)
	case reflect.Slice:
		if full {
			return fmt.Sprintf("--%s %s  (repeatable)", flagTag, metavar)
		}
		return fmt.Sprintf("--%s %s", flagTag, metavar)
	default:
		return fmt.Sprintf("--%s %s", flagTag, metavar)
	}
}

// optionHelpLines returns flag lines for the short help, one per Options field that has a flag: tag.
// Column width is computed dynamically so long bool-pair strings never truncate the help text.
func optionHelpLines() []string {
	t := reflect.TypeOf(Options{})

	width := 0
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Tag.Get("flag") == "" {
			continue
		}
		if w := len(formatFlagStr(f, false)); w > width {
			width = w
		}
	}

	var lines []string
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Tag.Get("flag") == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %-*s  %s", width, formatFlagStr(f, false), f.Tag.Get("help")))
	}
	return lines
}

// optionHelpFullLines returns the expanded option table for --help-full.
func optionHelpFullLines() []string {
	t := reflect.TypeOf(Options{})
	var lines []string
	for i := range t.NumField() {
		f := t.Field(i)
		envTag := f.Tag.Get("env")
		envsepTag := f.Tag.Get("envsep")
		yamlTag := f.Tag.Get("yaml")

		envStr := "(none)"
		if envTag != "" {
			envStr = envTag
			if envsepTag == "fields" {
				envStr += "  (space-separated)"
			}
		}

		lines = append(lines, "  "+formatFlagStr(f, true))
		lines = append(lines, "    env : "+envStr)
		if yamlTag != "" {
			lines = append(lines, "    yaml: "+yamlTag)
		}
		if help := f.Tag.Get("help"); help != "" {
			lines = append(lines, "    "+help)
		}
		lines = append(lines, "")
	}
	return lines
}
