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
│  │  --init  --allow  --logs  --list  xb <command>       │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  xit-guardian (per-sandbox proxy)                    │   │
│  │  ├─ Domain allowlist + deny list                     │   │
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
│  │  macOS:  Seatbelt (sandbox-exec) — filesystem only  │   │
│  │  Linux:  bwrap + network namespace                   │   │
│  │          (pasta / slirp4netns / relay fallback)      │   │
│  └──────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

---

## 2. Components

### 2.1 xb CLI (`cmd/xb/`)

Entry point. Parses args with stdlib `flag` (no cobra). The first non-flag argument is treated as the command to sandbox; management operations use `--init`, `--allow`, `--logs`, `--list` flags.

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

Uses **Seatbelt** (`sandbox-exec`) for filesystem isolation. Network filtering is via `HTTP_PROXY` → guardian.

The per-run Seatbelt profile:

```scheme
(version 1)
(allow default)

; Filesystem writes: deny outside safe dirs
(deny file-write*)
(allow file-write* (subpath "/path/to/cwd"))
(allow file-write* (subpath "/path/to/agent-config"))  ; e.g. ~/.claude
(allow file-write* (subpath "/tmp"))
(allow file-write* (subpath "/private/tmp"))
(allow file-write* (regex #"^/private/var/folders/"))
; Extra paths from filesystem.allow_write config
(allow file-write* (regex #"^/Users/x/\.claude\.json"))  ; covers atomic temp files

; Filesystem reads: deny credential files
; (more specific rules override the (allow default) above)
(deny file-read* (subpath "/Users/x/.ssh"))
(deny file-read* (literal "/Users/x/.aws/credentials"))
(deny file-read* (subpath "/Users/x/.gnupg"))
; ... etc from filesystem.deny_read config

; Network: no OS-level deny — see §3 for rationale
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

Seatbelt cannot restrict outbound connections to specific external hostnames — only `localhost` and `*` (all) are valid host targets in Seatbelt's `tcp4` network rules. Attempting to deny all outbound and re-allow specific external ports breaks TUI agents that use native macOS networking (which ignores `HTTP_PROXY`).

xb therefore relies entirely on `HTTP_PROXY` → guardian for network filtering on macOS:

```
sandboxed process
  │
  │  HTTP_PROXY=http://127.0.0.1:PORT  (proxy-aware agents)
  │  direct connection                  (native macOS networking)
  ▼
guardian on host (127.0.0.1:PORT)
  │
  │  check allowlist/deny list
  │  optionally forward through upstream_proxy
  ▼
internet (or corp proxy)
```

Agents that respect `HTTP_PROXY` (claude, aider, npm, pip, curl, etc.) are fully filtered and logged. Agents using native macOS APIs for specific operations (e.g. Claude Code's OAuth auth flow) can reach external hosts directly.

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

All TCP is intercepted regardless of whether the process uses `HTTP_PROXY`.

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

The Seatbelt profile:
- Starts with `(allow default)` — everything readable
- Denies writes outside: cwd, agent config dir, `/tmp`, macOS temp dirs, and paths in `filesystem.allow_write`
- Denies reads of credential files listed in `filesystem.deny_read` — these rules are more specific than `(allow default)` so they take effect

No path remapping. The agent writes directly to real host paths.

**What the sandboxed process can read:** everything on the host filesystem, except paths in `deny_read` (default: `~/.ssh`, `~/.aws/credentials`, `~/.gnupg`, `~/.netrc`, `~/.bash_history`, `~/.zsh_history`, `~/.azure`, `~/.config/gcloud`, `~/.docker/config.json`).

**What the sandboxed process can write:** cwd, `/tmp`, macOS temp dirs, agent config dir (e.g. `~/.claude`), and any paths in `filesystem.allow_write`.

### Linux — bwrap mounts

```
Sandbox filesystem:
  /workspace      → bind mount: project cwd (rw)
  /tmp            → tmpfs
  /home/sandbox   → tmpfs  ← host home NOT mounted; credentials simply absent
  /dev            → minimal (null, zero, random, urandom, tty)
  /proc           → procfs
  /usr /bin /etc  → bind from host (ro)
  /xb-bin         → xb binary (ro) — relay mode only
  /xb-state       → state dir (rw) — relay mode only
```

The host home directory is **never mounted**. `~/.ssh`, `~/.aws`, `~/.gnupg` and similar don't exist inside the sandbox at all. Agent config dirs (e.g. `~/.claude`) are mapped from `~/.xb/persist/<agent>/` into `/home/sandbox/.<agent>/`.

The `deny_read` config has no effect on Linux — those paths simply aren't present.

### Project config tamper protection

`.xb.yaml` in the project directory is bind-mounted **read-only** inside the sandbox, even though the rest of cwd is read-write. This prevents the sandboxed process from modifying allow rules for future sandbox runs.

---

## 5. Data Flows

### Sandbox startup

```
1. xb reads config (default + .xb.yaml merged additively)
2. xb starts guardian on 127.0.0.1:PORT with rules from merged config
3. macOS: write Seatbelt profile to temp file → sandbox-exec -f profile command
   Linux: detect pasta/slirp4netns → set up network namespace → bwrap command
4. Sandbox runs. Proxy-aware traffic flows through guardian.
5. On exit: guardian stopped, state dir removed.
```

### Blocked connection (proxy-aware agent, both platforms)

```
1. Process sends CONNECT api.openai.com:443 to HTTP_PROXY (guardian)
2. guardian checks: api.openai.com not in allowlist → DENY
3. guardian sends 403 Forbidden
4. guardian writes JSONL: {"ts":"...","dest":"api.openai.com:443","action":"deny","reason":"not-in-allowlist"}
5. Process gets 403
```

### Blocked connection (iptables DNAT, Linux pasta/slirp mode only)

```
1. Process tries to connect to api.openai.com:443 directly (ignores HTTP_PROXY)
2. iptables DNAT redirects to guardian
3. guardian reads SNI from TLS ClientHello → api.openai.com → DENY
4. guardian sends 403, logs denial
5. Process gets connection refused / 403
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

- Agent writes files outside the project directory
- Agent reads credential files (`~/.ssh`, `~/.aws/credentials`, etc.) — macOS: blocked by `deny_read` rules; Linux: not mounted
- Agent exfiltrates data to a public domain via `HTTP_PROXY`-aware calls — blocked by guardian
- Sandboxed process modifies `.xb.yaml` to broaden future sessions — blocked by read-only bind

### Threat model (out of scope)

- Kernel privilege escalation or CVE-based sandbox escape
- Exfiltration via native macOS networking that bypasses `HTTP_PROXY` (Seatbelt limitation — see §3)
- Malicious `.xb.yaml` committed to the repo (supply chain — visible in code review)

### Defense layers

| Layer | Linux | macOS |
|-------|-------|-------|
| **Write isolation** | bwrap bind mounts — only mounted paths writable | Seatbelt `(deny file-write*)` with per-path exceptions |
| **Credential read protection** | Host home not mounted; credentials absent | Seatbelt `(deny file-read*)` on credential paths |
| **Network filtering** | iptables DNAT → guardian (all processes, pasta/slirp) or relay (HTTP_PROXY only) | `HTTP_PROXY` → guardian (proxy-aware processes only) |
| **Audit log** | guardian JSONL | guardian JSONL |
| **Project config tamper** | `.xb.yaml` ro bind | `.xb.yaml` ro bind |

---

## 7. Multi-Sandbox Support

Multiple sandboxes can run simultaneously. Each gets:
- Its own guardian instance (different port + Unix sockets)
- Its own network namespace (Linux) or sandbox-exec process (macOS)
- Its own state directory (`~/.xb/sandboxes/<name>/`)

Liveness is determined by whether the guardian control socket is accepting connections — not by PID, which can be reused by other processes. Stale state directories are pruned automatically on `xb --list`.

---

## 8. Config Loading

Config is loaded and **merged additively** across scopes:

1. Built-in defaults (hardcoded in `DefaultConfig()`)
2. User config (`~/Library/Application Support/xb/default.yaml` or `~/.config/xb/default.yaml`)
3. Project config (`.xb.yaml` in cwd)

Slice fields (`network.allow`, `network.deny_list`, `filesystem.allow_write`, `filesystem.deny_read`, `env.allow`, etc.) are **appended** — each scope adds to the previous. Scalar fields (`default_policy`, `cwd`, etc.) are replaced by the most specific scope that sets them.

This means project configs only need to list their extras, not replicate the full default.

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
│   │       ├── root.go      # Flag dispatch
│   │       ├── init.go      # --init: write default config
│   │       ├── sandbox.go   # runSandbox()
│   │       ├── allow.go     # --allow implementation
│   │       ├── logs.go      # --logs implementation
│   │       └── list.go      # --list implementation
│   └── xit-guardian/
│       └── main.go          # Standalone guardian daemon
├── pkg/
│   ├── config/config.go     # Config types, defaults, additive YAML merge
│   ├── guardian/
│   │   ├── proxy.go         # TCP proxy, SNI extraction, upstream proxy dial
│   │   ├── rules.go         # Allowlist/deny list engine
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
    └── CONFIG.md        # Config reference
```
