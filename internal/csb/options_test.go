package csb

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func resolveWith(t *testing.T, cli map[int]any, yaml map[string]any) Options {
	t.Helper()
	opts, err := resolveOptions(cli, yaml)
	require.NoError(t, err)
	return opts
}

func resolveErr(t *testing.T, cli map[int]any, yaml map[string]any) error {
	t.Helper()
	_, err := resolveOptions(cli, yaml)
	return err
}

// fieldIdx returns the Options field index for the given field name.
func fieldIdx(name string) int {
	t := reflect.TypeOf(Options{})
	for i := range t.NumField() {
		if t.Field(i).Name == name {
			return i
		}
	}
	panic("unknown Options field: " + name)
}

// ── validateTags ─────────────────────────────────────────────────────────────

func TestValidateTags_Good(t *testing.T) {
	assert.NoError(t, validateTags(reflect.TypeOf(Options{})))
}

func TestValidateTags_UnknownDefault(t *testing.T) {
	type bad struct {
		X bool `default:"@noSuchFunc"`
	}
	assert.Error(t, validateTags(reflect.TypeOf(bad{})))
}

func TestValidateTags_LiteralDefaultOnBool(t *testing.T) {
	type bad struct {
		X bool `default:"true"`
	}
	assert.Error(t, validateTags(reflect.TypeOf(bad{})))
}

func TestValidateTags_ParseOnNonSlice(t *testing.T) {
	type bad struct {
		X string `parse:"mount"`
	}
	assert.Error(t, validateTags(reflect.TypeOf(bad{})))
}

func TestValidateTags_UnknownParse(t *testing.T) {
	type bad struct {
		X []string `parse:"noSuchParser"`
	}
	assert.Error(t, validateTags(reflect.TypeOf(bad{})))
}

func TestValidateTags_EnvsepWithoutEnv(t *testing.T) {
	type bad struct {
		X []string `envsep:"fields"`
	}
	assert.Error(t, validateTags(reflect.TypeOf(bad{})))
}

func TestValidateTags_EnvsepOnNonSlice(t *testing.T) {
	type bad struct {
		X string `env:"FOO" envsep:"fields"`
	}
	assert.Error(t, validateTags(reflect.TypeOf(bad{})))
}

func TestValidateTags_UnknownValidate(t *testing.T) {
	type bad struct {
		X []string `validate:"noSuchValidator"`
	}
	assert.Error(t, validateTags(reflect.TypeOf(bad{})))
}

// ── Precedence: CLI > env > yaml > default ───────────────────────────────────

func TestPrecedence_StringCLIWins(t *testing.T) {
	t.Setenv("CSB_RUNTIME", "docker")
	opts := resolveWith(t, map[int]any{fieldIdx("Runtime"): "auto"}, map[string]any{"runtime": "podman"})
	assert.Equal(t, "auto", opts.Runtime)
}

func TestPrecedence_StringEnvOverYAML(t *testing.T) {
	t.Setenv("CSB_RUNTIME", "docker")
	opts := resolveWith(t, nil, map[string]any{"runtime": "podman"})
	assert.Equal(t, "docker", opts.Runtime)
}

func TestPrecedence_StringYAMLOverDefault(t *testing.T) {
	opts := resolveWith(t, nil, map[string]any{"runtime": "podman"})
	assert.Equal(t, "podman", opts.Runtime)
}

func TestPrecedence_StringDefault(t *testing.T) {
	assert.Equal(t, "auto", resolveWith(t, nil, nil).Runtime)
}

func TestPrecedence_BoolCLIWins(t *testing.T) {
	t.Setenv("CSB_NESTED_PODMAN", "true")
	opts := resolveWith(t, map[int]any{fieldIdx("NestedPodman"): false}, map[string]any{"nested_podman": true})
	assert.False(t, opts.NestedPodman)
}

func TestPrecedence_BoolEnvOverYAML(t *testing.T) {
	t.Setenv("CSB_NESTED_PODMAN", "1")
	opts := resolveWith(t, nil, map[string]any{"nested_podman": false})
	assert.True(t, opts.NestedPodman)
}

func TestPrecedence_SliceCLIWins(t *testing.T) {
	t.Setenv("CSB_ENV_FORWARD", "FROM_ENV")
	opts := resolveWith(t,
		map[int]any{fieldIdx("EnvForward"): []string{"FROM_CLI"}},
		map[string]any{"env_forward": []any{"FROM_YAML"}},
	)
	assert.Equal(t, []string{"FROM_CLI"}, opts.EnvForward)
}

func TestPrecedence_SliceEnvOverYAML(t *testing.T) {
	t.Setenv("CSB_ENV_FORWARD", "A B")
	opts := resolveWith(t, nil, map[string]any{"env_forward": []any{"FROM_YAML"}})
	assert.Equal(t, []string{"A", "B"}, opts.EnvForward)
}

// ── envsep:"fields" ──────────────────────────────────────────────────────────

func TestEnvsep_SplitsOnWhitespace(t *testing.T) {
	t.Setenv("CSB_ENV_FORWARD", "A  B  C")
	assert.Equal(t, []string{"A", "B", "C"}, resolveWith(t, nil, nil).EnvForward)
}

func TestEnvsep_PublishSpaceSeparated(t *testing.T) {
	t.Setenv("CSB_PUBLISH", "8080:8080 9090:9090")
	assert.Equal(t, []string{"8080:8080", "9090:9090"}, resolveWith(t, nil, nil).Publish)
}

// ── Non-nil slice defaults ────────────────────────────────────────────────────

func TestSlices_NeverNil(t *testing.T) {
	opts := resolveWith(t, nil, nil)
	assert.NotNil(t, opts.EnvForward)
	assert.NotNil(t, opts.EnvInject)
	assert.NotNil(t, opts.Publish)
	assert.NotNil(t, opts.HostExecAllow)
	assert.NotNil(t, opts.Mount)
}

// ── validate:"publish" ───────────────────────────────────────────────────────

func TestPublishValidate_ValidSpec(t *testing.T) {
	cli := map[int]any{fieldIdx("Publish"): []string{"8080:8080", "127.0.0.1:5432:5432"}}
	assert.NoError(t, resolveErr(t, cli, nil))
}

func TestPublishValidate_InvalidSpec(t *testing.T) {
	assert.Error(t, resolveErr(t, map[int]any{fieldIdx("Publish"): []string{"bogus"}}, nil))
}

func TestPublishValidate_InvalidViaYAML(t *testing.T) {
	assert.Error(t, resolveErr(t, nil, map[string]any{"publish": []any{"not-a-port"}}))
}

// ── parse:"mount" ────────────────────────────────────────────────────────────

func TestMount_ParsedFromCLI(t *testing.T) {
	cli := map[int]any{fieldIdx("Mount"): []string{"/src:/dst:ro", "/a:/b"}}
	opts := resolveWith(t, cli, nil)
	require.Len(t, opts.Mount, 2)
	assert.Equal(t, Mount{Src: "/src", Dst: "/dst", Readonly: true}, opts.Mount[0])
}

func TestMount_ParseErrorPropagated(t *testing.T) {
	assert.Error(t, resolveErr(t, map[int]any{fieldIdx("Mount"): []string{"bogus-no-colon"}}, nil))
}

func TestMount_ParsedFromYAML(t *testing.T) {
	opts := resolveWith(t, nil, map[string]any{"mount": []any{"/x:/y:ro"}})
	require.Len(t, opts.Mount, 1)
	assert.Equal(t, "/x", opts.Mount[0].Src)
}

// ── optParser.handle ─────────────────────────────────────────────────────────

func TestOptParser_BoolFlag(t *testing.T) {
	p := newOptParser()
	newI, handled, err := p.handle("--tmux", []string{"--tmux"}, 0)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, 0, newI)
	assert.Equal(t, true, p.values[fieldIdx("UseTmux")])
}

func TestOptParser_BoolNoFlag(t *testing.T) {
	p := newOptParser()
	_, handled, err := p.handle("--no-tmux", []string{"--no-tmux"}, 0)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, false, p.values[fieldIdx("UseTmux")])
}

func TestOptParser_BoolNoFlagWithEquals_Errors(t *testing.T) {
	_, _, err := newOptParser().handle("--no-tmux=true", []string{"--no-tmux=true"}, 0)
	assert.Error(t, err)
}

func TestOptParser_StringFlagSpaceSep(t *testing.T) {
	p := newOptParser()
	newI, handled, err := p.handle("--runtime", []string{"--runtime", "docker"}, 0)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, 1, newI)
	assert.Equal(t, "docker", p.values[fieldIdx("Runtime")])
}

func TestOptParser_StringFlagEqualsSep(t *testing.T) {
	p := newOptParser()
	newI, handled, err := p.handle("--runtime=podman", []string{"--runtime=podman"}, 0)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, 0, newI)
	assert.Equal(t, "podman", p.values[fieldIdx("Runtime")])
}

func TestOptParser_SliceRepeatable(t *testing.T) {
	argv := []string{"--addon", "foo", "--addon", "bar"}
	p := newOptParser()
	for i := 0; i < len(argv); {
		newI, _, err := p.handle(argv[i], argv, i)
		require.NoError(t, err)
		i = newI + 1
	}
	assert.Equal(t, []string{"foo", "bar"}, p.values[fieldIdx("Addons")])
}

func TestOptParser_SliceEqualsForm(t *testing.T) {
	argv := []string{"--addon=foo", "--addon=bar"}
	p := newOptParser()
	for i := 0; i < len(argv); {
		newI, _, err := p.handle(argv[i], argv, i)
		require.NoError(t, err)
		i = newI + 1
	}
	assert.Equal(t, []string{"foo", "bar"}, p.values[fieldIdx("Addons")])
}

func TestOptParser_UnknownFlagNotHandled(t *testing.T) {
	_, handled, err := newOptParser().handle("--no-such-flag", []string{"--no-such-flag"}, 0)
	assert.NoError(t, err)
	assert.False(t, handled)
}

func TestOptParser_MissingArgErrors(t *testing.T) {
	_, _, err := newOptParser().handle("--runtime", []string{"--runtime"}, 0)
	assert.Error(t, err)
}

// ── ParseArgs: flag requires argument ────────────────────────────────────────

func TestParseArgs_ConfigDirLastArg(t *testing.T) {
	_, err := ParseArgs([]string{"--config-dir"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--config-dir")
}

func TestParseArgs_WorkspaceLastArg(t *testing.T) {
	_, err := ParseArgs([]string{"--workspace"})
	assert.Error(t, err)
}

// ── ParseArgs: --no-X overrides yaml ─────────────────────────────────────────

func TestParseArgs_NoFlagOverridesYAML(t *testing.T) {
	cfg, err := ParseArgs([]string{"--config-dir", t.TempDir(), "--no-tmux", "--", "echo"})
	require.NoError(t, err)
	assert.False(t, cfg.UseTmux)
}

// ── renderConfigTemplate drift guard ─────────────────────────────────────────

func TestRenderConfigTemplate_ContainsAllYAMLKeys(t *testing.T) {
	tmpl := renderConfigTemplate()
	typ := reflect.TypeOf(Options{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Tag.Get("yaml") == "" || f.Tag.Get("example") == "" {
			continue
		}
		assert.Contains(t, tmpl, f.Tag.Get("yaml"), "missing yaml key for field %s", f.Name)
	}
}

// ── formatHelp ───────────────────────────────────────────────────────────────

func TestFormatHelp_NoTruncation(t *testing.T) {
	for _, line := range strings.Split(formatHelp(), "\n") {
		if strings.HasPrefix(line, "  --") {
			assert.Contains(t, line, "  ", "help line missing double-space separator: %q", line)
		}
	}
}

func TestFormatHelp_ContainsExpectedFlags(t *testing.T) {
	help := formatHelp()
	for _, want := range []string{"--tmux", "--no-tmux", "--nested-podman", "--no-nested-podman", "--host-exec-allow", "--env-forward"} {
		assert.Contains(t, help, want)
	}
}

// ── shQuote ──────────────────────────────────────────────────────────────────

func TestShQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"bash", "bash"},
		{"-l", "-l"},
		{"foo/bar", "foo/bar"},
		{"foo bar", "'foo bar'"},
		{"*.go", "'*.go'"},
		{"~/x", "'~/x'"},
		{"", "''"},
		{"it's", "'it'\\''s'"},
		{"$HOME", "'$HOME'"},
		{"a;b", "'a;b'"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, shQuote(tt.in), "shQuote(%q)", tt.in)
	}
}
