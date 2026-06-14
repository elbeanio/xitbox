# xb — Implementation Status

> What's built and what's pending.

---

## Built (v0.1)

### CLI
- `xb <command>` — sandbox any command
- `xb --init` — write default config to platform path, print project override instructions
- `xb --allow --domain / --cidr / --from-log` — manage network allowlist
- `xb --logs / --logs --follow / --logs --since` — view audit log
- `xb --list` — list running sandboxes (stale entries auto-pruned via guardian socket liveness check)
- Static binary (`CGO_ENABLED=0`) for Linux relay mode bind-mount

### Guardian proxy
- TLS SNI extraction (no decryption)
- HTTP CONNECT tunnel handling; `403 Forbidden` on deny (not silent drop)
- HTTP Host header fallback for plain HTTP
- Domain glob + CIDR allowlist
- JSONL audit log
- Unix socket control API (`add_allow`, `add_deny`, `list`, `stats`)
- Bidirectional relay with proper goroutine lifecycle
- Upstream proxy chaining with basic auth (`upstream_proxy` config)
- Unix socket proxy listener for Linux relay mode
- Hot reload — watches config files every 2s, swaps rules atomically

### macOS backend (Seatbelt)
- Per-run profile written to temp file, passed to `sandbox-exec -f`, deleted on exit
- **Write isolation**: `(deny file-write*)` with exceptions for cwd, agent config dir, /tmp, macOS temp dirs, and `filesystem.allow_write` paths
- **Read protection**: `(deny file-read*)` rules for credential paths from `filesystem.deny_read`
- File entries in `allow_write` use regex prefix rules (covers atomic temp files like `~/.claude.json.tmp.*`)
- No OS-level network enforcement (Seatbelt limitation — see NETWORKING.md)
- Terminal state save/restore (handles TUI crashes)
- Path canonicalisation via `filepath.EvalSymlinks`

### Linux backend
- **pasta mode**: `bwrap --unshare-user --unshare-net` → pasta provides userspace network → iptables DNAT redirects all TCP to guardian
- **slirp4netns mode**: same pattern, older tool
- **Relay mode**: `bwrap --unshare-net` + xb binary bind-mounted + relay inside sandbox bridges TCP loopback → guardian Unix socket
- Detection and fallback: tries pasta → slirp4netns → relay
- Host home NOT mounted — `~/.ssh`, `~/.aws`, etc. absent from sandbox
- Agent config dirs mapped from `~/.xb/persist/<agent>/`
- `.xb.yaml` bind-mounted read-only

### Config
- Additive merge across scopes (built-ins → user config → project config)
- Slices appended; scalars replaced
- `filesystem.allow_write` — extra write paths (macOS)
- `filesystem.deny_read` — credential read blocklist (macOS)
- `upstream_proxy` — corp HTTP proxy chaining
- `ca_bundle` + `ca_bundle_env_vars` — TLS inspection support
- Telemetry env vars (`DISABLE_TELEMETRY`, `DISABLE_ERROR_REPORTING`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`) injected automatically to prevent TUI hangs

---

## Pending

### Session chrome — interactive allow prompts
Terminal overlay showing blocked connections in real-time with a keypress to allow. Needs a way to inject UI alongside a running TUI agent without interfering with its terminal.

### Apple Container backend (macOS 26+)
Proper VM-level isolation as a Seatbelt complement for machines running macOS Tahoe+. Seatbelt continues working; Apple Container would provide OS-level network enforcement that Seatbelt lacks.

### cgroups resource limits
`resources.memory`, `resources.cpus`, `resources.pids` config fields exist but aren't wired to actual cgroup enforcement on Linux.

### Per-agent config files
`~/.xb/agents/<agent>/config.yaml` for agent-specific network/env overrides.

### Environment presets
Named environments (`XB_ENV=corporate`) for developers who switch between home and corporate networks.

### `xb doctor`
Dependency check and setup verification.

### Session-scoped SSH support
Mount a session-specific SSH key into the sandbox for git push without exposing the host keyring.
