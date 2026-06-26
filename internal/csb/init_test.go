package csb

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── InitConfigDir ─────────────────────────────────────────────────────────────

func TestInitConfigDir_CreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	memFS := fstest.MapFS{
		"files/addons/mise/install.sh": &fstest.MapFile{
			Data: []byte("#!/bin/bash\necho mise\n"),
			Mode: 0755,
		},
		// test.sh is a dev artifact and must not be seeded into the config dir.
		"files/addons/mise/test.sh": &fstest.MapFile{
			Data: []byte("#!/bin/bash\ntrue\n"),
			Mode: 0755,
		},
	}

	InitConfigDir(dir, testDockerfile, memFS)

	for _, rel := range []string{"config.yaml", "Dockerfile", "addons/mise/install.sh"} {
		_, err := os.Stat(filepath.Join(dir, rel))
		assert.NoError(t, err, "expected %s to be created", rel)
	}
	// home dir created
	info, err := os.Stat(filepath.Join(dir, "home"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	// test.sh must NOT be seeded
	_, err = os.Stat(filepath.Join(dir, "addons/mise/test.sh"))
	assert.True(t, os.IsNotExist(err), "test.sh should not be seeded")
}

// ── renderConfigTemplate (already covered; add smoke test) ───────────────────

func TestRenderConfigTemplate_NonEmpty(t *testing.T) {
	tmpl := renderConfigTemplate()
	assert.NotEmpty(t, tmpl)
	assert.Contains(t, tmpl, "# csb configuration")
}

// ── workdirConfigPath ─────────────────────────────────────────────────────────

func TestWorkdirConfigPath_Deterministic(t *testing.T) {
	dir := t.TempDir()
	a := workdirConfigPath(dir, "/some/workspace")
	b := workdirConfigPath(dir, "/some/workspace")
	assert.Equal(t, a, b)
}

func TestWorkdirConfigPath_DifferentWorkspace(t *testing.T) {
	dir := t.TempDir()
	a := workdirConfigPath(dir, "/workspace/a")
	b := workdirConfigPath(dir, "/workspace/b")
	assert.NotEqual(t, a, b)
}

func TestWorkdirConfigPath_InProjectsDir(t *testing.T) {
	dir := t.TempDir()
	p := workdirConfigPath(dir, "/some/ws")
	assert.Equal(t, filepath.Join(dir, "projects"), filepath.Dir(p))
	assert.True(t, filepath.IsAbs(p))
}

// ── loadWorkdirYAMLConfig ─────────────────────────────────────────────────────

func TestLoadWorkdirYAMLConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	m, err := loadWorkdirYAMLConfig(dir, "/some/workspace")
	require.NoError(t, err)
	assert.NotNil(t, m)
}

func TestLoadWorkdirYAMLConfig_WithContent(t *testing.T) {
	dir := t.TempDir()
	ws := "/some/workspace"
	path := workdirConfigPath(dir, ws)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte("runtime: podman\n"), 0644))

	m, err := loadWorkdirYAMLConfig(dir, ws)
	require.NoError(t, err)
	assert.Equal(t, "podman", m["runtime"])
}

// ── detectSubcommand ─────────────────────────────────────────────────────────

func TestDetectSubcommand_Run(t *testing.T) {
	sub, argv := detectSubcommand([]string{"run", "--tmux", "cmd"})
	assert.Equal(t, "run", sub)
	assert.NotContains(t, argv, "run")
}

func TestDetectSubcommand_Clean(t *testing.T) {
	sub, _ := detectSubcommand([]string{"clean", "-v"})
	assert.Equal(t, "clean", sub)
}

func TestDetectSubcommand_Config(t *testing.T) {
	sub, _ := detectSubcommand([]string{"config", "show"})
	assert.Equal(t, "config", sub)
}

func TestDetectSubcommand_DefaultIsRun(t *testing.T) {
	sub, _ := detectSubcommand([]string{"--tmux", "echo", "hello"})
	assert.Equal(t, "run", sub)
}

func TestDetectSubcommand_DashDashStopsSearch(t *testing.T) {
	sub, _ := detectSubcommand([]string{"--", "clean"})
	assert.Equal(t, "run", sub)
}

func TestDetectSubcommand_Unknown(t *testing.T) {
	sub, _ := detectSubcommand([]string{"unknowncmd"})
	assert.Equal(t, "run", sub)
}

// ── parseCleanArgs (via ParseArgs) ───────────────────────────────────────────

func TestParseCleanArgs_Verbose(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cfg, err := ParseArgs([]string{"clean", "--verbose"})
	require.NoError(t, err)
	assert.Equal(t, "clean", cfg.Subcommand)
	assert.True(t, cfg.Verbose)
}

func TestParseCleanArgs_NoVerbose(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cfg, err := ParseArgs([]string{"clean"})
	require.NoError(t, err)
	assert.Equal(t, "clean", cfg.Subcommand)
	assert.False(t, cfg.Verbose)
}

// ── parseConfigArgs (via ParseArgs) ──────────────────────────────────────────

func TestParseConfigArgs_ShowConfig(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cfg, err := ParseArgs([]string{"config", "show"})
	require.NoError(t, err)
	assert.Equal(t, "config", cfg.Subcommand)
	assert.Equal(t, "show", cfg.ConfigAction)
	assert.Equal(t, "config", cfg.ConfigShowTarget)
}

func TestParseConfigArgs_ShowContext(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cfg, err := ParseArgs([]string{"config", "show", "context"})
	require.NoError(t, err)
	assert.Equal(t, "context", cfg.ConfigShowTarget)
}

func TestParseConfigArgs_EditUser(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cfg, err := ParseArgs([]string{"config", "edit"})
	require.NoError(t, err)
	assert.Equal(t, "edit", cfg.ConfigAction)
	assert.Equal(t, "user", cfg.ConfigEditTarget)
}

func TestParseConfigArgs_EditWorkdir(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cfg, err := ParseArgs([]string{"config", "edit", "workdir"})
	require.NoError(t, err)
	assert.Equal(t, "workdir", cfg.ConfigEditTarget)
}

func TestParseConfigArgs_InvalidShowTarget(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	_, err := ParseArgs([]string{"config", "show", "bogus"})
	require.Error(t, err)
}

func TestParseConfigArgs_InvalidEditTarget(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	_, err := ParseArgs([]string{"config", "edit", "bogus"})
	require.Error(t, err)
}

func TestParseConfigArgs_UnknownAction(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	_, err := ParseArgs([]string{"config", "unknown"})
	require.Error(t, err)
}

// ── lookPath / lookupInPath ───────────────────────────────────────────────────

func TestLookPath_ShellExists(t *testing.T) {
	// /bin/sh should always exist
	path, err := lookPath("sh")
	require.NoError(t, err)
	assert.Contains(t, path, "sh")
}

func TestLookupInPath_NotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no binaries in tempdir
	_, err := lookupInPath("nonexistent-csb-binary-xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in PATH")
}
