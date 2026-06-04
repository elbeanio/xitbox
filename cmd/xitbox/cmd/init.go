package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/iangeorge/xitbox/pkg/config"
	"github.com/iangeorge/xitbox/pkg/platform"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize xitbox configuration and check dependencies",
	Long:  `Creates the default configuration file, detects installed agents, and verifies that required dependencies are available.`,
	RunE:  runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	fmt.Println("🧰  Initializing xitbox...")

	// Ensure directories
	if err := platform.EnsureDirs(); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}
	fmt.Println("✓ Created xitbox directories")

	// Detect platform and deps
	info, err := platform.Detect()
	if err != nil {
		return fmt.Errorf("platform detection: %w", err)
	}

	// Save default config
	cfg := config.DefaultConfig()
	if err := cfg.SaveDefault(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Printf("✓ Created default config: %s\n", config.DefaultConfigPath())

	// Detect and create agent persist dirs
	if len(info.Agents) > 0 {
		fmt.Printf("✓ Detected agents: %s\n", join(info.Agents, ", "))
		for _, agent := range info.Agents {
			dir := filepath.Join(info.PersistDir, agent)
			if err := os.MkdirAll(dir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ Failed to create persist dir for %s: %v\n", agent, err)
			} else {
				fmt.Printf("  ✓ Persist dir: %s\n", dir)
			}
		}
	} else {
		fmt.Println("  No agents detected (this is fine — you can add them later)")
	}

	// Report dependencies
	fmt.Println("\n📋  Dependencies:")
	allOk := true
	for _, dep := range info.Deps {
		status := "✓"
		if !dep.Found {
			if dep.Required {
				status = "✗ REQUIRED"
				allOk = false
			} else {
				status = "○ optional"
			}
		}
		fmt.Printf("  %s %s", status, dep.Name)
		if dep.Found {
			fmt.Printf("  (%s)", dep.Path)
		} else {
			fmt.Printf("  → install: %s", dep.Install)
		}
		fmt.Println()
	}

	fmt.Println()
	if allOk {
		fmt.Println("🚀  xitbox is ready! Run 'xitbox run -- <command>' to start sandboxing.")
	} else {
		fmt.Println("⚠️   Some required dependencies are missing. Install them and run 'xitbox init' again.")
	}

	return nil
}

func join(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for _, s := range ss[1:] {
		result += sep + s
	}
	return result
}
