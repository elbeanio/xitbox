package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/iangeorge/xitbox/pkg/guardian"
)

func main() {
	var (
		listenAddr    = flag.String("listen", "127.0.0.1:0", "Proxy listen address")
		controlSock   = flag.String("control", "", "Unix socket for control API")
		logPath       = flag.String("log", "", "JSONL audit log path")
		upstreamProxy = flag.String("upstream-proxy", "", "Upstream proxy URL (e.g. http://proxy.corp:8080)")
	)
	flag.Parse()

	// Parse allow/deny lists from environment
	allowList := os.Getenv("XB_ALLOW")
	denyList := os.Getenv("XB_DENY")

	rules := guardian.NewRules(split(allowList), split(denyList))

	server, err := guardian.NewServer(*listenAddr, *controlSock, *logPath, *upstreamProxy, rules)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	if err := server.Start(); err != nil {
		log.Fatalf("start server: %v", err)
	}

	addr := server.Addr()
	if addr != nil {
		fmt.Printf("xit-guardian listening on %s\n", addr.String())
	}
	if *controlSock != "" {
		fmt.Printf("control socket: %s\n", *controlSock)
	}

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("shutting down...")
	server.Stop()
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
