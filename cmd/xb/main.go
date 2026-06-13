package main

import (
	"fmt"
	"io"
	"net"
	"os"

	"github.com/iangeorge/xitbox/cmd/xb/cmd"
)

func main() {
	// Internal relay mode: runs inside the bwrap sandbox (Linux relay fallback).
	// Bridges TCP 127.0.0.1:PORT → Unix socket PATH so the isolated sandbox
	// loopback can reach the guardian proxy on the host filesystem.
	if len(os.Args) > 1 && os.Args[1] == "--xb-internal-relay" {
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: --xb-internal-relay PORT SOCKET_PATH")
			os.Exit(1)
		}
		if err := runRelay(os.Args[2], os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runRelay(port, socketPath string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return fmt.Errorf("relay listen: %w", err)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			upstream, err := net.Dial("unix", socketPath)
			if err != nil {
				return
			}
			defer upstream.Close()
			done := make(chan struct{}, 2)
			go func() { io.Copy(upstream, c); upstream.Close(); done <- struct{}{} }()
			go func() { io.Copy(c, upstream); c.Close(); done <- struct{}{} }()
			<-done
			<-done
		}(conn)
	}
}
