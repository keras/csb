package csb

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// renderConfigTemplate builds the commented config.yaml template from Options struct tags.
// Each field with a yaml: and example: tag produces one commented entry.
// Example values containing literal \n are rendered as multi-line list blocks.
func renderConfigTemplate() string {
	t := reflect.TypeOf(Options{})

	lines := []string{
		"# csb configuration — uncomment and edit as needed.",
		"# See: csb --help",
		"#",
	}

	for i := range t.NumField() {
		f := t.Field(i)
		yamlKey := f.Tag.Get("yaml")
		example := f.Tag.Get("example")
		if yamlKey == "" || example == "" {
			continue
		}
		// Literal \n in the tag value acts as a line separator for multi-line blocks.
		example = strings.ReplaceAll(example, `\n`, "\n")

		if strings.HasPrefix(example, "\n") {
			lines = append(lines, "# "+yamlKey+":")
			for _, line := range strings.Split(strings.TrimLeft(example, "\n"), "\n") {
				lines = append(lines, "#   "+line)
			}
		} else {
			lines = append(lines, fmt.Sprintf("# %s: %s", yamlKey, example))
		}
		lines = append(lines, "#")
	}

	// Remove trailing "#"
	if len(lines) > 0 && lines[len(lines)-1] == "#" {
		lines = lines[:len(lines)-1]
	}

	return strings.Join(lines, "\n") + "\n"
}

// InitConfigDir creates the config directory with default files if needed.
func InitConfigDir(configDir string, miseAddonContent, podmanAddonContent, sudoAddonContent, guiAddonContent []byte) {
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
		{"addons/podman.sh", podmanAddonContent, false},
		{"addons/sudo.sh", sudoAddonContent, false},
		{"addons/gui.sh", guiAddonContent, false},
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
