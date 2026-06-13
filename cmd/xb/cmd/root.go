package cmd

import (
	"flag"
	"fmt"
	"os"
)

func Execute() error {
	return execute(os.Args[1:])
}

func execute(args []string) error {
	fs := flag.NewFlagSet("xb", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { printHelp(fs) }

	// Management flags
	doAllow := fs.Bool("allow", false, "Add a domain or CIDR to the allowlist")
	doLogs := fs.Bool("logs", false, "View blocked connection attempts")
	doList := fs.Bool("list", false, "List running sandboxes")

	// --allow sub-flags
	allowDomain := fs.String("domain", "", "Domain to allow (with --allow)")
	allowCIDR := fs.String("cidr", "", "CIDR range to allow (with --allow)")
	allowFromLog := fs.Bool("from-log", false, "Allow the most recently blocked destination (with --allow)")

	// --logs sub-flags
	logsSince := fs.String("since", "", "Show entries since duration, e.g. 5m, 1h (with --logs)")
	logsFollow := fs.Bool("follow", false, "Follow log output in real-time (with --logs)")

	// Sandbox flags
	sandboxName := fs.String("name", "", "Sandbox name (auto-generated if empty)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	// Validate sub-flags aren't used without their parent
	if (*allowDomain != "" || *allowCIDR != "" || *allowFromLog) && !*doAllow {
		return fmt.Errorf("--domain, --cidr, and --from-log require --allow")
	}
	if (*logsSince != "" || *logsFollow) && !*doLogs {
		return fmt.Errorf("--since and --follow require --logs")
	}

	switch {
	case *doAllow:
		return runAllow(*allowDomain, *allowCIDR, *allowFromLog)
	case *doLogs:
		return runLogs(*logsSince, *logsFollow)
	case *doList:
		return runList()
	default:
		command := fs.Args()
		if len(command) == 0 {
			printHelp(fs)
			return nil
		}
		return runSandbox(*sandboxName, command)
	}
}

func printHelp(_ *flag.FlagSet) {
	fmt.Fprint(os.Stderr, `xb — sandbox any command or AI agent

Usage:
  xb [flags] <command> [args...]     Run a command inside a sandbox
  xb --allow [flags]                 Manage the network allowlist
  xb --logs [flags]                  View blocked connections
  xb --list                          List running sandboxes

Examples:
  xb claude --dangerously-skip-permissions
  xb npm install
  xb --name myproject python script.py
  xb --allow --domain api.mycompany.internal
  xb --allow --from-log
  xb --logs --since 5m
  xb --logs --follow

Sandbox flags:
  --name string     Sandbox name (auto-generated if empty)

Allow flags (require --allow):
  --domain string   Domain to add, supports wildcards (*.example.com)
  --cidr string     CIDR range to add (e.g. 10.0.0.0/8)
  --from-log        Add the most recently blocked destination

Log flags (require --logs):
  --since duration  Show entries from the last duration (e.g. 5m, 1h)
  --follow          Stream log output in real-time
`)
}
