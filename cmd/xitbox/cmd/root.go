package cmd

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "xitbox",
	Short: "A lightweight sandbox for AI coding agents",
	Long: `xitbox creates ephemeral sandboxes for AI coding agents with
default-deny network and filesystem access. One command to run,
one keystroke to allow a blocked domain.`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(allowCmd)
	rootCmd.AddCommand(logsCmd)
}
