# xb — Product

> A lightweight, cross-platform sandbox for AI coding agents and arbitrary commands.

---

## 1. Problem Statement

AI coding agents (Claude Code, Codex, Aider, Gemini, etc.) execute arbitrary code on your machine. They can:

- Read SSH keys, AWS credentials, and `.env` files outside the project
- Exfiltrate secrets to public LLM APIs or arbitrary hosts
- Accidentally delete or corrupt files on the host

**Claude Code has its own sandbox mode**, but it only covers Claude Code. Every other agent — Aider, Codex, Gemini, arbitrary `npm install` — has no sandboxing at all.

In corporate environments there's an additional concern: developers run agents against public LLM APIs that bypass internal sovereign models hosted on Azure/AWS.

---

## 2. Target Users

**Personal developer**: runs agents with `--dangerously-skip-permissions` for autonomous coding. Wants a safety net — agents shouldn't be able to read credentials or damage files outside the project. Doesn't want friction.

**Corporate developer**: company mandates sovereign models via an internal proxy. Needs agents blocked from public LLM endpoints. Needs to work within corporate TLS inspection (custom CA chain).

---

## 3. Competitive Landscape

| Tool | Isolation | Cross-Platform | Docker Required | Agent Config Persistence | Notes |
|------|-----------|---------------|-----------------|------------------------|-------|
| **Claude Code sandbox** | bwrap / Seatbelt | Yes | No | Yes | First-party, Claude Code only |
| **agentcage** | VM/Container/Apple Container | Yes | Optional | Manual | Heavier, broader threat model |
| **greywall** | bwrap / sandbox-exec | Yes | No | No | No corp proxy support |
| **hole** | Docker | Yes | Yes | No | Docker required |
| **clampdown** | Podman + Landlock | Yes | Yes | No | Complex |
| **claude-sandbox** | Seatbelt | macOS only | No | No | Minimal, no network filtering |
| **agent-seatbelt** | Seatbelt | macOS only | No | No | Credential read protection |
| **xb** *(this)* | bwrap + Seatbelt | Yes | No | Automatic | Corp-ready, honest about macOS network limits |

### Key Differentiators

1. **Generic** — `xb <anything>`. Not just named agents; any command.
2. **Corporate-ready** — upstream proxy chaining, custom CA bundle with configurable env var injection, deny-list enforcement.
3. **No Docker** — native bubblewrap on Linux, Seatbelt on macOS. Instant startup.
4. **Credential read protection** — on macOS, `deny_read` blocks access to `~/.ssh`, `~/.aws/credentials`, etc. via Seatbelt. On Linux, the host home is never mounted.
5. **OS-level network enforcement on Linux** — with pasta/slirp4netns, all TCP is intercepted via iptables DNAT even for processes that ignore `HTTP_PROXY` (JVMs, native binaries). macOS relies on `HTTP_PROXY` → guardian.
6. **Automatic agent config persistence** — detects known agents by command name, grants write access to their config dirs.
7. **Tamper-resistant project config** — `.xb.yaml` is mounted read-only; sandboxed processes can't modify their own allow rules.
8. **Honest about limitations** — the macOS network enforcement limitation is documented, not hidden.

---

## 4. Core Features

### Network
- **Guardian proxy** — all proxy-aware traffic filtered by hostname; JSONL audit log
- **Domain and CIDR allowlist** with glob support
- **Deny list** — overrides allow list; used in corp deployments to block public LLM endpoints
- **Upstream proxy support** — chain through corporate HTTP proxy
- **Custom CA bundle** — inject corp CA cert into sandbox env for TLS inspection environments
- **Hot reload** — config changes take effect without restarting the sandbox

### Filesystem
- **Write isolation** — writes denied outside project directory; configurable via `allow_write`
- **Credential read protection** — macOS: Seatbelt `deny_read` rules; Linux: host home not mounted
- **Agent config dirs** writable — agents persist their own settings naturally
- **`.xb.yaml` read-only** — project config not modifiable by the sandbox

### Usability
- `xb --init` — writes default config, prints project override instructions
- `xb --allow --from-log` — add last blocked domain in one command
- `xb --logs --follow` — real-time view of blocked connections
- Additive config merging — project configs only list extras, not full defaults

---

## 5. Security Model

### What xb protects against
- Agent writes files outside the project directory
- Agent reads credential files (`~/.ssh`, AWS keys, etc.)
- Agent calls blocked network domains (for proxy-respecting processes)
- Sandboxed agent modifying its own allow rules (via `.xb.yaml` read-only)

### What xb does NOT protect against
- Kernel privilege escalation or CVE-based sandbox escape
- On macOS: exfiltration via native macOS networking that bypasses `HTTP_PROXY`
- A malicious `.xb.yaml` in a repo (visible in code review; not a technical bypass)
- Determined attackers with local access

---

## 6. User Flows

### Personal developer

```bash
# Write default config once
xb --init

# Run any agent — network filtered, credentials protected
xb claude --dangerously-skip-permissions
xb npm install
xb python script.py

# When something is blocked
xb --logs --follow          # see what's being blocked
xb --allow --from-log       # allow the last blocked domain
xb --allow --domain api.mycompany.internal
```

### Corporate developer

User config at `~/Library/Application Support/xb/default.yaml`:

```yaml
network:
  upstream_proxy: http://proxy.corp.internal:8080
  ca_bundle: /etc/corp-ca.pem
  deny_list:
    - api.openai.com
    - api.anthropic.com
  allow:
    - llm-proxy.corp.internal
```

Then just `xb claude --dangerously-skip-permissions` — corp proxy is used, public endpoints blocked.

---

## 7. CLI Design

`xb <command>` sandboxes anything. Management is via lowercase flags:

```bash
xb --init                        # write default config
xb claude                        # sandbox claude TUI
xb npm install                   # sandbox npm
xb --name myproject python main.py

xb --allow --domain foo.com      # management
xb --allow --from-log
xb --logs --follow
xb --list
```

---

## 8. Supported Agents (First-Class)

xb detects these agents by command name and grants write access to their config dirs:

| Agent | Config Dir |
|-------|-----------|
| Claude Code | `~/.claude/` |
| Codex CLI | `~/.codex/` |
| Aider | `~/.aider/` |
| Cline | `~/.cline/` |
| Gemini CLI | `~/.gemini/` |

Any other command also runs sandboxed — it just doesn't get special config dir access.

---

## 9. Non-Goals

- **Perfect jail / threat-proof isolation** — process-level sandboxing, not hardware VMs.
- **TLS inspection / MITM** — xb trusts the allowlist. It does not decrypt traffic.
- **Container/Docker integration** — no Docker, by design.
- **GUI applications** — CLI only.
- **Command whitelisting** — filesystem isolation is the protection, not blocking `rm -rf`.
- **macOS OS-level network enforcement** — Seatbelt cannot restrict to specific external hostnames. This is a known limitation, documented honestly.

---

## 10. Roadmap

| Version | Feature |
|---------|---------|
| v0.1 | Core sandbox: Seatbelt (macOS), bwrap + pasta/relay (Linux), guardian proxy, upstream proxy, CA bundle, credential read protection |
| v0.2 | Session chrome — interactive allow prompts in-terminal without restarting the agent |
| v0.3 | Apple Container backend for macOS 26+ |
| v0.4 | Inter-sandbox communication |
| v0.5 | Session-scoped SSH support |
