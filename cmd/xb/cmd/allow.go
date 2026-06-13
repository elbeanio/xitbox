package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iangeorge/xitbox/pkg/config"
	"github.com/iangeorge/xitbox/pkg/guardian"
	"github.com/iangeorge/xitbox/pkg/sandbox"
)

func runAllow(domain, cidr string, fromLog bool) error {
	cfg, err := config.Load("", nil)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	var value, kind string

	switch {
	case domain != "":
		value, kind = domain, "domain"
	case cidr != "":
		value, kind = cidr, "cidr"
	case fromLog:
		v, err := lastBlockedFromLog(cfg.Network.LogFile)
		if err != nil {
			return err
		}
		fmt.Printf("allow %s? [Y/n] ", v)
		reader := bufio.NewReader(os.Stdin)
		resp, _ := reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "" && resp != "y" && resp != "yes" {
			fmt.Println("cancelled")
			return nil
		}
		value, kind = v, "domain"
	default:
		return fmt.Errorf("specify --domain, --cidr, or --from-log")
	}

	for _, existing := range cfg.Network.Allow {
		if existing == value {
			fmt.Printf("%s is already in the allowlist\n", value)
			return nil
		}
	}

	cfg.Network.Allow = append(cfg.Network.Allow, value)
	if err := cfg.SaveDefault(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("added %s (%s) to allowlist\n", value, kind)

	// Live-update any running sandbox guardians.
	updated := liveUpdateRunning(value, kind)
	if updated == 0 {
		fmt.Println("(no running sandboxes — will take effect on next run)")
	}
	return nil
}

// liveUpdateRunning sends an add_allow or add_deny to every running sandbox's
// guardian control socket. Returns the number of sandboxes successfully updated.
func liveUpdateRunning(value, kind string) int {
	sandboxes, err := sandbox.ListRunning()
	if err != nil || len(sandboxes) == 0 {
		return 0
	}

	action := "add_allow"
	if kind == "deny" {
		action = "add_deny"
	}

	updated := 0
	for _, sb := range sandboxes {
		sockPath := sandbox.ControlSockPath(sb.Name)
		resp, err := guardian.SendControl(sockPath, guardian.ControlRequest{
			Action: action,
			Value:  value,
		})
		if err != nil || !resp.OK {
			continue
		}
		fmt.Printf("  live-updated sandbox %q\n", sb.Name)
		updated++
	}
	return updated
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

	var lastDest string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry struct {
			Dest   string `json:"dest"`
			Action string `json:"action"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Action == "deny" {
			lastDest = entry.Dest
		}
	}

	if lastDest == "" {
		return "", fmt.Errorf("no blocked destinations found in log")
	}
	if idx := strings.LastIndex(lastDest, ":"); idx > 0 {
		return lastDest[:idx], nil
	}
	return lastDest, nil
}

func expandTilde(p string) string {
	if len(p) > 0 && p[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
