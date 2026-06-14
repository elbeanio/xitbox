package guardian

import (
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
)

// Rules manages whitelist and blocklist entries.
type Rules struct {
	mu        sync.RWMutex
	allowList []Rule
	denyList  []Rule
}

// Rule is a single allow/deny pattern.
type Rule struct {
	Type  string // "domain", "cidr"
	Value string
	Port  int        // 0 = any port; non-zero = exact port match only
	Net   *net.IPNet // parsed CIDR
}

// NewRules creates a new Rules engine from string lists.
func NewRules(allow, deny []string) *Rules {
	r := &Rules{}
	for _, v := range allow {
		r.addRule(&r.allowList, v)
	}
	for _, v := range deny {
		r.addRule(&r.denyList, v)
	}
	return r
}

// AddAllow adds a domain or CIDR to the allow list.
func (r *Rules) AddAllow(value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addRule(&r.allowList, value)
}

// AddDeny adds a domain or CIDR to the deny list.
func (r *Rules) AddDeny(value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addRule(&r.denyList, value)
}

// Replace atomically replaces all rules. Used for live config reload.
func (r *Rules) Replace(allow, deny []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowList = nil
	r.denyList = nil
	for _, v := range allow {
		r.addRule(&r.allowList, v)
	}
	for _, v := range deny {
		r.addRule(&r.denyList, v)
	}
}

func (r *Rules) addRule(list *[]Rule, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}

	// Check if it's a CIDR (contains /)
	if strings.Contains(value, "/") {
		_, ipnet, err := net.ParseCIDR(value)
		if err == nil {
			*list = append(*list, Rule{Type: "cidr", Value: value, Net: ipnet})
			return
		}
	}

	// Check for optional port suffix: "domain:port"
	// Use LastIndex so IPv6 literals (if ever used without CIDR) aren't split badly.
	var port int
	domain := strings.ToLower(value)
	if idx := strings.LastIndex(value, ":"); idx > 0 {
		if p, err := strconv.Atoi(value[idx+1:]); err == nil && p > 0 && p <= 65535 {
			port = p
			domain = strings.ToLower(value[:idx])
		}
	}

	*list = append(*list, Rule{Type: "domain", Value: domain, Port: port})
}

// Check evaluates a destination against the rules.
// Returns ("allow", "") or ("deny", reason).
func (r *Rules) Check(host string, port int) (string, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "deny", "empty-host"
	}

	// First check deny list
	for _, rule := range r.denyList {
		if r.matches(rule, host, port) {
			return "deny", "blocklist"
		}
	}

	// Then check allow list
	for _, rule := range r.allowList {
		if r.matches(rule, host, port) {
			return "allow", "whitelist"
		}
	}

	return "deny", "not-in-allowlist"
}

func (r *Rules) matches(rule Rule, host string, port int) bool {
	if rule.Type == "cidr" {
		ip := net.ParseIP(host)
		if ip != nil && rule.Net != nil {
			return rule.Net.Contains(ip)
		}
		return false
	}

	// Port check: if the rule specifies a port, the request must match it.
	if rule.Port != 0 && rule.Port != port {
		return false
	}

	// Domain matching with glob support
	pattern := rule.Value
	if strings.HasPrefix(pattern, "*.") {
		// *.example.com matches example.com and sub.example.com
		suffix := strings.TrimPrefix(pattern, "*.")
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	} else if pattern == host {
		return true
	} else if strings.HasPrefix(pattern, ".") {
		// .example.com matches sub.example.com but not example.com
		if strings.HasSuffix(host, pattern) {
			return true
		}
	}

	// Path.Match supports limited globs; for full glob we use custom logic above
	if matched, _ := path.Match(pattern, host); matched {
		return true
	}

	return false
}

// List returns current rules for inspection.
func (r *Rules) List() struct {
	Allow []Rule
	Deny  []Rule
} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return struct {
		Allow []Rule
		Deny  []Rule
	}{
		Allow: append([]Rule(nil), r.allowList...),
		Deny:  append([]Rule(nil), r.denyList...),
	}
}
