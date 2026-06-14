package fs

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/iangeorge/xitbox/pkg/config"
)

// MountSpec describes a single bind mount for bubblewrap.
type MountSpec struct {
	Source string
	Dest   string
	Mode   string // "ro" or "rw"
}

// PrepareMounts builds the list of mounts for a sandbox.
func PrepareMounts(cfg *config.Config, cwd string) []MountSpec {
	var mounts []MountSpec

	// Working directory
	mounts = append(mounts, MountSpec{
		Source: cwd,
		Dest:   "/workspace",
		Mode:   cfg.Filesystem.CWD,
	})

	// Re-mount .xb.yaml read-only even though the rest of cwd is rw.
	// The sandboxed process can read it (useful) but cannot modify it to
	// broaden allow rules for future sandbox runs.
	projectCfg := filepath.Join(cwd, ".xb.yaml")
	if _, err := os.Stat(projectCfg); err == nil {
		mounts = append(mounts, MountSpec{
			Source: projectCfg,
			Dest:   "/workspace/.xb.yaml",
			Mode:   "ro",
		})
	}

	// Agent persistence directories
	for agent, persistDir := range cfg.Filesystem.AgentPersistence {
		if _, err := os.Stat(persistDir); os.IsNotExist(err) {
			continue
		}
		dest := agentHomePath(agent)
		if dest != "" {
			mounts = append(mounts, MountSpec{
				Source: persistDir,
				Dest:   dest,
				Mode:   "rw",
			})
		}
	}

	// Additional shares
	for _, share := range cfg.Filesystem.Shares {
		mounts = append(mounts, MountSpec{
			Source: expandPath(share.Path),
			Dest:   filepath.Join("/mnt/shared", filepath.Base(share.Path)),
			Mode:   share.Mode,
		})
	}

	// Essential system directories
	// On macOS (Lima VM), always include /lib even if it doesn't exist on host
	// because Alpine Linux needs it for dynamically linked binaries
	hostOS := "unknown"
	if runtime.GOOS == "darwin" {
		hostOS = "darwin"
	}

	essential := []string{"/usr", "/bin", "/sbin", "/etc"}
	for _, path := range essential {
		if _, err := os.Stat(path); err == nil || hostOS == "darwin" {
			mounts = append(mounts, MountSpec{
				Source: path,
				Dest:   path,
				Mode:   "ro",
			})
		}
	}
	// /lib is always needed inside the VM (musl libc)
	mounts = append(mounts, MountSpec{
		Source: "/lib",
		Dest:   "/lib",
		Mode:   "ro",
	})
	// /lib64 only on some systems (not macOS, not Alpine)
	if _, err := os.Stat("/lib64"); err == nil {
		mounts = append(mounts, MountSpec{
			Source: "/lib64",
			Dest:   "/lib64",
			Mode:   "ro",
		})
	}

	return mounts
}

// BuildBwrapArgs converts mount specs to bubblewrap command-line arguments.
func BuildBwrapArgs(mounts []MountSpec, envWhitelist []string) []string {
	var args []string

	// Create minimal dev
	args = append(args, "--dev", "/dev")
	args = append(args, "--proc", "/proc")
	args = append(args, "--tmpfs", "/tmp")

	// Create sandbox home
	args = append(args, "--tmpfs", "/home/sandbox")

	// Bind mounts
	for _, m := range mounts {
		switch m.Mode {
		case "ro":
			args = append(args, "--ro-bind", m.Source, m.Dest)
		case "rw":
			args = append(args, "--bind", m.Source, m.Dest)
		default:
			args = append(args, "--bind", m.Source, m.Dest)
		}
	}

	// Set working directory inside sandbox
	args = append(args, "--chdir", "/workspace")

	// Environment filtering
	if len(envWhitelist) > 0 {
		args = append(args, "--clearenv")
		for _, name := range envWhitelist {
			if val := os.Getenv(name); val != "" {
				args = append(args, "--setenv", name, val)
			}
		}
	}

	// Die with parent
	args = append(args, "--die-with-parent")

	return args
}

func agentHomePath(agent string) string {
	switch agent {
	case "claude":
		return "/home/sandbox/.claude"
	case "aider":
		return "/home/sandbox/.aider"
	case "codex":
		return "/home/sandbox/.codex"
	case "cline":
		return "/home/sandbox/.cline"
	case "gemini":
		return "/home/sandbox/.gemini"
	case "opencode":
		return "/home/sandbox/.opencode"
	case "opencode-config":
		return "/home/sandbox/.config/opencode"
	}
	return ""
}

func expandPath(p string) string {
	if len(p) > 0 && p[0] == '~' {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// SandboxDir returns the per-sandbox working directory.
func SandboxDir(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".xb", "sandboxes", name)
}
