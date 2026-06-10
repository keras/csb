package csb

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// syncState classifies a managed resource by comparing its on-disk copy under
// <ConfigDir> against the version embedded in the csb binary.
type syncState int

const (
	syncUpToDate  syncState = iota // on disk, identical to the shipped version
	syncMissing                    // not on disk; csb would (re)create it
	syncDiffers                    // on disk but different from the shipped version
	syncLocalOnly                  // on disk under addons/, not shipped by this binary
)

func (s syncState) label() string {
	switch s {
	case syncUpToDate:
		return "up to date"
	case syncMissing:
		return "missing"
	case syncDiffers:
		return "differs"
	case syncLocalOnly:
		return "local-only"
	}
	return "unknown"
}

// fileStatus is the drift state of one managed resource.
type fileStatus struct {
	Path  string // path relative to <ConfigDir>, e.g. "Dockerfile" or "addons/mise/install.sh"
	State syncState
}

// isPending reports whether this file needs action — it is either missing or
// differs from the shipped version.
func (f fileStatus) isPending() bool {
	return f.State == syncMissing || f.State == syncDiffers
}

const embedAddonsRoot = "files/addons"

// managedEmbeddedFiles returns the set of resources csb seeds into <ConfigDir>
// and tracks for drift, keyed by their path relative to <ConfigDir>. This is the
// single source of truth shared by InitConfigDir (seeding) and the sync commands
// (drift detection). test.sh is a dev artifact and is not shipped.
func managedEmbeddedFiles(addonsFS fs.FS) (map[string][]byte, error) {
	out := map[string][]byte{
		"Dockerfile": []byte(dockerfile),
	}
	err := fs.WalkDir(addonsFS, embedAddonsRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Name() == "test.sh" {
			return nil
		}
		rel, err := filepath.Rel(embedAddonsRoot, p)
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(addonsFS, p)
		if err != nil {
			return err
		}
		out[filepath.Join("addons", rel)] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// computeSyncStatus compares each managed file (and any local-only addon files)
// on disk against the embedded set, returning one fileStatus per resource sorted
// by path.
func computeSyncStatus(configDir string, embedded map[string][]byte) []fileStatus {
	var out []fileStatus
	seen := map[string]bool{}

	for rel, content := range embedded {
		seen[rel] = true
		disk, err := os.ReadFile(filepath.Join(configDir, rel))
		switch {
		case os.IsNotExist(err):
			out = append(out, fileStatus{rel, syncMissing})
		case err != nil:
			// Unreadable — treat as differing so it surfaces.
			out = append(out, fileStatus{rel, syncDiffers})
		case bytes.Equal(disk, content):
			out = append(out, fileStatus{rel, syncUpToDate})
		default:
			out = append(out, fileStatus{rel, syncDiffers})
		}
	}

	// Detect addon install scripts on disk that this binary does not ship.
	addonsDir := filepath.Join(configDir, "addons")
	_ = filepath.WalkDir(addonsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "install.sh" {
			return nil
		}
		rel, err := filepath.Rel(configDir, p)
		if err != nil || seen[rel] {
			return nil
		}
		out = append(out, fileStatus{rel, syncLocalOnly})
		return nil
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// pendingUpdateCount returns how many managed files are missing or differ from
// the shipped version — used for the verbose run-time nudge.
func pendingUpdateCount(configDir string, embedded map[string][]byte) int {
	n := 0
	for _, s := range computeSyncStatus(configDir, embedded) {
		if s.isPending() {
			n++
		}
	}
	return n
}

// embeddedFileMode returns the appropriate permission for a managed resource.
// Install scripts are seeded executable; all other shipped files get 0644.
func embeddedFileMode(relPath string) os.FileMode {
	if filepath.Base(relPath) == "install.sh" {
		return 0755
	}
	return 0644
}

// applySyncEntry writes the shipped content for relPath into <ConfigDir>. If a
// differing file already exists it is first copied to <file>.bak so a local copy
// is never lost. Identical existing files are left untouched.
func applySyncEntry(configDir, relPath string, content []byte) error {
	dest := filepath.Join(configDir, relPath)
	if existing, err := os.ReadFile(dest); err == nil {
		if bytes.Equal(existing, content) {
			return nil
		}
		if err := os.WriteFile(dest+".bak", existing, 0644); err != nil {
			return fmt.Errorf("writing backup %s.bak: %w", relPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", relPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("creating dir for %s: %w", relPath, err)
	}
	if err := os.WriteFile(dest, content, embeddedFileMode(relPath)); err != nil {
		return fmt.Errorf("writing %s: %w", relPath, err)
	}
	return nil
}

// RunConfigStatus prints a table of managed resources and their drift state.
func RunConfigStatus(cfg *Config, assets Assets) error {
	embedded, err := managedEmbeddedFiles(assets.AddonsFS)
	if err != nil {
		return fmt.Errorf("enumerating shipped resources: %w", err)
	}
	statuses := computeSyncStatus(cfg.ConfigDir, embedded)

	width := 0
	for _, s := range statuses {
		if len(s.Path) > width {
			width = len(s.Path)
		}
	}

	fmt.Printf("# csb managed resources\n# config dir: %s\n\n", cfg.ConfigDir)
	pending := 0
	for _, s := range statuses {
		fmt.Printf("  %s  %s\n", padRight(s.Path, width), s.State.label())
		if s.isPending() {
			pending++
		}
	}

	fmt.Println()
	if pending == 0 {
		fmt.Println("All resources up to date.")
	} else {
		fmt.Printf("%d resource(s) differ from the shipped version — run 'csb config update' to review.\n", pending)
	}
	return nil
}

// RunConfigUpdate interactively selects managed resources to overwrite with the
// version shipped in the current binary, mirroring `csb clean`. Missing files are
// pre-checked; differing files start unchecked and are flagged, since updating
// them replaces a local copy (backed up to <file>.bak).
func RunConfigUpdate(cfg *Config, assets Assets) error {
	embedded, err := managedEmbeddedFiles(assets.AddonsFS)
	if err != nil {
		return fmt.Errorf("enumerating shipped resources: %w", err)
	}
	statuses := computeSyncStatus(cfg.ConfigDir, embedded)

	width := 0
	for _, s := range statuses {
		if (s.State == syncMissing || s.State == syncDiffers) && len(s.Path) > width {
			width = len(s.Path)
		}
	}

	var entries []selectorEntry
	for _, s := range statuses {
		switch s.State {
		case syncMissing:
			entries = append(entries, selectorEntry{
				label:    padRight(s.Path, width) + "  missing",
				selected: true,
				syncPath: s.Path,
			})
		case syncDiffers:
			entries = append(entries, selectorEntry{
				label:    padRight(s.Path, width) + "  differs  (overwrites local copy → .bak)",
				syncPath: s.Path,
			})
		}
	}

	if len(entries) == 0 {
		fmt.Println("All resources up to date.")
		return nil
	}

	selected, ok := runSelector(entries)
	if !ok {
		fmt.Println("\nAborted.")
		return nil
	}
	if len(selected) == 0 {
		fmt.Println("\nNothing selected.")
		return nil
	}

	fmt.Println()
	applySelectedEntries(cfg.ConfigDir, selected, embedded)
	return nil
}

// applySelectedEntries writes the embedded content for each selected entry to
// <ConfigDir>, printing a one-line result per file. Errors are printed to stderr
// and skipped so one bad file does not abort the rest.
func applySelectedEntries(configDir string, entries []selectorEntry, embedded map[string][]byte) {
	for _, e := range entries {
		dest := filepath.Join(configDir, e.syncPath)
		existed := false
		if _, err := os.Stat(dest); err == nil {
			existed = true
		}
		if err := applySyncEntry(configDir, e.syncPath, embedded[e.syncPath]); err != nil {
			fmt.Fprintf(os.Stderr, "csb: error updating %s: %v\n", e.syncPath, err)
			continue
		}
		if existed {
			fmt.Printf("Updated %s (backup: %s.bak)\n", e.syncPath, e.syncPath)
		} else {
			fmt.Printf("Created %s\n", e.syncPath)
		}
	}
}
