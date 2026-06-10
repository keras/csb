package csb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── detectSubcommand ──────────────────────────────────────────────────────────

// TestDetectSubcommand_EqualsFlagThenSubcmd verifies --flag=value form skips correctly.
func TestDetectSubcommand_EqualsFlagThenSubcmd(t *testing.T) {
	sub, _ := detectSubcommand([]string{"--arch=arm64", "config"})
	assert.Equal(t, "config", sub)
}

// TestDetectSubcommand_SpaceFlagThenSubcmd verifies --flag value form skips value token.
func TestDetectSubcommand_SpaceFlagThenSubcmd(t *testing.T) {
	sub, _ := detectSubcommand([]string{"--arch", "arm64", "config"})
	assert.Equal(t, "config", sub)
}

// TestDetectSubcommand_BoolFlagThenSubcmd verifies bool flags (no value) don't consume the next token.
func TestDetectSubcommand_BoolFlagThenSubcmd(t *testing.T) {
	sub, _ := detectSubcommand([]string{"--tmux", "config"})
	assert.Equal(t, "config", sub)
}

// TestDetectSubcommand_UnknownFlagDoesNotSkipNext verifies that an unknown flag does not
// consume the following token as a value (safe choice: unknown flags are treated as bool-like).
func TestDetectSubcommand_UnknownFlagDoesNotSkipNext(t *testing.T) {
	// "--unknown" is not in the Options flag index; we treat it as not taking a value,
	// so "config" should still be found as the subcommand.
	sub, _ := detectSubcommand([]string{"--unknown", "config"})
	assert.Equal(t, "config", sub)
}

// TestDetectSubcommand_NoSubcommand returns "run" when no subcommand keyword appears.
func TestDetectSubcommand_NoSubcommand(t *testing.T) {
	sub, _ := detectSubcommand([]string{"--tmux", "--arch=arm64"})
	assert.Equal(t, "run", sub)
}

// TestDetectSubcommand_MixedForms checks a realistic mixed argv.
func TestDetectSubcommand_MixedForms(t *testing.T) {
	sub, remaining := detectSubcommand([]string{"--arch", "arm64", "--no-tmux", "--runtime=docker", "run"})
	assert.Equal(t, "run", sub)
	// "run" token should be removed from remaining
	for _, tok := range remaining {
		assert.NotEqual(t, "run", tok)
	}
}

// TestDetectSubcommand_ConfigSubcmd verifies "config" is detected after value flags.
func TestDetectSubcommand_ConfigSubcmd(t *testing.T) {
	sub, _ := detectSubcommand([]string{"--arch", "arm64", "--addon", "foo", "config"})
	assert.Equal(t, "config", sub)
}

// TestDetectSubcommand_CleanSubcmd verifies "clean" is detected after value flags.
func TestDetectSubcommand_CleanSubcmd(t *testing.T) {
	sub, _ := detectSubcommand([]string{"--arch", "arm64", "clean"})
	assert.Equal(t, "clean", sub)
}

// ── ParseArgs: config subcommand honors CLI flags (Bug 2) ─────────────────────

// TestParseArgs_ArchFlagSpaceFormConfigShow verifies --arch arm64 config show sets Arch.
func TestParseArgs_ArchFlagSpaceFormConfigShow(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cfg, err := ParseArgs([]string{"--arch", "arm64", "--no-workspace", "config", "show"})
	require.NoError(t, err)
	assert.Equal(t, "arm64", cfg.Arch)
	assert.Equal(t, "config", cfg.Subcommand)
	assert.Equal(t, "show", cfg.ConfigAction)
}

// TestParseArgs_ArchFlagEqualsFormConfigShow verifies --arch=arm64 config show sets Arch.
func TestParseArgs_ArchFlagEqualsFormConfigShow(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cfg, err := ParseArgs([]string{"--arch=arm64", "--no-workspace", "config", "show"})
	require.NoError(t, err)
	assert.Equal(t, "arm64", cfg.Arch)
	assert.Equal(t, "config", cfg.Subcommand)
}

// TestParseArgs_AddonFlagConfigShow verifies --addon foo config show sets Addons.
func TestParseArgs_AddonFlagConfigShow(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	cfg, err := ParseArgs([]string{"--addon", "foo", "--no-workspace", "config", "show"})
	require.NoError(t, err)
	assert.Contains(t, cfg.Addons, "foo")
	assert.Equal(t, "config", cfg.Subcommand)
}

// TestParseArgs_CLIArchBeatsEnvAndYAML verifies CLI --arch beats CSB_ARCH env and yaml arch.
func TestParseArgs_CLIArchBeatsEnvAndYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("arch: i386\n"),
		0644,
	))
	t.Setenv("CSB_CONFIG_DIR", dir)
	t.Setenv("CSB_ARCH", "amd64")

	cfg, err := ParseArgs([]string{"--arch", "arm64", "--no-workspace", "config", "show"})
	require.NoError(t, err)
	assert.Equal(t, "arm64", cfg.Arch)
}

// TestParseArgs_WorkdirAdditiveAddonsExtendsUser verifies that a workdir config with
// additive addons entries (+addon) appends to the user config end-to-end.
func TestParseArgs_WorkdirAdditiveAddonsExtendsUser(t *testing.T) {
	dir := t.TempDir()

	// User config: replace default with [gui]
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("addons:\n  - gui\n"),
		0644,
	))

	// Workdir config: additive +podman
	workspace := t.TempDir()
	workdirCfgPath := workdirConfigPath(dir, workspace)
	require.NoError(t, os.MkdirAll(filepath.Dir(workdirCfgPath), 0755))
	require.NoError(t, os.WriteFile(workdirCfgPath, []byte("addons:\n  - +podman\n"), 0644))

	t.Setenv("CSB_CONFIG_DIR", dir)

	cfg, err := ParseArgs([]string{"--workspace", workspace, "--no-tmux", "config", "show"})
	require.NoError(t, err)
	assert.Equal(t, []string{"gui", "podman"}, cfg.Addons)
}

// ── input-validation error paths ─────────────────────────────────────────────

// TestParseArgs_UnknownFlagRunErrors verifies an unrecognized flag is a hard
// error for the run subcommand (config/clean ignore unknown flags instead).
func TestParseArgs_UnknownFlagRunErrors(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())

	_, err := ParseArgs([]string{"--bogus", "--no-workspace"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown flag")
}

// TestParseArgs_ConfigInvalidShowTarget verifies an invalid `config show` target
// is rejected.
func TestParseArgs_ConfigInvalidShowTarget(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())

	_, err := ParseArgs([]string{"--no-workspace", "config", "show", "bogus"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config show target")
}

// TestParseArgs_ConfigInvalidEditTarget verifies an invalid `config edit` target
// is rejected.
func TestParseArgs_ConfigInvalidEditTarget(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())

	_, err := ParseArgs([]string{"--no-workspace", "config", "edit", "bogus"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config edit target")
}

// TestParseArgs_ConfigUnknownAction verifies an unknown config action is rejected.
func TestParseArgs_ConfigUnknownAction(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())

	_, err := ParseArgs([]string{"--no-workspace", "config", "bogus"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config action")
}

// TestParseArgs_ConfigStatusAndUpdate verifies the no-target actions parse and
// set the action through.
func TestParseArgs_ConfigStatusAndUpdate(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())

	for _, action := range []string{"status", "update"} {
		cfg, err := ParseArgs([]string{"--no-workspace", "config", action})
		require.NoError(t, err)
		assert.Equal(t, "config", cfg.Subcommand)
		assert.Equal(t, action, cfg.ConfigAction)
	}
}

// ── locator (bootParams) resolution ──────────────────────────────────────────

// TestParseArgs_WorkspaceFromEnv verifies CSB_WORKSPACE supplies the workspace
// when no --workspace flag is given.
func TestParseArgs_WorkspaceFromEnv(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	ws := t.TempDir()
	t.Setenv("CSB_WORKSPACE", ws)

	cfg, err := ParseArgs([]string{"config", "show"})
	require.NoError(t, err)
	require.NotNil(t, cfg.Workspace)
	assert.Equal(t, ws, *cfg.Workspace)
}

// TestParseArgs_CLIWorkspaceBeatsEnv verifies --workspace overrides CSB_WORKSPACE.
func TestParseArgs_CLIWorkspaceBeatsEnv(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())
	t.Setenv("CSB_WORKSPACE", t.TempDir())
	cliWs := t.TempDir()

	cfg, err := ParseArgs([]string{"--workspace", cliWs, "config", "show"})
	require.NoError(t, err)
	require.NotNil(t, cfg.Workspace)
	assert.Equal(t, cliWs, *cfg.Workspace)
}

// TestParseArgs_RebuildFlag verifies --rebuild sets Config.Rebuild for run.
func TestParseArgs_RebuildFlag(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())

	cfg, err := ParseArgs([]string{"--rebuild", "--no-workspace"})
	require.NoError(t, err)
	assert.True(t, cfg.Rebuild)
	assert.Equal(t, "run", cfg.Subcommand)

	cfg, err = ParseArgs([]string{"--no-workspace"})
	require.NoError(t, err)
	assert.False(t, cfg.Rebuild)
}

// TestParseArgs_RebuildIgnoredForConfig verifies --rebuild is ignored (not an
// error) for the config subcommand, where it has no meaning.
func TestParseArgs_RebuildIgnoredForConfig(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())

	cfg, err := ParseArgs([]string{"--rebuild", "--no-workspace", "config", "show"})
	require.NoError(t, err)
	assert.False(t, cfg.Rebuild)
	assert.Equal(t, "config", cfg.Subcommand)
}

// TestParseArgs_RebuildIgnoredForClean verifies --rebuild is ignored (not an
// error) for the clean subcommand.
func TestParseArgs_RebuildIgnoredForClean(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())

	cfg, err := ParseArgs([]string{"--rebuild", "clean"})
	require.NoError(t, err)
	assert.False(t, cfg.Rebuild)
	assert.Equal(t, "clean", cfg.Subcommand)
}

// TestParseArgs_NoWorkspaceYieldsNil verifies --no-workspace collapses the
// tri-state to a nil workspace, and does not collide with the --no-X bool
// negation path (which also runs for the neighbouring --no-motd flag).
func TestParseArgs_NoWorkspaceYieldsNil(t *testing.T) {
	t.Setenv("CSB_CONFIG_DIR", t.TempDir())

	cfg, err := ParseArgs([]string{"--no-motd", "--no-workspace", "config", "show"})
	require.NoError(t, err)
	assert.Nil(t, cfg.Workspace)
	assert.False(t, cfg.Motd)
}

// TestParseArgs_LocatorFlagAfterBoundaryNotConsumed verifies a --workspace that
// appears after the command boundary is passed through to the inner command,
// not consumed by csb — consistent with every other flag.
func TestParseArgs_LocatorFlagAfterBoundaryNotConsumed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CSB_CONFIG_DIR", dir)

	cfg, err := ParseArgs([]string{"--", "tool", "--workspace", "/elsewhere"})
	require.NoError(t, err)
	require.NotNil(t, cfg.Workspace)
	assert.NotEqual(t, "/elsewhere", *cfg.Workspace)
	assert.Equal(t, []string{"tool", "--workspace", "/elsewhere"}, cfg.PassthroughArgs)
}
