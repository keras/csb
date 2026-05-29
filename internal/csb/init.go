package csb

import (
	"embed"
	"fmt"
	"io/fs"
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
// addonsFS is expected to be rooted such that it contains "files/addons/<name>/<file>" entries;
// those are materialized into <configDir>/addons/<name>/<file>.
func InitConfigDir(configDir string, addonsFS embed.FS) {
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Initialising default config at %s …\n", configDir)
	}

	writeFile := func(rel string, content []byte) {
		path := filepath.Join(configDir, rel)
		if _, err := os.Stat(path); err == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "Creating %s …\n", path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "csb: warning: failed to create dir for %s: %v\n", path, err)
			return
		}
		if err := os.WriteFile(path, content, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "csb: warning: failed to write %s: %v\n", path, err)
		}
	}

	mkDir := func(rel string) {
		path := filepath.Join(configDir, rel)
		if _, err := os.Stat(path); err == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "Creating %s …\n", path)
		if err := os.MkdirAll(path, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "csb: warning: failed to create %s: %v\n", path, err)
		}
	}

	writeFile("config.yaml", []byte(renderConfigTemplate()))
	writeFile("Dockerfile", []byte(dockerfile))
	mkDir("home")

	const embedRoot = "files/addons"
	_ = fs.WalkDir(addonsFS, embedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// test.sh is a dev artifact (black-box addon tests) — don't ship it
		// into the user's config dir.
		if d.Name() == "test.sh" {
			return nil
		}
		rel, err := filepath.Rel(embedRoot, p)
		if err != nil {
			return nil
		}
		data, err := addonsFS.ReadFile(p)
		if err != nil {
			return nil
		}
		writeFile(filepath.Join("addons", rel), data)
		return nil
	})
}
