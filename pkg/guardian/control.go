package guardian

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
)

// ControlRequest is sent over the Unix socket API.
type ControlRequest struct {
	Action string `json:"action"`         // "add_allow", "add_deny", "list", "stats"
	Type   string `json:"type,omitempty"` // "domain" or "cidr"
	Value  string `json:"value,omitempty"`
}

// ControlResponse is returned from the Unix socket API.
type ControlResponse struct {
	OK     bool        `json:"ok"`
	Error  string      `json:"error,omitempty"`
	Result interface{} `json:"result,omitempty"`
}

func (s *Server) serveControl() {
	defer s.wg.Done()
	for {
		conn, err := s.controlLn.Accept()
		if err != nil {
			select {
			case <-s.stopCh:
				return
			default:
				log.Printf("control accept error: %v", err)
				continue
			}
		}
		s.wg.Add(1)
		go s.handleControl(conn)
	}
}

func (s *Server) handleControl(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var req ControlRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.writeResponse(conn, ControlResponse{OK: false, Error: "invalid json: " + err.Error()})
			continue
		}

		resp := s.processControl(req)
		s.writeResponse(conn, resp)
	}
}

func (s *Server) processControl(req ControlRequest) ControlResponse {
	switch req.Action {
	case "add_allow":
		if req.Value == "" {
			return ControlResponse{OK: false, Error: "missing value"}
		}
		s.rules.AddAllow(req.Value)
		return ControlResponse{OK: true}

	case "add_deny":
		if req.Value == "" {
			return ControlResponse{OK: false, Error: "missing value"}
		}
		s.rules.AddDeny(req.Value)
		return ControlResponse{OK: true}

	case "list":
		rules := s.rules.List()
		return ControlResponse{OK: true, Result: rules}

	case "stats":
		return ControlResponse{OK: true, Result: map[string]interface{}{
			"listen_addr": s.listenAddr,
		}}

	default:
		return ControlResponse{OK: false, Error: fmt.Sprintf("unknown action: %s", req.Action)}
	}
}

func (s *Server) writeResponse(conn net.Conn, resp ControlResponse) {
	data, _ := json.Marshal(resp)
	fmt.Fprintf(conn, "%s\n", data)
}

// SendControl sends a control request to a guardian server via its Unix socket.
func SendControl(sockPath string, req ControlRequest) (*ControlResponse, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial control socket: %w", err)
	}
	defer conn.Close()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(conn, "%s\n", data); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no response from guardian")
	}

	var resp ControlResponse
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &resp, nil
}
