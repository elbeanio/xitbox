package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iangeorge/xitbox/pkg/config"
)

func runLogs(since string, follow bool) error {
	cfg, err := config.Load("", nil)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logPath := expandTilde(cfg.Network.LogFile)
	if logPath == "" {
		return fmt.Errorf("no log file configured")
	}

	var sinceTime time.Time
	if since != "" {
		d, err := time.ParseDuration(since)
		if err != nil {
			return fmt.Errorf("invalid --since: %w", err)
		}
		sinceTime = time.Now().Add(-d)
	}

	if follow {
		return followLog(logPath, sinceTime)
	}
	return printLog(logPath, sinceTime)
}

func printLog(logPath string, since time.Time) error {
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("(no log entries yet)")
			return nil
		}
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	found := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry struct {
			Timestamp time.Time `json:"ts"`
			Dest      string    `json:"dest"`
			Action    string    `json:"action"`
			Reason    string    `json:"reason"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if !since.IsZero() && entry.Timestamp.Before(since) {
			continue
		}
		fmt.Printf("%s  %-5s  %s  (%s)\n",
			entry.Timestamp.Format("2006-01-02 15:04:05"),
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
	fmt.Fprintln(os.Stderr, "following log (Ctrl-C to stop)...")
	for {
		if err := printLog(logPath, since); err != nil {
			return err
		}
		time.Sleep(2 * time.Second)
	}
}
