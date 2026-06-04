package guardian

import "testing"

func TestRulesCheck(t *testing.T) {
	r := NewRules([]string{
		"github.com",
		"*.github.com",
		"registry.npmjs.org",
		"140.82.0.0/16",
	}, []string{
		"api.openai.com",
		"*.anthropic.com",
	})

	tests := []struct {
		host   string
		port   int
		want   string
		reason string
	}{
		{"github.com", 443, "allow", "whitelist"},
		{"api.github.com", 443, "allow", "whitelist"},
		{"gist.github.com", 443, "allow", "whitelist"},
		{"registry.npmjs.org", 443, "allow", "whitelist"},
		{"140.82.112.4", 443, "allow", "whitelist"},
		{"api.openai.com", 443, "deny", "blocklist"},
		{"console.anthropic.com", 443, "deny", "blocklist"},
		{"evil.com", 443, "deny", "not-in-allowlist"},
		{"", 443, "deny", "empty-host"},
	}

	for _, tt := range tests {
		action, reason := r.Check(tt.host, tt.port)
		if action != tt.want {
			t.Errorf("Check(%q, %d) action = %q, want %q", tt.host, tt.port, action, tt.want)
		}
		if reason != tt.reason {
			t.Errorf("Check(%q, %d) reason = %q, want %q", tt.host, tt.port, reason, tt.reason)
		}
	}
}

func TestExtractSNI(t *testing.T) {
	// Build a minimal TLS ClientHello with SNI
	// This is a real ClientHello for example.com captured from openssl
	// Minimal valid TLS ClientHello with SNI extension for "example.com"
	clientHello := []byte{
		// Record header: content_type=handshake(0x16), version=TLS1.0(0x0301), length
		0x16, 0x03, 0x01, 0x00, 0x3b,
		// Handshake header: type=client_hello(0x01), length (3 bytes)
		0x01, 0x00, 0x00, 0x37,
		// ClientHello: version TLS 1.2
		0x03, 0x03,
		// random (32 bytes)
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		// session_id length
		0x00,
		// cipher suites: length=2, suites
		0x00, 0x02, 0x00, 0xff,
		// compression methods: length=1, methods
		0x01, 0x00,
		// extensions length = 14
		0x00, 0x0e,
		// Extension: server_name (type=0x0000, len=10)
		0x00, 0x00, 0x00, 0x0a,
		// SNI list len=8
		0x00, 0x08,
		// name_type=hostname(0x00), name len=5
		0x00, 0x00, 0x05,
		// "a.com" (5 bytes)
		'a', '.', 'c', 'o', 'm',
	}

	sni, ok := extractSNI(clientHello)
	if !ok {
		t.Fatal("failed to extract SNI")
	}
	if sni != "a.com" {
		t.Errorf("SNI = %q, want a.com", sni)
	}
}

func TestExtractSNINotTLS(t *testing.T) {
	data := []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	_, ok := extractSNI(data)
	if ok {
		t.Error("expected no SNI from HTTP request")
	}
}
