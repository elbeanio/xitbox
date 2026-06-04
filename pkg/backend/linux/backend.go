//go:build linux

package linux

import (
	"fmt"
	"os/exec"
)

// IsAvailable checks if the linux backend can run.
func IsAvailable() bool {
	_, err := exec.LookPath("bwrap")
	return err == nil
}

// RunSandbox runs a command inside a Linux sandbox.
func RunSandbox(command []string) error {
	return fmt.Errorf("Linux backend not yet fully implemented")
}
