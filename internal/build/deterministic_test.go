// Package build tests properties of the compiled binaries rather than their runtime behaviour.
package build_test

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// moduleRoot returns the directory containing go.mod by asking the toolchain.
func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

// buildHostRun compiles cmd/csb-host-run with the same flags the Makefile uses
// and writes the result to outPath.
func buildHostRun(t *testing.T, root, outPath string) {
	t.Helper()
	cmd := exec.Command("go", "build",
		"-ldflags=-s -w",
		"-trimpath",
		"-o", outPath,
		"./cmd/csb-host-run",
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build (out=%s): %v\n%s", outPath, err, output)
	}
}

// TestDeterministicBuild verifies that building cmd/csb-host-run twice with
// identical flags produces byte-for-byte identical output. This catches missing
// -trimpath, CGO leakage, or any other source of build-time non-determinism.
func TestDeterministicBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping deterministic build test in short mode")
	}

	root := moduleRoot(t)

	var hashes [2][sha256.Size]byte
	for i := range hashes {
		outPath := filepath.Join(t.TempDir(), "csb-host-run")
		buildHostRun(t, root, outPath)

		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		hashes[i] = sha256.Sum256(data)
	}

	if hashes[0] != hashes[1] {
		t.Errorf("build is not deterministic: build 1 sha256=%x, build 2 sha256=%x", hashes[0], hashes[1])
	}
}
