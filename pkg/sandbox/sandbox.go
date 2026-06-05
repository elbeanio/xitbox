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

	// Build env for the VM:
	// - PATH: prepend $HOME/.local/bin so uv-installed tools (aider) are found
	// - TERM/COLORTERM/TERM_PROGRAM: pass through from the host so TUIs can
	//   detect terminal capabilities. Many TUI libs (e.g. opencode's opentui)
	//   wait for capability-query responses and silently time out if the
	//   VM has no TERM set.
	// - HTTP_PROXY/HTTPS_PROXY: route traffic through the host guardian
	hostTerm := os.Getenv("TERM")
	if hostTerm == "" {
		hostTerm = "xterm-256color"
	}
	proxyEnv := fmt.Sprintf(
		"PATH=$HOME/.local/bin:$PATH TERM=%s COLORTERM=%s TERM_PROGRAM=%s HTTP_PROXY=http://host.lima.internal:%s HTTPS_PROXY=http://host.lima.internal:%s NO_PROXY=localhost,127.0.0.1",
		hostTerm,
		envOr("COLORTERM", "truecolor"),
		envOr("TERM_PROGRAM", "xitbox"),
		guardianPort, guardianPort,
	)

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

	// Reset the host terminal after the child exits. TUI apps that crash
	// (opencode, claude TUI) often leave the terminal in raw/alt-screen mode.
	defer resetTerminal()

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
					fmt.Fprintf(os.Stderr, "  limactl shell %s -- sudo apt-get install -y curl build-essential python3-dev pipx python3 git && pipx install uv && uv python install 3.12 && uv tool install --python 3.12 aider-chat\n", vmName)
				default:
					fmt.Fprintf(os.Stderr, "  limactl shell %s -- <install-command>\n", vmName)
				}
			}
		}
		return fmt.Errorf("sandboxed command: %w", err)
	}
	return nil
}

// resetTerminal puts the controlling terminal back into a sane state.
//
// TUI apps (opencode, claude TUI, vim, etc.) toggle a bunch of terminal
// modes on entry that they normally toggle off on exit. If they crash or
// get killed mid-run, those modes stay on, leaving the terminal in a
// weird state. `stty sane` only fixes tty settings (raw mode, echo) —
// it does NOT undo the terminal-emulator mode switches. We have to send
// the matching close sequences ourselves:
//
//	ESC[?1049l  leave alternate screen buffer
//	ESC[?25h    show cursor
//	ESC[?1000l  disable X11 mouse click tracking
//	ESC[?1002l  disable X11 mouse drag tracking
//	ESC[?1003l  disable X11 mouse-motion tracking (every move = garbage chars)
//	ESC[?1006l  disable SGR extended mouse mode
//	ESC[?1004l  disable focus in/out events
//	ESC[!p      DECSTR soft reset (catches anything we missed)
//
// Combined with `stty sane` this restores a healthy terminal after any
// kind of agent exit.
func resetTerminal() {
	const resetSeq = "\x1b[!p" +
		"\x1b[?1049l" +
		"\x1b[?25h" +
		"\x1b[?1000l" +
		"\x1b[?1002l" +
		"\x1b[?1003l" +
		"\x1b[?1006l" +
		"\x1b[?1004l"
	_, _ = os.Stderr.WriteString(resetSeq)
	stty := exec.Command("stty", "sane")
	stty.Stdin = os.Stdin
	_ = stty.Run()
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
		// Install system packages the agent needs (nodejs, python, etc.)
		if agentName != "" {
			if err := bootstrapAgentVM(vmName, agentName); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Could not bootstrap agent packages: %v\n", err)
				fmt.Fprintf(os.Stderr, "    You may need to install them manually:\n")
				fmt.Fprintf(os.Stderr, "      limactl shell %s -- sudo apk add <packages>\n", vmName)
			}
			// Install the agent itself (npm/uv). Skipped if already present.
			if err := installAgent(vmName, agentName); err != nil {
				fmt.Fprintf(os.Stderr, "⚠️  Could not install %s: %v\n", agentName, err)
				fmt.Fprintf(os.Stderr, "    The agent CLI is not available inside the VM.\n")
			}
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

	// Debian (glibc) base. Alpine is musl so no prebuilt wheels exist for
	// aarch64 Python packages with C extensions (tree-sitter for aider, etc.).
	// Debian is glibc and much lighter than Ubuntu (~400MB vs ~1GB base).
	args = append(args, "template:debian")

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

// bootstrapAgentVM installs the system packages a known agent needs.
// This is a one-time setup that runs after the VM is first created.
// npm-based agents (claude, opencode, codex) need nodejs + npm + git.
// aider uses uv (installed separately) which manages its own Python, but
// tree-sitter is a C extension so we need a build toolchain on Ubuntu.
func bootstrapAgentVM(vmName, agentName string) error {
	var pkgs string
	switch agentName {
	case "claude", "opencode", "codex", "cline":
		pkgs = "nodejs npm git"
	case "aider":
		// build-essential + python3-dev: tree-sitter and other C extensions
		// need a compiler and Python.h. pipx is the cleanest way to get uv
		// installed in the VM — PyPI works over the default TLS, but the
		// Debian image has a TLS 1.3 issue with astral.sh / GitHub that
		// breaks the official uv installer.
		pkgs = "python3 git curl build-essential python3-dev pipx"
	default:
		pkgs = "git curl"
	}
	fmt.Fprintf(os.Stderr, "📦  Installing %s system packages in %s...\n", agentName, vmName)
	cmd := fmt.Sprintf("sudo apt-get update -qq && sudo apt-get install -y --no-install-recommends %s", pkgs)
	return limaSilent("shell", vmName, "--", "sh", "-c", cmd)
}

// installAgent installs the agent's binary inside the VM. One-time per VM,
// runs after bootstrapAgentVM. Skipped if the agent is already installed.
//
// claude/opencode/codex install via npm; aider installs via uv (with uv
// itself bootstrapped from the official installer).
func installAgent(vmName, agentName string) error {
	// Skip if already installed — keeps re-runs cheap and survives VM restarts.
	probe := exec.Command("limactl", "shell", vmName, "--", "sh", "-c", "command -v "+agentName)
	probe.Stderr = &bytes.Buffer{}
	if out, err := probe.Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
		return nil
	}

	var installCmd string
	switch agentName {
	case "claude":
		installCmd = "sudo npm install -g @anthropic-ai/claude-code"
	case "opencode":
		installCmd = "sudo npm install -g opencode-ai"
	case "codex":
		installCmd = "sudo npm install -g @openai/codex"
	case "aider":
		// Get uv via pipx (which is in apt). Pin --python 3.12 because
		// aider-chat's pinned numpy 1.26.4 has cp312 wheels on aarch64
		// linux but no cp313 wheels. Install as the user — uv tool
		// binaries land in ~/.local/bin which the runDarwin shell wrapper
		// prepends to PATH.
		installCmd = `sh -c '
			set -e
			export TMPDIR=/tmp
			export PATH="$HOME/.local/bin:$PATH"
			if ! command -v uv >/dev/null 2>&1; then
				pipx install uv
			fi
			uv python install 3.12 >/dev/null
			uv tool install --python 3.12 aider-chat
		'`
	case "cline":
		// Cline is a VS Code extension, not a CLI — nothing to install in the VM.
		return nil
	default:
		return nil
	}

	fmt.Fprintf(os.Stderr, "📥  Installing %s in %s (this can take a minute)...\n", agentName, vmName)
	return limaSilent("shell", vmName, "--", "sh", "-c", installCmd)
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

// envOr returns the value of the host env var, or fallback if unset/empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
