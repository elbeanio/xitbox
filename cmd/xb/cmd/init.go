package cmd

import (
	"fmt"
	"os"

	"github.com/iangeorge/xitbox/pkg/config"
)

func runInit() error {
	path := config.DefaultConfigPath()

	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(os.Stderr, "config already exists at %s\n", path)
		fmt.Fprintf(os.Stderr, "delete it first if you want to reset to defaults\n")
		return nil
	}

	if err := config.DefaultConfig().SaveDefault(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	fmt.Printf("wrote default config to %s\n", path)
	fmt.Println()
	fmt.Println("to override for a specific project, create .xb.yaml in that directory:")
	fmt.Println("  network:")
	fmt.Println("    allow:")
	fmt.Println("      - api.mycompany.internal")
	fmt.Println("  filesystem:")
	fmt.Println("    allow_write:")
	fmt.Println("      - ~/some/extra/path")
	fmt.Println()
	fmt.Println("project config is additive — you only need to list the extras,")
	fmt.Println("not replicate the full default.")
	return nil
}
