package cmd

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/iangeorge/xitbox/pkg/config"
	"github.com/iangeorge/xitbox/pkg/platform"
	"gopkg.in/yaml.v3"
)

func runDoctor() error {
	info, err := platform.Detect()
	if err != nil {
		return fmt.Errorf("detect platform: %w", err)
	}

	fmt.Printf("xb doctor — %s/%s\n\n", runtime.GOOS, runtime.GOARCH)

	// --- Dependencies ---
	fmt.Println("Dependencies:")
	allRequired := true
	for _, d := range info.Deps {
		tag := "ok"
		extra := ""
		if !d.Found {
			if d.Required {
				tag = "MISSING (required)"
				allRequired = false
			} else {
				tag = "not found (optional)"
			}
			if d.Install != "" {
				extra = "\n         install: " + d.Install
			}
		} else {
			extra = "  " + d.Path
		}
		fmt.Printf("  [%s] %s%s\n", tag, d.Name, extra)
	}

	// --- Network mode (Linux only) ---
	if info.IsLinux() {
		fmt.Printf("\nNetwork mode: %s\n", info.LinuxNetMode())
	} else {
		fmt.Println("\nNetwork mode: HTTP_PROXY → guardian (macOS Seatbelt cannot restrict by hostname)")
	}

	// --- Config ---
	fmt.Println("\nConfig:")
	cfgPath := config.DefaultConfigPath()
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		fmt.Printf("  [not found] %s\n", cfgPath)
		fmt.Println("             run: xb --init  to create it")
	} else {
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			fmt.Printf("  [error] %s: %v\n", cfgPath, err)
		} else {
			var check map[string]interface{}
			if err := yaml.Unmarshal(data, &check); err != nil {
				fmt.Printf("  [invalid YAML] %s: %v\n", cfgPath, err)
			} else {
				fmt.Printf("  [ok] %s\n", cfgPath)
			}
		}
	}

	cwd, _ := os.Getwd()
	projectCfg := cwd + "/.xb.yaml"
	if _, err := os.Stat(projectCfg); err == nil {
		data, _ := os.ReadFile(projectCfg)
		var check map[string]interface{}
		if err := yaml.Unmarshal(data, &check); err != nil {
			fmt.Printf("  [invalid YAML] .xb.yaml: %v\n", err)
		} else {
			fmt.Printf("  [ok] .xb.yaml (project override)\n")
		}
	}

	// --- Detected agents ---
	if len(info.Agents) > 0 {
		fmt.Printf("\nDetected agents: %s\n", strings.Join(info.Agents, ", "))
	} else {
		fmt.Println("\nDetected agents: none")
	}

	// --- Summary ---
	fmt.Println()
	if !allRequired {
		fmt.Println("Some required dependencies are missing. xb may not work correctly.")
		return fmt.Errorf("missing required dependencies")
	}
	fmt.Println("All required dependencies found. xb is ready.")
	return nil
}
