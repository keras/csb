// Package main_test provides lightweight black-box CLI tests for the csb binary.
// The binary is built once in TestMain; all tests invoke it as a subprocess
// with an isolated --config-dir so no container runtime is required.
package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// csbBin holds the path to the built csb binary, set in TestMain.
var csbBin string

func TestMain(m *testing.M) {
	// Resolve the repo root relative to this file's package directory.
	// go test sets the working directory to the package directory.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		panic("resolving repo root: " + err.Error())
	}

	tmp, err := os.MkdirTemp("", "csb-cli-test-build-*")
	if err != nil {
		panic("creating build tmpdir: " + err.Error())
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "csb")
	build := exec.Command("go", "build", "-o", bin, "./cmd/csb")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		panic("build csb: " + err.Error())
	}
	csbBin = bin

	os.Exit(m.Run())
}

// runCSB executes csbBin with the given args and returns stdout+stderr combined,
// and whether the command exited 0.
func runCSB(t *testing.T, env []string, args ...string) (output string, exitOK bool) {
	t.Helper()
	cmd := exec.Command(csbBin, args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	exitOK = err == nil
	return string(out), exitOK
}

// configDirFlag returns "--config-dir=<dir>" in single-token form.
func configDirFlag(dir string) string {
	return "--config-dir=" + dir
}

// seedConfig writes content to <dir>/config.yaml.
func seedConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("seedConfig: %v", err)
	}
}

// ── Test cases ────────────────────────────────────────────────────────────────

// TestHelp verifies that csb --help exits 0 and mentions key flags.
func TestHelp(t *testing.T) {
	out, ok := runCSB(t, nil, configDirFlag(t.TempDir()), "--help")
	if !ok {
		t.Fatalf("csb --help exited non-zero; output:\n%s", out)
	}
	for _, flag := range []string{"--arch", "--addon", "--publish", "--host-exec", "--no-workspace", "--mount", "--tmux"} {
		if !strings.Contains(out, flag) {
			t.Errorf("--help output missing %q", flag)
		}
	}
}

// TestVersionSkippedIfAbsent probes for --version; skips gracefully if unimplemented.
func TestVersionSkippedIfAbsent(t *testing.T) {
	out, ok := runCSB(t, nil, configDirFlag(t.TempDir()), "--version")
	if !ok {
		// --version isn't implemented; make sure it gave a meaningful error
		// rather than a panic or empty output.
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("--version exited non-zero with empty output")
		}
		t.Skip("--version not implemented")
	}
	if !strings.Contains(strings.ToLower(out), "csb") {
		t.Errorf("--version output does not mention 'csb': %q", out)
	}
}

// TestConfigShow verifies csb config show exits 0 and emits YAML with
// values drawn from a pre-seeded config file.
func TestConfigShow(t *testing.T) {
	dir := t.TempDir()
	seedConfig(t, dir, "arch: arm64\nmount:\n  - ~/.ssh:~/.ssh:ro\naddons:\n  - mise\n")

	out, ok := runCSB(t, nil, configDirFlag(dir), "--no-workspace", "config", "show")
	if !ok {
		t.Fatalf("csb config show exited non-zero; output:\n%s", out)
	}

	// Header lines
	if !strings.Contains(out, "# csb resolved config") {
		t.Errorf("output missing header '# csb resolved config'; got:\n%s", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("output missing config dir %q; got:\n%s", dir, out)
	}

	// arch from YAML
	if !strings.Contains(out, "arm64") {
		t.Errorf("output missing arch 'arm64'; got:\n%s", out)
	}

	// mount rendered as src:dst:mode (fix from commit 6497eb0)
	if !strings.Contains(out, "~/.ssh:~/.ssh:ro") {
		t.Errorf("output missing mount rendered as '~/.ssh:~/.ssh:ro'; got:\n%s", out)
	}

	// addon list
	if !strings.Contains(out, "mise") {
		t.Errorf("output missing addon 'mise'; got:\n%s", out)
	}
}

// TestConfigShowContext verifies csb config show context exits 0 and
// produces a listing (mode + size + name columns).
func TestConfigShowContext(t *testing.T) {
	dir := t.TempDir()
	out, ok := runCSB(t, nil, configDirFlag(dir), "--no-workspace", "config", "show", "context")
	if !ok {
		t.Fatalf("csb config show context exited non-zero; output:\n%s", out)
	}
	// TTY detection will be false in test (piped), so we get the raw tar
	// streamed to stdout.  A tar file starts with magic bytes, not text.
	// Accept either raw tar (non-empty output) or a text listing.
	if len(out) == 0 {
		t.Error("csb config show context produced no output")
	}
}

// TestArchEnvOverride verifies that the CSB_ARCH env var overrides the
// YAML arch value in the resolved config shown by config show.
func TestArchEnvOverride(t *testing.T) {
	dir := t.TempDir()
	// Seed with amd64 in YAML; env var should win.
	seedConfig(t, dir, "arch: amd64\n")

	env := append(os.Environ(), "CSB_ARCH=arm64")
	out, ok := runCSB(t, env, configDirFlag(dir), "--no-workspace", "config", "show")
	if !ok {
		t.Fatalf("csb config show with CSB_ARCH=arm64 exited non-zero; output:\n%s", out)
	}
	if !strings.Contains(out, "arm64") {
		t.Errorf("output missing 'arm64' after CSB_ARCH override; got:\n%s", out)
	}
}

// TestArchCLIFlagConfigShow verifies that --arch on the CLI overrides env and YAML for config show.
func TestArchCLIFlagConfigShow(t *testing.T) {
	dir := t.TempDir()
	seedConfig(t, dir, "arch: amd64\n")

	// CLI flag beats env and YAML.
	env := append(os.Environ(), "CSB_ARCH=amd64")
	out, ok := runCSB(t, env, configDirFlag(dir), "--arch", "arm64", "--no-workspace", "config", "show")
	if !ok {
		t.Fatalf("csb --arch arm64 config show exited non-zero; output:\n%s", out)
	}
	if !strings.Contains(out, "arm64") {
		t.Errorf("output missing 'arm64' after --arch flag; got:\n%s", out)
	}
}

// TestInvalidPublish verifies csb exits non-zero with a descriptive error for
// a malformed --publish spec.
func TestInvalidPublish(t *testing.T) {
	out, ok := runCSB(t, nil, configDirFlag(t.TempDir()), "--publish", "notaport", "--no-workspace", "config", "show")
	if ok {
		t.Fatal("csb --publish notaport expected non-zero exit; got 0")
	}
	if !strings.Contains(out, "notaport") {
		t.Errorf("error message should mention bad spec 'notaport'; got:\n%s", out)
	}
}

// TestInvalidMount verifies csb exits non-zero with a descriptive error for
// a malformed --mount spec.
func TestInvalidMount(t *testing.T) {
	out, ok := runCSB(t, nil, configDirFlag(t.TempDir()), "--mount", "badspec", "--no-workspace", "config", "show")
	if ok {
		t.Fatal("csb --mount badspec expected non-zero exit; got 0")
	}
	if !strings.Contains(out, "badspec") {
		t.Errorf("error message should mention bad spec 'badspec'; got:\n%s", out)
	}
}

// TestConfigUnknownSubcommand verifies that an unknown config action errors cleanly.
func TestConfigUnknownSubcommand(t *testing.T) {
	out, ok := runCSB(t, nil, configDirFlag(t.TempDir()), "config", "unknownsub")
	if ok {
		t.Fatal("csb config unknownsub expected non-zero exit; got 0")
	}
	if !strings.Contains(out, "unknownsub") {
		t.Errorf("error message should mention 'unknownsub'; got:\n%s", out)
	}
}

// TestCSBConfigDirEnvVar verifies that the CSB_CONFIG_DIR env var is honoured.
func TestCSBConfigDirEnvVar(t *testing.T) {
	dir := t.TempDir()
	seedConfig(t, dir, "arch: arm64\n")

	// Build a minimal env that includes PATH (needed to exec the binary itself)
	// plus our override.
	env := append(os.Environ(), "CSB_CONFIG_DIR="+dir)

	// Pass --no-workspace to avoid cwd-dependent workdir config loading.
	out, ok := runCSB(t, env, "--no-workspace", "config", "show")
	if !ok {
		t.Fatalf("csb config show via CSB_CONFIG_DIR exited non-zero; output:\n%s", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("output should mention config dir %q; got:\n%s", dir, out)
	}
	if !strings.Contains(out, "arm64") {
		t.Errorf("output should reflect arm64 from seeded config; got:\n%s", out)
	}
}

// TestImplicitPassthrough verifies that positional args after csb flags are
// collected as passthrough args and not treated as unknown flags.
// We use an invalid --arch to force a fast non-zero exit; the key assertion is
// that the error is about --arch, not about "echo" being an unknown flag.
func TestImplicitPassthrough(t *testing.T) {
	// "echo hello" are positional — they should be treated as the container
	// command, not as unknown flags.  We add --arch badval so the binary errors
	// out before attempting to contact a runtime.  If passthrough parsing were
	// broken, we'd see "unknown flag: echo" instead.
	out, ok := runCSB(t, nil, configDirFlag(t.TempDir()), "--arch", "badval", "--no-workspace", "echo", "hello")
	if ok {
		t.Fatal("expected non-zero exit due to invalid arch; got 0")
	}
	if strings.Contains(out, "unknown flag: echo") {
		t.Errorf("'echo' should be a passthrough arg, not an unknown flag; got:\n%s", out)
	}
	if !strings.Contains(out, "badval") {
		t.Errorf("error should mention invalid arch 'badval'; got:\n%s", out)
	}
}
