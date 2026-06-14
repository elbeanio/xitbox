package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/iangeorge/xitbox/pkg/sandbox"
)

func runList() error {
	sandboxes, err := sandbox.ListRunning()
	if err != nil {
		return fmt.Errorf("list sandboxes: %w", err)
	}

	if len(sandboxes) == 0 {
		fmt.Println("no running sandboxes")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PID\tCOMMAND\tDIRECTORY")
	for _, s := range sandboxes {
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.PID, s.Command, s.CWD)
	}
	return w.Flush()
}
