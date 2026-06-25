package csb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Runtime wraps the docker/podman CLI.
type Runtime struct {
	CLI string
}

// NewRuntime creates a Runtime with the given CLI.
func NewRuntime(cli string) *Runtime {
	return &Runtime{CLI: cli}
}

// HostIDs returns the uid/gid the container's user should be created with so
// that files it writes to bind mounts end up owned by the invoking host user.
//
// Docker and rootful podman run the container's user as the caller's real uid,
// so that is what we pass. Rootless podman is the exception: it remaps the
// container's uid 0 back to the invoking user via /etc/subuid, so the container
// must run as 0/0 to land back on the host user. This encapsulates that
// runtime-specific quirk instead of leaking it into env construction.
func (r *Runtime) HostIDs() (uid, gid string) {
	uid = fmt.Sprintf("%d", os.Getuid())
	gid = fmt.Sprintf("%d", os.Getgid())
	if r.CLI == "podman" && r.podmanRootless() {
		uid, gid = "0", "0"
	}
	return uid, gid
}

// podmanRootless reports whether podman runs containers rootless, asking podman
// directly (Host.Security.Rootless) rather than guessing from the caller's uid —
// the rootful `podman` wrapper from the podman addon runs as a non-root user but
// is not rootless. Falls back to the non-root-invoker heuristic if podman can't
// be queried.
func (r *Runtime) podmanRootless() bool {
	out, err := exec.Command(r.CLI, "info", "--format", "{{.Host.Security.Rootless}}").Output()
	if err != nil {
		return os.Getuid() != 0
	}
	return strings.TrimSpace(string(out)) == "true"
}

// ImageExists returns true if the named image exists locally.
func (r *Runtime) ImageExists(name string) bool {
	cmd := exec.Command(r.CLI, "image", "inspect", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// BuildImage builds an image from the given tar context. platform is a
// docker-style "linux/<arch>" string passed to --platform.
func (r *Runtime) BuildImage(name string, context []byte, labels map[string]string, platform string, quiet bool) error {
	args := []string{"build", "-t", name, "--platform", platform}
	// Sort labels for determinism
	labelKeys := make([]string, 0, len(labels))
	for k := range labels {
		labelKeys = append(labelKeys, k)
	}
	sort.Strings(labelKeys)
	for _, k := range labelKeys {
		args = append(args, "--label", k+"="+labels[k])
	}
	args = append(args, "-")

	cmd := exec.Command(r.CLI, args...)
	cmd.Stdin = bytes.NewReader(context)

	if quiet {
		out, err := cmd.CombinedOutput()
		if err != nil {
			os.Stdout.Write(out)
			return fmt.Errorf("build failed: %w", err)
		}
		return nil
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ListCSBImageIDs returns IDs of all csb-managed images.
func (r *Runtime) ListCSBImageIDs() []string {
	cmd := exec.Command(r.CLI, "images",
		"--filter", "label=csb.managed=true",
		"--format", "{{.ID}}",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	// Deduplicate (like Python's dict.fromkeys)
	seen := map[string]bool{}
	var ids []string
	for _, line := range strings.Fields(string(out)) {
		if line != "" && !seen[line] {
			seen[line] = true
			ids = append(ids, line)
		}
	}
	return ids
}

// ImageInfo holds metadata about a csb-managed image.
type ImageInfo struct {
	ID         string
	Repository string
	Tag        string
	Size       string
	Age        string
	ConfigDir  string
}

// VolumeInfo holds metadata about a csb-managed volume.
type VolumeInfo struct {
	Name      string
	Age       string
	ConfigDir string
}

// ListCSBImagesInfo returns metadata for all csb-managed images.
func (r *Runtime) ListCSBImagesInfo() []ImageInfo {
	// docker images --format does not support .Label, so fetch ID/size/age
	// in one pass then retrieve labels via image inspect.
	cmd := exec.Command(r.CLI, "images",
		"--filter", "label=csb.managed=true",
		"--format", "{{.ID}}\t{{.Repository}}\t{{.Tag}}\t{{.Size}}\t{{.CreatedSince}}",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var infos []ImageInfo
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		id := parts[0]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		info := ImageInfo{ID: id}
		if len(parts) > 1 {
			info.Repository = parts[1]
		}
		if len(parts) > 2 {
			info.Tag = parts[2]
		}
		if len(parts) > 3 {
			info.Size = parts[3]
		}
		if len(parts) > 4 {
			info.Age = parts[4]
		}
		infos = append(infos, info)
	}
	if len(infos) == 0 {
		return nil
	}
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	configDirs := r.imageConfigDirs(ids)
	for i := range infos {
		infos[i].ConfigDir = configDirs[infos[i].ID]
	}
	return infos
}

// imageConfigDirs returns a short-ID → csb.config-dir map for the given image IDs.
func (r *Runtime) imageConfigDirs(ids []string) map[string]string {
	args := append([]string{"image", "inspect"}, ids...)
	cmd := exec.Command(r.CLI, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	type imageJSON struct {
		Id     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	var images []imageJSON
	if err := json.Unmarshal(out, &images); err != nil {
		return nil
	}
	result := make(map[string]string, len(images))
	for _, img := range images {
		// Id is "sha256:abc123..." — trim prefix and truncate to 12-char short form.
		id := strings.TrimPrefix(img.Id, "sha256:")
		if len(id) > 12 {
			id = id[:12]
		}
		result[id] = img.Config.Labels["csb.config-dir"]
	}
	return result
}

// ListCSBVolumesInfo returns metadata for all csb-managed volumes.
func (r *Runtime) ListCSBVolumesInfo() []VolumeInfo {
	names := r.ListCSBVolumes()
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"volume", "inspect"}, names...)
	cmd := exec.Command(r.CLI, args...)
	out, err := cmd.Output()
	if err != nil {
		var infos []VolumeInfo
		for _, n := range names {
			infos = append(infos, VolumeInfo{Name: n})
		}
		return infos
	}
	type volJSON struct {
		Name      string            `json:"Name"`
		CreatedAt string            `json:"CreatedAt"`
		Labels    map[string]string `json:"Labels"`
	}
	var vols []volJSON
	if err := json.Unmarshal(out, &vols); err != nil {
		var infos []VolumeInfo
		for _, n := range names {
			infos = append(infos, VolumeInfo{Name: n})
		}
		return infos
	}
	var infos []VolumeInfo
	for _, v := range vols {
		info := VolumeInfo{
			Name:      v.Name,
			ConfigDir: v.Labels["csb.config-dir"],
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999 -0700 MST"} {
			if t, err := time.Parse(layout, v.CreatedAt); err == nil {
				info.Age = humanAge(t)
				break
			}
		}
		infos = append(infos, info)
	}
	return infos
}

func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n := int(d.Minutes())
		if n == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", n)
	case d < 24*time.Hour:
		n := int(d.Hours())
		if n == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", n)
	case d < 7*24*time.Hour:
		n := int(d.Hours() / 24)
		if n == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", n)
	case d < 30*24*time.Hour:
		n := int(d.Hours() / 24 / 7)
		if n == 1 {
			return "1 week ago"
		}
		return fmt.Sprintf("%d weeks ago", n)
	case d < 365*24*time.Hour:
		n := int(d.Hours() / 24 / 30)
		if n == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", n)
	default:
		n := int(d.Hours() / 24 / 365)
		if n == 1 {
			return "1 year ago"
		}
		return fmt.Sprintf("%d years ago", n)
	}
}

// RemoveImages removes images by ID.
func (r *Runtime) RemoveImages(ids []string) {
	if len(ids) == 0 {
		return
	}
	args := append([]string{"rmi", "-f"}, ids...)
	cmd := exec.Command(r.CLI, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// EnsureVolume creates the named volume with labels if it doesn't exist.
// Returns (created, error): created is true when the volume was newly created.
func (r *Runtime) EnsureVolume(name string, labels map[string]string) (bool, error) {
	inspectCmd := exec.Command(r.CLI, "volume", "inspect", name)
	inspectCmd.Stdout = nil
	inspectCmd.Stderr = nil
	if inspectCmd.Run() == nil {
		return false, nil // already exists
	}

	args := []string{"volume", "create"}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, name)

	cmd := exec.Command(r.CLI, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return true, cmd.Run()
}

// ListCSBVolumes returns names of all csb-managed volumes.
func (r *Runtime) ListCSBVolumes() []string {
	cmd := exec.Command(r.CLI, "volume", "ls",
		"--filter", "label=csb.managed=true",
		"--format", "{{.Name}}",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

// RemoveVolume removes the named volume.
func (r *Runtime) RemoveVolume(name string) {
	cmd := exec.Command(r.CLI, "volume", "rm", "-f", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()
}

// ExecRun replaces the current process with the given command using syscall.Exec.
func (r *Runtime) ExecRun(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("exec %s: %w", argv[0], err)
	}
	return syscall.Exec(path, argv, os.Environ())
}

// containerGatewayIP returns the host IP on the container bridge network (Linux only),
// but only if that IP is actually bound to a local interface (so the broker can bind to it).
// Returns "" when the gateway is not a local address (e.g. rootless podman with slirp4netns).
func containerGatewayIP(containerCLI string) string {
	if runtime.GOOS == "darwin" {
		return "" // Docker Desktop handles host.docker.internal
	}
	var cmd *exec.Cmd
	if containerCLI == "podman" {
		cmd = exec.Command("podman", "network", "inspect", "podman",
			"--format", "{{range .Subnets}}{{.Gateway}}{{end}}")
	} else {
		cmd = exec.Command("docker", "network", "inspect", "bridge",
			"--format", "{{(index .IPAM.Config 0).Gateway}}")
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return ""
	}
	// Verify the IP is actually bindable on this host (not just a virtual gateway).
	ln, err := net.Listen("tcp", ip+":0")
	if err != nil {
		return ""
	}
	ln.Close()
	return ip
}

// StartHostExec starts the broker (embedded in this binary via CSB_HOST_BROKER_MODE)
// and returns (cmd, wsURL, token, error).
func StartHostExec(allowRules []string, bind string, containerCLI string) (*exec.Cmd, string, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, "", "", fmt.Errorf("locating csb executable: %w", err)
	}

	gatewayIP := containerGatewayIP(containerCLI)
	if gatewayIP != "" {
		logInfo("host exec gateway", "ip", gatewayIP)
	}
	actualBind := bind
	if gatewayIP != "" {
		parts := strings.Split(bind, ":")
		portPart := parts[len(parts)-1]
		actualBind = gatewayIP + ":" + portPart
	}

	args := []string{"--bind", actualBind}
	for _, rule := range allowRules {
		args = append(args, "--allow", rule)
	}

	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "CSB_HOST_BROKER_MODE=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", "", fmt.Errorf("creating stdout pipe: %w", err)
	}
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, "", "", fmt.Errorf("starting broker: %w", err)
	}
	logInfo("host exec broker starting", "pid", cmd.Process.Pid, "bind", actualBind)

	// Read the ready JSON line with timeout
	type readyMsg struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}

	resultCh := make(chan readyMsg, 1)
	errCh := make(chan error, 1)
	go func() {
		dec := json.NewDecoder(stdout)
		var msg readyMsg
		if err := dec.Decode(&msg); err != nil {
			errCh <- err
			return
		}
		resultCh <- msg
	}()

	select {
	case msg := <-resultCh:
		var urlHost string
		if gatewayIP != "" {
			urlHost = gatewayIP
		} else if containerCLI == "podman" {
			urlHost = "host.containers.internal"
		} else {
			urlHost = "host.docker.internal"
		}
		wsURL := fmt.Sprintf("ws://%s:%d/run", urlHost, msg.Port)
		logInfo("host exec broker ready", "url", wsURL)
		return cmd, wsURL, msg.Token, nil
	case err := <-errCh:
		_ = cmd.Process.Kill()
		return nil, "", "", fmt.Errorf("reading broker ready signal: %w", err)
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		return nil, "", "", fmt.Errorf("csb-host-broker did not print ready signal within 5s")
	}
}
