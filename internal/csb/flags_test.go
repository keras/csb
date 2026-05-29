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
