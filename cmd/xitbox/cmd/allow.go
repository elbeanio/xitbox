package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iangeorge/xitbox/pkg/config"
	"github.com/spf13/cobra"
)

var allowCmd = &cobra.Command{
	Use:           "allow",
	Short:         "Add a domain or IP to the whitelist",
	Long:          `Whitelists a domain, CIDR, or recent log entry. Modifies the xitbox config file.`,
	RunE:          runAllow,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var (
	allowDomain  string
	allowCIDR    string
	allowFromLog bool
)

func init() {
	allowCmd.Flags().StringVar(&allowDomain, "domain", "", "Domain to allow (supports wildcards: *.example.com)")
	allowCmd.Flags().StringVar(&allowCIDR, "cidr", "", "CIDR range to allow (e.g. 10.0.0.0/8)")
	allowCmd.Flags().BoolVar(&allowFromLog, "from-log", false, "Allow the most recently blocked destination")
}

func runAllow(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load("", nil)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	var value string
	var kind string

	if allowDomain != "" {
		value = allowDomain
		kind = "domain"
	} else if allowCIDR != "" {
		value = allowCIDR
		kind = "cidr"
	} else if allowFromLog {
		v, err := lastBlockedFromLog(cfg.Network.LogFile)
		if err != nil {
			return err
		}
		value = v
		kind = "domain"
		fmt.Printf("? Allow %s? (Y/n) ", value)
		reader := bufio.NewReader(os.Stdin)
		resp, _ := reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "" && resp != "y" && resp != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	} else {
		return fmt.Errorf("specify --domain, --cidr, or --from-log")
	}

	// Check if already allowed
	for _, existing := range cfg.Network.Allow {
		if existing == value {
			fmt.Printf("✓ %s is already in the whitelist\n", value)
			return nil
		}
	}

	cfg.Network.Allow = append(cfg.Network.Allow, value)
	if err := cfg.SaveDefault(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("✓ Added %s (%s) to default whitelist\n", value, kind)
	fmt.Println("  Restart your sandbox for this to take effect.")
	return nil
}

func lastBlockedFromLog(logPath string) (string, error) {
	if logPath == "" {
		return "", fmt.Errorf("no log file configured")
	}
	logPath = expandTilde(logPath)

	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no log file found at %s", logPath)
		}
		return "", fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	var lastEntry struct {
		Dest string `json:"dest"`
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry struct {
			Dest   string `json:"dest"`
			Action string `json:"action"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Action == "deny" {
			lastEntry.Dest = entry.Dest
		}
	}

	if lastEntry.Dest == "" {
		return "", fmt.Errorf("no blocked destinations found in log")
	}

	// Extract just the host from "host:port"
	if idx := strings.LastIndex(lastEntry.Dest, ":"); idx > 0 {
		return lastEntry.Dest[:idx], nil
	}
	return lastEntry.Dest, nil
}

func expandTilde(p string) string {
	if len(p) > 0 && p[0] == '~' {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
