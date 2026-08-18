package csb

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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
	assert.Equal(t, "/src", opts.Mount[0].Src)
	assert.Equal(t, "/dst", opts.Mount[0].Dst)
	assert.True(t, opts.Mount[0].Readonly)
}

func TestMount_ParseErrorPropagated(t *testing.T) {
	assert.Error(t, resolveErr(t, map[int]any{fieldIdx("Mount"): []string{"bogus-no-colon"}}, nil))
}

func TestMount_ParsedFromYAML(t *testing.T) {
	opts := resolveWith(t, nil, map[string]any{"mount": []any{"/x:/y:ro"}})
	require.Len(t, opts.Mount, 1)
	assert.Equal(t, "/x", opts.Mount[0].Src)
}

func TestMount_MarshalYAML_PreservesTildeAndDefaultsRo(t *testing.T) {
	opts := resolveWith(t, nil, map[string]any{"mount": []any{
		"~/.gitconfig:~/.gitconfig",
		"~/.ssh:~/.ssh:rw",
	}})
	out, err := yaml.Marshal(opts.Mount)
	require.NoError(t, err)
	got := strings.TrimSpace(string(out))
	assert.Contains(t, got, "- ~/.gitconfig:~/.gitconfig:ro")
	assert.Contains(t, got, "- ~/.ssh:~/.ssh:rw")
}

func TestMount_MarshalYAML_ProgrammaticFallsBackToFields(t *testing.T) {
	m := Mount{Src: "/a", Dst: "/b", Readonly: false}
	out, err := yaml.Marshal(m)
	require.NoError(t, err)
	assert.Equal(t, "/a:/b:rw", strings.TrimSpace(string(out)))
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

// ── ParseArgs: implicit passthrough after first positional ───────────────────

func TestParseArgs_ImplicitPassthrough(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cases := []struct {
		name     string
		argv     []string
		expected []string
	}{
		{"bare", []string{"uname", "-a"}, []string{"uname", "-a"}},
		{"run-explicit", []string{"run", "uname", "-a"}, []string{"uname", "-a"}},
		{"dash-dash", []string{"run", "--", "uname", "-a"}, []string{"uname", "-a"}},
		{"flag-before", []string{"--no-tmux", "uname", "-a", "--foo"}, []string{"uname", "-a", "--foo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseArgs(tc.argv)
			require.NoError(t, err)
			assert.Equal(t, "run", cfg.Subcommand)
			assert.Equal(t, tc.expected, cfg.PassthroughArgs)
		})
	}
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
	for _, want := range []string{"--tmux", "--no-tmux", "--host-exec-allow", "--env-forward"} {
		assert.Contains(t, help, want)
	}
}

// ── Additive slice resolution ─────────────────────────────────────────────────

// resolveWithLayers is like resolveWith but accepts multiple YAML layers (low→high).
func resolveWithLayers(t *testing.T, cli map[int]any, layers ...map[string]any) Options {
	t.Helper()
	opts, err := resolveOptions(cli, layers...)
	require.NoError(t, err)
	return opts
}

// resolveErrLayers is like resolveErr but accepts multiple YAML layers.
func resolveErrLayers(t *testing.T, cli map[int]any, layers ...map[string]any) error {
	t.Helper()
	_, err := resolveOptions(cli, layers...)
	return err
}

// yamlList builds a map[string]any with a []any list value, matching how yaml.v3 unmarshals.
func yamlList(key string, items ...string) map[string]any {
	list := make([]any, len(items))
	for i, s := range items {
		list[i] = s
	}
	return map[string]any{key: list}
}

// yamlEmptyList builds a map with an explicitly empty []any list.
func yamlEmptyList(key string) map[string]any {
	return map[string]any{key: []any{}}
}

// readStringSlice returns the named []string field from opts via reflection.
func readStringSlice(opts Options, fieldName string) []string {
	v := reflect.ValueOf(opts)
	return v.FieldByName(fieldName).Interface().([]string)
}

// TestAdditiveSlice_TableDriven consolidates the repetitive []string-slice resolution cases.
// Rows include original cases 1-11 plus new edge-case rows (fix #1, empty-CLI, publish strip).
func TestAdditiveSlice_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) // optional env setup (t.Setenv calls); may be nil
		cli     map[int]any        // nil if none
		layers  []map[string]any   // low→high; nil entries allowed
		field   string             // Options field to inspect
		want    []string           // expected resolved []string (ignored if wantErr != "")
		wantErr string             // substring to assert on error; "" = no error
	}{
		// ── original cases 1-11 (Addons) ──────────────────────────────────────
		{
			name:  "case1: nothing set => default [mise sudo]",
			field: "Addons",
			want:  []string{"mise", "sudo"},
		},
		{
			name:   "case2: user yaml replaces default",
			layers: []map[string]any{yamlList("addons", "gui")},
			field:  "Addons",
			want:   []string{"gui"},
		},
		{
			name:   "case3: workdir additive extends user",
			layers: []map[string]any{yamlList("addons", "mise", "sudo", "gui"), yamlList("addons", "+podman")},
			field:  "Addons",
			want:   []string{"mise", "sudo", "gui", "podman"},
		},
		{
			name:   "case4: workdir additive extends default when user unset",
			layers: []map[string]any{nil, yamlList("addons", "+podman")},
			field:  "Addons",
			want:   []string{"mise", "sudo", "podman"},
		},
		{
			name:   "case5: all layers additive",
			cli:    map[int]any{fieldIdx("Addons"): []string{"+tmux"}},
			layers: []map[string]any{yamlList("addons", "gui"), yamlList("addons", "+podman")},
			field:  "Addons",
			want:   []string{"gui", "podman", "tmux"},
		},
		{
			name:   "case6: bare CLI replaces all yaml layers",
			cli:    map[int]any{fieldIdx("Addons"): []string{"node"}},
			layers: []map[string]any{yamlList("addons", "gui"), yamlList("addons", "+podman")},
			field:  "Addons",
			want:   []string{"node"},
		},
		{
			name:   "case7: workdir empty list clears all",
			layers: []map[string]any{yamlList("addons", "gui"), yamlEmptyList("addons")},
			field:  "Addons",
			want:   []string{},
		},
		{
			name:   "case8: dedup keeps first occurrence",
			layers: []map[string]any{yamlList("addons", "mise", "sudo", "gui"), yamlList("addons", "+sudo")},
			field:  "Addons",
			want:   []string{"mise", "sudo", "gui"},
		},
		{
			name:    "case9: CLI mixed plain+additive errors with 'cannot mix'",
			cli:     map[int]any{fieldIdx("Addons"): []string{"gui", "+podman"}},
			field:   "Addons",
			wantErr: "cannot mix",
		},
		{
			name:    "case10: YAML mixed plain+additive errors with 'cannot mix'",
			layers:  []map[string]any{nil, yamlList("addons", "gui", "+podman")},
			field:   "Addons",
			wantErr: "cannot mix",
		},
		{
			name:   "case11: both layers bare, higher wins",
			layers: []map[string]any{yamlList("addons", "gui"), yamlList("addons", "podman")},
			field:  "Addons",
			want:   []string{"podman"},
		},
		// ── fix #1: trim + empty-token guard ──────────────────────────────────
		{
			name:    "literal '+' entry returns error containing 'empty'",
			layers:  []map[string]any{yamlList("addons", "+")},
			field:   "Addons",
			wantErr: "empty",
		},
		{
			name:    "'+  ' (plus-space) entry returns error containing 'empty'",
			layers:  []map[string]any{yamlList("addons", "+ ")},
			field:   "Addons",
			wantErr: "empty",
		},
		{
			name:   "leading space before + is trimmed and treated as additive",
			layers: []map[string]any{yamlList("addons", "gui"), yamlList("addons", " +podman")},
			field:  "Addons",
			want:   []string{"gui", "podman"},
		},
		// ── empty CLI slice clears ─────────────────────────────────────────────
		{
			name:   "empty CLI slice clears non-empty yaml base",
			cli:    map[int]any{fieldIdx("Addons"): []string{}},
			layers: []map[string]any{yamlList("addons", "gui", "podman")},
			field:  "Addons",
			want:   []string{},
		},
		// ── env-additive fold ─────────────────────────────────────────────────
		{
			name: "env additive appends to yaml base (EnvForward)",
			setup: func(t *testing.T) {
				t.Setenv("CSB_ENV_FORWARD", "+TOKEN1 +TOKEN2")
			},
			layers: []map[string]any{yamlList("env_forward", "BASE_VAR")},
			field:  "EnvForward",
			want:   []string{"BASE_VAR", "TOKEN1", "TOKEN2"},
		},
		// ── publish validate-after-strip ──────────────────────────────────────
		{
			name:   "+8080:8080 publish entry resolves to [8080:8080] without validation error",
			layers: []map[string]any{yamlList("publish", "+8080:8080")},
			field:  "Publish",
			want:   []string{"8080:8080"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setup != nil {
				tt.setup(t)
			}
			opts, err := resolveOptions(tt.cli, tt.layers...)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			got := readStringSlice(opts, tt.field)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ── String scalar regressions (kept separate: different column type) ──────────

// TestDefaultShell_WorkdirWins: STRING regression: user "zsh" + workdir "fish" => "fish"
func TestDefaultShell_WorkdirWins(t *testing.T) {
	user := map[string]any{"default_shell": "zsh"}
	workdir := map[string]any{"default_shell": "fish"}
	opts := resolveWithLayers(t, nil, user, workdir)
	assert.Equal(t, "fish", opts.DefaultShell)
}

// TestDefaultShell_UserOnlyNoWorkdir: STRING regression: user "zsh", workdir unset => "zsh"
func TestDefaultShell_UserOnlyNoWorkdir(t *testing.T) {
	user := map[string]any{"default_shell": "zsh"}
	opts := resolveWithLayers(t, nil, user, nil)
	assert.Equal(t, "zsh", opts.DefaultShell)
}

// ── Mount additive (kept separate: different element type) ───────────────────

// TestMount_AdditiveNoDedupDupesAllowed: Mount has parse:"mount" and no dedup tag.
// Additive workdir entry appends; duplicates are preserved.
func TestMount_AdditiveNoDedupDupesAllowed(t *testing.T) {
	user := yamlList("mount", "/tmp/a:/mnt/a:ro")
	workdir := yamlList("mount", "+/tmp/b:/mnt/b:ro")
	opts := resolveWithLayers(t, nil, user, workdir)
	require.Len(t, opts.Mount, 2)
	assert.Equal(t, "/tmp/a", opts.Mount[0].Src)
	assert.Equal(t, "/tmp/b", opts.Mount[1].Src)

	// Dupes allowed: same mount in both layers appears twice.
	workdir2 := yamlList("mount", "+/tmp/a:/mnt/a:ro")
	opts2 := resolveWithLayers(t, nil, user, workdir2)
	assert.Len(t, opts2.Mount, 2, "mount has no dedup tag, same entry should appear twice")
	assert.Equal(t, "/tmp/a", opts2.Mount[0].Src)
	assert.Equal(t, "/tmp/a", opts2.Mount[1].Src)
}

// ── validateTags: dedup tag ───────────────────────────────────────────────────

func TestValidateTags_DedupOnNonSlice(t *testing.T) {
	type bad struct {
		X string `dedup:"first"`
	}
	err := validateTags(reflect.TypeOf(bad{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dedup")
}

func TestValidateTags_DedupUnknownValue(t *testing.T) {
	type bad struct {
		X []string `dedup:"last"`
	}
	err := validateTags(reflect.TypeOf(bad{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dedup")
}

// ── validateTags: @default on slice guard (fix #3) ───────────────────────────

func TestValidateTags_AtDefaultOnSlice(t *testing.T) {
	type bad struct {
		X []string `default:"@something"`
	}
	err := validateTags(reflect.TypeOf(bad{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "@")
}

// ── hostTimezone / Timezone option ───────────────────────────────────────────

func TestHostTimezone_FromTZEnv(t *testing.T) {
	t.Setenv("TZ", "Pacific/Auckland")
	assert.Equal(t, "Pacific/Auckland", hostTimezone())
}

// TestHostTimezone_InvalidTZEnvFallsThrough verifies that a $TZ holding a
// POSIX TZ string (legal for libc, but not an IANA name tzdata can resolve)
// never gets returned as-is — it would fail the "timezone" validateFunc and
// abort every csb invocation on such a host. hostTimezone must skip it and
// fall through to the next source (ultimately "UTC"), never propagating an
// unresolvable candidate.
func TestHostTimezone_InvalidTZEnvFallsThrough(t *testing.T) {
	t.Setenv("TZ", "<+12>-12")
	got := hostTimezone()
	assert.NotEqual(t, "<+12>-12", got)
	_, err := time.LoadLocation(got)
	assert.NoError(t, err, "hostTimezone must always return a resolvable zone")
}

func TestResolveOptions_TimezoneDefaultsToHost(t *testing.T) {
	t.Setenv("TZ", "Europe/Berlin")
	opts := resolveWith(t, nil, nil)
	assert.Equal(t, "Europe/Berlin", opts.Timezone)
}

func TestResolveOptions_TimezoneCLIOverride(t *testing.T) {
	t.Setenv("TZ", "Europe/Berlin")
	cli := map[int]any{fieldIdx("Timezone"): "Asia/Tokyo"}
	opts := resolveWith(t, cli, nil)
	assert.Equal(t, "Asia/Tokyo", opts.Timezone)
}

func TestValidateFuncs_TimezoneAcceptsUTC(t *testing.T) {
	assert.NoError(t, validateFuncs["timezone"]("UTC"))
}

func TestValidateFuncs_TimezoneRejectsInvalid(t *testing.T) {
	err := validateFuncs["timezone"]("Not/AZone")
	assert.Error(t, err)
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
