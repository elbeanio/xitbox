//go:build darwin

package darwin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iangeorge/xitbox/pkg/config"
	"github.com/iangeorge/xitbox/pkg/fs"
	"github.com/iangeorge/xitbox/pkg/platform"
)

const vmName = "xitbox"

// IsAvailable checks if the darwin backend can run.
func IsAvailable() bool {
	_, err := exec.LookPath("limactl")
	return err == nil
}

// RunSandbox runs a command inside the Lima VM sandbox.
func RunSandbox(name string, cfg *config.Config, info *platform.Info, command []string, guardianAddr string) error {
	// Ensure VM exists and is running
	if err := ensureVM(); err != nil {
		return fmt.Errorf("lima vm: %w", err)
	}

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

	// On macOS, Lima mounts the host home directory inside the VM at the same path
	// So paths like /Users/<user>/... work inside the VM too

	// Build the command to execute inside VM
	vmArgs := []string{"exec", vmName, "--"}

	// Set proxy env for the sandboxed process to reach the host guardian
	// Lima provides host.lima.internal which resolves to the host
	if guardianAddr != "" {
		vmArgs = append(vmArgs, "env", "HTTP_PROXY=http://host.lima.internal:"+guardianAddr)
		vmArgs = append(vmArgs, "env", "HTTPS_PROXY=http://host.lima.internal:"+guardianAddr)
	}

	vmArgs = append(vmArgs, "bwrap")
	vmArgs = append(vmArgs, bwrapArgs...)
	vmArgs = append(vmArgs, command...)

	cmd := exec.Command("limactl", vmArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sandboxed command: %w", err)
	}

	return nil
}

// ensureVM checks if the xitbox Lima VM exists, creates it if not, and starts it.
func ensureVM() error {
	// Check if VM exists
	exists, err := vmExists()
	if err != nil {
		return fmt.Errorf("check vm: %w", err)
	}

	if !exists {
		fmt.Fprintln(os.Stderr, "🔄  Creating xitbox Lima VM (this takes ~30s)...")
		if err := createVM(); err != nil {
			return fmt.Errorf("create vm: %w", err)
		}
	}

	// Check if running
	running, err := vmRunning()
	if err != nil {
		return fmt.Errorf("check vm status: %w", err)
	}

	if !running {
		fmt.Fprintln(os.Stderr, "🔄  Starting xitbox Lima VM...")
		if err := startVM(); err != nil {
			return fmt.Errorf("start vm: %w", err)
		}
		// Wait a moment for VM to be ready
		time.Sleep(2 * time.Second)
	}

	return nil
}

func vmExists() (bool, error) {
	out, err := exec.Command("limactl", "list", "--json").Output()
	if err != nil {
		return false, err
	}
	// Simple string check - parse the JSON output for VM name
	return strings.Contains(string(out), fmt.Sprintf(`"name":"%s"`, vmName)), nil
}

func vmRunning() (bool, error) {
	out, err := exec.Command("limactl", "list", vmName, "--json").Output()
	if err != nil {
		// If the list command fails, the VM probably doesn't exist
		return false, nil
	}
	return strings.Contains(string(out), `"status":"Running"`), nil
}

func createVM() error {
	// Find the template
	templatePath := filepath.Join(os.Getenv("HOME"), ".config", "xitbox", "lima", "xitbox.yaml")
	if _, err := os.Stat(templatePath); os.IsNotExist(err) {
		// Try to find it relative to the xitbox binary
		exe, err := os.Executable()
		if err == nil {
			templatePath = filepath.Join(filepath.Dir(exe), "..", "init", "lima", "xitbox.yaml")
			if _, err := os.Stat(templatePath); os.IsNotExist(err) {
				templatePath = filepath.Join(filepath.Dir(exe), "..", "..", "init", "lima", "xitbox.yaml")
			}
		}
	}

	// Copy template to Lima's default location if needed
	limaTemplateDir := filepath.Join(os.Getenv("HOME"), ".local", "share", "lima", "templates")
	_ = os.MkdirAll(limaTemplateDir, 0755)

	cmd := exec.Command("limactl", "start", "--name="+vmName, "--tty=false", templatePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func startVM() error {
	cmd := exec.Command("limactl", "start", vmName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
