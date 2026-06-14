# xb Networking

How network filtering works in xb.

---

## Overview

Network filtering works differently on each platform due to OS-level constraints.

### Linux (pasta/slirp4netns)

```
sandboxed process (network namespace)
      │
      │  all TCP (iptables DNAT intercepts everything)
      ▼
guardian proxy (via pasta/slirp4netns gateway)
      │
      │  domain allowlist + deny list + audit log
      │  optionally forward through upstream_proxy
      ▼
   internet
```

All TCP is intercepted at the network namespace level regardless of whether the process respects `HTTP_PROXY`. This is the strongest enforcement mode.

### Linux (relay fallback)

```
sandboxed process (--unshare-net: completely isolated)
      │
      │  HTTP_PROXY=http://127.0.0.1:RELAY_PORT
      ▼
relay inside sandbox (127.0.0.1:RELAY_PORT)
      │
      │  Unix socket on shared state dir
      ▼
guardian on host
      │
      ▼
   internet
```

Processes that ignore `HTTP_PROXY` have no network at all — connection refused.

### macOS

```
sandboxed process
      │
      │  HTTP_PROXY=http://127.0.0.1:GUARDIAN_PORT  (proxy-aware agents)
      │  direct connection                            (native macOS networking)
      ▼
guardian proxy (127.0.0.1:PORT)
      │
      │  domain allowlist + deny list + audit log
      │  optionally forward through upstream_proxy
      ▼
   internet
```

Seatbelt cannot restrict outbound connections to specific external hostnames — only `localhost` and `*` are valid host targets in `tcp4` network rules. Restricting outbound to `*:443` and falling back to iptables-style enforcement is not available on macOS without root.

xb relies on `HTTP_PROXY` → guardian for macOS network filtering. Agents that respect the proxy (claude, aider, npm, pip, curl, etc.) are fully filtered. Agents using native macOS networking for specific operations (e.g. Claude Code's OAuth auth flow) can reach the internet directly.

---

## Platform Enforcement

| Platform | Enforcement mechanism | Catches processes that ignore HTTP_PROXY |
|----------|-----------------------|------------------------------------------|
| Linux (pasta/slirp4netns) | iptables DNAT in network namespace | Yes |
| Linux (relay) | Network namespace isolation | Partial — they get connection refused |
| macOS | HTTP_PROXY → guardian | No |

---

## xit-guardian

Guardian is a TCP proxy that runs on `127.0.0.1:<random-port>` for each sandbox. It is the network exit point for all proxy-aware traffic.

### Protocol detection

Guardian reads the first bytes of each connection and determines the protocol:

**HTTP CONNECT tunnel** (HTTPS via proxy)

Most HTTPS traffic from proxy-aware processes arrives as:
```
CONNECT api.github.com:443 HTTP/1.1
Host: api.github.com:443
```
Guardian extracts the hostname from the `Host` header, checks the rules, and either:
- Sends `403 Forbidden` and closes (deny)
- Sends `200 Connection established`, dials the destination, and starts bidirectional relay (allow)

TLS is never terminated — guardian passes raw bytes through. It never sees plaintext.

**Direct TLS (Linux iptables DNAT mode)**

When iptables redirects raw TCP, the first bytes are a TLS ClientHello. Guardian extracts the hostname from the **SNI extension** in the ClientHello without decrypting anything.

**Plain HTTP**

Guardian reads the `Host` header from the HTTP request to get the destination hostname.

### Allow/deny rules

Rules are checked in this order:

1. **Deny list** — checked first, always. Matches here are blocked regardless of the allow list.
2. **Allow list** — if not in the deny list, the destination must match here or it is blocked. Supports exact domains, `*.example.com` globs, and CIDRs.
3. **Default deny** — anything not matched by either list is blocked.

Domain matching is case-insensitive. CIDR rules are checked against the resolved IP only when the destination is an IP address (transparent proxy mode); for hostname-based connections (CONNECT), CIDRs don't apply since guardian doesn't resolve DNS.

### Audit log

Every connection decision is appended to a JSONL file:

```json
{"ts":"2026-06-12T14:23:01Z","dest":"api.openai.com:443","action":"deny","reason":"not-in-allowlist"}
{"ts":"2026-06-12T14:23:05Z","dest":"api.github.com:443","action":"allow","reason":"whitelist"}
```

`xb --logs` and `xb --logs --follow` read this file. `xb --allow --from-log` reads the last denied entry and adds it to the allowlist.

### Control socket

Guardian exposes a Unix socket at `~/.xb/sandboxes/<name>/guardian.sock` for live rule updates without restarting the sandbox. The protocol is newline-delimited JSON:

```json
{"action":"add_allow","value":"api.mycompany.internal"}
{"ok":true}
```

Supported actions: `add_allow`, `add_deny`, `list`, `stats`.

`xb --allow --domain <domain>` uses this socket to add a rule to all running guardians, then writes it to the default config for persistence.

### Hot reload

Guardian watches the default config and project `.xb.yaml` every 2 seconds. If either file changes, it reloads the allow and deny lists atomically. No sandbox restart needed.

---

## What guardian does NOT do

- **No TLS termination or inspection** — guardian never decrypts traffic.
- **No DNS blocking** — domains are matched by hostname at connection time. DNS queries are not intercepted.
- **No UDP filtering** — only TCP connections are proxied. UDP traffic is unfiltered on macOS; blocked by network namespace on Linux.
- **No content inspection** — once a connection is allowed, the bytes flow through unexamined.
