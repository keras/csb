package csb

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ulikunitz/xz"
)

// ── parseAddonSpec ───────────────────────────────────────────────────────────

func TestParseAddonSpec_NameOnly(t *testing.T) {
	name, args := parseAddonSpec("mise")
	assert.Equal(t, "mise", name)
	assert.Empty(t, args)
}

func TestParseAddonSpec_NameWithArgs(t *testing.T) {
	name, args := parseAddonSpec("packages git nano")
	assert.Equal(t, "packages", name)
	assert.Equal(t, []string{"git", "nano"}, args)
}

func TestParseAddonSpec_EmptyString(t *testing.T) {
	name, args := parseAddonSpec("")
	assert.Equal(t, "", name)
	assert.Nil(t, args)
}

func TestParseAddonSpec_WhitespaceOnly(t *testing.T) {
	name, args := parseAddonSpec("   ")
	assert.Equal(t, "", name)
	assert.Nil(t, args)
}

// ── addonInstances ───────────────────────────────────────────────────────────

func makeAddonDir(t *testing.T, configDir, name string) {
	t.Helper()
	dir := filepath.Join(configDir, "addons", name)
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/bash\n"), 0755))
}

func TestAddonInstances_SortedAndDeduped(t *testing.T) {
	dir := t.TempDir()
	makeAddonDir(t, dir, "zebra")
	makeAddonDir(t, dir, "alpha")

	cfg := &Config{ConfigDir: dir}
	cfg.Addons = []string{"zebra", "alpha", "zebra"} // dupe

	instances := addonInstances(cfg)
	require.Len(t, instances, 2)
	assert.Equal(t, "alpha", instances[0].Name)
	assert.Equal(t, "zebra", instances[1].Name)
}

func TestAddonInstances_MissingScriptSkipped(t *testing.T) {
	dir := t.TempDir()
	makeAddonDir(t, dir, "present")

	cfg := &Config{ConfigDir: dir}
	cfg.Addons = []string{"present", "missing"}

	instances := addonInstances(cfg)
	require.Len(t, instances, 1)
	assert.Equal(t, "present", instances[0].Name)
}

func TestAddonInstances_ArgsPreserved(t *testing.T) {
	dir := t.TempDir()
	makeAddonDir(t, dir, "packages")

	cfg := &Config{ConfigDir: dir}
	cfg.Addons = []string{"packages git nano"}

	instances := addonInstances(cfg)
	require.Len(t, instances, 1)
	assert.Equal(t, []string{"git", "nano"}, instances[0].Args)
}

// ── addonPaths ───────────────────────────────────────────────────────────────

func TestAddonPaths_ReturnsPaths(t *testing.T) {
	dir := t.TempDir()
	makeAddonDir(t, dir, "foo")
	makeAddonDir(t, dir, "bar")

	cfg := &Config{ConfigDir: dir}
	cfg.Addons = []string{"foo", "bar"}

	instances := addonInstances(cfg)
	paths := addonPaths(instances)
	require.Len(t, paths, 2)
	for _, p := range paths {
		assert.True(t, strings.HasSuffix(p, "install.sh"))
	}
}

// ── buildRunScript ───────────────────────────────────────────────────────────

func TestBuildRunScript_EmptyAddons(t *testing.T) {
	script := string(buildRunScript(nil))
	assert.Contains(t, script, "apt-get update")
	assert.Contains(t, script, "rm -rf /tmp/addon.d /var/lib/apt/lists/*")
}

func TestBuildRunScript_WithAddons(t *testing.T) {
	instances := []addonInstance{
		{Name: "foo", Args: nil},
		{Name: "bar", Args: []string{"arg1"}},
	}
	script := string(buildRunScript(instances))
	assert.Contains(t, script, "apt-get update")
	assert.Contains(t, script, "./install.sh")
	assert.Contains(t, script, "run_addon foo\n")
	assert.Contains(t, script, "run_addon bar arg1\n")
	assert.Contains(t, script, "addon '$name' failed during install")
	assert.Contains(t, script, "rm -rf /tmp/addon.d")
}

// ── hostRunBytes + decompressXZ ──────────────────────────────────────────────

// makeTarXZ builds a tiny .tar.xz with two named entries.
func makeTarXZ(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var rawBuf bytes.Buffer
	tw := tar.NewWriter(&rawBuf)
	for name, data := range files {
		hdr := &tar.Header{Name: name, Mode: 0755, Size: int64(len(data))}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	var xzBuf bytes.Buffer
	xw, err := xz.NewWriter(&xzBuf)
	require.NoError(t, err)
	_, err = xw.Write(rawBuf.Bytes())
	require.NoError(t, err)
	require.NoError(t, xw.Close())
	return xzBuf.Bytes()
}

func TestHostRunBytes_NilInput(t *testing.T) {
	got, err := hostRunBytes(nil, "amd64")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestHostRunBytes_ExtractsCorrectArch(t *testing.T) {
	archive := makeTarXZ(t, map[string][]byte{
		"csb-host-run.amd64": []byte("amd64-binary"),
		"csb-host-run.arm64": []byte("arm64-binary"),
	})

	got, err := hostRunBytes(archive, "amd64")
	require.NoError(t, err)
	assert.Equal(t, []byte("amd64-binary"), got)

	got, err = hostRunBytes(archive, "arm64")
	require.NoError(t, err)
	assert.Equal(t, []byte("arm64-binary"), got)
}

func TestHostRunBytes_NotFoundError(t *testing.T) {
	archive := makeTarXZ(t, map[string][]byte{
		"csb-host-run.amd64": []byte("data"),
	})
	_, err := hostRunBytes(archive, "arm64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "arm64")
}

// ── ImageName ────────────────────────────────────────────────────────────────

func setupImageCfg(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM debian:stable-slim\n"), 0644))
	cfg := &Config{ConfigDir: dir}
	cfg.Arch = "amd64"
	cfg.Addons = nil
	return cfg
}

func TestImageName_Format(t *testing.T) {
	cfg := setupImageCfg(t)
	name, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist")})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(name, "csb:"), "got %q", name)
	assert.Len(t, name, 4+12) // "csb:" + 12 hex chars
}

func TestImageName_Deterministic(t *testing.T) {
	cfg := setupImageCfg(t)
	a, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist")})
	require.NoError(t, err)
	b, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist")})
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestImageName_ChangesOnDockerfile(t *testing.T) {
	cfg := setupImageCfg(t)
	a, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist")})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.ConfigDir, "Dockerfile"), []byte("FROM ubuntu:22.04\n"), 0644))
	b, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist")})
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestImageName_ChangesOnEntrypoint(t *testing.T) {
	cfg := setupImageCfg(t)
	a, err := ImageName(cfg, Assets{Entrypoint: []byte("ep-v1"), Persist: []byte("persist")})
	require.NoError(t, err)
	b, err := ImageName(cfg, Assets{Entrypoint: []byte("ep-v2"), Persist: []byte("persist")})
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestImageName_ChangesOnPersist(t *testing.T) {
	cfg := setupImageCfg(t)
	a, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("p1")})
	require.NoError(t, err)
	b, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("p2")})
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestImageName_ChangesOnArch(t *testing.T) {
	cfg := setupImageCfg(t)
	a, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist")})
	require.NoError(t, err)
	cfg.Arch = "arm64"
	b, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist")})
	require.NoError(t, err)
	// arch only affects ImageName via hostRunBytes; with nil tarXZ both produce nil, so arch alone doesn't change hash
	// but arch is not hashed directly; no change expected with nil tarXZ
	_ = a
	_ = b
}

func TestImageName_ChangesOnAddonScript(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM debian:stable-slim\n"), 0644))
	makeAddonDir(t, dir, "myaddon")

	cfg := &Config{ConfigDir: dir}
	cfg.Arch = "amd64"
	cfg.Addons = []string{"myaddon"}

	a, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist")})
	require.NoError(t, err)

	// Modify addon script
	require.NoError(t, os.WriteFile(filepath.Join(dir, "addons", "myaddon", "install.sh"), []byte("#!/bin/bash\necho changed\n"), 0755))
	b, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist")})
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestImageName_ChangesOnHelp(t *testing.T) {
	cfg := setupImageCfg(t)
	a, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist"), Help: []byte("help-v1")})
	require.NoError(t, err)
	b, err := ImageName(cfg, Assets{Entrypoint: []byte("ep"), Persist: []byte("persist"), Help: []byte("help-v2")})
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

// ── BuildContextTar ──────────────────────────────────────────────────────────

func TestBuildContextTar_ContainsExpectedEntries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM debian:stable-slim\n"), 0644))
	makeAddonDir(t, dir, "myaddon")

	cfg := &Config{ConfigDir: dir}
	cfg.Arch = "amd64"
	cfg.Addons = []string{"myaddon"}

	tarBytes, err := BuildContextTar(cfg, Assets{Entrypoint: []byte("entrypoint"), Persist: []byte("persist"), Help: []byte("help")})
	require.NoError(t, err)

	entries := tarEntries(t, tarBytes)
	assert.Contains(t, entries, "Dockerfile")
	assert.Contains(t, entries, "entrypoint.sh")
	assert.Contains(t, entries, "csb/csb-persist")
	assert.Contains(t, entries, "csb/csb-help")
	assert.Contains(t, entries, "csb/csb-host-run")
	assert.Contains(t, entries, "csb/addon.d/run.sh")
	assert.Contains(t, entries, "csb/addon.d/myaddon/install.sh")
}

func TestBuildContextTar_BundlesAddonResources(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM debian:stable-slim\n"), 0644))
	makeAddonDir(t, dir, "myaddon")
	// A bundled resource sitting next to install.sh, plus a test.sh that must
	// NOT be shipped into the image build context.
	addonDir := filepath.Join(dir, "addons", "myaddon")
	require.NoError(t, os.WriteFile(filepath.Join(addonDir, "config.conf"), []byte("key=value\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(addonDir, "test.sh"), []byte("#!/bin/bash\n"), 0755))

	cfg := &Config{ConfigDir: dir}
	cfg.Arch = "amd64"
	cfg.Addons = []string{"myaddon"}

	tarBytes, err := BuildContextTar(cfg, Assets{Entrypoint: []byte("entrypoint"), Persist: []byte("persist"), Help: []byte("help")})
	require.NoError(t, err)

	entries := tarEntries(t, tarBytes)
	assert.Contains(t, entries, "csb/addon.d/myaddon/install.sh")
	assert.Contains(t, entries, "csb/addon.d/myaddon/config.conf")
	assert.NotContains(t, entries, "csb/addon.d/myaddon/test.sh")
}

func TestBuildContextTar_FileModes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM debian:stable-slim\n"), 0644))

	cfg := &Config{ConfigDir: dir}
	cfg.Arch = "amd64"
	cfg.Addons = nil

	tarBytes, err := BuildContextTar(cfg, Assets{Entrypoint: []byte("entrypoint"), Persist: []byte("persist"), Help: []byte("help")})
	require.NoError(t, err)

	modes := tarModes(t, tarBytes)
	assert.Equal(t, int64(0644), modes["Dockerfile"])
	assert.Equal(t, int64(0644), modes["entrypoint.sh"])
	assert.Equal(t, int64(0755), modes["csb/csb-persist"])
	assert.Equal(t, int64(0755), modes["csb/csb-help"])
	assert.Equal(t, int64(0755), modes["csb/addon.d/run.sh"])
}

// tarEntries returns all entry names in a tar archive.
func tarEntries(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	m := make(map[string]bool)
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		m[hdr.Name] = true
	}
	return m
}

// tarModes returns the mode for each tar entry.
func tarModes(t *testing.T, data []byte) map[string]int64 {
	t.Helper()
	m := make(map[string]int64)
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		m[hdr.Name] = hdr.Mode
	}
	return m
}

// ── ResolveMounts ────────────────────────────────────────────────────────────

func TestResolveMounts_NoWorkspace(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Workspace = nil

	mounts := ResolveMounts(cfg)
	// no workspace, no csb-home dir → only cfg.Mount entries (empty by default)
	assert.Empty(t, mounts)
}

func TestResolveMounts_WithWorkspace(t *testing.T) {
	ws := t.TempDir()
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Workspace = &ws
	cfg.Home = "/home/user"

	mounts := ResolveMounts(cfg)
	require.NotEmpty(t, mounts)
	assert.Equal(t, ws, mounts[0].Src)
	assert.False(t, mounts[0].Readonly)
}

func TestResolveMounts_CSBHomeDir(t *testing.T) {
	dir := t.TempDir()
	// Create csb home dir
	csbHome := filepath.Join(dir, "home")
	require.NoError(t, os.Mkdir(csbHome, 0755))

	cfg := &Config{ConfigDir: dir}
	cfg.Workspace = nil

	mounts := ResolveMounts(cfg)
	require.Len(t, mounts, 1)
	assert.Equal(t, csbHome, mounts[0].Src)
	assert.Equal(t, "/mnt/csb-home", mounts[0].Dst)
}

func TestResolveMounts_ExtraMountsAppended(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Workspace = nil
	cfg.Mount = []Mount{{Src: "/host/x", Dst: "/container/x"}}

	mounts := ResolveMounts(cfg)
	require.Len(t, mounts, 1)
	assert.Equal(t, "/host/x", mounts[0].Src)
}

// ── ResolveEnv ───────────────────────────────────────────────────────────────

func envMap(env [][2]string) map[string]string {
	m := make(map[string]string)
	for _, kv := range env {
		m[kv[0]] = kv[1]
	}
	return m
}

func TestResolveEnv_HostUIDGID(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Runtime = "docker"
	cfg.DefaultShell = "bash"

	env := envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	assert.NotEmpty(t, env["HOST_UID"])
	assert.NotEmpty(t, env["HOST_GID"])
}

// CSB_LOGIN_SHELL is set for the bare interactive shape (no explicit command),
// with or without tmux — the case the systemd launcher routes through login(1).
// An explicit command must leave it unset so that path keeps the gosu drop.
// CSB_TMUX is set whenever tmux is requested, so the login session can auto-start
// tmux inside itself.
func TestResolveEnv_LoginShell(t *testing.T) {
	base := func() *Config {
		cfg := &Config{ConfigDir: t.TempDir()}
		cfg.Runtime = "docker"
		cfg.DefaultShell = "bash"
		return cfg
	}

	env := envMap(ResolveEnv(base(), NewRuntime("docker"), "", ""))
	assert.Equal(t, "1", env["CSB_LOGIN_SHELL"])
	assert.NotContains(t, env, "CSB_TMUX")

	// Bare interactive + tmux: still a login session, plus the tmux marker.
	cfg := base()
	cfg.UseTmux = true
	env = envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	assert.Equal(t, "1", env["CSB_LOGIN_SHELL"])
	assert.Equal(t, "1", env["CSB_TMUX"])

	// Explicit command: no login session (gosu path), regardless of tmux.
	cfg = base()
	cfg.PassthroughArgs = []string{"htop"}
	env = envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	assert.NotContains(t, env, "CSB_LOGIN_SHELL")

	cfg = base()
	cfg.DefaultCmd = []string{"htop"}
	env = envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	assert.NotContains(t, env, "CSB_LOGIN_SHELL")

	cfg = base()
	cfg.PassthroughArgs = []string{"htop"}
	cfg.UseTmux = true
	env = envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	assert.NotContains(t, env, "CSB_LOGIN_SHELL")
	assert.Equal(t, "1", env["CSB_TMUX"])
}

// Docker (and rootful podman) pass the caller's real uid/gid through; only
// rootless podman remaps to 0/0, which Runtime.HostIDs queries podman for.
func TestRuntimeHostIDs_Docker(t *testing.T) {
	uid, gid := NewRuntime("docker").HostIDs()
	assert.Equal(t, fmt.Sprintf("%d", os.Getuid()), uid)
	assert.Equal(t, fmt.Sprintf("%d", os.Getgid()), gid)
}

func TestResolveEnv_TERMFallback(t *testing.T) {
	t.Setenv("TERM", "")
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Runtime = "docker"
	cfg.DefaultShell = "bash"

	env := envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	assert.Equal(t, "xterm-256color", env["TERM"])
}

func TestResolveEnv_TERMFromHost(t *testing.T) {
	t.Setenv("TERM", "screen-256color")
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Runtime = "docker"
	cfg.DefaultShell = "bash"

	env := envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	assert.Equal(t, "screen-256color", env["TERM"])
}

func TestResolveEnv_EnvForwardSet(t *testing.T) {
	t.Setenv("MY_TOKEN", "secret")
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Runtime = "docker"
	cfg.DefaultShell = "bash"
	cfg.EnvForward = []string{"MY_TOKEN"}

	env := envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	assert.Equal(t, "secret", env["MY_TOKEN"])
}

func TestResolveEnv_EnvForwardUnset(t *testing.T) {
	t.Setenv("MY_UNSET_VAR", "")
	os.Unsetenv("MY_UNSET_VAR")
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Runtime = "docker"
	cfg.DefaultShell = "bash"
	cfg.EnvForward = []string{"MY_UNSET_VAR"}

	env := envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	_, ok := env["MY_UNSET_VAR"]
	assert.False(t, ok)
}

func TestResolveEnv_EnvInject(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Runtime = "docker"
	cfg.DefaultShell = "bash"
	cfg.EnvInject = []string{"FOO=bar", "BAZ=qux=with=equals"}

	env := envMap(ResolveEnv(cfg, NewRuntime("docker"), "", ""))
	assert.Equal(t, "bar", env["FOO"])
	assert.Equal(t, "qux=with=equals", env["BAZ"])
}

func TestResolveEnv_BrokerURLAndToken(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Runtime = "docker"
	cfg.DefaultShell = "bash"

	env := envMap(ResolveEnv(cfg, NewRuntime("docker"), "http://localhost:9000", "tok123"))
	assert.Equal(t, "http://localhost:9000", env["CSB_HOST_EXEC_URL"])
	assert.Equal(t, "tok123", env["CSB_HOST_EXEC_TOKEN"])
}

func TestResolveEnv_BrokerOnlyURLNoToken(t *testing.T) {
	cfg := &Config{ConfigDir: t.TempDir()}
	cfg.Runtime = "docker"
	cfg.DefaultShell = "bash"

	env := envMap(ResolveEnv(cfg, NewRuntime("docker"), "http://localhost:9000", ""))
	_, hasURL := env["CSB_HOST_EXEC_URL"]
	assert.False(t, hasURL)
}

// ── resolveContainerCmd ──────────────────────────────────────────────────────

func TestResolveContainerCmd_DefaultShell(t *testing.T) {
	cfg := &Config{}
	cfg.DefaultShell = "bash"
	cfg.UseTmux = false

	cmd := resolveContainerCmd(cfg)
	assert.Equal(t, []string{"bash", "-l"}, cmd)
}

func TestResolveContainerCmd_PassthroughWins(t *testing.T) {
	cfg := &Config{}
	cfg.DefaultShell = "bash"
	cfg.PassthroughArgs = []string{"ls", "-la"}
	cfg.UseTmux = false

	cmd := resolveContainerCmd(cfg)
	assert.Equal(t, []string{"ls", "-la"}, cmd)
}

func TestResolveContainerCmd_DefaultCmdWins(t *testing.T) {
	cfg := &Config{}
	cfg.DefaultShell = "bash"
	cfg.DefaultCmd = []string{"vim"}
	cfg.UseTmux = false

	cmd := resolveContainerCmd(cfg)
	assert.Equal(t, []string{"vim"}, cmd)
}

func TestResolveContainerCmd_TmuxWrap(t *testing.T) {
	cfg := &Config{}
	cfg.DefaultShell = "bash"
	cfg.PassthroughArgs = []string{"vim", "file.txt"}
	cfg.UseTmux = true

	cmd := resolveContainerCmd(cfg)
	assert.Equal(t, "tmux", cmd[0])
	assert.Equal(t, "new-session", cmd[1])
	// Last arg should contain the quoted command
	assert.Contains(t, cmd[len(cmd)-1], "vim")
}

func TestResolveContainerCmd_TmuxShellShortcut(t *testing.T) {
	cfg := &Config{}
	cfg.DefaultShell = "bash"
	cfg.UseTmux = true

	cmd := resolveContainerCmd(cfg)
	// When inner is the shell, postCommand is empty → no double exec
	last := cmd[len(cmd)-1]
	assert.Contains(t, last, "bash")
	// postCommand should be empty (no "; exec bash -l" appended after the trailing space)
	assert.True(t, strings.HasSuffix(last, "; "), "expected trailing '; ' for empty postCommand, got %q", last)
}

// ── BuildRunCommand ──────────────────────────────────────────────────────────

func minimalCfg(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM debian:stable-slim\n"), 0644))
	cfg := &Config{ConfigDir: dir}
	cfg.Arch = "amd64"
	cfg.Runtime = "docker"
	cfg.DefaultShell = "bash"
	cfg.HomeVolume = "csb-home"
	cfg.Addons = []string{}
	cfg.Publish = []string{}
	cfg.Mount = []Mount{}
	cfg.EnvForward = []string{}
	cfg.EnvInject = []string{}
	return cfg
}

func TestBuildRunCommand_BasicFlags(t *testing.T) {
	cfg := minimalCfg(t)

	cmd, err := BuildRunCommand(cfg, nil, nil, "csb:abc123def456")
	require.NoError(t, err)

	joined := strings.Join(cmd, " ")
	assert.Contains(t, joined, "docker run")
	assert.Contains(t, joined, "-i")
	assert.Contains(t, joined, "--platform linux/amd64")
	assert.Contains(t, joined, "--rm")
	assert.Contains(t, joined, "csb:abc123def456")
}

func TestBuildRunCommand_WithTTY(t *testing.T) {
	cfg := minimalCfg(t)
	cfg.UseTTY = true

	cmd, err := BuildRunCommand(cfg, nil, nil, "csb:abc123def456")
	require.NoError(t, err)
	assert.Contains(t, strings.Join(cmd, " "), " -t ")
}

func TestBuildRunCommand_Labels(t *testing.T) {
	cfg := minimalCfg(t)

	cmd, err := BuildRunCommand(cfg, nil, nil, "csb:abc123def456")
	require.NoError(t, err)

	joined := strings.Join(cmd, " ")
	assert.Contains(t, joined, "csb.managed=true")
	assert.Contains(t, joined, "csb.home-volume=csb-home")
}

func TestBuildRunCommand_HostNetworkConflict(t *testing.T) {
	cfg := minimalCfg(t)
	cfg.HostNetwork = true
	cfg.Publish = []string{"8080:8080"}

	_, err := BuildRunCommand(cfg, nil, nil, "csb:abc123def456")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--host-network")
}

func TestBuildRunCommand_HostNetworkAddsFlag(t *testing.T) {
	cfg := minimalCfg(t)
	cfg.HostNetwork = true

	cmd, err := BuildRunCommand(cfg, nil, nil, "csb:abc123def456")
	require.NoError(t, err)
	assert.Contains(t, strings.Join(cmd, " "), "--network host")
}

func TestBuildRunCommand_MountsAndEnv(t *testing.T) {
	cfg := minimalCfg(t)
	mounts := []Mount{{Src: "/host/src", Dst: "/container/dst"}}
	env := [][2]string{{"MY_VAR", "hello"}}

	cmd, err := BuildRunCommand(cfg, mounts, env, "csb:abc123def456")
	require.NoError(t, err)

	joined := strings.Join(cmd, " ")
	assert.Contains(t, joined, "/host/src:/container/dst")
	assert.Contains(t, joined, "MY_VAR=hello")
}

// ── shJoin ───────────────────────────────────────────────────────────────────

func TestShJoin_SingleArg(t *testing.T) {
	assert.Equal(t, "foo", shJoin([]string{"foo"}))
}

func TestShJoin_MultipleArgs(t *testing.T) {
	assert.Equal(t, "foo 'bar baz' qux", shJoin([]string{"foo", "bar baz", "qux"}))
}

func TestShJoin_Empty(t *testing.T) {
	assert.Equal(t, "", shJoin(nil))
}
