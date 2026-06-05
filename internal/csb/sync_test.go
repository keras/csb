package csb

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAddonsFS() fstest.MapFS {
	return fstest.MapFS{
		"files/addons/mise/install.sh": &fstest.MapFile{Data: []byte("echo mise\n")},
		"files/addons/mise/test.sh":    &fstest.MapFile{Data: []byte("true\n")},
	}
}

// ── managedEmbeddedFiles ─────────────────────────────────────────────────────

func TestManagedEmbeddedFiles(t *testing.T) {
	m, err := managedEmbeddedFiles(testAddonsFS())
	require.NoError(t, err)
	assert.Equal(t, []byte(dockerfile), m["Dockerfile"])
	assert.Equal(t, []byte("echo mise\n"), m["addons/mise/install.sh"])
	_, hasTest := m["addons/mise/test.sh"]
	assert.False(t, hasTest, "test.sh must not be in the managed set")
}

// ── computeSyncStatus ────────────────────────────────────────────────────────

func stateOf(statuses []fileStatus, path string) syncState {
	for _, s := range statuses {
		if s.Path == path {
			return s.State
		}
	}
	return -1
}

func TestComputeSyncStatus_States(t *testing.T) {
	dir := t.TempDir()
	embedded, err := managedEmbeddedFiles(testAddonsFS())
	require.NoError(t, err)

	// Dockerfile: missing (not written). addon: up to date. plus a local-only addon.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "addons/mise"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "addons/mise/install.sh"), embedded["addons/mise/install.sh"], 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "addons/custom"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "addons/custom/install.sh"), []byte("echo local\n"), 0644))

	statuses := computeSyncStatus(dir, embedded)
	assert.Equal(t, syncMissing, stateOf(statuses, "Dockerfile"))
	assert.Equal(t, syncUpToDate, stateOf(statuses, "addons/mise/install.sh"))
	assert.Equal(t, syncLocalOnly, stateOf(statuses, "addons/custom/install.sh"))

	// Now make the addon differ.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "addons/mise/install.sh"), []byte("echo edited\n"), 0644))
	statuses = computeSyncStatus(dir, embedded)
	assert.Equal(t, syncDiffers, stateOf(statuses, "addons/mise/install.sh"))
}

func TestPendingUpdateCount(t *testing.T) {
	dir := t.TempDir()
	embedded, err := managedEmbeddedFiles(testAddonsFS())
	require.NoError(t, err)
	// Nothing on disk: Dockerfile + 1 addon both missing → 2.
	assert.Equal(t, 2, pendingUpdateCount(dir, embedded))
}

func TestPendingUpdateCount_ExcludesLocalOnly(t *testing.T) {
	dir := t.TempDir()
	embedded, err := managedEmbeddedFiles(testAddonsFS())
	require.NoError(t, err)

	// Write a local-only addon that is not in the embedded set.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "addons/custom"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "addons/custom/install.sh"), []byte("echo custom\n"), 0644))

	// local-only must not inflate the pending count.
	assert.Equal(t, 2, pendingUpdateCount(dir, embedded))
}

// ── applySyncEntry ───────────────────────────────────────────────────────────

func TestApplySyncEntry_CreatesNew(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, applySyncEntry(dir, "addons/mise/install.sh", []byte("echo new\n")))

	got, err := os.ReadFile(filepath.Join(dir, "addons/mise/install.sh"))
	require.NoError(t, err)
	assert.Equal(t, "echo new\n", string(got))
	_, err = os.Stat(filepath.Join(dir, "addons/mise/install.sh.bak"))
	assert.True(t, os.IsNotExist(err), "no backup expected when creating new file")
}

func TestApplySyncEntry_BacksUpDiffering(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "Dockerfile")
	require.NoError(t, os.WriteFile(dest, []byte("OLD\n"), 0644))

	require.NoError(t, applySyncEntry(dir, "Dockerfile", []byte("NEW\n")))

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "NEW\n", string(got))
	bak, err := os.ReadFile(dest + ".bak")
	require.NoError(t, err)
	assert.Equal(t, "OLD\n", string(bak))
}

// ── applySelectedEntries ─────────────────────────────────────────────────────

func TestApplySelectedEntries_CreatesNew(t *testing.T) {
	dir := t.TempDir()
	embedded := map[string][]byte{"addons/mise/install.sh": []byte("echo mise\n")}
	entries := []selectorEntry{{syncPath: "addons/mise/install.sh"}}

	applySelectedEntries(dir, entries, embedded)

	got, err := os.ReadFile(filepath.Join(dir, "addons/mise/install.sh"))
	require.NoError(t, err)
	assert.Equal(t, "echo mise\n", string(got))
	_, err = os.Stat(filepath.Join(dir, "addons/mise/install.sh.bak"))
	assert.True(t, os.IsNotExist(err), "no backup expected for a newly created file")
}

func TestApplySelectedEntries_BacksUpExisting(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "Dockerfile")
	require.NoError(t, os.WriteFile(dest, []byte("OLD\n"), 0644))

	embedded := map[string][]byte{"Dockerfile": []byte("NEW\n")}
	entries := []selectorEntry{{syncPath: "Dockerfile"}}

	applySelectedEntries(dir, entries, embedded)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "NEW\n", string(got))
	bak, err := os.ReadFile(dest + ".bak")
	require.NoError(t, err)
	assert.Equal(t, "OLD\n", string(bak))
}

func TestApplySyncEntry_IdenticalNoBackup(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "Dockerfile")
	require.NoError(t, os.WriteFile(dest, []byte("SAME\n"), 0644))

	require.NoError(t, applySyncEntry(dir, "Dockerfile", []byte("SAME\n")))

	_, err := os.Stat(dest + ".bak")
	assert.True(t, os.IsNotExist(err), "identical content should not create a backup")
}
