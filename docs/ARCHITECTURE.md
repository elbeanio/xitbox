# xb — Architecture

> How the pieces fit together.

---

## 1. System Overview

```
┌──────────────────────────────────────────────────────────────┐
│  HOST                                                        │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  xb CLI                                              │   │
│  │  stdlib flag dispatch — management flags or sandbox  │   │
│  │                                                      │   │
│  │  --allow  --logs  --list   xb <command> [args...]    │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  xit-guardian (per-sandbox proxy)                    │   │
│  │  ├─ Allowlist + LLM blocklist                        │   │
│  │  ├─ TLS SNI extraction (no decryption)               │   │
│  │  ├─ HTTP CONNECT handling                            │   │
│  │  ├─ Optional upstream proxy (corp proxy chaining)    │   │
│  │  ├─ JSONL audit log                                  │   │
│  │  └─ Unix socket control API (live rule updates)      │   │
│  │                                                      │   │
│  │  TCP listener:   127.0.0.1:<random-port>             │   │
│  │  Control socket: ~/.xb/sandboxes/<name>/guardian.sock│   │
│  │  Proxy socket:   ~/.xb/sandboxes/<name>/guardian-    │   │
│  │                  proxy.sock  (Linux relay mode only) │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  Platform backend                                    │   │
│  │                                                      │   │
│  │  macOS:  Seatbelt (sandbox-exec) — instant startup   │   │
│  │  Linux:  bwrap + network namespace                   │   │
│  │          (pasta / slirp4netns / relay fallback)      │   │
│  └──────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

---

## 2. Components

### 2.1 xb CLI (`cmd/xb/`)

Entry point. Parses args with stdlib `flag` (no cobra). The first non-flag argument is treated as the command to sandbox; management operations use `--allow`, `--logs`, `--list` flags.

Also contains the internal relay binary (`--xb-internal-relay PORT SOCKET`) used in Linux relay mode — see §4.3.

### 2.2 xit-guardian (`cmd/xit-guardian/`, `pkg/guardian/`)

A TCP proxy that is the **sole network exit point** for each sandbox. Key properties:

- Inspects destinations by hostname (HTTP CONNECT tunnel, TLS SNI, or HTTP Host header) — never decrypts
- Checks deny list first, then allowlist; default deny
- Logs every decision to JSONL
- Accepts live rule updates via a Unix socket control API
- Optionally chains through an upstream proxy (for corp environments)

See `docs/NETWORKING.md` for full protocol detail.

### 2.3 macOS backend (`pkg/sandbox/sandbox.go`)

Uses **Seatbelt** (`sandbox-exec`) with a per-run profile written to a temp file:

```scheme
(version 1)
(allow default)
(deny file-write*)
(allow file-write* (subpath "/path/to/cwd"))
(allow file-write* (subpath "/path/to/agent-config"))  ; e.g. ~/.claude
(allow file-write* (subpath "/tmp"))
(allow file-write* (subpath "/private/tmp"))
(allow file-write* (regex #"^/private/var/folders/"))
(deny network-outbound)
(allow network-outbound (remote tcp4 "localhost:GUARDIAN_PORT"))
(allow network-outbound (remote unix-socket))   ; macOS system IPC + DNS
```

No external dependencies. Instant startup. `sandbox-exec` is built into macOS.

### 2.4 Linux backend (`pkg/sandbox/sandbox_linux.go`)

Three modes, tried in order:

**pasta mode** (preferred):
1. bwrap starts with `--unshare-user --uid 0 --gid 0 --unshare-net` — sandbox owns its network namespace
2. Sandbox writes its PID to the shared state dir, waits for a ready signal
3. Host runs `pasta --config-net --netns /proc/<pid>/ns/net`
4. Host signals ready; sandbox brings up the tap interface
5. iptables DNAT inside the sandbox redirects all TCP to guardian via pasta gateway (`10.0.2.2`)
6. `HTTP_PROXY` also set as belt-and-suspenders for well-behaved tools

**slirp4netns mode** (fallback if pasta not found): same pattern with `slirp4netns --configure`.

**Relay mode** (zero external deps fallback):
- `bwrap --unshare-net` — completely isolated network namespace, no internet
- xb binary bind-mounted as `/xb-bin`; state dir bind-mounted as `/xb-state`
- Relay (`/xb-bin --xb-internal-relay PORT /xb-state/guardian-proxy.sock`) starts as a background process inside the sandbox
- Relay bridges sandbox TCP loopback → guardian's Unix socket on the shared state dir
- Processes that respect `HTTP_PROXY` are filtered by guardian
- Processes that ignore it (JVMs, raw sockets) get connection refused

---

## 3. Network Architecture

### macOS

```
sandboxed process
  │
  │  (Seatbelt blocks all outbound except localhost:GUARDIAN_PORT)
  ▼
guardian on host (127.0.0.1:PORT)
  │
  │  check allowlist/blocklist
  │  optionally forward through upstream_proxy
  ▼
internet (or corp proxy)
```

### Linux — pasta/slirp4netns mode

```
sandboxed process (network namespace)
  │
  │  (iptables DNAT: all TCP → pasta gateway:GUARDIAN_PORT)
  ▼
pasta/slirp4netns gateway (10.0.2.2:GUARDIAN_PORT)
  │
  │  (pasta routes to host loopback)
  ▼
guardian on host (127.0.0.1:PORT)
  │
  ▼
internet
```

### Linux — relay mode

```
sandboxed process (--unshare-net: isolated loopback, no internet)
  │
  │  HTTP_PROXY=http://127.0.0.1:RELAY_PORT
  ▼
relay (inside sandbox, 127.0.0.1:RELAY_PORT)
  │
  │  (filesystem Unix socket in shared state dir)
  ▼
guardian Unix socket (~/.xb/sandboxes/<name>/guardian-proxy.sock)
  │
  ▼
internet
```

### Corporate proxy chaining

When `upstream_proxy` is configured, guardian routes allowed traffic through it using HTTP CONNECT:

```
sandboxed process → guardian → upstream_proxy → internet
```

Basic auth is supported: `http://user:pass@proxy.corp.internal:8080`.

---

## 4. Filesystem Architecture

### macOS — Seatbelt

The Seatbelt profile grants:
- Read access to everything on the host (tools need their system deps)
- Write access only to: cwd, agent config dir (e.g. `~/.claude`), `/tmp`, macOS temp dirs
- Network outbound only to guardian port

No path remapping. The agent writes directly to its real config dir on the host, which persists naturally across runs.

### Linux — bwrap mounts

```
Sandbox filesystem:
  /workspace      → bind mount: project cwd (rw)
  /tmp            → tmpfs
  /home/sandbox   → tmpfs
  /dev            → minimal (null, zero, random, urandom, tty)
  /proc           → procfs
  /usr /bin /etc  → bind from host (ro)
  /xb-bin         → xb binary (ro) — relay mode only
  /xb-state       → state dir (rw) — relay mode only
```

Agent config directories (`~/.claude`, `~/.gemini`, etc.) are accessible at their real host paths — bwrap maps the host home directory. The agent writes directly to its config dir.

### Project config read-only protection

`.xitbox.yaml` in the project directory is bind-mounted **read-only** inside the sandbox, even though the rest of cwd is read-write. This prevents the sandboxed process from modifying allow rules for future sandbox runs.

---

## 5. Data Flows

### Sandbox startup

```
1. xb reads config (default + .xitbox.yaml merged)
2. xb starts guardian on 127.0.0.1:PORT with rules from merged config
3. macOS: write Seatbelt profile → sandbox-exec -f profile command
   Linux: detect pasta/slirp4netns → set up network namespace → bwrap command
4. Sandbox runs. All traffic flows through guardian.
5. On exit: guardian stopped, state dir removed.
```

### Blocked connection

```
1. Process tries to connect to api.openai.com:443
2. OS enforcement intercepts (Seatbelt / iptables DNAT / relay)
3. guardian receives connection request
4. guardian checks: api.openai.com matches deny list → DENY
5. For CONNECT requests: guardian sends 403 Forbidden
6. guardian writes JSONL: {"ts":"...","dest":"api.openai.com:443","action":"deny","reason":"blocklist"}
7. Process gets connection refused / 403
```

### Live allow rule update

```
1. xb --allow --domain api.mycompany.internal
2. xb connects to ~/.xb/sandboxes/<name>/guardian.sock
3. xb sends: {"action":"add_allow","value":"api.mycompany.internal"}
4. guardian adds to in-memory allowlist
5. xb writes the rule to the default config file for persistence
```

---

## 6. Security Model

### Threat model (in scope)

- Agent reads files outside the project directory
- Agent exfiltrates data to a public LLM API
- Agent installs a malicious package that phones home
- Sandboxed process modifies `.xitbox.yaml` to broaden future sessions

### Threat model (out of scope)

- Kernel privilege escalation or CVE-based sandbox escape
- Side-channel attacks
- Malicious `.xitbox.yaml` committed to the repo (supply chain — visible in code review, not preventable technically)

### Defense layers

| Layer | Linux | macOS |
|-------|-------|-------|
| Filesystem isolation | bwrap bind mounts | Seatbelt `(deny file-write*)` |
| Network isolation | network namespace | Seatbelt `(deny network-outbound)` |
| Traffic filtering | iptables DNAT → guardian | Seatbelt → guardian |
| LLM blocklist | guardian deny list | guardian deny list |
| Project config protection | `.xitbox.yaml` ro bind | `.xitbox.yaml` ro bind |

---

## 7. Multi-Sandbox Support

Multiple sandboxes can run simultaneously. Each gets:
- Its own guardian instance (different port + Unix sockets)
- Its own network namespace (Linux) or sandbox-exec process (macOS)
- Its own state directory (`~/.xb/sandboxes/<name>/`)

There is no shared state between running sandboxes. Cleanup is automatic on exit.

---

## 8. Platform Comparison

| Concern | Linux | macOS |
|---------|-------|-------|
| **Startup time** | <1s | <1s |
| **Root required** | No | No |
| **Filesystem isolation** | bwrap | Seatbelt |
| **Network enforcement** | Network namespace (pasta/slirp/relay) | Seatbelt |
| **Transparent proxy** | iptables DNAT (with pasta/slirp4netns) | Seatbelt enforces gateway |
| **External deps** | bwrap + pasta (recommended) | None |

---

## 9. Dependencies

### Build
- Go 1.23+ (`CGO_ENABLED=0` for static xb binary)

### Runtime — macOS
- Nothing. `sandbox-exec` is built into macOS.

### Runtime — Linux
- `bubblewrap` (required): `sudo apt install bubblewrap`
- `pasta` (recommended): `sudo apt install passt`
- `slirp4netns` (alternative): `sudo apt install slirp4netns`
- Without pasta/slirp4netns: relay mode (no iptables enforcement)

---

## 10. Key Files

```
xb/
├── cmd/
│   ├── xb/
│   │   ├── main.go          # Entry point + internal relay mode
│   │   └── cmd/
│   │       ├── root.go      # Flag dispatch (allow/logs/list vs sandbox)
│   │       ├── sandbox.go   # runSandbox()
│   │       ├── allow.go     # --allow implementation
│   │       ├── logs.go      # --logs implementation
│   │       └── list.go      # --list implementation
│   └── xit-guardian/
│       └── main.go          # Standalone guardian daemon
├── pkg/
│   ├── config/config.go     # Config types, defaults, YAML loader
│   ├── guardian/
│   │   ├── proxy.go         # TCP proxy, SNI extraction, upstream proxy dial
│   │   ├── rules.go         # Allowlist/blocklist engine
│   │   └── control.go       # Unix socket control API
│   ├── sandbox/
│   │   ├── sandbox.go       # Start(), Darwin backend (Seatbelt)
│   │   ├── sandbox_linux.go # Linux backend (pasta/slirp/relay)
│   │   └── sandbox_notlinux.go  # Stub for macOS builds
│   ├── fs/mounts.go         # bwrap mount preparation
│   └── platform/platform.go # OS detection, dep checking
└── docs/
    ├── ARCHITECTURE.md  (this file)
    ├── NETWORKING.md    # Guardian protocol detail
    ├── PRODUCT.md       # Product strategy
    └── CONFIG.md        # Config model design notes
```
