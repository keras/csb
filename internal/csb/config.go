package csb

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ContainerHome = "/home/sandbox"
const ContainerWorkdir = "/workspace"

// Mount represents a single container bind mount.
type Mount struct {
	Src      string `yaml:"src"`
	Dst      string `yaml:"dst"`
	Readonly bool   `yaml:"readonly"`
}

// ToArgs returns the -v flag arguments for this mount.
func (m Mount) ToArgs() []string {
	spec := m.Src + ":" + m.Dst
	if m.Readonly {
		spec += ":ro"
	}
	return []string{"-v", spec}
}

// ParseMount parses a "src:dst[:mode]" mount string.
func ParseMount(entry string) (Mount, error) {
	parts := strings.Split(entry, ":")
	var srcRaw, dstRaw string
	readonly := true

	if len(parts) >= 3 && (parts[len(parts)-1] == "ro" || parts[len(parts)-1] == "rw") {
		mode := parts[len(parts)-1]
		readonly = mode == "ro"
		srcRaw = parts[0]
		dstRaw = strings.Join(parts[1:len(parts)-1], ":")
	} else if len(parts) >= 2 {
		srcRaw = parts[0]
		dstRaw = strings.Join(parts[1:], ":")
		readonly = true
	} else {
		return Mount{}, fmt.Errorf("mount entry must be 'src:dst[:mode]', got %q", entry)
	}

	src := strings.TrimSpace(srcRaw)
	dst := strings.TrimSpace(dstRaw)
	if src == "" || dst == "" {
		return Mount{}, fmt.Errorf("mount entry has empty src or dst: %q", entry)
	}

	// Expand ~ in src
	if strings.HasPrefix(src, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Mount{}, err
		}
		src = filepath.Join(home, src[2:])
	} else if src == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Mount{}, err
		}
		src = home
	}

	// Expand ~ in dst to container home
	if strings.HasPrefix(dst, "~/") {
		dst = ContainerHome + "/" + dst[2:]
	} else if dst == "~" {
		dst = ContainerHome
	}

	return Mount{Src: src, Dst: dst, Readonly: readonly}, nil
}

// Config holds all parsed CLI options and derived paths.
type Config struct {
	CWD       string
	Home      string
	ConfigDir string

	Workspace  *string // nil = no workspace
	Subcommand string  // "run", "clean", "config_edit"
	Rebuild bool
	Verbose bool

	ConfigAction     string // "edit" or "show"
	ConfigEditTarget string // "user" or "workdir" (for config edit)
	PassthroughArgs  []string

	Options
}

// ContainerCLI resolves the container runtime ("docker" or "podman").
func (c *Config) ContainerCLI() string {
	return resolveRuntime(c.Runtime)
}

// Workdir returns the container working directory.
func (c *Config) Workdir() string {
	if c.Workspace == nil {
		return ContainerWorkdir
	}
	ws := *c.Workspace
	// Try relative_to home
	home := c.Home
	if strings.HasPrefix(ws, home+"/") || ws == home {
		rel := strings.TrimPrefix(ws, home)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return ContainerWorkdir
		}
		return ContainerWorkdir + "/" + rel
	}
	// Outside home: use last 2 path components
	parts := strings.Split(filepath.Clean(ws), string(os.PathSeparator))
	if len(parts) >= 2 {
		return ContainerWorkdir + "/" + parts[len(parts)-2] + "/" + parts[len(parts)-1]
	} else if len(parts) == 1 {
		return ContainerWorkdir + "/" + parts[0]
	}
	return ContainerWorkdir
}

// CSBHome returns the config dir + "/home".
func (c *Config) CSBHome() string {
	return filepath.Join(c.ConfigDir, "home")
}

// WorkdirConfigPath returns the per-workdir config file path, or "" if no workspace.
func (c *Config) WorkdirConfigPath() string {
	if c.Workspace == nil {
		return ""
	}
	return workdirConfigPath(c.ConfigDir, *c.Workspace)
}

func workdirConfigPath(configDir, workspace string) string {
	h := sha256.Sum256([]byte(workspace))
	hex := fmt.Sprintf("%x", h)[:16]
	return filepath.Join(configDir, "projects", hex+".yaml")
}

func resolveRuntime(runtime string) string {
	if runtime != "auto" {
		return runtime
	}
	for _, candidate := range []string{"docker", "podman"} {
		if _, err := lookPath(candidate); err == nil {
			return candidate
		}
	}
	return "docker"
}

// lookPath is a wrapper around filepath lookup in PATH.
func lookPath(name string) (string, error) {
	return lookupInPath(name)
}

func lookupInPath(name string) (string, error) {
	path := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(path) {
		full := filepath.Join(dir, name)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			return full, nil
		}
	}
	return "", fmt.Errorf("%s: not found in PATH", name)
}

// BoolFromEnv converts an env var string to bool.
func BoolFromEnv(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "":
		return false
	default:
		return true
	}
}
