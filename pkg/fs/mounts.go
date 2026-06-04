package fs

import (
	"os"
	"path/filepath"

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
	mounts = append(mounts, MountSpec{
		Source: "/usr",
		Dest:   "/usr",
		Mode:   "ro",
	})
	mounts = append(mounts, MountSpec{
		Source: "/etc",
		Dest:   "/etc",
		Mode:   "ro",
	})

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

	// New session for non-interactive
	args = append(args, "--new-session")

	return args
}

func agentHomePath(agent string) string {
	switch agent {
	case "claude":
		return "/home/sandbox/.claude"
	case "opencode":
		return "/home/sandbox/.opencode"
	case "aider":
		return "/home/sandbox/.aider"
	case "codex":
		return "/home/sandbox/.codex"
	case "cline":
		return "/home/sandbox/.cline"
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
	return filepath.Join(home, ".xitbox", "sandboxes", name)
}
