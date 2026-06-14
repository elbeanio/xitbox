# xb

> A lightweight, cross-platform sandbox for AI coding agents and arbitrary commands.
> Controlled filesystem access. Network filtering via proxy. One command to run.

[![Go Version](https://img.shields.io/badge/go-1.23+-blue.svg)](https://golang.org)

---

## What is xb?

**xb** runs any command — AI coding agents, `npm install`, `python script.py` — in an ephemeral sandbox with controlled filesystem and network access. It protects against accidental misuse: agents can't read your SSH keys or credentials, can only write files in your project directory, and network traffic is filtered through a local proxy with an allowlist.

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
./bin/xb --init     # write default config
./bin/xb --help
```

### Run a sandboxed command

```bash
./bin/xb claude --dangerously-skip-permissions
```

Filesystem writes are restricted to your project directory. Network traffic is filtered through the guardian proxy. When the command exits, the sandbox is gone.

---

## How It Works

```
sandboxed process
      │
      │  HTTP_PROXY → guardian (proxy-aware agents)
      │  direct (native macOS networking / Linux bwrap)
      ▼
 guardian proxy      domain allowlist + JSONL audit log
      │              optionally forwards through upstream_proxy
      ▼
   internet (or corp proxy)
```

### Platform Backends

| Feature | Linux | macOS |
|---------|-------|-------|
| **Filesystem write isolation** | bubblewrap bind mounts (rw only where allowed) | Seatbelt `(deny file-write*)` |
| **Credential read protection** | host `~/.ssh`, `~/.aws` etc. not mounted at all | Seatbelt `(deny file-read*)` on specific paths |
| **Network enforcement** | network namespace + iptables DNAT (pasta/slirp4netns) or relay | `HTTP_PROXY` → guardian (proxy-aware agents only) |
| **Startup time** | <1s | <1s |
| **Root required** | No | No |
| **External deps** | bwrap (required), pasta or slirp4netns (recommended) | None |

### macOS network model

macOS Seatbelt cannot restrict outbound connections to specific external hostnames — only `localhost` and `*` (all) are valid targets in Seatbelt network rules. Attempting to deny all outbound and re-allow specific ports breaks TUI agents that use native macOS networking (which ignores `HTTP_PROXY`).

xb therefore does **not** use Seatbelt for network enforcement on macOS. Instead it relies on `HTTP_PROXY` → guardian:

- Agents that respect the proxy (claude, aider, codex, npm, pip, curl…) are fully filtered and logged by guardian.
- Agents using native macOS APIs (e.g. Claude Code's auth flow) can reach external hosts directly. This matches the approach used by claude-sandbox and agent-seatbelt.

**Linux has stronger network isolation.** With pasta/slirp4netns, all TCP is redirected to guardian via iptables DNAT regardless of whether the process respects `HTTP_PROXY`. With the relay fallback, processes that ignore `HTTP_PROXY` have no network at all.

**Linux network modes** (tried in order):
1. **pasta** — transparent proxy via userspace networking + iptables DNAT. Catches all TCP including JVMs and raw-socket code.
2. **slirp4netns** — same, older tool.
3. **Relay** — zero extra deps. Sandbox has no internet; only `HTTP_PROXY`-aware processes can reach guardian.

---

## Configuration

### Init

```bash
xb --init
```

Writes the default config to the platform config path and prints instructions for project-level overrides. Safe to run repeatedly — won't overwrite an existing config.

### Config locations

- **macOS:** `~/Library/Application Support/xb/default.yaml`
- **Linux:** `~/.config/xb/default.yaml`

Per-project overrides live in `.xb.yaml` in your project directory.

### Merging behaviour

Config is **additive** across scopes. Slice fields (`allow`, `deny_list`, `allow_write`, `deny_read`, `env.allow`, etc.) are merged — you only need to list your extras in project or user config, not replicate the built-in defaults. Scalar fields (`default_policy`, `cwd`, etc.) are replaced by the most specific scope.

### Personal config (default)

```yaml
network:
  default_policy: deny
  allow:
    # Dev tooling
    - github.com
    - '*.github.com'
    - registry.npmjs.org
    - pypi.org
    - crates.io
    - golang.org
    # Anthropic auth + API
    - claude.ai
    - '*.claude.ai'
    - platform.claude.com
    - api.anthropic.com
    # Other LLM providers
    - api.openai.com
    - generativelanguage.googleapis.com
    - api.mistral.ai
    - api.groq.com

filesystem:
  cwd: rw
  # Extra write paths (macOS only — on Linux these are simply not mounted).
  # Additive: list only what you need beyond the built-in defaults.
  allow_write:
    - ~/.claude.json   # Claude Code atomic-writes its state here
  # Paths the sandboxed process cannot read (macOS only).
  # Additive: add entries here to extend the built-in blocklist.
  deny_read:
    - ~/.ssh
    - ~/.gnupg
    - ~/.aws/credentials
    - ~/.azure
    - ~/.config/gcloud
    - ~/.netrc
    - ~/.bash_history
    - ~/.zsh_history
    - ~/.docker/config.json

env:
  filter: true
  allow:
    - PATH
    - HOME
    - TERM
    - LANG
    - ANTHROPIC_API_KEY
    - OPENAI_API_KEY
```

### Corporate config

```yaml
network:
  # Route allowed traffic through your corporate proxy
  upstream_proxy: http://proxy.corp.internal:8080

  # Corp CA cert for TLS inspection — injected into sandbox env automatically
  ca_bundle: /etc/corp-ca.pem
  ca_bundle_env_vars:
    - NODE_EXTRA_CA_CERTS    # Node.js
    - REQUESTS_CA_BUNDLE     # Python requests/httpx
    - SSL_CERT_FILE          # curl, OpenSSL
    - CURL_CA_BUNDLE         # curl
    - GIT_SSL_CAINFO         # git
    - MY_INTERNAL_TOOL_CA    # add your own

  # Block public LLM endpoints — deny_list overrides allow_list
  deny_list:
    - api.openai.com
    - api.anthropic.com
    - generativelanguage.googleapis.com

  # Allow internal endpoints
  allow:
    - llm-proxy.corp.internal
    - registry.corp.internal
```

### Per-project overrides

Create `.xb.yaml` in your project directory. It is merged with the default config and mounted **read-only** inside the sandbox so the agent cannot modify its own allow rules.

```yaml
network:
  allow:
    - api.mycompany.internal

filesystem:
  allow_write:
    - ~/some/tool/state
  shares:
    - path: /home/user/shared-lib
      mode: ro
```

---

## Commands

```
xb [flags] <command> [args...]     Run a command inside a sandbox
xb --init                          Write default config file
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

xb detects known agents by command name and grants write access to their config directories so settings persist across runs:

| Agent | Config Dir |
|-------|-----------|
| Claude Code | `~/.claude/` |
| Codex CLI | `~/.codex/` |
| Aider | `~/.aider/` |
| Cline | `~/.cline/` |
| Gemini CLI | `~/.gemini/` |

---

## Security Model

### What xb protects against

- Agent writes files outside the project directory
- Agent reads credential files (`~/.ssh/`, `~/.aws/credentials`, etc.) — macOS
- Agent exfiltrates data to domains not on the allowlist — for proxy-aware processes
- Agent modifies `.xb.yaml` to broaden its own allow rules

### What xb does not protect against

- Kernel privilege escalation or sandbox CVEs
- Agents using native macOS networking bypassing `HTTP_PROXY` (macOS Seatbelt limitation)
- A malicious `.xb.yaml` committed to a repo (it's in your code review, not hidden)
- Determined attackers with local access

### Defense layers

| Layer | Linux | macOS |
|-------|-------|-------|
| **Write isolation** | bwrap bind mounts — only mounted paths are writable | Seatbelt `(deny file-write*)` with per-path exceptions |
| **Credential read protection** | Host home not mounted — `~/.ssh`, `~/.aws` etc. simply absent | Seatbelt `(deny file-read*)` on credential paths |
| **Network filtering** | iptables DNAT → guardian (all processes) or relay (HTTP_PROXY only) | `HTTP_PROXY` → guardian (proxy-aware processes only) |
| **Audit log** | guardian JSONL log of all allowed/denied connections | same |
| **Project config tamper** | `.xb.yaml` bind-mounted read-only | `.xb.yaml` bind-mounted read-only |

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
│   └── fs/              # Mount preparation (Linux bind mounts)
└── docs/
    ├── ARCHITECTURE.md
    ├── NETWORKING.md
    └── CONFIG.md
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
- [claude-sandbox](https://github.com/kohkimakimoto/claude-sandbox) — Minimal Seatbelt wrapper for Claude Code
- [agent-seatbelt](https://github.com/CJHwong/agent-seatbelt) — Seatbelt + credential read protection for coding agents

xb differs in being **cross-platform with identical UX**, **purpose-built for coding agents**, **deployable in corporate environments** with upstream proxy and custom CA support, and **honest about the macOS network enforcement limitation** rather than silently failing.
