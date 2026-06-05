package cmd

import (
	"fmt"
	"os"

	"github.com/iangeorge/xitbox/pkg/config"
	"github.com/iangeorge/xitbox/pkg/platform"
	"github.com/iangeorge/xitbox/pkg/sandbox"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run [flags] -- <command> [args...]",
	Short: "Run a command inside an ephemeral sandbox",
	Long: `Creates an ephemeral sandbox, runs the given command inside it,
and cleans up when the command exits. Network is default-deny; use
'xitbox allow' to whitelist domains.`,
	Example: `  xitbox run -- claude
  xitbox run --name frontend -- npm run dev
  xitbox run -- echo "hello from sandbox"`,
	RunE:          runRun,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var (
	runName string
)

func init() {
	runCmd.Flags().StringVar(&runName, "name", "", "Sandbox name (auto-generated if empty)")
}

func runRun(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no command specified; use: xitbox run -- <command>")
	}

	// Load config
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}
	cfg, err := config.Load(cwd, nil)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Detect platform
	info, err := platform.Detect()
	if err != nil {
		return fmt.Errorf("platform detection: %w", err)
	}

	if !info.AllRequiredFound() {
		fmt.Fprintln(os.Stderr, "⚠️  Some required dependencies are missing. Run 'xitbox init' to check.")
	}

	// Start sandbox
	_, err = sandbox.Start(runName, cfg, info, args)
	if err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}

	return nil
}
