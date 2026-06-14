package guardian

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is the guardian proxy.
type Server struct {
	listenAddr    string
	controlSock   string
	logPath       string
	upstreamProxy string // optional corporate proxy, e.g. http://proxy.corp:8080
	sandboxName   string // included in log entries for correlation
	command       string // included in log entries for correlation
	rules         *Rules
	logFile       *os.File
	logMu         sync.Mutex
	listener      net.Listener
	controlLn     net.Listener
	proxySockLn   net.Listener // optional Unix socket proxy listener (Linux relay mode)
	wg            sync.WaitGroup
	stopCh        chan struct{}
}

// NewServer creates a new guardian server.
func NewServer(listenAddr, controlSock, logPath, upstreamProxy string, rules *Rules) (*Server, error) {
	if rules == nil {
		rules = NewRules(nil, nil)
	}
	return &Server{
		listenAddr:    listenAddr,
		controlSock:   controlSock,
		logPath:       logPath,
		upstreamProxy: upstreamProxy,
		rules:         rules,
		stopCh:        make(chan struct{}),
	}, nil
}

// Start begins listening for proxy and control connections.
func (s *Server) Start() error {
	// Open log file
	if s.logPath != "" {
		if err := os.MkdirAll(filepath.Dir(s.logPath), 0755); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
		f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		s.logFile = f
	}

	// Start proxy listener
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("listen proxy: %w", err)
	}
	s.listener = ln

	// Start control socket listener
	if s.controlSock != "" {
		if err := os.Remove(s.controlSock); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old control socket: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(s.controlSock), 0755); err != nil {
			return fmt.Errorf("create control socket dir: %w", err)
		}
		cln, err := net.Listen("unix", s.controlSock)
		if err != nil {
			return fmt.Errorf("listen control socket: %w", err)
		}
		s.controlLn = cln
		s.wg.Add(1)
		go s.serveControl()
	}

	s.wg.Add(1)
	go s.serveProxy()

	return nil
}

// AddProxySock opens an additional Unix domain socket for proxy connections.
// Used by the Linux relay mode to accept connections from inside the sandbox.
func (s *Server) AddProxySock(path string) error {
	os.Remove(path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create proxy sock dir: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("listen proxy sock: %w", err)
	}
	s.proxySockLn = ln
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-s.stopCh:
					return
				default:
					continue
				}
			}
			s.wg.Add(1)
			go s.handleConnection(conn)
		}
	}()
	return nil
}

// Stop shuts down the server.
func (s *Server) Stop() error {
	close(s.stopCh)
	if s.listener != nil {
		s.listener.Close()
	}
	if s.controlLn != nil {
		s.controlLn.Close()
	}
	if s.proxySockLn != nil {
		s.proxySockLn.Close()
	}
	s.wg.Wait()
	if s.logFile != nil {
		s.logFile.Close()
	}
	return nil
}

// SetMeta sets the sandbox name and command included in log entries.
// Call after Start() to annotate log output for correlation across sandboxes.
func (s *Server) SetMeta(sandboxName, command string) {
	s.sandboxName = sandboxName
	s.command = command
}

// ReplaceRules atomically replaces the guardian's allow and deny lists.
// Used for live config reload without restarting the sandbox.
func (s *Server) ReplaceRules(allow, deny []string) {
	s.rules.Replace(allow, deny)
}

// Addr returns the actual listening address.
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

func (s *Server) serveProxy() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.stopCh:
				return
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}
		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(clientConn net.Conn) {
	defer s.wg.Done()
	defer clientConn.Close()

	// Peek at the first bytes to determine protocol
	buf := make([]byte, 4096)
	n, err := clientConn.Read(buf)
	if err != nil {
		return
	}
	peek := buf[:n]

	var destHost string
	var destPort int
	var isConnect bool

	if bytes.HasPrefix(peek, []byte("CONNECT ")) {
		// HTTP CONNECT proxy request
		isConnect = true
		req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(peek)))
		if err != nil {
			s.logDecision("", "deny", "invalid-connect", string(peek))
			return
		}
		host, portStr, err := net.SplitHostPort(req.Host)
		if err != nil {
			host = req.Host
			portStr = "443"
		}
		destHost = host
		destPort = parsePort(portStr)
		// 200 is sent below, after the rules check.
	} else {
		// Try TLS SNI extraction
		sni, ok := extractSNI(peek)
		if ok {
			destHost = sni
			destPort = 443
		} else {
			// Plain HTTP or unknown - try to read Host header
			if idx := bytes.Index(peek, []byte("\r\nHost: ")); idx >= 0 {
				after := peek[idx+8:]
				if end := bytes.Index(after, []byte("\r\n")); end >= 0 {
					destHost = strings.TrimSpace(string(after[:end]))
					destPort = 80
				}
			}
		}

		// If we still don't know the destination, we can't proxy
		if destHost == "" {
			s.logDecision("", "deny", "unknown-destination", "")
			return
		}
	}

	action, reason := s.rules.Check(destHost, destPort)
	s.logDecision(fmt.Sprintf("%s:%d", destHost, destPort), action, reason, "")

	if action == "deny" {
		if isConnect {
			fmt.Fprintf(clientConn, "HTTP/1.1 403 Forbidden\r\n\r\n")
		}
		return
	}

	if isConnect {
		fmt.Fprintf(clientConn, "HTTP/1.1 200 Connection established\r\n\r\n")
	}

	// Connect to destination (directly or via upstream proxy)
	destAddr := net.JoinHostPort(destHost, strconv.Itoa(destPort))
	serverConn, err := s.dialDest(destAddr)
	if err != nil {
		log.Printf("connect to %s: %v", destAddr, err)
		return
	}
	defer serverConn.Close()

	if !isConnect {
		// For non-CONNECT, write the peeked data first
		if _, err := serverConn.Write(peek); err != nil {
			log.Printf("write peek to server: %v", err)
			return
		}
	}

	// Bidirectional copy. When either side closes, close the other so the
	// second goroutine unblocks and we exit cleanly without leaking it.
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(serverConn, clientConn)
		serverConn.Close()
		done <- struct{}{}
	}()
	go func() {
		io.Copy(clientConn, serverConn)
		clientConn.Close()
		done <- struct{}{}
	}()
	<-done
	<-done
}

// dialDest connects to destAddr, routing through s.upstreamProxy if configured.
// For upstream proxies it uses HTTP CONNECT to establish the tunnel.
func (s *Server) dialDest(destAddr string) (net.Conn, error) {
	if s.upstreamProxy == "" {
		return net.Dial("tcp", destAddr)
	}

	u, err := url.Parse(s.upstreamProxy)
	if err != nil {
		return nil, fmt.Errorf("parse upstream proxy: %w", err)
	}

	proxyHost := u.Host
	conn, err := net.Dial("tcp", proxyHost)
	if err != nil {
		return nil, fmt.Errorf("dial upstream proxy %s: %w", proxyHost, err)
	}

	// Send CONNECT to the upstream proxy
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", destAddr, destAddr)
	if u.User != nil {
		creds := u.User.String()
		encoded := basicAuthHeader(creds)
		req += "Proxy-Authorization: Basic " + encoded + "\r\n"
	}
	req += "\r\n"

	if _, err := fmt.Fprint(conn, req); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send CONNECT to upstream proxy: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read upstream proxy response: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("upstream proxy CONNECT failed: %s", resp.Status)
	}

	return conn, nil
}

func basicAuthHeader(userinfo string) string {
	return base64.StdEncoding.EncodeToString([]byte(userinfo))
}

func (s *Server) logDecision(dest, action, reason, detail string) {
	if s.logFile == nil {
		return
	}
	entry := LogEntry{
		Timestamp: time.Now().UTC(),
		Sandbox:   s.sandboxName,
		Command:   s.command,
		Dest:      dest,
		Action:    action,
		Reason:    reason,
		Detail:    detail,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logFile.Write(data)
	s.logFile.WriteString("\n")
}

// LogEntry is a single audit log record.
type LogEntry struct {
	Timestamp time.Time `json:"ts"`
	Sandbox   string    `json:"sandbox,omitempty"`
	Command   string    `json:"command,omitempty"`
	Dest      string    `json:"dest"`
	Action    string    `json:"action"` // "allow" or "deny"
	Reason    string    `json:"reason"`
	Detail    string    `json:"detail,omitempty"`
}

func parsePort(s string) int {
	var p int
	fmt.Sscanf(s, "%d", &p)
	if p == 0 {
		return 443
	}
	return p
}

// extractSNI extracts the Server Name Indication from a TLS ClientHello.
func extractSNI(data []byte) (string, bool) {
	// TLS record layer
	if len(data) < 5 {
		return "", false
	}
	if data[0] != 0x16 { // ContentType: Handshake
		return "", false
	}

	// Skip record header (5 bytes) + handshake header (4 bytes)
	// Then ClientHello
	if len(data) < 43 {
		return "", false
	}

	// TLS record: [content_type:1][version:2][length:2]
	recordLen := int(data[3])<<8 | int(data[4])
	if len(data) < 5+recordLen {
		return "", false
	}

	// Handshake message starts at offset 5
	idx := 5

	// Handshake type (1 byte) + length (3 bytes)
	idx += 4

	// ClientHello: version (2) + random (32) + session_id_len (1) + session_id
	idx += 34 // version + random
	if idx >= len(data) {
		return "", false
	}
	sessionIDLen := int(data[idx])
	idx += 1 + sessionIDLen
	if idx >= len(data) {
		return "", false
	}

	// Cipher suites
	if idx+2 > len(data) {
		return "", false
	}
	cipherSuitesLen := int(data[idx])<<8 | int(data[idx+1])
	idx += 2 + cipherSuitesLen
	if idx >= len(data) {
		return "", false
	}

	// Compression methods
	if idx+1 > len(data) {
		return "", false
	}
	compressionLen := int(data[idx])
	idx += 1 + compressionLen
	if idx+2 > len(data) {
		return "", false
	}

	// Extensions length
	extensionsLen := int(data[idx])<<8 | int(data[idx+1])
	idx += 2
	if extensionsLen == 0 || idx+extensionsLen > len(data) {
		return "", false
	}

	// Parse extensions
	end := idx + extensionsLen
	for idx+4 <= end {
		extType := int(data[idx])<<8 | int(data[idx+1])
		extLen := int(data[idx+2])<<8 | int(data[idx+3])
		idx += 4
		if idx+extLen > end {
			break
		}
		if extType == 0x0000 { // server_name extension
			if extLen < 2 {
				break
			}
			sniListLen := int(data[idx])<<8 | int(data[idx+1])
			if sniListLen+2 > extLen {
				break
			}
			sniIdx := idx + 2
			if sniIdx+3 > idx+extLen {
				break
			}
			// SNI entry: name_type (1) + name_len (2) + name
			nameType := data[sniIdx]
			if nameType != 0 { // Only hostname type (0) is supported
				break
			}
			nameLen := int(data[sniIdx+1])<<8 | int(data[sniIdx+2])
			sniIdx += 3
			if sniIdx+nameLen > idx+extLen {
				break
			}
			return string(data[sniIdx : sniIdx+nameLen]), true
		}
		idx += extLen
	}

	return "", false
}
