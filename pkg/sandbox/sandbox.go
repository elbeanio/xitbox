package sandbox

import (
	"bytes"
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

// knownAgents maps common agent command names to their config directories.
var knownAgents = map[string]string{
	"opencode": ".opencode",
	"claude":   ".claude",
	"aider":    ".aider",
	"codex":    ".codex",
	"cline":    ".cline",
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
	// On macOS, each agent gets its own Lima VM for strong isolation.
	// The VM mounts ONLY the project dir and that agent's persist dir.
	vmName := vmNameForCommand(command)
	agentName := agentNameForCommand(command)

	// Ensure VM exists and is running
	if err := ensureLimaVM(vmName, agentName); err != nil {
		return err
	}

	// Build proxy env vars for the VM to route traffic through host guardian
	proxyEnv := fmt.Sprintf("HTTP_PROXY=http://host.lima.internal:%s HTTPS_PROXY=http://host.lima.internal:%s NO_PROXY=localhost,127.0.0.1",
		guardianPort, guardianPort)

	// Build the command: run inside VM with proxy env and cd to project dir
	cwd, _ := os.Getwd()
	vmArgs := []string{"shell", vmName, "--"}
	vmArgs = append(vmArgs, "sh", "-c", "cd "+shQuote(cwd)+" && "+proxyEnv+" exec "+strings.Join(command, " "))

	cmd := exec.Command("limactl", vmArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	rt.Cmd = cmd

	if err := cmd.Run(); err != nil {
		// Provide helpful message for common agents not installed in VM
		if len(command) > 0 {
			agent := command[0]
			probe := exec.Command("limactl", "shell", vmName, "--", "sh", "-c", "command -v "+agent)
			probe.Stderr = &bytes.Buffer{}
			if check, _ := probe.Output(); len(check) == 0 {
				fmt.Fprintf(os.Stderr, "\n⚠️  %s is not installed in the %s VM.\n", agent, vmName)
				fmt.Fprintf(os.Stderr, "Install it with:\n")
				switch agent {
				case "opencode":
					fmt.Fprintf(os.Stderr, "  limactl shell %s -- sudo npm install -g opencode-ai\n", vmName)
				case "claude":
					fmt.Fprintf(os.Stderr, "  limactl shell %s -- sudo npm install -g @anthropic-ai/claude-code\n", vmName)
				case "codex":
					fmt.Fprintf(os.Stderr, "  limactl shell %s -- sudo npm install -g @openai/codex\n", vmName)
				case "aider":
					fmt.Fprintf(os.Stderr, "  limactl shell %s -- sh -c \"apk add py3-pip && pip install aider-chat\"\n", vmName)
				default:
					fmt.Fprintf(os.Stderr, "  limactl shell %s -- <install-command>\n", vmName)
				}
			}
		}
		return fmt.Errorf("sandboxed command: %w", err)
	}
	return nil
}

// vmNameForCommand returns the Lima VM name for a given command.
// Known agents get their own VM; everything else shares "xitbox-default".
func vmNameForCommand(command []string) string {
	if len(command) == 0 {
		return "xitbox-default"
	}
	name := command[0]
	if _, ok := knownAgents[name]; ok {
		return "xitbox-" + name
	}
	return "xitbox-default"
}

// agentNameForCommand returns the agent name if the command is a known agent.
func agentNameForCommand(command []string) string {
	if len(command) == 0 {
		return ""
	}
	name := command[0]
	if _, ok := knownAgents[name]; ok {
		return name
	}
	return ""
}

// Lima VM management
func ensureLimaVM(vmName, agentName string) error {
	// Check if VM exists
	exists, err := limaVMExists(vmName)
	if err != nil {
		return fmt.Errorf("check vm: %w", err)
	}

	if !exists {
		fmt.Fprintf(os.Stderr, "🔄  Creating %s Lima VM (this takes ~30-60s)...\n", vmName)
		if err := createLimaVM(vmName, agentName); err != nil {
			return fmt.Errorf("create vm: %w", err)
		}
		// Create symlinks for agent persist dirs inside the VM
		if agentName != "" {
			if err := setupAgentSymlinks(vmName, agentName); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Could not set up agent symlinks: %v\n", err)
			}
		}
	}

	// Check if running
	running, err := limaVMRunning(vmName)
	if err != nil {
		return fmt.Errorf("check vm status: %w", err)
	}

	if !running {
		fmt.Fprintf(os.Stderr, "🔄  Starting %s Lima VM...\n", vmName)
		if err := startLimaVM(vmName); err != nil {
			return fmt.Errorf("start vm: %w", err)
		}
		// Wait for VM to be ready
		for i := 0; i < 30; i++ {
			time.Sleep(1 * time.Second)
			running, _ = limaVMRunning(vmName)
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

func limaVMExists(vmName string) (bool, error) {
	out, err := exec.Command("limactl", "list", "--json").Output()
	if err != nil {
		return false, err
	}
	return contains(string(out), fmt.Sprintf(`"name":"%s"`, vmName)), nil
}

func limaVMRunning(vmName string) (bool, error) {
	out, err := exec.Command("limactl", "list", vmName, "--json").Output()
	if err != nil {
		return false, nil
	}
	return contains(string(out), `"status":"Running"`), nil
}

func createLimaVM(vmName, agentName string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}

	// Build mount flags — only project dir + agent persist dir
	// Lima mounts host paths to the same path inside the VM by default.
	// We cd to the project path and create symlinks for agent configs.
	args := []string{
		"start",
		"--name=" + vmName,
		"--tty=false",
		"--cpus=2",
		"--memory=0.5",
		"--containerd=none",
	}

	// Use --mount-only for project dir (removes default home mount)
	mounts := []string{cwd + ":w"}

	// Add agent persist dir mount if this is a known agent
	if agentName != "" {
		home, _ := os.UserHomeDir()
		persistDir := filepath.Join(home, ".xitbox", "persist", agentName)
		if _, err := os.Stat(persistDir); err == nil {
			mounts = append(mounts, persistDir+":w")
		}
	}
	args = append(args, "--mount-only", strings.Join(mounts, ","))

	args = append(args, "template:alpine")

	return limaSilent(args...)
}

func startLimaVM(vmName string) error {
	return limaSilent("start", vmName)
}

func setupAgentSymlinks(vmName, agentName string) error {
	// Create symlink from ~/.<agent> to the persist dir inside the VM.
	// The persist dir is mounted at the same host path inside the VM.
	home, _ := os.UserHomeDir()
	persistDir := filepath.Join(home, ".xitbox", "persist", agentName)
	symlinkCmd := fmt.Sprintf("ln -sf %s ~/.%s", shQuote(persistDir), agentName)
	return limaSilent("shell", vmName, "--", "sh", "-c", symlinkCmd)
}

// limaSilent runs a limactl command while hiding its (very verbose) output.
// If the command fails, the captured output is shown so the user can debug.
func limaSilent(args ...string) error {
	cmd := exec.Command("limactl", args...)
	cmd.Env = os.Environ()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		out := strings.TrimSpace(buf.String())
		if out == "" {
			return fmt.Errorf("limactl %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("limactl %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

// shQuote quotes a string for safe use in a shell command.
func shQuote(s string) string {
	if strings.ContainsAny(s, " \t\n\"'\\$") {
		return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
	}
	return s
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
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
