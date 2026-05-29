//go:build addons

// Package addons_test runs each addon's companion test.sh script inside a
// freshly built csb container, asserting it exits 0. Opt-in: requires the
// `addons` build tag and a working Docker or Podman runtime on the host.
//
//	go test -tags addons ./internal/addons/...
package addons_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var (
	csbBin   string
	repoRoot string
)

func TestMain(m *testing.M) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		panic(err)
	}
	repoRoot = root

	build := exec.Command("make", "build-csb")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		panic("build csb: " + err.Error())
	}
	csbBin = filepath.Join(repoRoot, "bin", "csb")

	os.Exit(m.Run())
}

// TestGuiDynamicPublish verifies that `--publish 6080` (bare container port)
// allocates a host port and injects CSB_PUBLISH_6080 into the container.
func TestGuiDynamicPublish(t *testing.T) {
	script := `set -euo pipefail
test -n "${CSB_PUBLISH_6080:-}"
gui-start | grep -q "http://localhost:${CSB_PUBLISH_6080}/vnc.html"
`
	cmd := exec.Command(csbBin,
		"--config-dir", t.TempDir(),
		"--addon", "gui",
		"--publish", "6080",
		"--no-workspace",
		"--", "bash", "-c", script)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("dynamic publish failed: %v\n--- output ---\n%s", err, out.String())
	}
}

func TestAddons(t *testing.T) {
	addonsDir := filepath.Join(repoRoot, "cmd", "csb", "files", "addons")
	entries, err := os.ReadDir(addonsDir)
	if err != nil {
		t.Fatal(err)
	}

	var found int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		testPath := filepath.Join(addonsDir, name, "test.sh")
		if _, err := os.Stat(testPath); err != nil {
			continue
		}
		found++
		t.Run(name, func(t *testing.T) {
			script, err := os.ReadFile(testPath)
			if err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(csbBin,
				"--config-dir", t.TempDir(),
				"--addon", name,
				"--no-workspace",
				"--", "bash", "-c", string(script))

			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				t.Fatalf("addon %s failed: %v\n--- output ---\n%s", name, err, out.String())
			}
		})
	}
	if found == 0 {
		t.Fatal("no addon test.sh scripts found under " + addonsDir)
	}
}
