package main

import (
	"os"
	"path/filepath"
	"testing"
)

// chdir moves the test process into dir for the duration of the test and points
// CSB_WORKSPACE_DIR at root, mimicking a shell inside the sandbox workspace.
func chdir(t *testing.T, root, dir string) {
	t.Helper()
	t.Setenv("CSB_WORKSPACE_DIR", root)
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func TestResolveCwdMirrorsCurrentDirectory(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "internal", "broker")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, ws, sub)

	got, err := resolveCwd("", true)
	if err != nil {
		t.Fatalf("resolveCwd: %v", err)
	}
	if got != "internal/broker" {
		t.Errorf("got %q, want %q", got, "internal/broker")
	}
}

func TestResolveCwdMirrorsWorkspaceRoot(t *testing.T) {
	ws := t.TempDir()
	chdir(t, ws, ws)

	got, err := resolveCwd("", true)
	if err != nil {
		t.Fatalf("resolveCwd: %v", err)
	}
	if got != "." {
		t.Errorf("got %q, want %q", got, ".")
	}
}

func TestResolveCwdMirrorOutsideWorkspaceIsSilent(t *testing.T) {
	// $HOME is outside the workspace: fall back to the broker's own directory
	// rather than failing a command that would have worked before.
	ws := t.TempDir()
	elsewhere := t.TempDir()
	chdir(t, ws, elsewhere)

	got, err := resolveCwd("", true)
	if err != nil {
		t.Fatalf("resolveCwd: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolveCwdMirrorWithoutWorkspaceIsSilent(t *testing.T) {
	dir := t.TempDir()
	chdir(t, "", dir)

	got, err := resolveCwd("", true)
	if err != nil {
		t.Fatalf("resolveCwd: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolveCwdExplicitRelative(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, ws, filepath.Join(ws, "a"))

	got, err := resolveCwd("b", true)
	if err != nil {
		t.Fatalf("resolveCwd: %v", err)
	}
	if got != "a/b" {
		t.Errorf("got %q, want %q", got, "a/b")
	}
}

func TestResolveCwdExplicitAbsolute(t *testing.T) {
	ws := t.TempDir()
	chdir(t, ws, ws)

	got, err := resolveCwd(filepath.Join(ws, "cmd"), true)
	if err != nil {
		t.Fatalf("resolveCwd: %v", err)
	}
	if got != "cmd" {
		t.Errorf("got %q, want %q", got, "cmd")
	}
}

func TestResolveCwdExplicitOutsideWorkspaceErrors(t *testing.T) {
	ws := t.TempDir()
	chdir(t, ws, ws)

	if got, err := resolveCwd("/etc", true); err == nil {
		t.Errorf("resolveCwd(/etc) = %q, want error", got)
	}
	if got, err := resolveCwd("..", true); err == nil {
		t.Errorf("resolveCwd(..) = %q, want error", got)
	}
}

func TestResolveCwdNoCwdOptsOut(t *testing.T) {
	ws := t.TempDir()
	sub := filepath.Join(ws, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, ws, sub)

	got, err := resolveCwd("", false)
	if err != nil {
		t.Fatalf("resolveCwd: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
