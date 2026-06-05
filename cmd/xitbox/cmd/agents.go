package cmd

import (
	"github.com/spf13/cobra"
)

// agentNames lists the agents that get first-class subcommands.
// The list is intentionally small: these are the agents xitbox knows how
// to isolate via per-agent Lima VMs (see knownAgents in pkg/sandbox).
var agentNames = []string{"claude", "opencode", "codex", "aider"}

func init() {
	for _, agent := range agentNames {
		agent := agent
		cmd := &cobra.Command{
			Use:   agent + " [args...]",
			Short: "Run " + agent + " inside a sandboxed VM",
			Long: "Runs the " + agent + " agent inside an ephemeral sandbox with default-deny " +
				"network and filesystem access. Equivalent to `xitbox run -- " + agent + " [args...]`.\n\n" +
				"Use `--` to pass flags through to the agent, e.g. `xitbox " + agent + " -- --help`.",
			Args:          cobra.ArbitraryArgs,
			RunE:          agentRun(agent),
			SilenceUsage:  true,
			SilenceErrors: true,
		}
		cmd.Flags().StringVar(&runName, "name", "", "Sandbox name (auto-generated if empty)")
		rootCmd.AddCommand(cmd)
	}
}

// agentRun returns a cobra RunE that prepends the agent name to the args
// and delegates to runRun.
func agentRun(agent string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		return runRun(cmd, append([]string{agent}, args...))
	}
}
