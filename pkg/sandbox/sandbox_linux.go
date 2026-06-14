//go:build linux

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
)

// runLinux tries transparent proxy modes in order of preference:
//
//  1. pasta   — full transparent proxy via userspace networking + iptables DNAT
//  2. slirp4netns — same but with the older slirp4netns tool
//  3. relay   — bwrap --unshare-net + a tiny relay inside the sandbox that
//     bridges the isolated loopback to guardian via a Unix socket on the
//     shared filesystem. No external deps. Ill-behaved processes (e.g. JVMs
//     that ignore HTTP_PROXY) get no network at all rather than unfiltered access.
func runLinux(rt *Runtime, cfg *config.Config, command []string, guardianPort string) error {
	if pasta, err := exec.LookPath("pasta"); err == nil {
		if err := runLinuxUserspaceNet(rt, cfg, command, guardianPort, "pasta", pasta); err == nil {
			return nil
		} else {
			fmt.Fprintf(os.Stderr, "xb: pasta transparent proxy failed (%v), trying fallback\n", err)
		}
	}

	if slirp, err := exec.LookPath("slirp4netns"); err == nil {
		if err := runLinuxUserspaceNet(rt, cfg, command, guardianPort, "slirp4netns", slirp); err == nil {
			return nil
		} else {
			fmt.Fprintf(os.Stderr, "xb: slirp4netns transparent proxy failed (%v), trying fallback\n", err)
		}
	}

	fmt.Fprintln(os.Stderr, "xb: pasta/slirp4netns not available — using relay mode")
	fmt.Fprintln(os.Stderr, "    (install pasta or slirp4netns for OS-level network enforcement)")
	return runLinuxRelay(rt, cfg, command, guardianPort)
}

// userspaceGW is the gateway IP that pasta and slirp4netns present as the host.
const userspaceGW = "10.0.2.2"

// runLinuxUserspaceNet runs the sandbox with either pasta or slirp4netns providing
// rootless network connectivity.
//
// Sequence:
//  1. bwrap starts with --unshare-user (owns the network namespace) + --unshare-net
//  2. The shell inside writes its PID to /xb-state/sandbox-pid and waits
//  3. Host starts pasta/slirp4netns against that PID's network namespace
//  4. Host writes /xb-state/network-ready to signal the shell
//  5. Shell configures the tap interface, sets up iptables DNAT, execs command
func runLinuxUserspaceNet(rt *Runtime, cfg *config.Config, command []string, guardianPort, tool, toolPath string) error {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("bwrap not found in PATH")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}

	pidFile := filepath.Join(rt.StateDir, "sandbox-pid")
	readyFile := filepath.Join(rt.StateDir, "network-ready")
	os.Remove(pidFile)
	os.Remove(readyFile)

	mounts := fs.PrepareMounts(cfg, cwd)
	var envWhitelist []string
	if cfg.Env.Filter {
		envWhitelist = cfg.Env.Allow
	}
	bwrapBaseArgs := fs.BuildBwrapArgs(mounts, envWhitelist)

	// Setup script runs inside the sandbox:
	// 1. Report PID so the host can start the networking tool
	// 2. Wait for the host to signal network is ready
	// 3. Bring up interfaces, configure iptables DNAT, exec the real command
	setupScript := fmt.Sprintf(`
set -e
echo $$ > /xb-state/sandbox-pid
until [ -f /xb-state/network-ready ]; do sleep 0.05; done
ip link set lo up 2>/dev/null || true
for iface in tap0 passt0 eth0; do
    ip link set "$iface" up 2>/dev/null && break
done
ip route add default via %s 2>/dev/null || true
iptables -t nat -A OUTPUT -p tcp ! -d %s -j DNAT --to-destination %s:%s 2>/dev/null || true
HTTP_PROXY=http://%s:%s HTTPS_PROXY=http://%s:%s NO_PROXY=localhost,127.0.0.1 exec "$@"
`, userspaceGW, userspaceGW, userspaceGW, guardianPort,
		userspaceGW, guardianPort, userspaceGW, guardianPort)

	bwrapArgs := append(bwrapBaseArgs,
		"--unshare-user", "--uid", "0", "--gid", "0",
		"--unshare-net",
		"--bind", rt.StateDir, "/xb-state",
		"--",
		"sh", "-c", setupScript, "--",
	)
	bwrapArgs = append(bwrapArgs, command...)

	cmd := cgroupsWrap(exec.Command(bwrapPath, bwrapArgs...), cfg)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	rt.Cmd = cmd

	savedTTY := saveTTY()
	defer restoreTTY(savedTTY)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start bwrap: %w", err)
	}

	// Wait for the sandbox to write its PID
	childPID, err := waitForFile(pidFile, 5*time.Second, func(data string) (int, bool) {
		pid, err := strconv.Atoi(strings.TrimSpace(data))
		if err != nil || pid <= 0 {
			return 0, false
		}
		return pid, true
	})
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("sandbox did not report PID: %w", err)
	}

	// Start the userspace networking tool against the sandbox's network namespace
	netnsPath := fmt.Sprintf("/proc/%d/ns/net", childPID)
	var netCmd *exec.Cmd
	switch tool {
	case "pasta":
		netCmd = exec.Command(toolPath, "-q", "--config-net", "--netns", netnsPath)
	case "slirp4netns":
		netCmd = exec.Command(toolPath, "--configure", "--mtu=65520", "--disable-host-loopback",
			strconv.Itoa(childPID), "tap0")
	}
	netCmd.Stderr = os.Stderr
	if err := netCmd.Start(); err != nil {
		os.WriteFile(readyFile, []byte("abort"), 0644)
		cmd.Wait()
		return fmt.Errorf("start %s: %w", tool, err)
	}

	// Brief pause for the networking tool to configure the interface
	time.Sleep(200 * time.Millisecond)

	// Signal the sandbox that the network is ready
	if err := os.WriteFile(readyFile, []byte("ready"), 0644); err != nil {
		netCmd.Process.Kill()
		cmd.Process.Kill()
		cmd.Wait()
		return fmt.Errorf("signal sandbox: %w", err)
	}

	cmdErr := cmd.Wait()
	netCmd.Process.Kill()
	netCmd.Wait()
	os.Remove(pidFile)
	os.Remove(readyFile)

	if cmdErr != nil {
		return fmt.Errorf("sandboxed command: %w", cmdErr)
	}
	return nil
}

// runLinuxRelay runs the sandbox with --unshare-net (no external connectivity)
// and a relay process inside that bridges the sandbox's loopback to guardian
// via a Unix domain socket on the shared state directory.
//
// Processes that respect HTTP_PROXY are filtered by guardian.
// Processes that ignore HTTP_PROXY (e.g. JVMs) have no network whatsoever.
func runLinuxRelay(rt *Runtime, cfg *config.Config, command []string, guardianPort string) error {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("bwrap not found in PATH")
	}

	xbBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find xb binary: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}

	const relayPort = "34567"
	guardianProxySock := filepath.Join(rt.StateDir, "guardian-proxy.sock")

	// Tell guardian to also accept proxy connections on a Unix socket so the
	// relay inside the network-isolated sandbox can reach it.
	if err := rt.Guardian.AddProxySock(guardianProxySock); err != nil {
		return fmt.Errorf("add guardian proxy socket: %w", err)
	}

	mounts := fs.PrepareMounts(cfg, cwd)
	var envWhitelist []string
	if cfg.Env.Filter {
		envWhitelist = cfg.Env.Allow
	}
	bwrapBaseArgs := fs.BuildBwrapArgs(mounts, envWhitelist)

	// Start the relay as a background process, then exec the real command.
	// The relay listens on TCP 127.0.0.1:RELAY_PORT (sandbox loopback) and
	// forwards each connection to the guardian Unix socket.
	setupScript := fmt.Sprintf(
		`/xb-bin --xb-internal-relay %s /xb-state/guardian-proxy.sock & sleep 0.1 && HTTP_PROXY=http://127.0.0.1:%s HTTPS_PROXY=http://127.0.0.1:%s NO_PROXY=localhost,127.0.0.1 exec "$@"`,
		relayPort, relayPort, relayPort,
	)

	bwrapArgs := append(bwrapBaseArgs,
		"--unshare-net",
		"--ro-bind", xbBin, "/xb-bin",
		"--bind", rt.StateDir, "/xb-state",
		"--",
		"sh", "-c", setupScript, "--",
	)
	bwrapArgs = append(bwrapArgs, command...)

	cmd := cgroupsWrap(exec.Command(bwrapPath, bwrapArgs...), cfg)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	rt.Cmd = cmd

	savedTTY := saveTTY()
	defer restoreTTY(savedTTY)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sandboxed command: %w", err)
	}
	return nil
}

// waitForFile polls path until the content satisfies parse, or timeout.
func waitForFile[T any](path string, timeout time.Duration, parse func(string) (T, bool)) (T, error) {
	var zero T
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if v, ok := parse(string(data)); ok {
				return v, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return zero, fmt.Errorf("timeout after %s", timeout)
}
