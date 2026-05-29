package csb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Workdir ──────────────────────────────────────────────────────────────────

func TestWorkdir_NoWorkspace(t *testing.T) {
	cfg := &Config{}
	cfg.Workspace = nil
	assert.Equal(t, "/workspace", cfg.Workdir())
}

func TestWorkdir_WorkspaceIsHome(t *testing.T) {
	ws := "/home/user"
	cfg := &Config{Home: "/home/user"}
	cfg.Workspace = &ws
	assert.Equal(t, "/workspace", cfg.Workdir())
}

func TestWorkdir_WorkspaceBelowHome(t *testing.T) {
	ws := "/home/user/projects/myapp"
	cfg := &Config{Home: "/home/user"}
	cfg.Workspace = &ws
	assert.Equal(t, "/workspace/projects/myapp", cfg.Workdir())
}

func TestWorkdir_WorkspaceOutsideHome(t *testing.T) {
	ws := "/data/repos/myproject"
	cfg := &Config{Home: "/home/user"}
	cfg.Workspace = &ws
	// Outside home: use last 2 path components
	assert.Equal(t, "/workspace/repos/myproject", cfg.Workdir())
}

func TestWorkdir_WorkspaceSingleComponent(t *testing.T) {
	ws := "/myproject"
	cfg := &Config{Home: "/home/user"}
	cfg.Workspace = &ws
	// /myproject → Clean gives ["myproject"] with leading separator handling
	// Actually filepath.Clean("/myproject") → "/myproject", Split gives ["", "myproject"]
	// parts len >= 2: ContainerWorkdir + parts[len-2] + parts[len-1]
	// "" + "myproject"
	assert.Equal(t, "/workspace//myproject", cfg.Workdir())
}

// ── BoolFromEnv ──────────────────────────────────────────────────────────────

func TestBoolFromEnv_True(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "True", "yes", "YES"} {
		assert.True(t, BoolFromEnv(v), "expected true for %q", v)
	}
}

func TestBoolFromEnv_False(t *testing.T) {
	for _, v := range []string{"0", "false", "FALSE", "no", "NO", ""} {
		assert.False(t, BoolFromEnv(v), "expected false for %q", v)
	}
}

func TestBoolFromEnv_WhitespaceStripped(t *testing.T) {
	assert.False(t, BoolFromEnv("  0  "))
	assert.True(t, BoolFromEnv("  1  "))
}

// ── CSBHome ───────────────────────────────────────────────────────────────────

func TestCSBHome(t *testing.T) {
	cfg := &Config{ConfigDir: "/home/user/.config/csb"}
	assert.Equal(t, "/home/user/.config/csb/home", cfg.CSBHome())
}

// ── WorkdirConfigPath ─────────────────────────────────────────────────────────

func TestWorkdirConfigPath_WithWorkspace(t *testing.T) {
	dir := t.TempDir()
	ws := "/some/workspace"
	cfg := &Config{ConfigDir: dir}
	cfg.Workspace = &ws
	path := cfg.WorkdirConfigPath()
	assert.NotEmpty(t, path)
	assert.True(t, filepath.IsAbs(path))
}

func TestWorkdirConfigPath_NoWorkspace(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Workspace = nil
	assert.Equal(t, "", cfg.WorkdirConfigPath())
}

// ── ContainerLabels / VolumeLabels / ImageLabels ─────────────────────────────

func TestContainerLabels(t *testing.T) {
	cfg := &Config{ConfigDir: "/my/cfg"}
	cfg.HomeVolume = "csb-home"
	labels := ContainerLabels(cfg)
	assert.Equal(t, "true", labels["csb.managed"])
	assert.Equal(t, "csb-home", labels["csb.home-volume"])
	assert.Equal(t, "/my/cfg", labels["csb.config-dir"])
}

func TestVolumeLabels(t *testing.T) {
	cfg := &Config{ConfigDir: "/my/cfg"}
	labels := VolumeLabels(cfg)
	assert.Equal(t, "true", labels["csb.managed"])
	assert.Equal(t, "/my/cfg", labels["csb.config-dir"])
}

func TestImageLabels(t *testing.T) {
	cfg := &Config{ConfigDir: "/my/cfg"}
	labels := ImageLabels(cfg)
	assert.Equal(t, "true", labels["csb.managed"])
	assert.Equal(t, "/my/cfg", labels["csb.config-dir"])
}

// ── loadYAMLConfig ────────────────────────────────────────────────────────────

func TestLoadYAMLConfig_MissingFile(t *testing.T) {
	m, err := loadYAMLConfig(t.TempDir())
	require.NoError(t, err)
	assert.NotNil(t, m)
	assert.Empty(t, m)
}

func TestLoadYAMLConfig_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("runtime: docker\n"), 0644))
	m, err := loadYAMLConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, "docker", m["runtime"])
}

func TestLoadYAMLConfig_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte{}, 0644))
	m, err := loadYAMLConfig(dir)
	require.NoError(t, err)
	assert.NotNil(t, m)
}

// ── mergeMaps ────────────────────────────────────────────────────────────────

func TestMergeMaps_OverrideWins(t *testing.T) {
	base := map[string]interface{}{"a": "1", "b": "2"}
	over := map[string]interface{}{"b": "3", "c": "4"}
	result := mergeMaps(base, over)
	assert.Equal(t, "1", result["a"])
	assert.Equal(t, "3", result["b"])
	assert.Equal(t, "4", result["c"])
}

// ── expandUser ───────────────────────────────────────────────────────────────

func TestExpandUser_NoTilde(t *testing.T) {
	assert.Equal(t, "/abs/path", expandUser("/abs/path"))
}

func TestExpandUser_TildeSlash(t *testing.T) {
	result := expandUser("~/foo")
	assert.NotEqual(t, "~/foo", result, "tilde should be expanded")
	assert.True(t, filepath.IsAbs(result))
}

func TestExpandUser_TildeOnly(t *testing.T) {
	result := expandUser("~")
	assert.NotEqual(t, "~", result, "bare tilde should be expanded")
	assert.True(t, filepath.IsAbs(result))
}

// ── yamlBool / yamlStringList ─────────────────────────────────────────────────

func TestYamlBool_Present(t *testing.T) {
	m := map[string]interface{}{"tmux": true}
	v, ok := yamlBool(m, "tmux")
	assert.True(t, ok)
	assert.True(t, v)
}

func TestYamlBool_Missing(t *testing.T) {
	_, ok := yamlBool(map[string]interface{}{}, "tmux")
	assert.False(t, ok)
}

func TestYamlBool_NotBool(t *testing.T) {
	m := map[string]interface{}{"tmux": "yes"}
	_, ok := yamlBool(m, "tmux")
	assert.False(t, ok)
}

func TestYamlStringList_SliceOfInterface(t *testing.T) {
	m := map[string]interface{}{"addons": []interface{}{"mise", "sudo"}}
	vals, ok := yamlStringList(m, "addons")
	require.True(t, ok)
	assert.Equal(t, []string{"mise", "sudo"}, vals)
}

func TestYamlStringList_SingleString(t *testing.T) {
	m := map[string]interface{}{"addons": "mise"}
	vals, ok := yamlStringList(m, "addons")
	require.True(t, ok)
	assert.Equal(t, []string{"mise"}, vals)
}

func TestYamlStringList_Missing(t *testing.T) {
	_, ok := yamlStringList(map[string]interface{}{}, "addons")
	assert.False(t, ok)
}

// ── formatConfigHelp / formatHelpFull ─────────────────────────────────────────

func TestFormatConfigHelp_ContainsActions(t *testing.T) {
	h := formatConfigHelp()
	assert.Contains(t, h, "show")
	assert.Contains(t, h, "edit")
}

func TestFormatHelpFull_ContainsOptionsHeader(t *testing.T) {
	h := formatHelpFull()
	assert.Contains(t, h, "OPTIONS")
	assert.Contains(t, h, "EXAMPLE")
}

// ── optionHelpFullLines ───────────────────────────────────────────────────────

func TestOptionHelpFullLines_NotEmpty(t *testing.T) {
	lines := optionHelpFullLines()
	assert.NotEmpty(t, lines)
}
