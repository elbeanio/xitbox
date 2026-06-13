# xb — Implementation Status

> What's built, what's pending.

---

## Built (v0.1)

### CLI
- Binary renamed `xb` (was `xitbox`)
- Dropped cobra; stdlib `flag` dispatch — `xb --allow/--logs/--list` for management, `xb <command>` for sandbox
- No agent-specific subcommands (claude, aider, etc.) — all commands pass through the same sandbox path
- Static binary (`CGO_ENABLED=0`) so it can be bind-mounted inside the bwrap sandbox for relay mode

### Guardian proxy
- TLS SNI extraction (no decryption)
- HTTP CONNECT tunnel handling
- HTTP Host header fallback
- Domain glob + CIDR allowlist
- Built-in LLM provider deny list
- JSONL audit log
- Unix socket control API (`add_allow`, `add_deny`, `list`, `stats`)
- **HTTP CONNECT bug fixed**: 200 was sent before rule check; now sends 403 on deny
- **Bidirectional copy fix**: was only waiting for one goroutine, causing premature close
- Upstream proxy support (`upstream_proxy` config field, basic auth)
- Unix socket proxy listener (`AddProxySock`) for Linux relay mode

### macOS backend (Seatbelt)
- Replaced Lima VM with `sandbox-exec` — instant startup, no external deps
- Per-run Seatbelt profile written to temp file
- Filesystem: deny writes outside cwd + agent config dir + /tmp
- Network: deny all outbound except guardian port + unix sockets
- Agent config dirs written directly on host filesystem (no VM mount needed)
- Terminal state save/restore on exit (handles TUI crashes)
- Path canonicalisation via `filepath.EvalSymlinks` (fixes macOS case sensitivity)

### Linux backend (transparent proxy)
- **pasta mode**: `bwrap --unshare-user --unshare-net` → PID reported to host → `pasta --config-net --netns /proc/<pid>/ns/net` → iptables DNAT inside sandbox → guardian
- **slirp4netns mode**: same pattern as pasta, older tool
- **Relay mode** (zero deps fallback): `bwrap --unshare-net` + xb binary bind-mounted + relay bridges sandbox TCP → guardian Unix socket
- Detection and fallback: tries pasta → slirp4netns → relay
- `bwrap --unshare-user` gives sandbox ownership of its network namespace, enabling rootless iptables

### Config
- `upstream_proxy`: forward allowed traffic through corp HTTP proxy
- `ca_bundle`: path to corp CA cert
- `ca_bundle_env_vars`: configurable list of env vars to inject CA path into (defaults: NODE_EXTRA_CA_CERTS, REQUESTS_CA_BUNDLE, SSL_CERT_FILE, CURL_CA_BUNDLE, GIT_SSL_CAINFO)
- `.xitbox.yaml` project config supported but mounted read-only inside sandbox

### Security
- `.xitbox.yaml` bind-mounted read-only in bwrap (prevents sandbox from modifying its own allow rules)
- HTTP CONNECT 403 response on deny (was silently dropping after 200)

---

## Pending

### Interactive allow prompts (session chrome)
The terminal overlay that shows blocked connections in real-time and lets you press `[a]` to allow. Needs a way to inject UI into the terminal alongside a running TUI agent (complex — needs terminal multiplexing or alternate screen tricks).

### `xb --allow` live-updates running sandbox
Currently `xb --allow` only writes to the config file and says "restart your sandbox". The control socket is wired up in guardian and `SendControl` works, but `cmd/xb/cmd/allow.go` doesn't use it. Small change: detect running sandbox for cwd and send live update before writing config.

### Apple Container backend (macOS 26+)
For machines running macOS 26+, Apple Container provides proper VM-level isolation as a Seatbelt replacement. Seatbelt continues working but Apple Container would be stronger.

### Per-agent config files
`~/.xb/agents/<agent>/config.yaml` for agent-specific network/env overrides without touching the global default.

### Environment presets
Named environments (e.g. `~/.xb/environments/corporate.yaml`) activated via `XB_ENV=corporate` for developers who switch between home and corporate networks.

### `xb --check` / dependency doctor
Clear per-platform dependency check at startup instead of discovering failures at runtime.

### cgroups resource limits
Memory, CPU, PID limits via cgroups v2 on Linux. Config fields exist (`resources.memory`, `resources.cpus`, `resources.pids`) but aren't wired to actual cgroup setup.

### Session-scoped SSH support
Mount a session-specific SSH key into the sandbox for git push etc. (see earlier design discussion).

---

## Removed (was in original plan, deliberately dropped)

### Lima VM backend (macOS)
Replaced by Seatbelt. Lima provided VM-level isolation but required 30-60s first-run setup, 512MB RAM, and a Debian image. Seatbelt is instant and has no external deps. Apple Container is the path to VM-level isolation when needed.

### Agent-specific subcommands (`xitbox claude`, `xitbox aider`, etc.)
Removed — they only existed to manage Lima VM provisioning. With Seatbelt, all commands run through the same `xb <command>` path. Agent config persistence is handled by detecting the command name.

### `xitbox run -- <command>` pattern
Replaced by `xb <command>` directly. The `run --` separator was needed because cobra required a subcommand; without cobra it's unnecessary.

### `xitbox init` command
Removed — Lima VM setup was the main thing init did. Dependencies are checked lazily at sandbox start with informational error messages.

---

## Tech Stack

| Component | Choice |
|-----------|--------|
| Language | Go 1.23+, `CGO_ENABLED=0` for static builds |
| CLI | stdlib `flag` (was cobra, removed) |
| Config | `gopkg.in/yaml.v3` |
| Guardian proxy | Custom Go TCP proxy |
| macOS sandbox | `sandbox-exec` (Seatbelt) |
| Linux sandbox | `bubblewrap` + `pasta`/`slirp4netns`/relay |
| Testing | Go testing |
