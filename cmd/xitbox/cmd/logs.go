package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iangeorge/xitbox/pkg/config"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:           "logs",
	Short:         "View blocked connection attempts",
	Long:          `Tails the JSONL audit log of blocked network connections from the guardian proxy.`,
	RunE:          runLogs,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var (
	logsSince  string
	logsFollow bool
)

func init() {
	logsCmd.Flags().StringVar(&logsSince, "since", "", "Show entries since duration (e.g. 5m, 1h)")
	logsCmd.Flags().BoolVar(&logsFollow, "follow", false, "Follow log output in real-time")
}

func runLogs(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load("", nil)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logPath := cfg.Network.LogFile
	if logPath == "" {
		return fmt.Errorf("no log file configured")
	}
	logPath = expandTilde(logPath)

	var since time.Time
	if logsSince != "" {
		d, err := time.ParseDuration(logsSince)
		if err != nil {
			return fmt.Errorf("invalid --since: %w", err)
		}
		since = time.Now().Add(-d)
	}

	if logsFollow {
		return followLog(logPath, since)
	}
	return readLog(logPath, since)
}

func readLog(logPath string, since time.Time) error {
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("(no log entries yet)")
			return nil
		}
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry struct {
			Timestamp time.Time `json:"ts"`
			Dest      string    `json:"dest"`
			Action    string    `json:"action"`
			Reason    string    `json:"reason"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if !since.IsZero() && entry.Timestamp.Before(since) {
			continue
		}
		fmt.Printf("%s [%s] %s %s (%s)\n",
			entry.Timestamp.Format("2006-01-02 15:04:05"),
			"sandbox",
			strings.ToUpper(entry.Action),
			entry.Dest,
			entry.Reason,
		)
		found = true
	}
	if !found {
		fmt.Println("(no matching log entries)")
	}
	return nil
}

func followLog(logPath string, since time.Time) error {
	fmt.Println("Following log (Ctrl-C to exit)...")

	for {
		err := readLog(logPath, since)
		if err != nil {
			return err
		}
		time.Sleep(2 * time.Second)
	}
}
