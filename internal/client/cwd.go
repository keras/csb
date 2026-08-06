package client

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RelCwd maps a container-side directory to a path relative to the workspace
// root, which is the form the broker accepts. workspaceRoot and dir must both be
// absolute container paths. The result is "." for the root itself.
//
// The broker re-checks containment on the host, so this is about giving the user
// a clear local error rather than a round trip that ends in invalid_cwd.
func RelCwd(workspaceRoot, dir string) (string, error) {
	if workspaceRoot == "" {
		return "", fmt.Errorf("no workspace in this sandbox (CSB_WORKSPACE_DIR not set)")
	}
	root := filepath.Clean(workspaceRoot)
	abs := filepath.Clean(dir)
	if abs == root {
		return ".", nil
	}
	if !strings.HasPrefix(abs, root+"/") {
		return "", fmt.Errorf("%s is outside the workspace %s", dir, root)
	}
	return strings.TrimPrefix(abs, root+"/"), nil
}
