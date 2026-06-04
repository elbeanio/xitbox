# xitbox — Architecture Document

> How the pieces fit together.

---

## 1. System Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              HOST                                       │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │                     xitbox CLI (Go)                             │    │
  │  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────┐     │    │
  │  │  │  init    │  │  run     │  │  allow   │  │   logs       │     │    │
  │  │  └──────────┘  └──────────┘  └──────────┘  └──────────────┘     │    │
  │  │                                                                  │    │
  │  │  ┌──────────────────────────────────────────────────────────┐  │    │
  │  │  │              Session Chrome (terminal overlay)             │  │    │
  │  │  │   Shows blocked attempts, accepts [a]llow/[i]gnore keys   │  │    │
  │  │  └──────────────────────────────────────────────────────────┘  │    │
│  │                                                                 │    │
│  │  ┌─────────────────────────────────────────────────────────┐    │    │
│  │  │              Config Manager (YAML)                      │    │    │
│  │  │   Merges ~/.config/xitbox/default.yaml                │    │    │
│  │  │   with ./.xitbox.yaml and CLI flags                   │    │    │
│  │  └─────────────────────────────────────────────────────────┘    │    │
│  └─────────────────────────────────────────────────────────────────┘    │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │              xit-guardian (Go proxy)                          │    │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐ │    │
│  │  │  Whitelist   │  │  Blocklist   │  │  JSONL Audit Log   │ │    │
│  │  │  Engine      │  │  (LLMs)      │  │                    │ │    │
│  │  └──────────────┘  └──────────────┘  └──────────────────────┘ │    │
│  │                                                                 │    │
│  │  Listens on: 127.0.0.1:<random> (per-sandbox)                  │    │
│  │  API socket:  /tmp/xitbox-<name>/guardian.sock                 │    │
│  └─────────────────────────────────────────────────────────────────┘    │
│                                                                         │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │         Platform Backend (Linux native / Lima on macOS)        │    │
│  │                                                                 │    │
│  │   Linux:  bubblewrap + unshare --net + veth + iptables         │    │
│  │   macOS:  limactl exec inside shared Lima VM                   │    │
│  │           (same Linux stack runs inside VM)                    │    │
│  └─────────────────────────────────────────────────────────────────┘    │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Components

### 2.1 xitbox CLI

The user-facing command-line tool. Responsibilities:
- Parse CLI flags and config files
- Coordinate platform backend startup
- Manage sandbox lifecycle (start, list)
- Communicate with `xit-guardian` for dynamic rule updates
- Stream logs to the user

### 2.2 xit-guardian

A TCP proxy that is the **sole network gateway** for each sandbox. Runs on the host (or inside the Lima VM on macOS).

Responsibilities:
- Accept connections from the sandboxed process
- Match destination against whitelist/blocklist
- Forward allowed traffic to the real internet
- Log denied connections to structured JSONL
- Expose a Unix socket API for live rule updates

**Why a proxy instead of iptables-only?**
- iptables can only filter by IP/CIDR, not domain
- A proxy sees the hostname (from HTTP Host header or TLS SNI) before the connection is established
- We can log the *intended* destination, not just the resolved IP

**Proxy modes:**
- **Transparent mode** (Linux): `iptables -t nat` redirects all TCP traffic to the proxy. The sandboxed process doesn't know a proxy exists.
- **Explicit mode** (fallback): Set `HTTP_PROXY`/`HTTPS_PROXY` env vars inside the sandbox to point to the guardian.

### 2.3 Linux Backend

Native Linux sandboxing using:
- **`bubblewrap` (`bwrap`)**: Filesystem isolation via mount namespaces
- **`unshare --net`**: Network namespace isolation
- **`veth` pair + bridge**: Connects sandbox network namespace to host
- **`iptables`**: Transparently redirects all TCP to xit-guardian
- **cgroups v2**: Memory, CPU, and PID limits
- **`pasta` (or `slirp4netns`)**: Optional userspace networking if rootless

### 2.4 macOS Backend

Since macOS lacks Linux namespaces, we run the **entire Linux backend inside a Lima VM**:
- **Lima VM**: Lightweight Alpine Linux VM using Apple Virtualization.framework (vz driver)
- **virtiofs**: Fast file sharing from macOS host into VM
- **Shared VM**: One `xitbox` Lima VM runs continuously. Multiple sandboxes are independent Linux namespaces inside it.
- **Port forwarding**: Guardian proxy ports are forwarded from VM to host via Lima's built-in mechanism

**Why Lima and not Colima?**
- Colima is optimized for Docker daemon + container workloads
- xitbox needs a minimal Linux environment with namespace support, not a container runtime
- A dedicated VM keeps xitbox independent of Colima's lifecycle and version
- The VM can be very small (~512MB RAM, Alpine base)

---

## 3. Network Architecture

### Linux — Transparent Proxy

```
┌─────────────────┐         ┌──────────────────┐         ┌──────────────┐
│  Sandbox (bwrap)│         │  Host Network    │         │  Internet      │
│                 │         │                  │         │              │
│  ┌───────────┐  │  veth │  ┌────────────┐  │         │  github.com  │
│  │ App       │──┼───────┼──┤ iptables   │  │         │  npmjs.org   │
│  │ (agent)   │  │  pair │  │ REDIRECT   │  │         │  ...         │
│  └───────────┘  │         │  to :<guard> │  │         │              │
│                 │         └──────┬───────┘  │         │              │
│  Network NS     │                │          │         │              │
│  (isolated)     │                ▼          │         │              │
└─────────────────┘         ┌──────────────┐   │         └──────────────┘
                            │ xit-guardian │   │
                            │  :<port>     │───┘
                            └──────────────┘
                              │
                              ▼
                    ┌─────────────────────┐
                    │ Whitelist?          │
                    │ Blocklist?          │
                    │ Log to JSONL        │
                    └─────────────────────┘
```

**iptables rules** (set up per-sandbox):
```bash
# Create veth pair: veth-sandbox <-> veth-host
# Move veth-sandbox into sandbox network namespace
# Bridge veth-host to host network

# Redirect all TCP traffic from sandbox to guardian
iptables -t nat -A XITBOX-<name> -p tcp -j REDIRECT --to-port <guardian-port>
```

### macOS — Lima VM + Internal Proxy

```
┌─────────────────────────────────────────────────────────────────────┐
│                          macOS HOST                                 │
│                                                                     │
│  ┌─────────────┐         ┌─────────────────────────┐                │
│  │ xitbox CLI  │────────▶│ Lima VM 'xitbox'        │                │
│  └─────────────┘  limactl│  (Alpine Linux)         │                │
│                          │                         │                │
│                          │  ┌───────────────────┐   │                │
│                          │  │ Linux Backend     │   │                │
│                          │  │ (same as native)  │   │                │
│                          │  │                   │   │                │
│                          │  │  bwrap + unshare  │   │                │
│                          │  │  veth + iptables  │   │                │
│                          │  │  xit-guardian     │   │                │
│                          │  └───────────────────┘   │                │
│                          └─────────────────────────┘                │
│                                                                     │
│  virtiofs mounts:                                                   │
│    /Users/iangeorge/Code/myproject  ──▶ /mnt/project              │
│    ~/.xitbox/persist/claude         ──▶ /home/sandbox/.claude     │
└─────────────────────────────────────────────────────────────────────┘
```

The guardian runs **inside the Lima VM** and forwards traffic through the VM's NAT. Blocked attempts are logged to a file inside the VM, and `xitbox logs` reads it via `limactl copy` or a mounted log directory.

---

## 4. Filesystem Architecture

### Linux — bwrap Mounts

```
Sandbox filesystem:
  /                    (tmpfs, minimal)
  /tmp                 (tmpfs)
  /dev                 (minimal devtmpfs: null, zero, random, urandom, tty)
  /proc                (procfs, hidepid=2)
  /etc                 (minimal: passwd, hosts, resolv.conf)
  /usr                 (read-only from host, or minimal busybox)
  /workspace           (bind mount: project CWD, read-write)
  /home/sandbox        (tmpfs)
  /home/sandbox/.claude ──▶ bind mount from ~/.xitbox/persist/claude/ (rw)
  /home/sandbox/.opencode ──▶ bind mount from ~/.xitbox/persist/opencode/ (rw)
  /home/sandbox/.ssh   (not mounted by default; see bead Code-80f for future SSH support)
  /mnt/shared          (optional: additional user shares)
```

**Key decisions:**
- `/usr` is bind-mounted read-only from host (or a minimal copy) so binaries work
- No access to `/sys` beyond what's absolutely necessary
- No access to host `/home` except explicitly shared paths
- Agent home is a tmpfs with persistent config dirs bind-mounted in

### macOS — virtiofs + bwrap

Same bwrap mount structure, but the source directories are virtiofs mounts from the macOS host into the Lima VM:

```
macOS host path                    Lima VM path
─────────────────────────────────────────────────────────────────
/Users/iangeorge/Code/myproject    /mnt/host/project
/Users/iangeorge/.xitbox/persist   /mnt/host/persist
```

Inside the VM, bwrap bind-mounts from `/mnt/host/...` into the sandbox.

---

## 5. Data Flows

### 5.1 Sandbox Startup

```
1. User runs: xitbox run --name foo -- claude
2. xitbox CLI reads config (default + project + CLI flags)
3. xitbox starts xit-guardian on a random localhost port
4. xit-guardian loads whitelist/blocklist from config
5. xitbox creates network namespace (Linux) or uses VM (macOS)
6. xitbox sets up iptables REDIRECT rules (Linux)
7. xitbox prepares bwrap mount flags:
     - bind CWD to /workspace (rw)
     - bind persist dirs to agent home (rw)
     - bind /usr from host (ro)
     - tmpfs for /tmp, /home/sandbox
8. xitbox execs bwrap with the agent command
9. Agent runs. All TCP traffic transparently flows through guardian.
```

### 5.2 Blocked Connection Attempt

```
1. Agent tries: curl https://api.openai.com/v1/chat/completions
2. TCP SYN leaves sandbox network namespace
3. iptables REDIRECTs to xit-guardian
4. guardian sees destination: api.openai.com:443
5. guardian checks blocklist: MATCH (api.openai.com)
6. guardian logs JSONL:
     {"ts":"...","sandbox":"foo","dest":"api.openai.com:443",
      "action":"deny","reason":"llm-blocklist"}
7. guardian drops connection (TCP RST or close)
8. curl gets connection refused / timeout
```

### 5.3 Dynamic Allow-List Update

```
1. User runs: xitbox allow --domain api.some-registry.io
2. xitbox CLI connects to guardian's Unix socket:
     /tmp/xitbox-<name>/guardian.sock
3. xitbox sends: ADD_RULE { "type": "domain", "value": "api.some-registry.io" }
4. guardian adds to in-memory whitelist
5. Future connections to api.some-registry.io are allowed
6. xitbox updates ~/.config/xitbox/default.yaml for persistence
```

---

## 6. Security Model

### Threat Model

**In scope (accidental misuse / casual attacker):**
- Agent accidentally reads/writes files outside the project
- Agent accidentally exfiltrates data to a public LLM API
- Agent installs a malicious package that tries to phone home
- Agent scans for secrets on the host filesystem

**Out of scope (determined attacker / kernel exploit):**
- Kernel privilege escalation
- Sandbox escape via unpatched CVE
- Spectre/Meltdown-style side channels
- Physical access attacks

### Defense Layers

| Layer | Linux | macOS | What it blocks |
|-------|-------|-------|---------------|
| **Mount namespace** | bwrap | bwrap (in VM) | Host filesystem access outside allowed mounts |
| **Network namespace** | unshare --net | unshare --net (in VM) | Direct host network access |
| **Transparent proxy** | iptables REDIRECT | iptables REDIRECT (in VM) | Unwhitelisted destinations |
| **Blocklist** | Guardian memory | Guardian memory (in VM) | Known LLM provider domains |
| **cgroups** | cgroup v2 | VM resource limits | Resource exhaustion |
| **Minimal dev** | bwrap --dev-bind | bwrap --dev-bind (in VM) | Device access |
| **VM boundary** | N/A | Apple Virtualization.framework | Kernel escapes from VM |

### Why This Is Enough

For coding agents, the primary risks are:
1. **Overt exfiltration** — blocked by network whitelist + LLM blocklist
2. **Accidental damage** — blocked by filesystem isolation
3. **Dependency confusion** — mitigated by audit logging (you see what domains the agent hits)

A determined attacker with a kernel exploit can escape any process-level sandbox. For that threat model, use a hardware VM per sandbox (like agentcage's VM mode). xitbox trades perfect isolation for speed and simplicity.

---

## 7. Multi-Sandbox Support & Session Chrome

Each sandbox is an independent ephemeral instance with:
- **Unique network namespace** (Linux) or unique bwrap invocation inside shared VM (macOS)
- **Unique guardian process** (different port + Unix socket)
- **Unique iptables chain** (Linux) to avoid rule collisions
- **Shared Lima VM** (macOS) — one VM, many sandboxes
- **Session chrome** — a terminal overlay running alongside the agent

### Session Chrome

The chrome is a lightweight terminal UI (using a Go TUI library like `charmbracelet/bubbletea` or a simple ANSI-based overlay) that:
- Displays the last N blocked connection attempts
- Listens for keystrokes (`a` = allow, `i` = ignore, `l` = view log, `q` = quit chrome but keep sandbox)
- Sends allow commands to the guardian's Unix socket API
- Updates the persistent config so the allowance survives future sessions

The chrome runs in the **same terminal** as the agent, using a split-pane or overlay approach. It does not require a separate terminal window.

**Why this matters:** Without the chrome, the user must:
1. Notice the agent is stuck/broken
2. Guess which domain was blocked
3. Open a new terminal
4. Run `xitbox logs` to find the block
5. Run `xitbox allow --domain ...`
6. Restart the agent

With the chrome, it's: press `a`. Done.

```
┌──────────────────────────────────────────────────────────────┐
│                         Linux Host                           │
│                                                              │
│  ┌─────────────┐   ┌─────────────┐   ┌─────────────┐        │
│  │ Sandbox A   │   │ Sandbox B   │   │ Sandbox C   │        │
│  │ (netns A)   │   │ (netns B)   │   │ (netns C)   │        │
│  │ guardian:5001│  │ guardian:5002│  │ guardian:5003│       │
│  └──────┬──────┘   └──────┬──────┘   └──────┬──────┘        │
│         │                 │                 │                │
│         └─────────────────┴─────────────────┘                │
│                           │                                  │
│                    ┌──────┴──────┐                            │
│                    │  Host Net   │                            │
│                    │  (internet) │                            │
│                    └─────────────┘                            │
└──────────────────────────────────────────────────────────────┘
```

On macOS, the same diagram applies but inside the Lima VM.

---

## 8. Inter-Sandbox Communication (Future)

For V2, sandboxes can communicate via:
- **Shared Unix socket directory** — a `volumes` path mounted into all sandboxes that opt-in
- **Host-local TCP** — a special `host.internal` domain resolves to the host gateway
- **Message bus** — lightweight ZeroMQ or Unix socket broker inside the Lima VM

This is intentionally not designed yet. The V1 architecture leaves hooks (Unix socket directories, configurable bridge networks) but doesn't implement them.

---

## 9. Platform Comparison

| Concern | Linux | macOS |
|---------|-------|-------|
| **Startup time** | ~1s (bwrap + unshare) | ~3s (warm Lima VM + bwrap) |
| **Memory overhead** | ~10MB | ~512MB (Lima VM) + ~10MB per sandbox |
| **Disk overhead** | Minimal | ~2GB (Alpine VM image) |
| **Root required** | No (user namespaces) | No (Lima runs as user) |
| **Network isolation** | Native netns | VM netns |
| **Filesystem isolation** | bwrap | bwrap (in VM) |
| **cgroups** | v2 | Via VM limits |
| **Kernel** | Host kernel | Separate VM kernel |

The macOS backend trades some memory overhead for hardware isolation and identical behavior to Linux.

---

## 10. Dependencies

### Build
- Go 1.23+

### Runtime — Linux
- `bubblewrap` (bwrap)
- `iptables` (or `nftables`)
- `unshare` (util-linux)
- `socat` (optional, for Unix socket bridging)
- cgroup v2 mounted

### Runtime — macOS
- `lima` (`brew install lima`)
- Apple Silicon or Intel Mac (Virtualization.framework)

### Optional
- `fswatch` (for file watching passthrough, future)

---

## 11. Key Files

```
xitbox/
├── cmd/
│   ├── xitbox/          # Main CLI entrypoint
│   └── xit-guardian/    # Proxy daemon
├── pkg/
│   ├── backend/
│   │   ├── linux/       # Linux-specific sandbox setup
│   │   └── darwin/      # macOS Lima VM management
│   ├── config/          # YAML config parsing and merging
│   ├── guardian/        # Proxy engine, whitelist, logging
│   ├── sandbox/         # Sandbox lifecycle (start, list)
│   └── fs/              # Mount preparation, persistence mapping
├── init/
│   └── lima/            # Lima VM template YAML
└── docs/
    ├── PRODUCT.md
    ├── ARCHITECTURE.md
    └── IMPLEMENTATION_PLAN.md
```
