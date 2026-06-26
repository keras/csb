package csb

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
// addonsFS is expected to be rooted such that it contains "files/addons/<name>/<file>" entries;
// those are materialized into <configDir>/addons/<name>/<file>. The Dockerfile and addon
// install scripts come from managedEmbeddedFiles, which the sync commands also use so that
// seeding and drift detection always agree on the managed set.
func InitConfigDir(configDir string, dockerfile []byte, addonsFS fs.FS) {
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Initialising default config at %s …\n", configDir)
	}

	writeFile := func(rel string, content []byte) {
		path := filepath.Join(configDir, rel)
		if _, err := os.Stat(path); err == nil {
			return
		}
		// Per-file noise is verbose-only; the "Initialising …" line above is
		// enough to signal a fresh config dir is being seeded.
		logInfo("creating config file", "path", path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "csb: warning: failed to create dir for %s: %v\n", path, err)
			return
		}
		if err := os.WriteFile(path, content, embeddedFileMode(rel)); err != nil {
			fmt.Fprintf(os.Stderr, "csb: warning: failed to write %s: %v\n", path, err)
		}
	}

	mkDir := func(rel string) {
		path := filepath.Join(configDir, rel)
		if _, err := os.Stat(path); err == nil {
			return
		}
		logInfo("creating config dir", "path", path)
		if err := os.MkdirAll(path, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "csb: warning: failed to create %s: %v\n", path, err)
		}
	}

	writeFile("config.yaml", []byte(renderConfigTemplate()))
	mkDir("home")

	// Dockerfile + addon install scripts, from the shared managed set.
	managed, err := managedEmbeddedFiles(dockerfile, addonsFS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "csb: warning: failed to enumerate shipped resources: %v\n", err)
		return
	}
	rels := make([]string, 0, len(managed))
	for rel := range managed {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		writeFile(rel, managed[rel])
	}
}
