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

// knownAgents maps command names to their config directories under $HOME.
//
// opencode is intentionally NOT listed — its TUI silently exits with code 255
// over SSH/VM transports (opencode issues #6119, #24475). Use `opencode web`.
var knownAgents = map[string]string{
	"claude": ".claude",
	"aider":  ".aider",
	"codex":  ".codex",
	"cline":  ".cline",
	"gemini": ".gemini",
}

// Start creates and runs an ephemeral sandbox, blocking until the command exits.
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

	guardianPort := strconv.Itoa(50000 + os.Getpid()%10000)
	controlSock := filepath.Join(stateDir, "guardian.sock")

	rules := guardian.NewRules(cfg.Network.Allow, cfg.Network.DenyList)
	server, err := guardian.NewServer("127.0.0.1:"+guardianPort, controlSock, cfg.Network.LogFile, cfg.Network.UpstreamProxy, rules)
	if err != nil {
		return nil, fmt.Errorf("create guardian: %w", err)
	}
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("start guardian: %w", err)
	}
	rt.Guardian = server

	pidFile := filepath.Join(stateDir, "pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		return nil, fmt.Errorf("write pid file: %w", err)
	}

	// Watch config files for changes and hot-reload guardian rules.
	cwd, _ := os.Getwd()
	watchStop := make(chan struct{})
	go watchConfig(server, cwd, watchStop)

	var runErr error
	if info.IsDarwin() {
		runErr = runDarwin(rt, cfg, command, guardianPort)
	} else {
		runErr = runLinux(rt, cfg, command, guardianPort)
	}

	close(watchStop)
	rt.Cleanup()
	return rt, runErr
}

// watchConfig polls the default config and project config for mtime changes.
// When either file changes it reloads and hot-swaps guardian's rules.
func watchConfig(server *guardian.Server, cwd string, stop <-chan struct{}) {
	paths := []string{config.DefaultConfigPath()}
	if cwd != "" {
		paths = append(paths, filepath.Join(cwd, ".xitbox.yaml"))
	}

	mtimes := make(map[string]time.Time)
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			mtimes[p] = info.ModTime()
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			changed := false
			for _, p := range paths {
				info, err := os.Stat(p)
				if err != nil {
					continue
				}
				if !info.ModTime().Equal(mtimes[p]) {
					mtimes[p] = info.ModTime()
					changed = true
				}
			}
			if !changed {
				continue
			}
			newCfg, err := config.Load(cwd, nil)
			if err == nil {
				server.ReplaceRules(newCfg.Network.Allow, newCfg.Network.DenyList)
			}
		}
	}
}

// runLinux is implemented in sandbox_linux.go (Linux) and sandbox_notlinux.go (others).

// runDarwin runs the command on macOS using sandbox-exec (Seatbelt) for
// filesystem isolation and the guardian proxy for network filtering.
func runDarwin(rt *Runtime, cfg *config.Config, command []string, guardianPort string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	// Determine which agent config dir (if any) needs write access.
	agentConfigDir := ""
	if len(command) > 0 {
		if dotDir, ok := knownAgents[command[0]]; ok {
			home, _ := os.UserHomeDir()
			agentConfigDir = filepath.Join(home, dotDir)
		}
	}

	// Write a Seatbelt profile to a temp file.
	profile, err := buildSeatbeltProfile(cwd, agentConfigDir, guardianPort)
	if err != nil {
		return fmt.Errorf("build seatbelt profile: %w", err)
	}
	profileFile, err := os.CreateTemp("", "xb-sandbox-*.sb")
	if err != nil {
		return fmt.Errorf("create seatbelt profile: %w", err)
	}
	defer os.Remove(profileFile.Name())
	if _, err := profileFile.WriteString(profile); err != nil {
		return fmt.Errorf("write seatbelt profile: %w", err)
	}
	profileFile.Close()

	// sandbox-exec -f <profile> <command...>
	args := append([]string{"-f", profileFile.Name()}, command...)
	cmd := exec.Command("sandbox-exec", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = sandboxEnv(cfg, guardianPort)
	rt.Cmd = cmd

	savedTTY := saveTTY()
	defer restoreTTY(savedTTY)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sandboxed command: %w", err)
	}
	return nil
}

// buildSeatbeltProfile returns a Seatbelt sandbox profile that:
//   - Allows all reads (tools need their host deps)
//   - Denies writes outside cwd, agentConfigDir, and /tmp
//   - Denies all outbound network except to guardian on 127.0.0.1:guardianPort
func buildSeatbeltProfile(cwd, agentConfigDir, guardianPort string) (string, error) {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")

	// Filesystem: deny writes outside allowed paths.
	b.WriteString("(deny file-write*)\n")
	fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbPath(cwd))
	b.WriteString("(allow file-write* (subpath \"/tmp\"))\n")
	b.WriteString("(allow file-write* (subpath \"/private/tmp\"))\n")
	// macOS uses /private/var/folders/<xx>/<...>/T/ for per-process temp files.
	b.WriteString("(allow file-write* (regex #\"^/private/var/folders/\"))\n")
	if agentConfigDir != "" {
		fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbPath(agentConfigDir))
	}

	// Network: deny all outbound, allow only the guardian proxy.
	// This ensures processes that ignore HTTP_PROXY still can't reach the
	// internet directly — all TCP must go through guardian.
	b.WriteString("(deny network-outbound)\n")
	fmt.Fprintf(&b, "(allow network-outbound (remote tcp4 \"localhost:%s\"))\n", guardianPort)
	// Unix domain sockets are used for macOS system IPC including the DNS
	// resolver (mDNSResponder), so allow them unconditionally.
	b.WriteString("(allow network-outbound (remote unix-socket))\n")

	return b.String(), nil
}

// sbPath returns a Seatbelt-quoted path literal.
func sbPath(p string) string {
	return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
}

// sandboxEnv builds the environment for the sandboxed process.
func sandboxEnv(cfg *config.Config, guardianPort string) []string {
	base := os.Environ()
	if cfg.Env.Filter {
		base = filteredEnv(cfg.Env.Allow)
	}
	env := append(base,
		"HTTP_PROXY=http://127.0.0.1:"+guardianPort,
		"HTTPS_PROXY=http://127.0.0.1:"+guardianPort,
		"NO_PROXY=localhost,127.0.0.1",
	)
	if ca := expandTildePath(cfg.Network.CABundle); ca != "" {
		for _, varName := range cfg.Network.CABundleEnvVars {
			env = append(env, varName+"="+ca)
		}
	}
	return env
}

// filteredEnv returns only the env vars in the allowlist.
func filteredEnv(allow []string) []string {
	allowed := make(map[string]bool, len(allow))
	for _, k := range allow {
		allowed[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		key := kv
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			key = kv[:idx]
		}
		if allowed[key] {
			out = append(out, kv)
		}
	}
	return out
}

func expandTildePath(p string) string {
	if len(p) > 0 && p[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// saveTTY captures tty settings so they can be restored after the child exits.
// Returns empty string if stdin is not a tty.
func saveTTY() string {
	out, err := exec.Command("stty", "-g").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// restoreTTY restores tty settings and resets cursor/mouse escape modes.
// The alternate-screen escape (1049l) is intentionally omitted — sending it
// when the terminal is not in alt-screen mode can wipe visible output.
func restoreTTY(saved string) {
	if saved == "" {
		return
	}
	const modeReset = "\x1b[?25h" +
		"\x1b[?1000l" +
		"\x1b[?1002l" +
		"\x1b[?1003l" +
		"\x1b[?1006l" +
		"\x1b[?1004l"
	_, _ = os.Stderr.WriteString(modeReset)
	stty := exec.Command("stty", saved)
	stty.Stdin = os.Stdin
	_ = stty.Run()
}

// Cleanup removes the sandbox state directory and stops the guardian.
func (r *Runtime) Cleanup() {
	if r.Guardian != nil {
		r.Guardian.Stop()
	}
	if r.StateDir != "" {
		os.RemoveAll(r.StateDir)
	}
}

// ControlSockPath returns the guardian control socket path for a sandbox.
func ControlSockPath(name string) string {
	return filepath.Join(fs.SandboxDir(name), "guardian.sock")
}

// ListRunning returns all sandboxes that have a live pid file.
func ListRunning() ([]SandboxInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sandboxDir := filepath.Join(home, ".xb", "sandboxes")
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
