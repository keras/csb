package csb

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/template"

	"github.com/ulikunitz/xz"
)

// Base packages for the container image.
var basePackages = []string{
	"bash-completion",
	"build-essential",
	"curl",
	"git",
	"gosu",
	"gpg",
	"libssl-dev",
	"libx11-6",
	"libx11-xcb1",
	"libxcursor1",
	"libxext6",
	"libxi6",
	"libxkbcommon-x11-0",
	"libxrender1",
	"libxtst6",
	"libnss-wrapper",
	"nano",
	"pkg-config",
	"sudo",
	"tmux",
	"zsh",
}

var podmanPackages = []string{
	"fuse-overlayfs",
	"podman",
	"uidmap",
}

// Static container config files for nested Podman.
const containersPolicy = `{"default":[{"type":"insecureAcceptAnything"}]}`

const containersRegistries = `
[registries.search]
registries = ["docker.io"]
`

const containersStorage = `
[storage]
driver = "overlay"
[storage.options]
mount_program = "/usr/bin/fuse-overlayfs"
`

const containersContainers = `
[containers]
# Docker bind-mounts /proc/sys read-only so crun cannot set sysctls.
default_sysctls = []
# Sharing the outer PID namespace avoids crun needing to mount a new proc
# inside a nested user+mount namespace, which Docker prevents.
pidns = "host"
# slirp4netns sets accept_dad before the inner mount namespace is active,
# hitting the outer read-only /proc/sys; disabling IPv6 skips that sysctl.
network_cmd_options = ["enable_ipv6=false"]
`

// aptPackages returns the sorted list of apt packages to install.
func aptPackages(nestedPodman bool) []string {
	pkgs := make([]string, len(basePackages))
	copy(pkgs, basePackages)
	if nestedPodman {
		pkgs = append(pkgs, podmanPackages...)
	}
	sort.Strings(pkgs)
	return pkgs
}

var dockerfileTemplate = template.Must(template.New("dockerfile").Parse(`FROM {{.BaseImage}}

RUN apt-get update && apt-get install -y \
    {{.PkgLine}} \
    && rm -rf /var/lib/apt/lists/* \
    && echo "sandbox ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/sandbox \
    && chmod 0440 /etc/sudoers.d/sandbox
{{if .NestedPodman}}
COPY containers /etc/containers
{{end}}
# Shell setup
RUN printf '\n[ -f /usr/share/bash-completion/bash_completion ] && . /usr/share/bash-completion/bash_completion\n' \
    >> /etc/bash.bashrc \
    {{.PodmanAlias}}

RUN mkdir -p /etc/csb/entrypoint.d

COPY csb/build.d /tmp/build.d
RUN for script in /tmp/build.d/*.sh; do \
        [ -x "$script" ] && "$script"; \
    done && rm -rf /tmp/build.d

ENV LANG=C.UTF-8 LC_ALL=C.UTF-8 EDITOR=nano CSB_HOME=/home/sandbox

RUN mkdir -p $CSB_HOME {{.Workdir}} /mnt/csb-home && chmod 777 {{.Workdir}} /mnt/csb-home

COPY csb/csb-persist /usr/local/bin/csb-persist
RUN chmod +x /usr/local/bin/csb-persist
{{- if .HostRunHash}}

# csb-host-run sha256:{{.HostRunHash}}
COPY csb/csb-host-run /usr/local/bin/csb-host-run
RUN chmod +x /usr/local/bin/csb-host-run
{{- end}}

# Entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
CMD ["bash"]
`))

// MakeDockerfile generates the Dockerfile for the given configuration.
func MakeDockerfile(baseImage string, nestedPodman bool, hostRunHash string) string {
	podmanAlias := "&& true"
	if nestedPodman {
		podmanAlias = `&& printf '\nalias docker=podman\n' >> /etc/bash.bashrc`
	}
	data := struct {
		BaseImage    string
		PkgLine      string
		NestedPodman bool
		PodmanAlias  string
		Workdir      string
		HostRunHash  string
	}{
		BaseImage:    baseImage,
		PkgLine:      strings.Join(aptPackages(nestedPodman), " "),
		NestedPodman: nestedPodman,
		PodmanAlias:  podmanAlias,
		Workdir:      ContainerWorkdir,
		HostRunHash:  hostRunHash,
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
		name := e.Name()
		if !strings.HasSuffix(name, ".sh") {
			continue
		}
		stem := strings.TrimSuffix(name, ".sh")
		if addonSet[stem] {
			scripts = append(scripts, filepath.Join(addonsDir, name))
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
	dockerfile := MakeDockerfile(cfg.BaseImage, cfg.NestedPodman, hrh)

	var addonContent strings.Builder
	for _, p := range addonScripts(cfg) {
		data, err := os.ReadFile(p)
		if err == nil {
			addonContent.Write(data)
		}
	}

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
	dockerfile := MakeDockerfile(cfg.BaseImage, cfg.NestedPodman, hrh)

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
		{"containers/policy.json", 0644, []byte(containersPolicy)},
		{"containers/registries.conf", 0644, []byte(containersRegistries)},
		{"containers/storage.conf", 0644, []byte(containersStorage)},
		{"containers/containers.conf", 0644, []byte(containersContainers)},
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

	// Addon scripts
	for _, p := range addonScripts(cfg) {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("reading addon %s: %w", p, err)
		}
		name := "csb/build.d/" + filepath.Base(p)
		if err := addFile(name, data, 0755); err != nil {
			return nil, err
		}
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
	env = append(env, [2]string{"SHELL", "/bin/bash"})

	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}
	env = append(env, [2]string{"TERM", term})

	colorterm := os.Getenv("COLORTERM")
	env = append(env, [2]string{"COLORTERM", colorterm})

	// DISPLAY
	hostDisplay := os.Getenv("DISPLAY")
	var display string
	// darwin or empty/socket display → use docker/container internal hostname
	isDarwin := false
	// runtime.GOOS check
	if goos := runtimeGOOS(); goos == "darwin" {
		isDarwin = true
	}
	if isDarwin || hostDisplay == "" || strings.HasPrefix(hostDisplay, "/") {
		gateway := "host.docker.internal"
		if cfg.ContainerCLI() == "podman" {
			gateway = "host.containers.internal"
		}
		display = gateway + ":0"
	} else {
		display = hostDisplay
	}
	env = append(env, [2]string{"DISPLAY", display})

	if cfg.NestedPodman {
		env = append(env, [2]string{"CSB_NESTED_PODMAN", "1"})
	}
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

// runtimeGOOS returns the current OS (wrapper for testing).
var runtimeGOOS = func() string {
	return runtime.GOOS
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
	args := cfg.PassthroughArgs
	var inner []string
	if len(args) == 0 {
		inner = []string{"bash"}
	} else {
		inner = args
	}

	if cfg.UseTmux {
		postCommand := "exec bash"
		if len(inner) > 0 && (inner[0] == "bash" || inner[0] == "zsh") {
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
func shQuote(s string) string {
	if !strings.ContainsAny(s, " \t\n\"'\\|&;<>()$`!{}") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

	// Nested Podman flags
	if cfg.NestedPodman {
		cmd = append(cmd,
			"--device", "/dev/fuse",
			"--device", "/dev/net/tun",
			"--security-opt", "seccomp=unconfined",
			"--security-opt", "apparmor=unconfined",
			"--cap-add", "SYS_ADMIN",
			"--cap-add", "NET_ADMIN",
		)
	}

	if cfg.HostNetwork {
		cmd = append(cmd, "--network", "host")
	}

	for _, spec := range cfg.Publish {
		cmd = append(cmd, "-p", spec)
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
