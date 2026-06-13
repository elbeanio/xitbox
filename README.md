# xb

> A lightweight, cross-platform sandbox for AI coding agents and arbitrary commands.
> Default-deny network. Default-deny filesystem. One command to run.

[![Go Version](https://img.shields.io/badge/go-1.23+-blue.svg)](https://golang.org)

---

## What is xb?

**xb** runs any command — AI coding agents, `npm install`, `python script.py` — in an ephemeral sandbox with controlled network and filesystem access. It protects against accidental misuse: agents can't reach public LLM APIs, can't read your SSH keys, and can only touch files in your project directory.

```bash
# Sandbox Claude Code (with full auto-permission mode)
xb claude --dangerously-skip-permissions

# Sandbox any command
xb npm install
xb python script.py
xb npx @anthropic-ai/claude-code

# Named sandbox
xb --name frontend npm run dev
```

No `--` separator needed. No agent-specific subcommands to remember. `xb <anything>` sandboxes it.

---

## Quick Start

### Prerequisites

**macOS:** No external dependencies. `sandbox-exec` is built in.

**Linux (Ubuntu/Debian):**
```bash
sudo apt install bubblewrap
# For OS-level network enforcement (recommended):
sudo apt install passt        # pasta (preferred)
# or: sudo apt install slirp4netns
```

**Linux (Fedora/RHEL):**
```bash
sudo dnf install bubblewrap passt
```

### Install

```bash
git clone https://github.com/elbeanio/xb.git
cd xb
make
./bin/xb --help
```

### Run a sandboxed command

```bash
./bin/xb claude --dangerously-skip-permissions
```

Network is default-deny. Filesystem writes are restricted to your project directory. When the command exits, the sandbox is gone.

---

## How It Works

```
sandboxed process
      │
      │  all outbound TCP
      ▼
 OS enforcement          macOS: Seatbelt (deny all outbound except guardian)
                         Linux: network namespace (pasta/slirp4netns or relay)
      │
      │  only guardian port allowed through
      ▼
 xit-guardian proxy      domain allowlist + LLM blocklist + JSONL audit log
      │                  optionally forwards through upstream_proxy
      │
      ▼
   internet (or corp proxy)
```

### Platform Backends

| Feature | Linux | macOS |
|---------|-------|-------|
| **Filesystem isolation** | bubblewrap (bwrap) | Seatbelt (sandbox-exec) |
| **Network enforcement** | network namespace + iptables DNAT (pasta/slirp4netns) or relay | Seatbelt `(deny network-outbound)` |
| **Startup time** | <1s | <1s |
| **Root required** | No | No |
| **External deps** | bwrap (required), pasta or slirp4netns (recommended) | None |

**Linux network modes** (tried in order):
1. **pasta** — transparent proxy via userspace networking + iptables DNAT. Catches all TCP including processes that ignore `HTTP_PROXY` (e.g. JVMs).
2. **slirp4netns** — same as pasta, older tool.
3. **Relay** — zero extra deps. Sandbox has no internet at all; only processes that use `HTTP_PROXY` can reach guardian. JVMs and raw-socket code get connection refused.

---

## Configuration

### Default Config Location

- **Linux:** `~/.config/xb/default.yaml`
- **macOS:** `~/Library/Application Support/xb/default.yaml`

### Personal Config (default)

```yaml
network:
  default_policy: deny
  deny_list:
    - api.openai.com
    - api.anthropic.com
    - generativelanguage.googleapis.com
    - api.cohere.com
    - api.mistral.ai
  allow:
    - github.com
    - '*.github.com'
    - registry.npmjs.org
    - pypi.org
    - crates.io
    - golang.org
  log_file: ~/.local/share/xb/denied.jsonl

filesystem:
  cwd: rw

env:
  filter: true
  allow:
    - PATH
    - HOME
    - ANTHROPIC_API_KEY
    - OPENAI_API_KEY
```

### Corporate Config

```yaml
network:
  # Route allowed traffic through your corporate proxy
  upstream_proxy: http://proxy.corp.internal:8080

  # Corp CA cert for TLS inspection — injected into sandbox env automatically
  ca_bundle: /etc/corp-ca.pem

  # Env vars to inject with the CA path. Add your own toolchain vars here.
  ca_bundle_env_vars:
    - NODE_EXTRA_CA_CERTS    # Node.js (claude, gemini, codex)
    - REQUESTS_CA_BUNDLE     # Python requests/httpx (aider)
    - SSL_CERT_FILE          # curl, OpenSSL-linked tools
    - CURL_CA_BUNDLE         # curl
    - GIT_SSL_CAINFO         # git
    - MY_INTERNAL_TOOL_CA    # add anything custom here

  # Block public LLM endpoints — corp proxy handles approved models
  deny_list:
    - api.openai.com
    - api.anthropic.com
    - generativelanguage.googleapis.com

  # Allow internal endpoints
  allow:
    - llm-proxy.corp.internal
    - registry.corp.internal
```

### Per-Project Overrides

Create `.xitbox.yaml` in your project directory. It is merged with the default config and mounted **read-only** inside the sandbox (so the sandboxed process can read it but cannot modify allow rules for future runs):

```yaml
network:
  allow:
    - api.mycompany.internal

filesystem:
  shares:
    - path: /home/user/shared-lib
      mode: ro
```

---

## Commands

```
xb [flags] <command> [args...]     Run a command inside a sandbox
xb --allow [flags]                 Manage the network allowlist
xb --logs [flags]                  View blocked connections
xb --list                          List running sandboxes
```

### Sandbox flags

| Flag | Description |
|------|-------------|
| `--name <name>` | Sandbox name (auto-generated if empty) |

### Allow flags (use with `--allow`)

| Flag | Description |
|------|-------------|
| `--domain <domain>` | Domain to add, supports wildcards (`*.example.com`) |
| `--cidr <range>` | CIDR range to add (e.g. `10.0.0.0/8`) |
| `--from-log` | Add the most recently blocked destination |

### Log flags (use with `--logs`)

| Flag | Description |
|------|-------------|
| `--since <duration>` | Show entries from the last duration (e.g. `5m`, `1h`) |
| `--follow` | Stream log output in real-time |

---

## Agent Config Persistence

> **Note: opencode is intentionally not supported.**
> OpenCode's TUI silently exits with code 255 over SSH/VM transports. Use `opencode web` outside xb.

xb detects known agents by command name and grants write access to their config directories inside the sandbox. Config persists across runs naturally on the host filesystem:

| Agent | Config Dir |
|-------|-----------|
| Claude Code | `~/.claude/` |
| Codex CLI | `~/.codex/` |
| Aider | `~/.aider/` |
| Cline | `~/.cline/` |
| Gemini CLI | `~/.gemini/` |

---

## Security Model

**In scope (accidental misuse):**
- Agent reads files outside the project
- Agent exfiltrates data to public LLM APIs
- Agent installs packages from compromised registries
- Sandboxed process modifies `.xitbox.yaml` to broaden future allow rules

**Out of scope (determined attacker):**
- Kernel privilege escalation
- Sandbox escape via unpatched CVE
- Malicious `.xitbox.yaml` committed to a repo (visible in code review)

### Defense Layers

| Layer | Linux | macOS |
|-------|-------|-------|
| Filesystem isolation | bubblewrap bind mounts | Seatbelt `(deny file-write*)` |
| Network isolation | network namespace (no direct internet) | Seatbelt `(deny network-outbound)` |
| Traffic filtering | iptables DNAT → guardian (pasta/slirp4netns) or relay | Seatbelt → guardian |
| LLM blocklist | guardian deny list | guardian deny list |
| Project config tamper protection | `.xitbox.yaml` bind-mounted read-only | `.xitbox.yaml` bind-mounted read-only |

---

## Development

### Project Structure

```
xb/
├── cmd/
│   ├── xb/              # Main CLI (stdlib flag, no cobra)
│   └── xit-guardian/    # Proxy daemon (also embeddable)
├── pkg/
│   ├── config/          # YAML config parsing and merging
│   ├── guardian/        # Proxy engine, allowlist, logging, upstream proxy
│   ├── platform/        # OS detection, dependency checking
│   ├── sandbox/         # Sandbox lifecycle
│   │   ├── sandbox.go           # Platform dispatch, Darwin (Seatbelt)
│   │   ├── sandbox_linux.go     # Linux: pasta / slirp4netns / relay
│   │   └── sandbox_notlinux.go  # Stub for non-Linux builds
│   ├── backend/         # Unused legacy backends (to be removed)
│   └── fs/              # Mount preparation
├── docs/
│   ├── PRODUCT.md
│   ├── ARCHITECTURE.md
│   ├── NETWORKING.md
│   └── CONFIG.md
└── init/
    └── lima/            # Legacy Lima template (unused)
```

### Build

```bash
make            # build xb + xit-guardian into ./bin/
make xb         # build only xb (CGO_ENABLED=0 — static binary)
make xit-guardian
make check      # go vet + go test
make clean
make where      # print absolute paths to built binaries
make install    # install to $(HOME)/go/bin (override with INSTALL_DIR=...)
```

The xb binary is built static (`CGO_ENABLED=0`) so it can be bind-mounted into the bwrap sandbox on Linux for the relay mode.

---

## Roadmap

| Version | Feature |
|---------|---------|
| v0.1 | Core sandbox: Seatbelt (macOS), bwrap + pasta/relay (Linux), guardian proxy |
| v0.2 | Session chrome — interactive allow prompts in-terminal |
| v0.3 | `xb --allow` live-updates running sandbox via control socket |
| v0.4 | Apple Container backend for macOS 26+ |
| v0.5 | Inter-sandbox communication |
| v0.6 | Session-scoped SSH support |

---

## License

MIT

## Similar Projects

- [agentcage](https://github.com/agentcage/agentcage) — Defense-in-depth VM/container sandbox with TLS inspection
- [greywall](https://github.com/GreyhavenHQ/greywall) — Container-free bwrap/sandbox-exec with live dashboard
- [hole](https://github.com/lukashornych/hole) — Docker-based sandbox with domain allowlist
- [clampdown](https://github.com/89luca89/clampdown) — Hardened Podman + Landlock sandbox

xb differs in being **Docker-free**, **cross-platform with identical UX**, **purpose-built for coding agents**, and **deployable in corporate environments** with upstream proxy and custom CA support.
