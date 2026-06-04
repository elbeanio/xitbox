package guardian

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Server is the guardian proxy.
type Server struct {
	listenAddr  string
	controlSock string
	logPath     string
	rules       *Rules
	logFile     *os.File
	logMu       sync.Mutex
	listener    net.Listener
	controlLn   net.Listener
	wg          sync.WaitGroup
	stopCh      chan struct{}
}

// NewServer creates a new guardian server.
func NewServer(listenAddr, controlSock, logPath string, rules *Rules) (*Server, error) {
	if rules == nil {
		rules = NewRules(nil, nil)
	}
	return &Server{
		listenAddr:  listenAddr,
		controlSock: controlSock,
		logPath:     logPath,
		rules:       rules,
		stopCh:      make(chan struct{}),
	}, nil
}

// Start begins listening for proxy and control connections.
func (s *Server) Start() error {
	// Open log file
	if s.logPath != "" {
		if err := os.MkdirAll(filepath.Dir(s.logPath), 0755); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
		f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
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

// Stop shuts down the server.
func (s *Server) Stop() error {
	close(s.stopCh)
	if s.listener != nil {
		s.listener.Close()
	}
	if s.controlLn != nil {
		s.controlLn.Close()
	}
	s.wg.Wait()
	if s.logFile != nil {
		s.logFile.Close()
	}
	return nil
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
			// No port, default to 443
			host = req.Host
			portStr = "443"
		}
		destHost = host
		destPort = parsePort(portStr)

		// Send 200 OK back to client
		fmt.Fprintf(clientConn, "HTTP/1.1 200 Connection established\r\n\r\n")
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
		return
	}

	// Connect to destination
	destAddr := fmt.Sprintf("%s:%d", destHost, destPort)
	serverConn, err := net.Dial("tcp", destAddr)
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

	// Bidirectional copy
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(serverConn, clientConn)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(clientConn, serverConn)
		done <- struct{}{}
	}()
	<-done
}

func (s *Server) logDecision(dest, action, reason, detail string) {
	if s.logFile == nil {
		return
	}
	entry := LogEntry{
		Timestamp: time.Now().UTC(),
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
