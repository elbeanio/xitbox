package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

	// On macOS, guardian must listen on all interfaces so the Lima VM can reach it
	guardianHost := "127.0.0.1"
	if info.IsDarwin() {
		guardianHost = "0.0.0.0"
	}
	guardianAddr := guardianHost + ":" + guardianPort

	rules := guardian.NewRules(cfg.Network.Allow, cfg.Network.DenyList)
	server, err := guardian.NewServer(guardianAddr, controlSock, logPath, rules)
	if err != nil {
		return nil, fmt.Errorf("create guardian: %w", err)
	}
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("start guardian: %w", err)
	}
	rt.Guardian = server

	// On macOS, delegate to Lima VM backend
	if info.IsDarwin() {
		// Write PID file for listing
		pidFile := filepath.Join(stateDir, "pid")
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
			return nil, fmt.Errorf("write pid file: %w", err)
		}

		if err := runDarwin(rt, cfg, info, command, guardianPort); err != nil {
			rt.Cleanup()
			return rt, err
		}
		rt.Cleanup()
		return rt, nil
	}

	// Linux: run bwrap directly
	if err := runLinux(rt, cfg, info, command, guardianPort); err != nil {
		rt.Cleanup()
		return rt, err
	}
	rt.Cleanup()
	return rt, nil
}

func runLinux(rt *Runtime, cfg *config.Config, info *platform.Info, command []string, guardianPort string) error {
	// Build bwrap args
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
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
		return fmt.Errorf("bubblewrap (bwrap) not found; install it or run 'xitbox init' to check dependencies")
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
	pidFile := filepath.Join(rt.StateDir, "pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sandboxed command: %w", err)
	}
	return nil
}

func runDarwin(rt *Runtime, cfg *config.Config, info *platform.Info, command []string, guardianPort string) error {
	// Import darwin backend
	// We need to import it dynamically or use build tags
	// For now, we'll use exec.Command directly

	// Ensure VM exists and is running
	if err := ensureLimaVM(); err != nil {
		return err
	}

	// Build bwrap args (paths are same in VM due to virtiofs)
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}
	mounts := fs.PrepareMounts(cfg, cwd)
	// Don't use bwrap --clearenv on macOS - pass env via `env` command instead
	bwrapArgs := fs.BuildBwrapArgs(mounts, nil)

	// Build bwrap command as a single string for sh -c
	// (limactl shell with many individual args doesn't pass them correctly via SSH)
	bwrapCmd := "bwrap"
	for _, arg := range bwrapArgs {
		bwrapCmd += " " + shQuote(arg)
	}
	for _, arg := range command {
		bwrapCmd += " " + shQuote(arg)
	}

	// Build the limactl shell command (Lima v2 uses 'shell' not 'exec')
	// Use a minimal VM-safe PATH (host PATH may contain spaces e.g. "/Applications/Sublime Text.app/...")
	proxyEnv := fmt.Sprintf("HTTP_PROXY=http://host.lima.internal:%s HTTPS_PROXY=http://host.lima.internal:%s NO_PROXY=localhost,127.0.0.1 PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		guardianPort, guardianPort)

	vmArgs := []string{"shell", "xitbox", "--", "sh", "-c", proxyEnv + " " + bwrapCmd}

	cmd := exec.Command("limactl", vmArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	rt.Cmd = cmd

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sandboxed command: %w", err)
	}
	return nil
}

// Lima VM management
func ensureLimaVM() error {
	// Check if VM exists
	exists, err := limaVMExists()
	if err != nil {
		return fmt.Errorf("check vm: %w", err)
	}

	if !exists {
		fmt.Fprintln(os.Stderr, "🔄  Creating xitbox Lima VM (this takes ~30-60s)...")
		if err := createLimaVM(); err != nil {
			return fmt.Errorf("create vm: %w", err)
		}
	}

	// Check if running
	running, err := limaVMRunning()
	if err != nil {
		return fmt.Errorf("check vm status: %w", err)
	}

	if !running {
		fmt.Fprintln(os.Stderr, "🔄  Starting xitbox Lima VM...")
		if err := startLimaVM(); err != nil {
			return fmt.Errorf("start vm: %w", err)
		}
		// Wait for VM to be ready
		for i := 0; i < 30; i++ {
			time.Sleep(1 * time.Second)
			running, _ = limaVMRunning()
			if running {
				break
			}
		}
		if !running {
			return fmt.Errorf("vm failed to start")
		}
	}

	return nil
}

func limaVMExists() (bool, error) {
	out, err := exec.Command("limactl", "list", "--json").Output()
	if err != nil {
		return false, err
	}
	return contains(string(out), `"name":"xitbox"`), nil
}

func limaVMRunning() (bool, error) {
	out, err := exec.Command("limactl", "list", "xitbox", "--json").Output()
	if err != nil {
		return false, nil
	}
	return contains(string(out), `"status":"Running"`), nil
}

func createLimaVM() error {
	// Use Lima's built-in alpine template directly
	cmd := exec.Command("limactl", "start",
		"--name=xitbox",
		"--tty=false",
		"--cpus=2",
		"--memory=0.5",
		"--containerd=none",
		"template:alpine",
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("start vm: %w", err)
	}

	// Install bwrap inside the VM
	fmt.Fprintln(os.Stderr, "🔄  Installing bubblewrap in Lima VM...")
	installCmd := exec.Command("limactl", "shell", "xitbox", "--", "sh", "-c", "sudo apk add --no-cache bubblewrap iptables")
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	return installCmd.Run()
}

func startLimaVM() error {
	cmd := exec.Command("limactl", "start", "xitbox")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// shQuote quotes a string for safe use in a shell command.
func shQuote(s string) string {
	if strings.ContainsAny(s, " \t\n\"'\\$") {
		return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
	}
	return s
}

func findLimaTemplate() string {
	candidates := []string{
		filepath.Join(os.Getenv("HOME"), ".config", "xitbox", "init", "lima", "xitbox.yaml"),
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "..", "init", "lima", "xitbox.yaml"),
			filepath.Join(exeDir, "..", "..", "init", "lima", "xitbox.yaml"),
			filepath.Join(exeDir, "init", "lima", "xitbox.yaml"),
		)
	}
	candidates = append(candidates,
		"init/lima/xitbox.yaml",
		"../init/lima/xitbox.yaml",
	)
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
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
			Created: entry.Name(),
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
