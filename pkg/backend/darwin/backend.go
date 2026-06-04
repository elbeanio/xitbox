//go:build darwin

package darwin

import (
	"fmt"
	"os/exec"
)

// IsAvailable checks if the darwin backend can run.
func IsAvailable() bool {
	_, err := exec.LookPath("lima")
	return err == nil
}

// RunSandbox runs a command inside the Lima VM sandbox.
func RunSandbox(command []string) error {
	return fmt.Errorf("macOS backend not yet implemented; bwrap not available natively on macOS")
}
