package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Info describes the current platform and its capabilities.
type Info struct {
	OS         string
	Arch       string
	Deps       []Dependency
	Agents     []string // detected installed agents
	PersistDir string
	SandboxDir string
}

// Dependency is a required or optional external tool.
type Dependency struct {
	Name     string
	Required bool
	Found    bool
	Path     string
	Version  string
	Install  string // hint for installation
}

// Detect gathers platform information.
func Detect() (*Info, error) {
	info := &Info{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		PersistDir: persistDir(),
		SandboxDir: sandboxDir(),
	}

	// Detect dependencies
	info.Deps = detectDeps()

	// Detect installed agents
	info.Agents = detectAgents()

	return info, nil
}

// IsLinux returns true if running on Linux.
func (i *Info) IsLinux() bool {
	return i.OS == "linux"
}

// IsDarwin returns true if running on macOS.
func (i *Info) IsDarwin() bool {
	return i.OS == "darwin"
}

// AllRequiredFound returns true if all required dependencies are present.
func (i *Info) AllRequiredFound() bool {
	for _, d := range i.Deps {
		if d.Required && !d.Found {
			return false
		}
	}
	return true
}

func detectDeps() []Dependency {
	var deps []Dependency

	if runtime.GOOS == "linux" {
		deps = append(deps, check("bwrap", true, "sudo apt install bubblewrap  # or: sudo dnf install bubblewrap"))
		deps = append(deps, check("iptables", true, "usually pre-installed; sudo apt install iptables"))
		deps = append(deps, check("unshare", false, "usually part of util-linux"))
		deps = append(deps, check("socat", false, "sudo apt install socat"))
	} else if runtime.GOOS == "darwin" {
		// sandbox-exec is built into macOS — no external deps needed.
		deps = append(deps, check("sandbox-exec", true, "built into macOS; should always be present"))
	}

	return deps
}

func check(name string, required bool, install string) Dependency {
	path, err := exec.LookPath(name)
	found := err == nil && path != ""
	return Dependency{
		Name:     name,
		Required: required,
		Found:    found,
		Path:     path,
		Install:  install,
	}
}

func detectAgents() []string {
	var agents []string
	candidates := map[string]string{
		"claude": ".claude",
		"aider":  ".aider",
		"codex":  ".codex",
		"cline":  ".cline",
		"gemini": ".gemini",
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return agents
	}
	for name, dir := range candidates {
		if _, err := os.Stat(filepath.Join(home, dir)); err == nil {
			agents = append(agents, name)
		}
	}
	return agents
}

func persistDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".xb", "persist")
}

func sandboxDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".xb", "sandboxes")
}

// EnsureDirs creates the xb directories if they don't exist.
func EnsureDirs() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dirs := []string{
		filepath.Join(home, ".xb", "persist"),
		filepath.Join(home, ".xb", "sandboxes"),
		filepath.Join(home, ".xb", "logs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}
