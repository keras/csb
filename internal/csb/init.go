package csb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// renderConfigTemplate builds the commented config.yaml template.
func renderConfigTemplate() string {
	type optEntry struct {
		key     string
		example string
	}

	opts := []optEntry{
		{"tmux", "true"},
		{"tty", "true          # default: auto-detect from stdin"},
		{"mount", "\n#   - ~/.ssh:~/.ssh:ro"},
		{"runtime", "auto"},
		{"base_image", "debian:stable-slim"},
		{"nested_podman", "false"},
		{"addons", "[mise]"},
		{"home_volume", "csb-home"},
		{"image", "my-custom:latest"},
		{"env_forward", "[MY_TOKEN, OTHER_VAR]"},
		{"env", "[MY_VAR=hello, DEBUG=1]"},
		{"publish", "\n#   - 8080:8080\n#   - 127.0.0.1:5432:5432"},
		{"host_network", "false"},
		{"host_exec_enabled", "false"},
		{"host_exec_allow", `["open *", "git log **"]`},
		{"host_exec_bind", "0.0.0.0:0"},
	}

	lines := []string{
		"# csb configuration — uncomment and edit as needed.",
		"# See: csb --help",
		"#",
	}

	for _, opt := range opts {
		example := opt.example
		if strings.HasPrefix(example, "\n") {
			lines = append(lines, "# "+opt.key+":")
			for _, line := range strings.Split(strings.TrimLeft(example, "\n"), "\n") {
				if strings.HasPrefix(line, "#") {
					lines = append(lines, line)
				} else {
					lines = append(lines, "# "+line)
				}
			}
		} else {
			lines = append(lines, fmt.Sprintf("# %s: %s", opt.key, example))
		}
		lines = append(lines, "#")
	}

	// Remove trailing "#" if present
	if len(lines) > 0 && lines[len(lines)-1] == "#" {
		lines = lines[:len(lines)-1]
	}

	return strings.Join(lines, "\n") + "\n"
}

// InitConfigDir creates the config directory with default files if needed.
func InitConfigDir(configDir string, miseAddonContent []byte) {
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Initialising default config at %s …\n", configDir)
	}

	files := []struct {
		rel     string
		content []byte
		isDir   bool
	}{
		{"config.yaml", []byte(renderConfigTemplate()), false},
		{"home", nil, true},
		{"addons/mise.sh", miseAddonContent, false},
	}

	for _, f := range files {
		path := filepath.Join(configDir, f.rel)
		if _, err := os.Stat(path); err == nil {
			continue // already exists
		}

		fmt.Fprintf(os.Stderr, "Creating %s …\n", path)
		if f.isDir {
			if err := os.MkdirAll(path, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "csb: warning: failed to create %s: %v\n", path, err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				fmt.Fprintf(os.Stderr, "csb: warning: failed to create dir for %s: %v\n", path, err)
				continue
			}
			if err := os.WriteFile(path, f.content, 0644); err != nil {
				fmt.Fprintf(os.Stderr, "csb: warning: failed to write %s: %v\n", path, err)
			}
		}
	}
}
