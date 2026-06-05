package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/iangeorge/xitbox/pkg/sandbox"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:           "list",
	Short:         "List running sandboxes",
	Long:          `Shows all currently running ephemeral sandboxes.`,
	RunE:          runList,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func runList(cmd *cobra.Command, args []string) error {
	sandboxes, err := sandbox.ListRunning()
	if err != nil {
		return fmt.Errorf("list sandboxes: %w", err)
	}

	if len(sandboxes) == 0 {
		fmt.Println("No running sandboxes.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tPID\tNETWORK\tCREATED")
	for _, s := range sandboxes {
		fmt.Fprintf(w, "%s\t%s\t%s\tfiltered\t%s\n", s.Name, s.Status, s.PID, s.Created)
	}
	w.Flush()
	return nil
}
