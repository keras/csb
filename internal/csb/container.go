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
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/ulikunitz/xz"
)

// parseAddonRunArgs scans enabled addon scripts for "# csb:run-arg" directives
// and returns deduplicated tokens to append to the container run command.
func parseAddonRunArgs(scripts []string) ([]string, error) {
	seen := make(map[string]bool)
	var result []string
	for _, path := range scripts {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading addon %s: %w", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			val, ok := strings.CutPrefix(line, "# csb:run-arg ")
			if !ok {
				continue
			}
			val = strings.TrimSpace(val)
			if val == "" || seen[val] {
				continue
			}
			seen[val] = true
			result = append(result, strings.Fields(val)...)
		}
	}
	return result, nil
}

// dockerfile is the default image recipe — seeded into <ConfigDir>/Dockerfile
// on first run and read from there for every build, so users can edit it.
// Per-build variation otherwise comes from the build context (addons,
// persist script, entrypoint, host-run binary).
const dockerfile = `FROM debian:stable-slim

RUN apt-get update && apt-get install -y \
    bash-completion gosu libnss-wrapper tmux \
    && rm -rf /var/lib/apt/lists/*

# Shell setup
RUN printf '\n[ -f /usr/share/bash-completion/bash_completion ] && . /usr/share/bash-completion/bash_completion\n' \
    >> /etc/bash.bashrc

RUN mkdir -p /etc/csb/entrypoint.d

COPY csb/build.d /tmp/build.d
RUN /tmp/build.d/run.sh

ENV LANG=C.UTF-8 LC_ALL=C.UTF-8 CSB_HOME=/home/sandbox

RUN mkdir -p $CSB_HOME /workspace && chmod 777 /workspace

COPY csb/csb-persist /usr/local/bin/csb-persist
RUN chmod +x /usr/local/bin/csb-persist

COPY csb/csb-host-run /usr/local/bin/csb-host-run

# Entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
CMD ["bash", "-l"]
`

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

// hostRunBytes extracts the csb-host-run binary for the host architecture from
// a tar.xz archive containing csb-host-run.amd64 and csb-host-run.arm64.
// Returns nil if tarXZ is empty (e.g. in tests that pass nil).
func hostRunBytes(tarXZ []byte) ([]byte, error) {
	if len(tarXZ) == 0 {
		return nil, nil
	}
	want := "csb-host-run." + runtime.GOARCH
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
	return nil, fmt.Errorf("csb-host-run.%s not found in archive", runtime.GOARCH)
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
// /tmp/build.d/run.sh. It drives addon installation in a single RUN layer
// so the Dockerfile itself stays free of inline shell loops.
func buildRunScript(instances []addonInstance) []byte {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\napt-get update\n")
	for _, a := range instances {
		fmt.Fprintf(&b, "/tmp/build.d/%s.sh", a.Name)
		if len(a.Args) > 0 {
			b.WriteString(" ")
			b.WriteString(shJoin(a.Args))
		}
		b.WriteString("\n")
	}
	b.WriteString("rm -rf /tmp/build.d /var/lib/apt/lists/*\n")
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

// ImageName returns the image name to use for the given config.
func ImageName(cfg *Config, entrypointContent, persistContent string, hostRunTarXZ []byte) string {
	if cfg.Image != "" {
		return cfg.Image
	}

	dockerfileBytes, _ := os.ReadFile(dockerfilePath(cfg))

	instances := addonInstances(cfg)
	hasher := sha256.New()
	hasher.Write(dockerfileBytes)
	hasher.Write([]byte(entrypointContent))
	hasher.Write([]byte(persistContent))
	for _, a := range instances {
		if data, err := os.ReadFile(a.Path); err == nil {
			hasher.Write(data)
		}
	}
	hasher.Write(buildRunScript(instances))
	if data, err := hostRunBytes(hostRunTarXZ); err == nil {
		hasher.Write(data)
	}
	return fmt.Sprintf("csb:%x", hasher.Sum(nil))[:4+12] // "csb:" + 12 hex chars
}

// BuildContextTar creates an in-memory tar archive for docker build.
func BuildContextTar(cfg *Config, entrypointContent, persistContent, hostRunTarXZ []byte) ([]byte, error) {
	hostRunData, err := hostRunBytes(hostRunTarXZ)
	if err != nil {
		return nil, fmt.Errorf("decompressing csb-host-run: %w", err)
	}
	dockerfileBytes, err := os.ReadFile(dockerfilePath(cfg))
	if err != nil {
		return nil, fmt.Errorf("reading Dockerfile: %w", err)
	}

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

	type contextFile struct {
		name string
		mode int64
		data []byte
	}
	for _, f := range []contextFile{
		{"Dockerfile", 0644, dockerfileBytes},
		{"entrypoint.sh", 0644, entrypointContent},
		{"csb/csb-persist", 0755, persistContent},
	} {
		if err := addFile(f.name, f.data, f.mode); err != nil {
			return nil, err
		}
	}

	// csb/build.d/ directory
	dirHdr := &tar.Header{
		Name:     "csb/build.d/",
		Typeflag: tar.TypeDir,
		Mode:     0755,
	}
	if err := tw.WriteHeader(dirHdr); err != nil {
		return nil, err
	}

	// Addon scripts + the run.sh that drives them.
	instances := addonInstances(cfg)
	for _, a := range instances {
		data, err := os.ReadFile(a.Path)
		if err != nil {
			return nil, fmt.Errorf("reading addon %s: %w", a.Path, err)
		}
		name := "csb/build.d/" + a.Name + ".sh"
		if err := addFile(name, data, 0755); err != nil {
			return nil, err
		}
	}
	if err := addFile("csb/build.d/run.sh", buildRunScript(instances), 0755); err != nil {
		return nil, err
	}

	// csb-host-run binary (embedded, already decompressed above). Always
	// written — possibly empty in tests — so the Dockerfile's unconditional
	// COPY succeeds without templating.
	if err := addFile("csb/csb-host-run", hostRunData, 0755); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
func ResolveEnv(cfg *Config, brokerURL, brokerToken string) [][2]string {
	var env [][2]string

	// HOST_UID, HOST_GID
	hostUID := fmt.Sprintf("%d", os.Getuid())
	hostGID := fmt.Sprintf("%d", os.Getgid())
	if cfg.ContainerCLI() == "podman" && os.Getuid() != 0 {
		hostUID = "0"
		hostGID = "0"
	}
	env = append(env, [2]string{"HOST_UID", hostUID})
	env = append(env, [2]string{"HOST_GID", hostGID})
	env = append(env, [2]string{"HOME", ContainerHome})
	env = append(env, [2]string{"CSB_DEFAULT_SHELL", cfg.DefaultShell})

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

	cmd := []string{cfg.ContainerCLI(), "run", "-i"}
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

	// Addon run args (from # csb:run-arg directives in enabled addon scripts)
	addonArgs, err := parseAddonRunArgs(addonPaths(addonInstances(cfg)))
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
