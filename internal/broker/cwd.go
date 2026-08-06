package broker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveCwd turns the client-supplied workspace-relative path into an absolute
// host directory, refusing anything that would land outside the workspace root.
//
// An empty rel means "no preference" and leaves the broker's own cwd in place.
// The check is done on the symlink-resolved paths, so a symlink inside the
// workspace that points elsewhere on the host cannot be used to escape.
func resolveCwd(workspace, rel string) (string, error) {
	if rel == "" {
		return "", nil
	}
	if workspace == "" {
		return "", fmt.Errorf("no workspace is mounted on the host")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("cwd must be relative to the workspace root, got %q", rel)
	}

	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("cwd escapes the workspace root: %q", rel)
	}

	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolving workspace root: %w", err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return "", fmt.Errorf("resolving cwd %q: %w", rel, err)
	}
	if dir != root && !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("cwd escapes the workspace root: %q", rel)
	}

	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("resolving cwd %q: %w", rel, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd is not a directory: %q", rel)
	}
	return dir, nil
}
