# xitbox

> A lightweight, cross-platform sandbox for AI coding agents.
> Default-deny network. Default-deny filesystem. One command to run.

[![Go Version](https://img.shields.io/badge/go-1.23+-blue.svg)](https://golang.org)

---

## What is xitbox?

**xitbox** runs AI coding agents (Claude Code, OpenCode, Codex, etc.) in ephemeral sandboxes with controlled network and filesystem access. It protects against accidental misuse — agents can't reach public LLM APIs, can't read your SSH keys, and can only touch files you explicitly allow.

**Key idea:** Run any agent with `xitbox <agent>` (or `xitbox run -- <cmd>` for one-offs) and it runs inside a sandbox. When the command exits, the sandbox is gone.

```bash
# Sandbox Claude Code (or: xitbox opencode, xitbox codex, xitbox aider)
xitbox claude

# Sandbox a one-off command
xitbox run -- npm install

# Run multiple sandboxes simultaneously
xitbox run --name frontend -- npm run dev
xitbox run --name backend -- python server.py
```

Use `--` to pass flags through to the agent, e.g. `xitbox claude -- --help` or `xitbox opencode -- --version`.

---

## Quick Start

### Prerequisites

**macOS:**
```bash
brew install lima
```

**Linux (Ubuntu/Debian):**
```bash
sudo apt install bubblewrap iptables
```

**Linux (Fedora/RHEL):**
```bash
sudo dnf install bubblewrap iptables
```

### Install

Download the latest release for your platform, or build from source:

```bash
git clone https://github.com/elbeanio/xitbox.git
cd xitbox
make
./bin/xitbox --help    # or `make where` to see binary paths
```

### Initialize

```bash
./bin/xitbox init
```

This creates your default configuration, detects installed agents, and verifies dependencies.

### Run a sandboxed agent

```bash
./bin/xitbox claude
# or equivalently:
./bin/xitbox run -- claude
```

The agent starts inside a sandbox. Network access is default-deny. Filesystem access is restricted to your project directory and agent config persistence directories.

On macOS, each agent gets its own dedicated Lima VM (e.g. `xitbox-claude`, `xitbox-opencode`) so credentials and config don't leak between agents. One-off commands share a default VM.

### Allow a blocked domain

When the agent hits a blocked domain, you'll see it in the session overlay (coming in v0.2). For now, add it to the whitelist:

```bash
# Allow a domain for all future sessions
./bin/xitbox allow --domain api.example.com

# Allow from the most recent log entry
./bin/xitbox allow --from-log
```

---

## How It Works

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  xitbox CLI                                                  │
│  ├─ Reads config (default + project + CLI flags)            │
│  ├─ Starts xit-guardian proxy                                │
│  ├─ Sets up network namespace + iptables (Linux)             │
│  ├─ Prepares bubblewrap mount flags                          │
│  └─ Execs agent inside sandbox                               │
│                                                              │
│  xit-guardian (per-sandbox proxy)                            │
│  ├─ Transparent TCP proxy (iptables REDIRECT on Linux)       │
│  ├─ TLS SNI extraction (no decryption)                       │
│  ├─ Domain/CIDR whitelist + LLM blocklist                    │
│  ├─ JSONL audit logging                                      │
│  └─ Unix socket control API for live rule updates           │
└─────────────────────────────────────────────────────────────┘
```

### Platform Differences

| Feature | Linux | macOS |
|---------|-------|-------|
| **Isolation** | Native namespaces (bwrap + unshare) | Lima VM (Alpine Linux) with same stack |
| **Startup** | ~1 second | ~3 seconds (warm VM) |
| **Network** | iptables transparent proxy | Same, inside VM |
| **Filesystem** | bwrap bind mounts | virtiofs + bwrap |

On macOS, xitbox uses lightweight Lima VMs. Each known agent (`claude`, `opencode`, `codex`, `aider`) gets its own persistent VM so credentials and config are isolated between agents. One-off commands share a default VM. The VM is the sandbox boundary; the host is never directly exposed to the agent.

---

## Configuration

### Default Config Location

- **Linux:** `~/.config/xitbox/default.yaml`
- **macOS:** `~/Library/Application Support/xitbox/default.yaml`

### Default Config

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
    - 140.82.0.0/16
  log_file: ~/.local/share/xitbox/denied.jsonl

filesystem:
  cwd: rw
  agent_persistence:
    claude: ~/.xitbox/persist/claude
    opencode: ~/.xitbox/persist/opencode

resources:
  memory: 4g
  cpus: 2
  pids: 4096

env:
  filter: true
  allow:
    - PATH
    - HOME
    - ANTHROPIC_API_KEY
    - OPENAI_API_KEY
```

### Per-Project Overrides

Create `.xitbox.yaml` in your project directory:

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

| Command | Description |
|---------|-------------|
| `xitbox claude [args...]` | Run Claude Code in a sandboxed VM |
| `xitbox opencode [args...]` | Run OpenCode in a sandboxed VM |
| `xitbox codex [args...]` | Run Codex CLI in a sandboxed VM |
| `xitbox aider [args...]` | Run Aider in a sandboxed VM |
| `xitbox run -- <cmd>` | Run any command in an ephemeral sandbox |
| `xitbox run --name foo -- <cmd>` | Run with a named sandbox |
| `xitbox init` | Initialize configuration and check dependencies |
| `xitbox list` | List currently running sandboxes |
| `xitbox allow --domain <domain>` | Add a domain to the whitelist |
| `xitbox allow --cidr <range>` | Add a CIDR range to the whitelist |
| `xitbox allow --from-log` | Allow the most recently blocked destination |
| `xitbox logs --since 5m` | View blocked connection attempts |
| `xitbox logs --follow` | Follow log output in real-time |

Each agent subcommand accepts the same `--name` flag as `xitbox run` and forwards everything after `--` to the agent itself.

---

## Agent Config Persistence

xitbox automatically detects installed agents and persists their configuration across sandbox runs:

| Agent | Persistent Config Dir |
|-------|----------------------|
| Claude Code | `~/.xitbox/persist/claude/` |
| OpenCode | `~/.xitbox/persist/opencode/` |
| Aider | `~/.xitbox/persist/aider/` |
| Codex CLI | `~/.xitbox/persist/codex/` |
| Cline | `~/.xitbox/persist/cline/` |

Inside the sandbox, these are mounted at the agent's expected home directory locations (e.g., `~/.claude/`).

---

## Security Model

**In scope (accidental misuse):**
- Agent accidentally reads files outside the project
- Agent accidentally exfiltrates data to public LLM APIs
- Agent installs packages from compromised registries

**Out of scope (determined attacker):**
- Kernel privilege escalation
- Sandbox escape via unpatched CVE
- Side-channel attacks

xitbox uses **process-level sandboxing**, not hardware VMs. A determined attacker with a kernel exploit can escape. For threat models requiring hardware isolation, use a VM-per-sandbox tool like [agentcage](https://github.com/agentcage/agentcage).

### Defense Layers

| Layer | Linux | macOS |
|-------|-------|-------|
| Mount namespace | bubblewrap | bubblewrap (in VM) |
| Network namespace | unshare --net | unshare --net (in VM) |
| Traffic filtering | iptables REDIRECT → guardian | iptables REDIRECT → guardian |
| LLM blocklist | Built-in deny list | Built-in deny list |
| VM boundary | N/A | Apple Virtualization.framework |

---

## Development

### Project Structure

```
xitbox/
├── cmd/
│   ├── xitbox/          # Main CLI
│   └── xit-guardian/    # Proxy daemon
├── pkg/
│   ├── config/          # YAML config parsing
│   ├── guardian/        # Proxy engine, whitelist, logging
│   ├── platform/        # OS detection, dependency checking
│   ├── sandbox/         # Sandbox lifecycle
│   ├── backend/linux/   # Linux-specific backend
│   ├── backend/darwin/  # macOS Lima VM backend
│   └── fs/              # Mount preparation
├── docs/
│   ├── PRODUCT.md       # Product specification
│   ├── ARCHITECTURE.md  # Architecture document
│   └── IMPLEMENTATION_PLAN.md
└── init/
    └── lima/            # Lima VM template
```

### Build

Both binaries are produced under `./bin/`:

```bash
make            # build xitbox + xit-guardian into ./bin/
make xitbox     # build only xitbox
make xit-guardian
make check      # go vet + go test
make clean      # remove ./bin/
make where      # print absolute paths to the built binaries
make install    # install to $(HOME)/go/bin (override with INSTALL_DIR=...)
```

`xitbox` is the CLI; `xit-guardian` is the standalone proxy daemon (currently
used as a library by `xitbox`, but also runnable on its own for the future
transparent-proxy and system-service modes).

---

## Roadmap

| Version | Feature |
|---------|---------|
| v0.1 | Core sandbox (bwrap, netns, guardian, ephemeral lifecycle) |
| v0.2 | Session chrome overlay, per-project config, `doctor` command |
| v0.3 | Interactive allow prompts ("Allow api.example.com? [Y/n]") |
| v0.4 | Inter-sandbox communication |
| v0.5 | Apple Container backend for macOS 26+ |
| v0.6 | Session-scoped SSH support |

---

## License

MIT

## Similar Projects

- [agentcage](https://github.com/agentcage/agentcage) — Defense-in-depth VM/container sandbox with TLS inspection
- [greywall](https://github.com/GreyhavenHQ/greywall) — Container-free bwrap/sandbox-exec with live dashboard
- [hole](https://github.com/lukashornych/hole) — Docker-based sandbox with domain whitelist
- [clampdown](https://github.com/89luca89/clampdown) — Hardened Podman + Landlock sandbox

xitbox differs in being **Docker-free**, **cross-platform with identical UX**, and **purpose-built for coding agents** with automatic config persistence.
