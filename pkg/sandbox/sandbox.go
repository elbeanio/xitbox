package sandbox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
		name = fmt.Sprintf("sandbox-%d", os.Getpid())
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
	commandName := ""
	if len(command) > 0 {
		commandName = command[0]
	}
	server.SetMeta(name, commandName)
	rt.Guardian = server

	cwd, _ := os.Getwd()

	os.WriteFile(filepath.Join(stateDir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0644)
	os.WriteFile(filepath.Join(stateDir, "cwd"), []byte(cwd), 0644)
	if len(command) > 0 {
		os.WriteFile(filepath.Join(stateDir, "command"), []byte(command[0]), 0644)
	}

	// Watch config files for changes and hot-reload guardian rules.
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
		paths = append(paths, filepath.Join(cwd, ".xb.yaml"))
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

// forwardSignals starts a goroutine that forwards SIGTERM and SIGINT to cmd.
// Returns a stop function that must be called when the command exits.
// This ensures that killing xb directly (e.g. `kill <pid>`) also terminates
// the sandboxed child and allows the normal cleanup path to run.
func forwardSignals(cmd *exec.Cmd) func() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	done := make(chan struct{})
	go func() {
		defer signal.Stop(sigCh)
		select {
		case sig := <-sigCh:
			if cmd.Process != nil {
				cmd.Process.Signal(sig)
			}
		case <-done:
		}
	}()
	return func() { close(done) }
}

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
	profile, err := buildSeatbeltProfile(cwd, agentConfigDir, guardianPort, cfg.Filesystem.AllowWrite, cfg.Filesystem.DenyRead)
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
	stopSig := forwardSignals(cmd)
	defer stopSig()

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
func buildSeatbeltProfile(cwd, agentConfigDir, guardianPort string, allowWrite, denyRead []string) (string, error) {
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
	for _, p := range allowWrite {
		p = expandTildePath(p)
		info, err := os.Stat(p)
		if err == nil && !info.IsDir() {
			// For files use a prefix regex so atomic-write temp files
			// (e.g. ~/.claude.json.tmp.<pid>.<hash>) are also covered.
			fmt.Fprintf(&b, "(allow file-write* (regex #\"^%s\"))\n", sbRegexEscape(p))
		} else {
			fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbPath(p))
		}
	}
	if agentConfigDir != "" {
		fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbPath(agentConfigDir))
	}

	// Read denies — protect credential files from the sandboxed process.
	// More specific rules override (allow default), so these take effect
	// even though the profile starts with a broad allow.
	for _, p := range denyRead {
		p = expandTildePath(p)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			fmt.Fprintf(&b, "(deny file-read* (literal %s))\n", sbPath(p))
		} else {
			fmt.Fprintf(&b, "(deny file-read* (subpath %s))\n", sbPath(p))
		}
	}

	// Deny access to the xb sandbox state directory. The sandboxed process has
	// no legitimate reason to read it, and the guardian control socket lives
	// there — if reachable, a sandboxed process could add arbitrary allow rules.
	if home, err := os.UserHomeDir(); err == nil {
		sandboxesDir := filepath.Join(home, ".xb", "sandboxes")
		fmt.Fprintf(&b, "(deny file-read* (subpath %s))\n", sbPath(sandboxesDir))
	}

	// Network: no OS-level outbound deny on macOS.
	//
	// Seatbelt cannot restrict to specific external hostnames (only * or
	// localhost are valid in tcp4 rules), and Claude Code's TUI makes
	// connections on multiple ports using native macOS networking that ignores
	// HTTP_PROXY. Attempting OS-level network restriction breaks TUI startup.
	//
	// Network filtering is enforced via HTTP_PROXY → guardian instead:
	// agents that respect the proxy (claude, aider, codex, npm, pip…) are
	// fully filtered and logged; agents that bypass the proxy can reach the
	// internet directly. This matches the model used by claude-sandbox and
	// agent-seatbelt, and is the realistic macOS constraint.

	return b.String(), nil
}

// sbPath returns a Seatbelt-quoted path literal.
func sbPath(p string) string {
	return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
}

// sbRegexEscape escapes a path for use as a Seatbelt ICU regex prefix.
// All ICU regex metacharacters are escaped so the path matches literally.
func sbRegexEscape(p string) string {
	var out strings.Builder
	for _, c := range p {
		switch c {
		case '"':
			// Escape Seatbelt string delimiter.
			out.WriteString(`\"`)
		case '.', '\\', '^', '$', '|', '?', '*', '+', '(', ')', '[', ']', '{', '}':
			// Escape ICU regex metacharacters.
			out.WriteRune('\\')
			out.WriteRune(c)
		default:
			out.WriteRune(c)
		}
	}
	return out.String()
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
		// Disable agent telemetry inside the sandbox — sandboxed processes
		// shouldn't be phoning home, and blocked telemetry calls can hang TUIs.
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
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

// abbreviateHome replaces the home directory prefix with ~ for display.
func abbreviateHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
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
		dir := filepath.Join(sandboxDir, entry.Name())

		// Check liveness by dialing the guardian control socket.
		// A PID check alone is unreliable — PIDs get reused by other processes.
		sockPath := filepath.Join(dir, "guardian.sock")
		conn, err := net.DialTimeout("unix", sockPath, 100*time.Millisecond)
		if err != nil {
			os.RemoveAll(dir)
			continue
		}
		conn.Close()

		pid, _ := os.ReadFile(filepath.Join(dir, "pid"))
		cwd, _ := os.ReadFile(filepath.Join(dir, "cwd"))
		command, _ := os.ReadFile(filepath.Join(dir, "command"))
		sandboxes = append(sandboxes, SandboxInfo{
			Name:    entry.Name(),
			PID:     strings.TrimSpace(string(pid)),
			CWD:     abbreviateHome(strings.TrimSpace(string(cwd))),
			Command: strings.TrimSpace(string(command)),
		})
	}
	return sandboxes, nil
}

// SandboxInfo describes a running sandbox.
type SandboxInfo struct {
	Name    string
	PID     string
	Command string
	CWD     string
}

// CWDMatches reports whether sb is running in the given working directory.
// sb.CWD may be abbreviated with ~; cwd should be an absolute path.
func CWDMatches(sb SandboxInfo, cwd string) bool {
	sbCWD := sb.CWD
	if len(sbCWD) > 0 && sbCWD[0] == '~' {
		home, err := os.UserHomeDir()
		if err == nil {
			sbCWD = filepath.Join(home, sbCWD[1:])
		}
	}
	// Resolve symlinks so /private/var/... == /var/... on macOS.
	if resolved, err := filepath.EvalSymlinks(sbCWD); err == nil {
		sbCWD = resolved
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	return sbCWD == cwd
}
