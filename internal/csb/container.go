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
	"text/template"

	"github.com/ulikunitz/xz"
)

// Base packages for the container image.
var basePackages = []string{
	"bash-completion",
	"curl",
	"git",
	"gosu",
	"gpg",
	"libnss-wrapper",
	"nano",
	"pkg-config",
	"tmux",
	"zsh",
}

// aptPackages returns the sorted list of apt packages to install.
func aptPackages() []string {
	pkgs := make([]string, len(basePackages))
	copy(pkgs, basePackages)
	sort.Strings(pkgs)
	return pkgs
}

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

var dockerfileTemplate = template.Must(template.New("dockerfile").Parse(`FROM {{.BaseImage}}

RUN apt-get update && apt-get install -y \
    {{.PkgLine}} \
    && rm -rf /var/lib/apt/lists/*

# Shell setup
RUN printf '\n[ -f /usr/share/bash-completion/bash_completion ] && . /usr/share/bash-completion/bash_completion\n' \
    >> /etc/bash.bashrc

RUN mkdir -p /etc/csb/entrypoint.d

COPY csb/build.d /tmp/build.d
RUN /tmp/build.d/run.sh

ENV LANG=C.UTF-8 LC_ALL=C.UTF-8 EDITOR=nano CSB_HOME=/home/sandbox

RUN mkdir -p $CSB_HOME {{.Workdir}} /mnt/csb-home && chmod 777 {{.Workdir}} /mnt/csb-home

COPY csb/csb-persist /usr/local/bin/csb-persist
RUN chmod +x /usr/local/bin/csb-persist
{{- if .HostRunHash}}

# csb-host-run sha256:{{.HostRunHash}}
COPY csb/csb-host-run /usr/local/bin/csb-host-run
{{- end}}

# Entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
CMD ["bash", "-l"]
`))

// MakeDockerfile generates the Dockerfile for the given configuration.
func MakeDockerfile(baseImage string, hostRunHash string) string {
	data := struct {
		BaseImage   string
		PkgLine     string
		Workdir     string
		HostRunHash string
	}{
		BaseImage:   baseImage,
		PkgLine:     strings.Join(aptPackages(), " "),
		Workdir:     ContainerWorkdir,
		HostRunHash: hostRunHash,
	}
	var buf bytes.Buffer
	if err := dockerfileTemplate.Execute(&buf, data); err != nil {
		panic(fmt.Sprintf("dockerfile template: %v", err))
	}
	return buf.String()
}

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

// addonName extracts the addon name from an install-script path of the form
// ".../addons/<name>/install.sh".
func addonName(installPath string) string {
	return filepath.Base(filepath.Dir(installPath))
}

// buildRunScript returns the bash script copied into the image as
// /tmp/build.d/run.sh. It drives addon installation in a single RUN layer
// so the Dockerfile itself stays free of inline shell loops.
func buildRunScript(scripts []string) []byte {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -euo pipefail\napt-get update\n")
	for _, p := range scripts {
		fmt.Fprintf(&b, "/tmp/build.d/%s.sh\n", addonName(p))
	}
	b.WriteString("rm -rf /tmp/build.d /var/lib/apt/lists/*\n")
	return []byte(b.String())
}

// addonScripts returns sorted list of addon script paths that are enabled.
func addonScripts(cfg *Config) []string {
	addonsDir := filepath.Join(cfg.ConfigDir, "addons")
	addonSet := make(map[string]bool)
	for _, a := range cfg.Addons {
		addonSet[a] = true
	}

	var scripts []string
	entries, err := os.ReadDir(addonsDir)
	if err != nil {
		return scripts
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !addonSet[name] {
			continue
		}
		install := filepath.Join(addonsDir, name, "install.sh")
		if _, err := os.Stat(install); err == nil {
			scripts = append(scripts, install)
		}
	}
	sort.Strings(scripts)
	return scripts
}

// ImageName returns the image name to use for the given config.
func ImageName(cfg *Config, entrypointContent, persistContent string, hostRunTarXZ []byte) string {
	if cfg.Image != "" {
		return cfg.Image
	}

	var hrh string
	if data, err := hostRunBytes(hostRunTarXZ); err == nil && len(data) > 0 {
		sum := sha256.Sum256(data)
		hrh = fmt.Sprintf("%x", sum)
	}
	dockerfile := MakeDockerfile(cfg.BaseImage, hrh)

	scripts := addonScripts(cfg)
	var addonContent strings.Builder
	for _, p := range scripts {
		data, err := os.ReadFile(p)
		if err == nil {
			addonContent.Write(data)
		}
	}
	addonContent.Write(buildRunScript(scripts))

	h := sha256.Sum256([]byte(dockerfile + entrypointContent + persistContent + addonContent.String()))
	return fmt.Sprintf("csb:%x", h)[:4+12] // "csb:" + 12 hex chars
}

// BuildContextTar creates an in-memory tar archive for docker build.
func BuildContextTar(cfg *Config, entrypointContent, persistContent, hostRunTarXZ []byte) ([]byte, error) {
	hostRunData, err := hostRunBytes(hostRunTarXZ)
	if err != nil {
		return nil, fmt.Errorf("decompressing csb-host-run: %w", err)
	}
	var hrh string
	if len(hostRunData) > 0 {
		sum := sha256.Sum256(hostRunData)
		hrh = fmt.Sprintf("%x", sum)
	}
	dockerfile := MakeDockerfile(cfg.BaseImage, hrh)

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
		{"Dockerfile", 0644, []byte(dockerfile)},
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
	scripts := addonScripts(cfg)
	for _, p := range scripts {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("reading addon %s: %w", p, err)
		}
		name := "csb/build.d/" + addonName(p) + ".sh"
		if err := addFile(name, data, 0755); err != nil {
			return nil, err
		}
	}
	if err := addFile("csb/build.d/run.sh", buildRunScript(scripts), 0755); err != nil {
		return nil, err
	}

	// csb-host-run binary (embedded, already decompressed above)
	if len(hostRunData) > 0 {
		if err := addFile("csb/csb-host-run", hostRunData, 0755); err != nil {
			return nil, err
		}
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
	addonArgs, err := parseAddonRunArgs(addonScripts(cfg))
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
