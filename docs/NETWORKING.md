# xb Networking

How network filtering works in xb.

---

## Overview

Network filtering is a two-layer stack:

```
sandboxed process
      │
      │  (all outbound TCP)
      ▼
 OS enforcement          ← Seatbelt (macOS) / network namespace (Linux, TODO)
      │
      │  (only localhost:GUARDIAN_PORT allowed through)
      ▼
 xit-guardian proxy      ← domain allowlist + LLM blocklist + audit log
      │
      │  (allowed destinations only)
      ▼
   internet
```

**Layer 1 — OS enforcement** ensures the sandboxed process can only open TCP connections to the guardian port. It cannot reach the internet directly, even if it ignores `HTTP_PROXY` env vars or uses raw sockets.

**Layer 2 — Guardian** inspects each connection by hostname (not IP), applies the allow/deny rules, logs the decision, and either forwards the traffic or sends a `403 Forbidden`.

`HTTP_PROXY` and `HTTPS_PROXY` are set in the sandbox env pointing at guardian, so well-behaved tools route through it automatically. The OS enforcement is the backstop for tools that don't.

---

## Platform Status

| Platform | OS enforcement | Status |
|----------|---------------|--------|
| macOS    | Seatbelt `(deny network-outbound)` + exception for guardian port | ✅ Implemented |
| Linux    | iptables REDIRECT (network namespace) | 🔲 TODO — currently env-var proxy only |

On Linux right now, OS enforcement is not in place. `HTTP_PROXY`/`HTTPS_PROXY` are set, but a process that ignores them can make direct outbound connections. The iptables transparent proxy is the next item on the roadmap.

---

## xit-guardian

Guardian is a TCP proxy that runs on `127.0.0.1:<random-port>` for each sandbox. It is the sole exit point for all outbound traffic.

### Protocol detection

Guardian reads the first bytes of each connection and determines the protocol:

**HTTPS (HTTP CONNECT tunnel)**
Most HTTPS traffic arrives as an HTTP CONNECT request:
```
CONNECT api.github.com:443 HTTP/1.1
Host: api.github.com:443
```
Guardian extracts the hostname from the `Host` header, checks it against the rules, and either:
- Sends `403 Forbidden` and closes (deny)
- Sends `200 Connection established`, dials the destination, and starts bidirectional copy (allow)

The TLS is never terminated — guardian passes the raw bytes through. It never sees the plaintext.

**HTTPS (direct TLS — transparent proxy mode)**
When iptables redirects raw TCP (Linux, future), the first bytes are a TLS ClientHello. Guardian extracts the hostname from the **SNI extension** in the ClientHello handshake, without decrypting anything. This is a read-only parse of the unencrypted handshake header.

**HTTP (plain)**
Guardian reads the `Host` header from the request to get the destination hostname.

### Allow/deny rules

Rules are checked in this order:

1. **Deny list** — checked first, always. Matches here are blocked regardless of the allow list. The built-in deny list covers public LLM provider APIs (OpenAI, Anthropic, Google, Cohere, Mistral, etc.).
2. **Allow list** — if not in the deny list, the destination must match something here or it is blocked. Supports exact domains, `*.example.com` globs, and CIDRs.
3. **Default deny** — anything not matched by either list is blocked.

Domain matching is case-insensitive. CIDR rules are checked against the resolved IP only when the connection destination is already an IP (e.g. transparent proxy mode); for hostname-based connections (CONNECT), CIDR rules don't apply since guardian doesn't resolve DNS.

### Audit log

Every connection decision is appended to a JSONL file:

```json
{"ts":"2026-06-12T14:23:01Z","dest":"api.openai.com:443","action":"deny","reason":"blocklist"}
{"ts":"2026-06-12T14:23:05Z","dest":"api.github.com:443","action":"allow","reason":"whitelist"}
{"ts":"2026-06-12T14:23:09Z","dest":"sketchy-registry.io:443","action":"deny","reason":"not-in-allowlist"}
```

`xb --logs` and `xb --logs --follow` read this file. `xb --allow --from-log` reads the last denied entry and adds it to the allowlist.

### Control socket

Guardian exposes a Unix socket at `~/.xb/sandboxes/<name>/guardian.sock` for live rule updates without restarting the sandbox. The protocol is newline-delimited JSON:

```json
// request
{"action":"add_allow","value":"api.mycompany.internal"}

// response
{"ok":true}
```

Supported actions: `add_allow`, `add_deny`, `list`, `stats`.

`xb --allow --domain <domain>` uses this socket to add a rule to the running guardian, then also writes it to the default config file for persistence across future sandboxes.

---

## Seatbelt profile (macOS)

The Seatbelt profile written for each sandbox run looks like this:

```scheme
(version 1)
(allow default)                            ; allow everything by default...

; Filesystem: deny writes outside safe dirs
(deny file-write*)
(allow file-write* (subpath "/path/to/cwd"))
(allow file-write* (subpath "/path/to/agent-config"))   ; e.g. ~/.claude
(allow file-write* (subpath "/tmp"))
(allow file-write* (subpath "/private/tmp"))
(allow file-write* (regex #"^/private/var/folders/"))

; Network: deny all outbound except to guardian
(deny network-outbound)
(allow network-outbound (remote tcp4 "localhost:PORT"))
(allow network-outbound (remote unix-socket))           ; system IPC + DNS resolver
```

The profile is written to a temp file, passed to `sandbox-exec -f`, and deleted when the sandbox exits.

The `(allow network-outbound (remote unix-socket))` exception covers macOS system IPC including the DNS resolver (mDNSResponder), which communicates via unix domain socket rather than raw UDP.

---

## What guardian does NOT do

- **No TLS termination or inspection** — guardian never decrypts traffic. It sees the SNI hostname from the unencrypted handshake but not the request content.
- **No DNS blocking** — domains are matched by hostname at connection time. Guardian does not intercept or filter DNS queries.
- **No UDP filtering** — only TCP connections are proxied. UDP traffic (including direct DNS) is blocked by Seatbelt on macOS (unix socket exception covers system resolver) and will be blocked by network namespaces on Linux.
- **No content inspection** — once a connection is allowed, the bytes flow through unexamined.
