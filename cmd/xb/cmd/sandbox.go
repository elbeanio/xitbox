package cmd

import (
	"fmt"
	"os"

	"github.com/iangeorge/xitbox/pkg/config"
	"github.com/iangeorge/xitbox/pkg/platform"
	"github.com/iangeorge/xitbox/pkg/sandbox"
)

func runSandbox(name string, command []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}
	cfg, err := config.Load(cwd, nil)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	info, err := platform.Detect()
	if err != nil {
		return fmt.Errorf("platform detection: %w", err)
	}
	if !info.AllRequiredFound() {
		fmt.Fprintln(os.Stderr, "warning: some required dependencies are missing; run `xb --check` to diagnose")
	}
	_, err = sandbox.Start(name, cfg, info, command)
	return err
}
