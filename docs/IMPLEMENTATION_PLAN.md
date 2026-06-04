# xitbox — Implementation Plan

> Phased build from zero to working sandbox.

---

## Tech Stack

| Component | Choice | Rationale |
|-----------|--------|-----------|
| Language | Go 1.23+ | Single binary, cross-compile, great stdlib for networking |
| CLI framework | `spf13/cobra` | Mature, POSIX-compliant, shell completion |
| Session chrome | `charmbracelet/bubbletea` or custom ANSI overlay | Lightweight TUI for real-time blocked call display and interactive allow |
| Config | YAML via `gopkg.in/yaml.v3` | Human-readable, merges easily |
| Guardian proxy | Custom Go TCP proxy | We need SNI extraction + dynamic rules; existing proxies are overkill |
| Linux sandbox | `bubblewrap` + `unshare` | Battle-tested, no root needed |
| macOS sandbox | Lima VM (Alpine) | Same Linux stack inside, Apple Virtualization.framework |
| Testing | Go testing + `testscript` | Unit + integration via scriptable CLI tests |

---

## Phase 1: Foundation (Week 1)

### Milestone: `xitbox init` works on both platforms.

| Task | Description | Acceptance |
|------|-------------|------------|
| 1.1 | Scaffold Go module and directory structure | `go build ./...` succeeds |
| 1.2 | Implement config types and loader | Reads `~/.config/xitbox/default.yaml`, merges `.xitbox.yaml` |
| 1.3 | Implement `xitbox init` command | Scaffolds default config, detects agents, verifies deps |
| 1.4 | Platform detection abstraction | `pkg/platform` returns `linux` or `darwin`, checks deps |
| 1.5 | Dependency checking | Linux: verify bwrap, iptables, unshare. macOS: verify lima installed |

**Deliverable:** `xitbox init` creates config and tells the user what's ready / missing.

---

## Phase 2: xit-guardian (Week 1–2)

### Milestone: Proxy runs, blocks, allows, and logs.

| Task | Description | Acceptance |
|------|-------------|------------|
| 2.1 | TCP proxy core | Listen on a port, accept connections, forward to destination |
| 2.2 | TLS SNI extraction | Peek ClientHello to extract SNI without decrypting |
| 2.3 | HTTP CONNECT support | Handle proxy-aware clients |
| 2.4 | Whitelist engine | Match domain globs and CIDR ranges against destination |
| 2.5 | Built-in LLM blocklist | Hardcoded list of major LLM provider domains |
| 2.6 | JSONL audit logging | Structured log of every allow/deny decision |
| 2.7 | Unix socket control API | `ADD_RULE`, `REMOVE_RULE`, `LIST_RULES`, `STATS` |
| 2.8 | Standalone test suite | `go test ./pkg/guardian/...` passes |

**Deliverable:** `go run ./cmd/xit-guardian --config test.yaml` blocks and logs correctly.

---

## Phase 3: Linux Backend (Week 2)

### Milestone: `xitbox run -- <cmd>` isolates on Linux.

| Task | Description | Acceptance |
|------|-------------|------------|
| 3.1 | Network namespace setup | `unshare --net`, create veth pair, bridge to host |
| 3.2 | iptables REDIRECT rules | Per-sandbox chain redirects TCP to guardian port |
| 3.3 | bwrap mount generation | Build mount flags from config: CWD, shares, agent persist dirs, minimal /dev, /proc, /etc |
| 3.4 | Sandbox lifecycle manager | Start guardian → setup netns → exec bwrap → cleanup on exit (ephemeral) |
| 3.5 | cgroup setup (optional v2) | Apply memory, CPU, PID limits if available |
| 3.6 | Environment variable filtering | Strip env to whitelist before exec |
| 3.7 | `xitbox list` | List running sandboxes by PID |

**Deliverable:** On Linux, `xitbox run -- curl https://github.com` works, `curl https://api.openai.com` is blocked and logged.

---

## Phase 4: macOS Backend (Week 2–3)

### Milestone: Same UX on macOS via Lima.

| Task | Description | Acceptance |
|------|-------------|------------|
| 4.1 | Lima VM template | Alpine Linux, auto-start, virtiofs mounts, minimal config |
| 4.2 | VM lifecycle manager | Start VM on first `run`, keep warm, stop after idle timeout |
| 4.3 | Host→VM path translation | Convert macOS paths to VM virtiofs mount points |
| 4.4 | Run Linux backend inside VM | `limactl exec` to run the same Phase 3 code |
| 4.5 | Log forwarding from VM | Mount log dir via virtiofs so host can tail JSONL |
| 4.6 | Port forwarding coordination | Lima auto-forwards guardian ports to host localhost |

**Deliverable:** On macOS, `xitbox run -- curl https://github.com` works identically to Linux.

---

## Phase 5: UX Polish & Agent Integration (Week 3)

### Milestone: Frictionless allow-listing and agent persistence.

| Task | Description | Acceptance |
|------|-------------|------------|
| 5.1 | `xitbox allow` command | `--domain`, `--cidr`, `--from-log`, `--sandbox` scope |
| 5.2 | `xitbox logs` command | Tail/follow JSONL logs, filter by sandbox, time, action |
| 5.3 | Agent auto-detection | `init` detects claude, opencode, aider, codex, cline |
| 5.4 | Agent persistence mapping | Auto-create `~/.xitbox/persist/<agent>/`, bind into sandbox |
| 5.5 | Session chrome integration | Overlay runs during sandbox, shows blocks, handles [a]llow key |
| 5.6 | Default built-in whitelist | GitHub, npm, PyPI, crates.io, golang.org, etc. |
| 5.7 | `--allow-ssh` flag | Session-scoped SSH enablement (deferred to v0.2, see bead Code-80f) |
| 5.8 | Shell completion | bash, zsh, fish |
| 5.9 | `xitbox doctor` | Check setup, permissions, dependencies, common issues |

**Deliverable:** A developer can run `xitbox init`, then `xitbox run -- claude`, and have a fully working sandboxed agent.

---

## Phase 6: Testing & Hardening (Week 4)

### Milestone: Reliable enough for daily use.

| Task | Description | Acceptance |
|------|-------------|------------|
| 6.1 | Integration tests | Script-based tests: start sandbox, verify network block, verify allow, verify filesystem isolation |
| 6.2 | Multi-sandbox tests | Start 3 sandboxes simultaneously, verify independent isolation |
| 6.3 | macOS CI tests | Run tests in GitHub Actions macOS runner (Lima available) |
| 6.4 | Linux CI tests | Run tests in GitHub Actions Ubuntu runner |
| 6.5 | Security smoke tests | Attempt to read `~/.ssh` from inside sandbox (must fail), attempt to curl blocked domain (must fail) |
| 6.6 | Documentation | README with install, quickstart, config reference, troubleshooting |
| 6.7 | Release automation | Goreleaser config for Linux and macOS binaries |

**Deliverable:** v0.1.0 release with prebuilt binaries.

---

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| macOS Lima VM startup is too slow (>5s) | Medium | High | Keep VM warm (idle timeout), investigate vzNAT performance, document "first run" slowness |
| iptables requires root on some Linux distros | Medium | High | Fallback to explicit proxy mode (`HTTP_PROXY`), document rootless alternatives (pasta) |
| bwrap not installed on target Linux | Medium | Medium | `init` warns and links to install instructions; package bwrap dependency for package managers |
| SNI extraction fails for some TLS clients | Low | Medium | Fallback to destination IP matching (less precise but functional) |
| Agent config dirs change location between versions | Medium | Low | Configurable agent mappings, easy to update defaults |
| Inter-sandbox networking on macOS is complex | Low (V1) | Low | Not in V1 scope; design hooks only |

---

## Success Criteria for V1

1. `xitbox init` succeeds on a fresh Linux and macOS machine
2. `xitbox run -- claude` starts Claude Code in a sandbox with no manual config
3. `curl https://api.openai.com` from inside the sandbox is blocked and logged
4. `xitbox allow --domain example.com` updates the whitelist live
5. `~/.ssh` is invisible from inside the sandbox by default
6. Agent config changes (e.g., Claude settings) persist across sandbox restarts
7. Three sandboxes can run simultaneously without interfering
8. Session chrome overlay shows blocked attempts and allows interactive allow-listing without restarting

---

## Post-V1 Roadmap

| Version | Feature |
|---------|---------|
| v0.2 | `.xitbox.yaml` per-project config, `xitbox doctor` improvements |
| v0.3 | Auto-allow interactive mode ("Allow api.example.com? [Y/n]") |
| v0.4 | Inter-sandbox communication (shared sockets, host gateway) |
| v0.5 | Apple Container backend for macOS 26+ |
| v0.6 | Windows/WSL2 support |
| v1.0 | Stable API, plugin system for custom backends |
