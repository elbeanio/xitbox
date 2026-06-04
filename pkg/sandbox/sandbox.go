package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/iangeorge/xitbox/pkg/config"
	"github.com/iangeorge/xitbox/pkg/fs"
	"github.com/iangeorge/xitbox/pkg/guardian"
	"github.com/iangeorge/xitbox/pkg/platform"
)

// Runtime manages the lifecycle of an ephemeral sandbox.
type Runtime struct {
	Name     string
	Config   *config.Config
	Platform *platform.Info
	Guardian *guardian.Server
	Cmd      *exec.Cmd
	StateDir string
}

// Start creates and runs a sandbox.
func Start(name string, cfg *config.Config, info *platform.Info, command []string) (*Runtime, error) {
	if name == "" {
		name = fmt.Sprintf("sandbox-%d", time.Now().Unix())
	}

	stateDir := fs.SandboxDir(name)
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	rt := &Runtime{
		Name:     name,
		Config:   cfg,
		Platform: info,
		StateDir: stateDir,
	}

	// Start guardian proxy
	guardianPort := strconv.Itoa(50000 + os.Getpid()%10000)
	controlSock := filepath.Join(stateDir, "guardian.sock")
	logPath := cfg.Network.LogFile

	rules := guardian.NewRules(cfg.Network.Allow, cfg.Network.DenyList)
	server, err := guardian.NewServer("127.0.0.1:"+guardianPort, controlSock, logPath, rules)
	if err != nil {
		return nil, fmt.Errorf("create guardian: %w", err)
	}
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("start guardian: %w", err)
	}
	rt.Guardian = server

	// Build bwrap args
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get cwd: %w", err)
	}
	mounts := fs.PrepareMounts(cfg, cwd)
	var envWhitelist []string
	if cfg.Env.Filter {
		envWhitelist = cfg.Env.Allow
	}
	bwrapArgs := fs.BuildBwrapArgs(mounts, envWhitelist)

	// Check bwrap is available
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap (bwrap) not found; install it or run 'xitbox init' to check dependencies")
	}

	// Build command
	args := append([]string{}, bwrapArgs...)
	args = append(args, command...)

	cmd := exec.Command(bwrapPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	rt.Cmd = cmd

	// Set up proxy environment for the sandboxed process
	cmd.Env = append(os.Environ(),
		"HTTP_PROXY=http://127.0.0.1:"+guardianPort,
		"HTTPS_PROXY=http://127.0.0.1:"+guardianPort,
	)

	// Write PID file for listing
	pidFile := filepath.Join(stateDir, "pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		return nil, fmt.Errorf("write pid file: %w", err)
	}

	// Run command (this blocks until command exits)
	runErr := cmd.Run()

	// Cleanup
	rt.Cleanup()

	if runErr != nil {
		return rt, fmt.Errorf("sandboxed command: %w", runErr)
	}

	return rt, nil
}

// Cleanup removes the sandbox state.
func (r *Runtime) Cleanup() {
	if r.Guardian != nil {
		r.Guardian.Stop()
	}
	if r.StateDir != "" {
		os.RemoveAll(r.StateDir)
	}
}

// ListRunning returns all currently running sandboxes.
func ListRunning() ([]SandboxInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sandboxDir := filepath.Join(home, ".xitbox", "sandboxes")
	entries, err := os.ReadDir(sandboxDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sandboxes []SandboxInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pidFile := filepath.Join(sandboxDir, entry.Name(), "pid")
		data, err := os.ReadFile(pidFile)
		if err != nil {
			continue
		}
		sandboxes = append(sandboxes, SandboxInfo{
			Name:    entry.Name(),
			PID:     string(data),
			Created: entry.Name(), // simplified
		})
	}
	return sandboxes, nil
}

// SandboxInfo describes a running sandbox.
type SandboxInfo struct {
	Name    string
	PID     string
	Status  string
	Created string
}
