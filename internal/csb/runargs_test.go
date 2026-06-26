package csb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runArgFacts(runtime string) map[string]string {
	return map[string]string{"runtime": runtime, "arch": "arm64"}
}

func TestParseAddonRunArgs_Basic(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "install.sh")
	require.NoError(t, os.WriteFile(f, []byte("#!/bin/bash\n# csb:run-arg --device /dev/fuse\n"), 0644))

	got, err := parseAddonRunArgs([]string{f}, runArgFacts("docker"))
	require.NoError(t, err)
	assert.Equal(t, []string{"--device", "/dev/fuse"}, got)
}

func TestParseAddonRunArgs_Dedup(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.sh")
	f2 := filepath.Join(dir, "b.sh")
	require.NoError(t, os.WriteFile(f1, []byte("# csb:run-arg --cap-add SYS_ADMIN\n"), 0644))
	require.NoError(t, os.WriteFile(f2, []byte("# csb:run-arg --cap-add SYS_ADMIN\n"), 0644))

	got, err := parseAddonRunArgs([]string{f1, f2}, runArgFacts("docker"))
	require.NoError(t, err)
	// "--cap-add SYS_ADMIN" is a single directive; tokens are --cap-add and SYS_ADMIN
	// but the dedup key is the raw val "--cap-add SYS_ADMIN", so tokens appear once
	assert.Equal(t, []string{"--cap-add", "SYS_ADMIN"}, got)
}

func TestParseAddonRunArgs_MultipleDirectives(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "install.sh")
	content := "# csb:run-arg --device /dev/dri\n# csb:run-arg --group-add video\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0644))

	got, err := parseAddonRunArgs([]string{f}, runArgFacts("docker"))
	require.NoError(t, err)
	assert.Equal(t, []string{"--device", "/dev/dri", "--group-add", "video"}, got)
}

func TestParseAddonRunArgs_NoDirectives(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "install.sh")
	require.NoError(t, os.WriteFile(f, []byte("#!/bin/bash\napt-get install -y foo\n"), 0644))

	got, err := parseAddonRunArgs([]string{f}, runArgFacts("docker"))
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestParseAddonRunArgs_MissingFile(t *testing.T) {
	_, err := parseAddonRunArgs([]string{"/no/such/file.sh"}, runArgFacts("docker"))
	assert.Error(t, err)
}

// Scanning stops at the first line of actual script, so a "# csb:run-arg" that
// appears below the header (e.g. in a heredoc or echoed string) is ignored.
func TestParseAddonRunArgs_StopsAtFirstCodeLine(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "install.sh")
	content := "#!/bin/bash\n" +
		"# csb:run-arg --device /dev/fuse\n" + // header directive: counts
		"\n" +
		"set -euo pipefail\n" + // first code line: scanning stops here
		"# csb:run-arg --privileged\n" + // below code: ignored
		"echo '# csb:run-arg --cap-add SYS_ADMIN'\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0644))

	got, err := parseAddonRunArgs([]string{f}, runArgFacts("docker"))
	require.NoError(t, err)
	assert.Equal(t, []string{"--device", "/dev/fuse"}, got)
}

// Conditional directives ("# csb:run-arg[cond] ...") apply only when the
// bracketed condition holds for the build facts; the unbracketed form applies
// always. This is how the systemd addon ships --pid=private to podman without
// breaking docker, which rejects that token.
func TestParseAddonRunArgs_Conditional(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "install.sh")
	content := "# csb:run-arg --tmpfs /run\n" +
		"# csb:run-arg[runtime=podman] --pid=private\n" +
		"# csb:run-arg[runtime=docker] --pid=host\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0644))

	podman, err := parseAddonRunArgs([]string{f}, runArgFacts("podman"))
	require.NoError(t, err)
	assert.Equal(t, []string{"--tmpfs", "/run", "--pid=private"}, podman)

	docker, err := parseAddonRunArgs([]string{f}, runArgFacts("docker"))
	require.NoError(t, err)
	assert.Equal(t, []string{"--tmpfs", "/run", "--pid=host"}, docker)
}

// runArgDirective evaluates the condition mini-syntax: =, !=, value-OR (|),
// comma-AND, against the fact set.
func TestRunArgDirective_Conditions(t *testing.T) {
	facts := map[string]string{"runtime": "podman", "arch": "arm64"}
	cases := []struct {
		line string
		want bool
	}{
		{"# csb:run-arg --x", true},                             // unconditional
		{"# csb:run-arg[runtime=podman] --x", true},             // eq match
		{"# csb:run-arg[runtime=docker] --x", false},            // eq no match
		{"# csb:run-arg[runtime!=docker] --x", true},            // ne match
		{"# csb:run-arg[runtime!=podman] --x", false},           // ne no match
		{"# csb:run-arg[runtime=docker|podman] --x", true},      // value-OR
		{"# csb:run-arg[runtime=docker|lxc] --x", false},        // value-OR no match
		{"# csb:run-arg[runtime=podman,arch=arm64] --x", true},  // AND, both hold
		{"# csb:run-arg[runtime=podman,arch=amd64] --x", false}, // AND, one fails
		{"# csb:run-arg[arch!=amd64,runtime=podman] --x", true}, // AND with ne
		{"# csb:run-arg[unknown=x] --x", false},                 // unknown key (eq)
		{"# csb:run-arg[unknown!=x] --x", true},                 // unknown key (ne)
		{"# csb:run-arg[runtime=podman]", false},                // no args after cond
		{"# csb:run-arg[runtime=podman --x", false},             // unterminated
		{"# csb:run-arg[bogus] --x", false},                     // malformed term
		{"# csb:run-args --x", false},                           // not the keyword
		{"  # csb:run-arg --x", false},                          // indented: not matched
	}
	for _, c := range cases {
		_, ok := runArgDirective(c.line, facts)
		assert.Equal(t, c.want, ok, "line %q", c.line)
	}
}
