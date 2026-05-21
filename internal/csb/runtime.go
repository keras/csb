package csb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// ImageExists returns true if the named image exists locally.
func (r *Runtime) ImageExists(name string) bool {
	cmd := exec.Command(r.CLI, "image", "inspect", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// BuildImage builds an image from the given tar context.
func (r *Runtime) BuildImage(name string, context []byte, labels map[string]string, quiet bool) error {
	fmt.Printf("Building %s...\n", name)
	args := []string{"build", "-t", name}
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
func (r *Runtime) EnsureVolume(name string, labels map[string]string) error {
	inspectCmd := exec.Command(r.CLI, "volume", "inspect", name)
	inspectCmd.Stdout = nil
	inspectCmd.Stderr = nil
	if inspectCmd.Run() == nil {
		return nil // already exists
	}

	args := []string{"volume", "create"}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, name)

	cmd := exec.Command(r.CLI, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
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

// findBrokerBin locates the csb-host-broker binary.
func findBrokerBin() string {
	// 1. In PATH
	if p, err := exec.LookPath("csb-host-broker"); err == nil {
		return p
	}

	// 2. Same directory as current executable (pkg layout)
	exe, err := os.Executable()
	if err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		binDir := filepath.Dir(exe)
		p := filepath.Join(binDir, "csb-host-broker")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		// dev layout: bin/ sibling of the exe directory
		p = filepath.Join(binDir, "..", "bin", "csb-host-broker")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}

// containerGatewayIP returns the host IP on the container bridge network (Linux only).
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
	return ip
}

// StartHostExec starts the csb-host-broker and returns (cmd, wsURL, token, error).
func StartHostExec(allowRules []string, bind string, containerCLI string) (*exec.Cmd, string, string, error) {
	brokerBin := findBrokerBin()
	if brokerBin == "" {
		return nil, "", "", fmt.Errorf(
			"error: --host-exec requires the csb-host-broker binary, which was not found.\n" +
				"Install Go and run:  make build\n" +
				"Then reinstall csb or keep the binaries on PATH.",
		)
	}

	gatewayIP := containerGatewayIP(containerCLI)
	actualBind := bind
	if gatewayIP != "" {
		// Bind only to the container network interface
		parts := strings.Split(bind, ":")
		portPart := parts[len(parts)-1]
		actualBind = gatewayIP + ":" + portPart
	}

	args := []string{"--bind", actualBind}
	for _, rule := range allowRules {
		args = append(args, "--allow", rule)
	}

	cmd := exec.Command(brokerBin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", "", fmt.Errorf("creating stdout pipe: %w", err)
	}
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, "", "", fmt.Errorf("starting broker: %w", err)
	}

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
		return cmd, wsURL, msg.Token, nil
	case err := <-errCh:
		_ = cmd.Process.Kill()
		return nil, "", "", fmt.Errorf("reading broker ready signal: %w", err)
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		return nil, "", "", fmt.Errorf("csb-host-broker did not print ready signal within 5s")
	}
}
