# xb — Product

> A lightweight, cross-platform sandbox for AI coding agents and arbitrary commands.

---

## 1. Problem Statement

AI coding agents (Claude Code, Codex, Aider, Gemini, etc.) execute arbitrary code on your machine. They can:

- Exfiltrate secrets via HTTP requests to public LLM APIs
- Read SSH keys, AWS credentials, and `.env` files outside the project
- Accidentally delete or corrupt files on the host

**Claude Code has its own sandbox mode**, but it only covers Claude Code. Every other agent — Aider, Codex, Gemini, arbitrary `npm install` — has no sandboxing at all.

In corporate environments there's an additional concern: developers habitually run agents against public LLM APIs that bypass internal sovereign models hosted on Azure/AWS, sometimes unintentionally.

---

## 2. Target Users

**Personal developer**: runs agents with `--dangerously-skip-permissions` for autonomous coding. Wants a safety net — agents shouldn't be able to exfiltrate secrets or damage files outside the project. Doesn't want friction.

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
| **xb** *(this)* | bwrap + Seatbelt | Yes | No | Automatic | Designed for corp environments |

### Key Differentiators

1. **Generic** — `xb <anything>`. Not just named agents; any command.
2. **Corporate-ready** — upstream proxy chaining, custom CA bundle with configurable env var injection, deny-list enforcement even behind a corp proxy.
3. **No Docker** — native bubblewrap on Linux, Seatbelt on macOS. Instant startup.
4. **OS-level network enforcement** — on Linux with pasta/slirp4netns, even processes that ignore `HTTP_PROXY` (JVMs, native binaries) can't make direct outbound connections. On macOS, Seatbelt enforces the same.
5. **Automatic agent config persistence** — detects known agents by command name, grants write access to their config dirs. Config persists naturally on the host filesystem.
6. **Tamper-resistant project config** — `.xitbox.yaml` is mounted read-only inside the sandbox; sandboxed processes can't modify their own allow rules.

---

## 4. Core Features

### Network
- **Default deny** all outbound connections
- **Built-in blocklist** for public LLM providers (OpenAI, Anthropic, Google, Cohere, Mistral, etc.)
- **Domain and CIDR allowlist** with glob support
- **JSONL audit log** of all blocked attempts
- **Upstream proxy support** — chain through corporate HTTP proxy
- **Custom CA bundle** — inject corp CA cert into sandbox env for TLS inspection environments; configurable env var list

### Filesystem
- **Default deny writes** outside the project directory
- **Working directory** mounted read-write
- **Agent config dirs** writable (agents persist their own settings naturally)
- **No access to sensitive host dirs** — `~/.ssh`, `~/.aws`, etc. not visible unless explicitly shared
- **`.xitbox.yaml` read-only** — project config is not modifiable by the sandbox

### Multi-Sandbox
- Multiple isolated sandboxes simultaneously
- Each gets its own network enforcement, guardian instance, and state
- Sandboxes are ephemeral — no cleanup needed on exit

---

## 5. User Flows

### Personal developer

```bash
# One command. Agent runs with full auto-permissions but network is filtered.
xb claude --dangerously-skip-permissions

# Or any command
xb npm install
xb python script.py
```

When something is blocked:
```bash
xb --logs --follow          # see what's being blocked
xb --allow --from-log       # allow the last blocked domain
xb --allow --domain api.mycompany.internal
```

### Corporate developer

Config in `~/Library/Application Support/xb/default.yaml` (macOS) or `~/.config/xb/default.yaml` (Linux):

```yaml
network:
  upstream_proxy: http://proxy.corp.internal:8080
  ca_bundle: /etc/corp-ca.pem
  ca_bundle_env_vars:
    - NODE_EXTRA_CA_CERTS
    - REQUESTS_CA_BUNDLE
    - SSL_CERT_FILE
    - CURL_CA_BUNDLE
    - GIT_SSL_CAINFO
  deny_list:
    - api.openai.com
    - api.anthropic.com
    - generativelanguage.googleapis.com
  allow:
    - llm-proxy.corp.internal
```

Then just:
```bash
xb claude --dangerously-skip-permissions
```

---

## 6. CLI Design

`xb <command>` sandboxes anything. Management is via lowercase flags — no ambiguity about reserved words:

```bash
xb claude                        # sandbox claude
xb npm install                   # sandbox npm install
xb --name myproject python main.py

xb --allow --domain foo.com      # management
xb --allow --from-log
xb --logs --follow
xb --list
```

---

## 7. Supported Agents (First-Class)

xb detects these agents by command name and grants write access to their config dirs:

| Agent | Config Dir |
|-------|-----------|
| Claude Code | `~/.claude/` |
| Codex CLI | `~/.codex/` |
| Aider | `~/.aider/` |
| Cline | `~/.cline/` |
| Gemini CLI | `~/.gemini/` |

Any other command also runs sandboxed — it just doesn't get special config dir access.

> **Note:** opencode is intentionally not supported. Its TUI silently exits with code 255 over SSH transports (opencode issues #6119, #24475). Use `opencode web` outside xb.

---

## 8. Non-Goals

- **Perfect jail / threat-proof isolation** — process-level sandboxing, not hardware VMs. A kernel exploit is out of scope.
- **TLS inspection / MITM** — xb trusts the allowlist. It does not decrypt traffic.
- **Container/Docker integration** — no Docker, by design.
- **GUI applications** — CLI only.
- **Command whitelisting** — filesystem isolation is the protection, not blocking `rm -rf`.

---

## 9. Roadmap

| Version | Feature |
|---------|---------|
| v0.1 | Core sandbox: Seatbelt (macOS), bwrap + pasta/relay (Linux), guardian proxy, upstream proxy, CA bundle |
| v0.2 | Session chrome — interactive allow prompts in-terminal without restarting the agent |
| v0.3 | `xb --allow` live-updates running sandbox via control socket (currently writes config only) |
| v0.4 | Apple Container backend for macOS 26+ |
| v0.5 | Inter-sandbox communication |
| v0.6 | Session-scoped SSH support |
