package broker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCwd(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "cmd", "csb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// t.TempDir may hand back a path through a symlink (macOS /var → /private/var),
	// so expectations are built from the resolved root.
	root, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		workspace string
		rel       string
		want      string
		wantErr   bool
	}{
		{name: "empty keeps broker cwd", workspace: ws, rel: "", want: ""},
		{name: "dot is the root", workspace: ws, rel: ".", want: root},
		{name: "subdirectory", workspace: ws, rel: "cmd/csb", want: filepath.Join(root, "cmd", "csb")},
		{name: "normalised traversal stays inside", workspace: ws, rel: "cmd/../cmd/csb", want: filepath.Join(root, "cmd", "csb")},
		{name: "parent escape", workspace: ws, rel: "..", wantErr: true},
		{name: "traversal escape", workspace: ws, rel: "cmd/../../etc", wantErr: true},
		{name: "absolute rejected", workspace: ws, rel: "/etc", wantErr: true},
		{name: "missing directory", workspace: ws, rel: "nope", wantErr: true},
		{name: "file is not a directory", workspace: ws, rel: "file.txt", wantErr: true},
		{name: "no workspace configured", workspace: "", rel: "cmd", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveCwd(tc.workspace, tc.rel)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveCwd(%q, %q) = %q, want error", tc.workspace, tc.rel, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveCwd(%q, %q): %v", tc.workspace, tc.rel, err)
			}
			if got != tc.want {
				t.Errorf("resolveCwd(%q, %q) = %q, want %q", tc.workspace, tc.rel, got, tc.want)
			}
		})
	}
}

func TestResolveCwdSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got, err := resolveCwd(ws, "escape"); err == nil {
		t.Errorf("resolveCwd through escaping symlink = %q, want error", got)
	}
}

func TestResolveCwdSymlinkInsideAllowed(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ws, "real"), filepath.Join(ws, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}

	got, err := resolveCwd(ws, "link")
	if err != nil {
		t.Fatalf("resolveCwd: %v", err)
	}
	if want := filepath.Join(root, "real"); got != want {
		t.Errorf("resolveCwd = %q, want %q", got, want)
	}
}
