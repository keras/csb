package csb

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ulikunitz/xz"
)

// The default image recipe lives in cmd/csb/files/Dockerfile, embedded into the
// binary and carried as Assets.Dockerfile. It is seeded into <ConfigDir>/Dockerfile
// on first run and read from there for every build, so users can edit it; per-build
// variation otherwise comes from the build context (addons, persist script,
// entrypoint, host-run binary).

// decompressXZ decompresses an xz-compressed byte slice.
func decompressXZ(compressed []byte) ([]byte, error) {
	r, err := xz.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// hostRunBytes extracts the csb-host-run binary for the requested architecture
// from a tar.xz archive containing csb-host-run.amd64 and csb-host-run.arm64.
// Returns nil if tarXZ is empty (e.g. in tests that pass nil).
func hostRunBytes(tarXZ []byte, arch string) ([]byte, error) {
	if len(tarXZ) == 0 {
		return nil, nil
	}
	want := "csb-host-run." + arch
	raw, err := decompressXZ(tarXZ)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name == want {
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(tr); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("csb-host-run.%s not found in archive", arch)
}

// addonInstance is an enabled addon resolved to its install script plus the
// arguments supplied in the user's config (`addons: ["name arg1 arg2"]`).
type addonInstance struct {
	Name string
	Path string // absolute path to install.sh
	Args []string
}

// parseAddonSpec splits an "addons" list entry into the addon name and any
// trailing arguments. Whitespace-only splitting — quoting is not supported.
func parseAddonSpec(spec string) (name string, args []string) {
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], fields[1:]
}

// buildRunScript returns the bash script copied into the image as
// /tmp/addon.d/run.sh. It drives addon installation in a single RUN layer
// so the Dockerfile itself stays free of inline shell loops. Each addon lives
// in its own /tmp/addon.d/<name>/ directory (install.sh plus any bundled
// resources); install.sh is executed directly (honouring its own shebang and
// the exec bit carried through from the source file) with that directory as its
// working dir, so it can reference sibling files by relative path. The
// run_addon wrapper turns any failure into a message naming the offending addon
// instead of an opaque "run.sh failed".
func buildRunScript(instances []addonInstance) []byte {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\napt-get update\n\n")
	b.WriteString("run_addon() {\n")
	b.WriteString("\tlocal name=$1 rc=0\n")
	b.WriteString("\tshift\n")
	b.WriteString("\t( cd \"/tmp/addon.d/$name\" && ./install.sh \"$@\" ) || rc=$?\n")
	b.WriteString("\tif [ \"$rc\" -ne 0 ]; then\n")
	b.WriteString("\t\techo \"csb: addon '$name' failed during install (exit code $rc)\" >&2\n")
	b.WriteString("\t\texit \"$rc\"\n")
	b.WriteString("\tfi\n")
	b.WriteString("}\n\n")
	for _, a := range instances {
		argv := append([]string{a.Name}, a.Args...)
		fmt.Fprintf(&b, "run_addon %s\n", shJoin(argv))
	}
	// Record the enabled addon names for csb-help's orientation box.
	b.WriteString(": > /etc/csb/addons\n")
	for _, a := range instances {
		fmt.Fprintf(&b, "echo %s >> /etc/csb/addons\n", shJoin([]string{a.Name}))
	}
	b.WriteString("rm -rf /tmp/addon.d /var/lib/apt/lists/*\n")
	return []byte(b.String())
}

// addonInstances returns the enabled addons (name + install-script path +
// args) in alphabetical order by name. Duplicate names and missing install
// scripts are silently skipped; validation happens earlier in RunRun.
func addonInstances(cfg *Config) []addonInstance {
	addonsDir := filepath.Join(cfg.ConfigDir, "addons")
	seen := make(map[string]bool)
	var out []addonInstance
	for _, spec := range cfg.Addons {
		name, args := parseAddonSpec(spec)
		if name == "" || seen[name] {
			continue
		}
		install := filepath.Join(addonsDir, name, "install.sh")
		if _, err := os.Stat(install); err != nil {
			continue
		}
		seen[name] = true
		out = append(out, addonInstance{Name: name, Path: install, Args: args})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// addonPaths returns just the install-script paths in addon order.
func addonPaths(instances []addonInstance) []string {
	paths := make([]string, len(instances))
	for i, a := range instances {
		paths[i] = a.Path
	}
	return paths
}

// dockerfilePath returns the path to the user-editable Dockerfile.
func dockerfilePath(cfg *Config) string {
	return filepath.Join(cfg.ConfigDir, "Dockerfile")
}

// contextFile is one entry to be written into the docker build context tar.
type contextFile struct {
	name string
	mode int64
	data []byte
}

// buildInputs is the fully-resolved set of files that define an image: what
// gets hashed into the image name AND what gets written to the build context.
type buildInputs struct {
	dockerfile []byte
	entrypoint []byte
	persist    []byte
	help       []byte
	addons     []resolvedAddon
	runScript  []byte
	hostRun    []byte
}

// resolvedAddon pairs an addon instance with the contents of its directory —
// install.sh plus any bundled resource files — keeping them together so hash()
// and tar() can't fall out of alignment.
type resolvedAddon struct {
	instance addonInstance
	files    []addonFile
}

// addonFile is one file bundled with an addon, named relative to the addon's
// directory (e.g. "install.sh" or "config/foo.conf").
type addonFile struct {
	rel  string
	mode int64
	data []byte
}

// readAddonFiles walks an addon's directory and returns every file except the
// test harness (test.sh), sorted by relative path for deterministic hashing and
// tarring. Executable files keep mode 0755; everything else gets 0644.
func readAddonFiles(dir string) ([]addonFile, error) {
	var files []addonFile
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "test.sh" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := int64(0644)
		if info.Mode()&0111 != 0 {
			mode = 0755
		}
		files = append(files, addonFile{rel: filepath.ToSlash(rel), mode: mode, data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, nil
}

// resolveBuildInputs reads the Dockerfile, addon scripts, and host-run binary
// for cfg, returning the resolved set of build inputs shared by hash() and tar().
func resolveBuildInputs(cfg *Config, assets Assets) (buildInputs, error) {
	dockerfileBytes, err := os.ReadFile(dockerfilePath(cfg))
	if err != nil {
		return buildInputs{}, fmt.Errorf("reading Dockerfile: %w", err)
	}

	instances := addonInstances(cfg)
	addons := make([]resolvedAddon, len(instances))
	for i, a := range instances {
		files, err := readAddonFiles(filepath.Dir(a.Path))
		if err != nil {
			return buildInputs{}, fmt.Errorf("reading addon %s: %w", a.Name, err)
		}
		addons[i] = resolvedAddon{instance: a, files: files}
	}

	hostRunData, err := hostRunBytes(assets.HostRun, cfg.Arch)
	if err != nil {
		return buildInputs{}, fmt.Errorf("decompressing csb-host-run: %w", err)
	}

	return buildInputs{
		dockerfile: dockerfileBytes,
		entrypoint: assets.Entrypoint,
		persist:    assets.Persist,
		help:       assets.Help,
		addons:     addons,
		runScript:  buildRunScript(instances),
		hostRun:    hostRunData,
	}, nil
}

// hash produces the "csb:<12hex>" image name from: dockerfile, entrypoint,
// persist, help, each addon's bundled files (relative path + bytes, in
// addonInstances order then sorted-rel order), the run script, then the
// host-run binary bytes. Including the relative path means renaming a bundled
// resource changes the image even if its contents don't.
func (b buildInputs) hash() string {
	hasher := sha256.New()
	hasher.Write(b.dockerfile)
	hasher.Write(b.entrypoint)
	hasher.Write(b.persist)
	hasher.Write(b.help)
	for _, a := range b.addons {
		for _, f := range a.files {
			hasher.Write([]byte(f.rel))
			hasher.Write(f.data)
		}
	}
	hasher.Write(b.runScript)
	hasher.Write(b.hostRun)
	return fmt.Sprintf("csb:%x", hasher.Sum(nil))[:4+12] // "csb:" + 12 hex chars
}

// tar creates an in-memory tar archive for docker build.
func (b buildInputs) tar() ([]byte, error) {
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)

	addFile := func(name string, data []byte, mode int64) error {
		hdr := &tar.Header{
			Name: name,
			Mode: mode,
			Size: int64(len(data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}

	for _, f := range []contextFile{
		{"Dockerfile", 0644, b.dockerfile},
		{"entrypoint.sh", 0644, b.entrypoint},
		{"csb/csb-persist", 0755, b.persist},
		{"csb/csb-help", 0755, b.help},
	} {
		if err := addFile(f.name, f.data, f.mode); err != nil {
			return nil, err
		}
	}

	addDir := func(name string) error {
		return tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0755})
	}

	// csb/addon.d/<name>/ directories, each carrying install.sh plus any
	// bundled resources, alongside the run.sh that drives them.
	if err := addDir("csb/addon.d/"); err != nil {
		return nil, err
	}
	for _, a := range b.addons {
		dir := "csb/addon.d/" + a.instance.Name + "/"
		if err := addDir(dir); err != nil {
			return nil, err
		}
		for _, f := range a.files {
			if err := addFile(dir+f.rel, f.data, f.mode); err != nil {
				return nil, err
			}
		}
	}
	if err := addFile("csb/addon.d/run.sh", b.runScript, 0755); err != nil {
		return nil, err
	}

	// csb-host-run binary (embedded, already decompressed above). Always
	// written — possibly empty in tests — so the Dockerfile's unconditional
	// COPY succeeds without templating.
	if err := addFile("csb/csb-host-run", b.hostRun, 0755); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ImageName returns the image name to use for the given config.
func ImageName(cfg *Config, assets Assets) (string, error) {
	inputs, err := resolveBuildInputs(cfg, assets)
	if err != nil {
		return "", err
	}
	return inputs.hash(), nil
}

// BuildContextTar creates an in-memory tar archive for docker build.
func BuildContextTar(cfg *Config, assets Assets) ([]byte, error) {
	inputs, err := resolveBuildInputs(cfg, assets)
	if err != nil {
		return nil, err
	}
	return inputs.tar()
}

// ResolveMounts builds the list of bind mounts for the container.
func ResolveMounts(cfg *Config) []Mount {
	var mounts []Mount

	if cfg.Workspace != nil {
		mounts = append(mounts, Mount{
			Src:      *cfg.Workspace,
			Dst:      cfg.Workdir(),
			Readonly: false,
		})
	}

	csbHome := cfg.CSBHome()
	if info, err := os.Stat(csbHome); err == nil && info.IsDir() {
		mounts = append(mounts, Mount{
			Src:      csbHome,
			Dst:      "/mnt/csb-home",
			Readonly: false,
		})
	}

	mounts = append(mounts, cfg.Mount...)
	return mounts
}

// ResolveEnv collects environment variables to pass into the container.
func ResolveEnv(cfg *Config, rt *Runtime, brokerURL, brokerToken string) [][2]string {
	var env [][2]string

	// HOST_UID, HOST_GID — the runtime decides whether the container user maps
	// to the caller's real uid or to 0 (rootless podman); see Runtime.HostIDs.
	hostUID, hostGID := rt.HostIDs()
	env = append(env, [2]string{"HOST_UID", hostUID})
	env = append(env, [2]string{"HOST_GID", hostGID})
	env = append(env, [2]string{"HOME", ContainerHome})
	env = append(env, [2]string{"CSB_DEFAULT_SHELL", cfg.DefaultShell})

	// CSB_WORKSPACE_DIR is the container path the host workspace is mounted at.
	// csb-host-run uses it to translate the current directory into the
	// workspace-relative form the broker accepts.
	if cfg.Workspace != nil {
		env = append(env, [2]string{"CSB_WORKSPACE_DIR", cfg.Workdir()})
	}

	// CSB_LOGIN_SHELL marks the bare interactive shape: no explicit command (the
	// else-branch of resolveContainerCmd, with or without tmux). The systemd
	// launcher reads this to route that session through login(1)/PAM so it becomes
	// a real logind session; an explicit command keeps the lightweight gosu drop.
	// With tmux, login runs a login shell rather than the tmux command, so tmux is
	// started from within the login session (see CSB_TMUX / csb-tmux.sh) — that
	// keeps the tmux server inside the logind session. Harmless when the systemd
	// addon is absent (nothing reads it).
	if len(cfg.PassthroughArgs) == 0 && len(cfg.DefaultCmd) == 0 {
		env = append(env, [2]string{"CSB_LOGIN_SHELL", "1"})
	}

	// CSB_TMUX tells the login session to auto-start tmux (csb-tmux.sh). Set
	// whenever tmux is requested; the snippet is guarded so it only fires for the
	// interactive login shell that is not already inside tmux — i.e. the systemd
	// login session, not the tmux windows of the default (non-systemd) path.
	if cfg.UseTmux {
		env = append(env, [2]string{"CSB_TMUX", "1"})
	}

	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}
	env = append(env, [2]string{"TERM", term})

	colorterm := os.Getenv("COLORTERM")
	env = append(env, [2]string{"COLORTERM", colorterm})

	if cfg.Verbose {
		env = append(env, [2]string{"CSB_VERBOSE", "1"})
	}
	if !cfg.Motd {
		env = append(env, [2]string{"CSB_MOTD", "0"})
	}

	// env_forward
	for _, name := range cfg.EnvForward {
		if val, ok := os.LookupEnv(name); ok {
			env = append(env, [2]string{name, val})
		}
	}

	// env_inject
	for _, pair := range cfg.EnvInject {
		idx := strings.Index(pair, "=")
		if idx >= 0 {
			env = append(env, [2]string{pair[:idx], pair[idx+1:]})
		}
	}

	// broker
	if brokerURL != "" && brokerToken != "" {
		env = append(env, [2]string{"CSB_HOST_EXEC_URL", brokerURL})
		env = append(env, [2]string{"CSB_HOST_EXEC_TOKEN", brokerToken})
	}

	return env
}

// ContainerLabels returns labels for the container.
func ContainerLabels(cfg *Config) map[string]string {
	return map[string]string{
		"csb.managed":     "true",
		"csb.home-volume": cfg.HomeVolume,
		"csb.config-dir":  cfg.ConfigDir,
	}
}

// VolumeLabels returns labels for the home volume.
func VolumeLabels(cfg *Config) map[string]string {
	return map[string]string{
		"csb.managed":    "true",
		"csb.config-dir": cfg.ConfigDir,
	}
}

// ImageLabels returns labels for the built image.
func ImageLabels(cfg *Config) map[string]string {
	return map[string]string{
		"csb.managed":    "true",
		"csb.config-dir": cfg.ConfigDir,
	}
}

// resolveContainerCmd determines the command the container should run.
func resolveContainerCmd(cfg *Config) []string {
	shell := cfg.DefaultShell
	if shell == "" {
		shell = "bash"
	}

	var inner []string
	if len(cfg.PassthroughArgs) > 0 {
		inner = cfg.PassthroughArgs
	} else if len(cfg.DefaultCmd) > 0 {
		inner = cfg.DefaultCmd
	} else {
		inner = []string{shell, "-l"}
	}

	if cfg.UseTmux {
		postCommand := "exec " + shell + " -l"
		if inner[0] == shell {
			postCommand = ""
		}
		quoted := shJoin(inner)
		return []string{"tmux", "new-session", "-s", "main", quoted + "; " + postCommand}
	}

	return inner
}

// shJoin quotes each arg for shell.
func shJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shQuote single-quotes a string for shell.
// Uses an allowlist rather than a denylist so that glob chars (*, ?, []),
// tilde, and the empty string are all quoted rather than passed through raw.
func shQuote(s string) string {
	if s != "" && shSafeRE.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var shSafeRE = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// resolveDynamicPublish rewrites bare-port publish specs (e.g. "6080" or
// "6080/tcp") into "127.0.0.1:<picked>:<containerPort>" with a host port
// allocated by binding to ":0" and immediately closing. For each rewritten
// spec, returns a CSB_PUBLISH_<containerPort>=<picked> env var so the
// container can discover the host-side port.
//
// Specs that already include an explicit host port (anything containing ':')
// pass through unchanged with no env var injected.
func resolveDynamicPublish(specs []string) ([]string, [][2]string, error) {
	out := make([]string, 0, len(specs))
	var env [][2]string
	for _, spec := range specs {
		portPart, proto := spec, ""
		if i := strings.IndexByte(spec, '/'); i >= 0 {
			portPart, proto = spec[:i], spec[i:]
		}
		if strings.ContainsRune(portPart, ':') {
			out = append(out, spec)
			continue
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, fmt.Errorf("allocating dynamic host port for --publish %q: %w", spec, err)
		}
		hostPort := l.Addr().(*net.TCPAddr).Port
		l.Close()
		out = append(out, fmt.Sprintf("127.0.0.1:%d:%s%s", hostPort, portPart, proto))
		env = append(env, [2]string{"CSB_PUBLISH_" + portPart, strconv.Itoa(hostPort)})
	}
	return out, env, nil
}

// BuildRunCommand assembles the full container run command.
func BuildRunCommand(cfg *Config, mounts []Mount, env [][2]string, imageName string) ([]string, error) {
	if cfg.HostNetwork && len(cfg.Publish) > 0 {
		return nil, fmt.Errorf("--publish is ignored when --host-network is set; use one or the other")
	}

	cmd := []string{cfg.ContainerCLI(), "run", "-i", "--platform", "linux/" + cfg.Arch}
	if cfg.UseTTY {
		cmd = append(cmd, "-t")
	}
	cmd = append(cmd, "--rm")

	// Container labels
	labels := ContainerLabels(cfg)
	// Sort for determinism
	labelKeys := []string{"csb.managed", "csb.home-volume", "csb.config-dir"}
	for _, k := range labelKeys {
		cmd = append(cmd, "--label", k+"="+labels[k])
	}

	// Addon run args (from # csb:run-arg directives in enabled addon scripts).
	// facts are what conditional directives ("# csb:run-arg[runtime=podman] ...")
	// match against.
	facts := map[string]string{"runtime": cfg.ContainerCLI(), "arch": cfg.Arch}
	addonArgs, err := parseAddonRunArgs(addonPaths(addonInstances(cfg)), facts)
	if err != nil {
		return nil, err
	}
	cmd = append(cmd, addonArgs...)

	if cfg.HostNetwork {
		cmd = append(cmd, "--network", "host")
	}

	publishSpecs, publishEnv, err := resolveDynamicPublish(cfg.Publish)
	if err != nil {
		return nil, err
	}
	for _, spec := range publishSpecs {
		cmd = append(cmd, "-p", spec)
	}
	for _, kv := range publishEnv {
		cmd = append(cmd, "-e", kv[0]+"="+kv[1])
	}

	// Named volume for home
	cmd = append(cmd, "-v", cfg.HomeVolume+":"+ContainerHome)

	// Bind mounts
	for _, m := range mounts {
		cmd = append(cmd, m.ToArgs()...)
	}

	cmd = append(cmd, "-w", cfg.Workdir())

	// Environment variables
	for _, kv := range env {
		cmd = append(cmd, "-e", kv[0]+"="+kv[1])
	}

	cmd = append(cmd, imageName)
	cmd = append(cmd, resolveContainerCmd(cfg)...)

	return cmd, nil
}
