# xitbox — Product Specification

> A lightweight, cross-platform sandbox for AI coding agents.
> Default-deny network. Default-deny filesystem. One command to run.

---

## 1. Problem Statement

AI coding agents (Claude Code, Codex, Aider, Gemini, etc.) execute arbitrary code on your machine. They can:

- Exfiltrate secrets via HTTP requests to public LLM APIs
- Read SSH keys, AWS credentials, and `.env` files outside the project
- Accidentally delete or corrupt files on the host
- Install malicious packages from compromised registries

Existing sandbox tools are either **too heavy** (full Docker/VM per session), **too narrow** (Linux-only), **too complex** (secret injection, MITM TLS inspection), or **use deprecated APIs** (macOS `sandbox-exec`). There is no tool that offers a **lightweight, identical UX across Linux and macOS** with **coding-agent-specific ergonomics**.

---

## 2. Target User

- Developers running AI coding agents locally
- Wants protection against **accidental misuse** and **casual exfiltration**
- Values **simplicity** over military-grade isolation
- Needs to **whitelist domains/IPs** dynamically as agents encounter new dependencies
- Wants **agent config persistence** (API keys, preferences) without giving the agent access to the rest of `$HOME`

---

## 3. Competitive Landscape

| Tool | Isolation | Cross-Platform | Docker Required | Agent Config Persistence | UX Simplicity |
|------|-----------|---------------|-----------------|------------------------|---------------|
| **agentcage** | VM/Container/Apple Container | Yes | Optional | Manual | Medium |
| **greywall** | bwrap / sandbox-exec | Yes | No | No | Medium |
| **ai-jail** | bwrap / sandbox-exec | Yes | No | No | Medium |
| **hole** | Docker | Yes (via VM) | **Yes** | No | Medium |
| **clampdown** | Podman + Landlock | Yes (via VM) | **Yes** | No | Complex |
| **devsandbox** | bwrap / Docker | Yes | **Yes (macOS)** | Per-project | Medium |
| **abox** | libvirt VM | Linux | No | Snapshots | Medium |
| **code-on-incus** | Incus containers | Linux + VM | **Yes (macOS)** | Yes | Complex |
| **xitbox** *(this)* | **bwrap + Lima** | **Yes** | **No** | **Automatic** | **High** |

### Key Differentiators

1. **No Docker anywhere** — Native `bubblewrap` on Linux, lightweight Lima VM on macOS. Same stack runs inside both.
2. **Automatic agent config persistence** — Detects agent config directories and maps them to persistent host storage without user configuration.
3. **Session chrome / overlay** — A lightweight UI wrapper around the sandboxed command shows blocked connection attempts in real-time and lets you allow them with a keystroke, without quitting the agent.
4. **Transparent proxy with structured logging** — Every blocked connection attempt is logged to JSONL with reason, destination, and timestamp. A simple `xitbox allow --from-log` command adds it to the whitelist.
5. **Identical UX on Linux and macOS** — One binary, one config format, one mental model.
6. **Purpose-built for coding agents** — Not a generic sandbox. Knows about Claude Code, Codex, Aider, Gemini, etc.
7. **Session-scoped everything** — Sandboxes are ephemeral. When the agent exits, the sandbox is gone. No `stop` command needed.

---

## 4. Core Features

### Network
- **Default deny** all outbound connections
- **Built-in blocklist** for public LLM providers (OpenAI, Anthropic, Google, Cohere, etc.)
- **Domain and CIDR whitelist** with glob support (`*.github.com`, `140.82.0.0/16`)
- **JSONL audit log** of all blocked attempts with metadata (timestamp, sandbox name, destination, reason)
- **Dynamic rule updates** — add to whitelist without restarting the sandbox
  - **No TLS inspection** — we trust the whitelist, we don't MITM connections
  - **Session chrome** — real-time overlay showing blocked attempts with interactive allow prompts

### Filesystem
- **Default deny** outside the project working directory
- **Working directory** mounted read-write by default
- **Explicit shares** — additional host paths can be mounted read-only or read-write
- **Agent config persistence** — automatically maps known agent config dirs (`~/.claude/`, `~/.gemini/`, etc.) to persistent host storage
- **Device access denied** — `/dev` is minimal (null, zero, random, urandom, tty)
- **No access to host secrets** — `~/.ssh`, `~/.aws`, `~/.npmrc` etc. are invisible unless explicitly shared

### Resources
- Optional **memory and CPU limits** via cgroups (Linux) or Lima VM limits (macOS)
- Optional **PID limits** to prevent fork bombs

### Multi-Sandbox
- Run **multiple isolated sandboxes simultaneously**
- Each sandbox gets its own network namespace, filesystem view, guardian proxy instance, and chrome overlay
- Named sandboxes: `xitbox run --name frontend -- npm dev` alongside `xitbox run --name backend -- python server.py`
- Sandboxes are **ephemeral** — they exit when the command exits. No manual cleanup needed.

---

## 5. User Experience

### Philosophy
- **One command to sandbox** — if you can run the agent without xitbox, you can run it with xitbox by prefixing one command.
- **Sensible defaults** — whitelists common coding domains (GitHub, npm, PyPI, crates.io) out of the box.
- **Frictionless allow-listing** — when something is blocked, add it interactively from the session chrome or with one CLI command. No config file editing.
- **Clear feedback** — the session chrome shows every blocked attempt. The user always knows what the sandbox is doing.
- **Ephemeral by default** — sandboxes start fast, run the command, and clean up on exit. No daemon, no `stop` command.

### User Flows

#### 5.1 First Run — Initialization

```bash
$ xitbox init
✓ Created ~/.config/xitbox/default.yaml
✓ Detected agents: claude, gemini
✓ Created persistent config directories:
    ~/.xitbox/persist/claude/
    ~/.xitbox/persist/gemini/
✓ Lima VM 'xitbox' created (macOS) / bubblewrap verified (Linux)
```

`xitbox init` scaffolds a default config, detects installed agents, and prepares the platform backend.

#### 5.2 Running an Agent

```bash
$ xitbox run -- claude
# or with explicit name
$ xitbox run --name myproject -- claude
# or running a one-off command
$ xitbox run -- npm install
```

The sandbox starts in ~1 second (Linux) or ~3 seconds (macOS, warm Lima VM). The agent runs with network and filesystem restrictions enforced.

#### 5.3 Discovering a Blocked Domain — Session Chrome

While the agent runs, it tries to reach `api.some-registry.io`. The connection is blocked. In the session chrome (a small overlay at the bottom/top of the terminal), the user sees:

```
┌─ xitbox: myproject ─────────────────────────────────────────────┐
│ 12:34:56  BLOCKED  api.some-registry.io:443                     │
│ Press [a] to allow, [i] to ignore, [l] to view log             │
└─────────────────────────────────────────────────────────────────┘
```

Pressing `a` immediately adds `api.some-registry.io` to the running guardian's whitelist. The agent's next retry succeeds. No restart needed.

The user can also review all blocked attempts later:

```bash
$ xitbox logs --since 5m
2026-06-04 12:34:56 [myproject] DENY api.some-registry.io:443 (not-in-allowlist)
```

#### 5.4 Allow-Listing a Domain

**From the session chrome** (while the agent is running):
```
# Press [a] when a block appears in the chrome overlay
# The domain is added to the live whitelist immediately
```

**From the CLI** (after the session exits):
```bash
# Allow for all future sandboxes (updates default config)
$ xitbox allow --domain api.some-registry.io

# Allow from the last log entry (interactive)
$ xitbox allow --from-log
? Allow api.some-registry.io:443? (Y/n) y
✓ Added api.some-registry.io to default whitelist
```

#### 5.5 Listing Running Sandboxes

```bash
$ xitbox list
NAME        STATUS    PID    NETWORK    CREATED
myproject   running   4821   filtered   2m ago
frontend    running   4950   filtered   5m ago
```

Sandboxes are ephemeral — they exit when the command exits. There is no `stop` command. If you need to kill one, use `kill <pid>` or Ctrl-C.

---

## 6. Configuration

### Default Config Location
- Linux: `~/.config/xitbox/default.yaml`
- macOS: `~/Library/Application Support/xitbox/default.yaml`

### Config Format (YAML)

```yaml
# ~/.config/xitbox/default.yaml

network:
  default_policy: deny
  
  # Always blocked regardless of whitelist
  deny_list:
    - api.openai.com
    - api.anthropic.com
    - generativelanguage.googleapis.com
    - api.cohere.com
    - api.mistral.ai
  
  # Allowed destinations
  allow:
    - github.com
    - '*.github.com'
    - registry.npmjs.org
    - pypi.org
    - crates.io
    - 140.82.0.0/16  # GitHub IP range
  
  # Log blocked attempts to this file
  log_file: ~/.local/share/xitbox/denied.jsonl

filesystem:
  # Working directory is always mounted read-write
  cwd: rw
  
  # Additional host paths to expose
  shares:
    - path: /home/user/projects/shared-lib
      mode: ro
    - path: /tmp/agent-cache
      mode: rw
  
  # Agent configs are auto-detected and persisted.
  # Override here if you want to disable or remap.
  agent_persistence:
    claude: ~/.xitbox/persist/claude
    gemini: ~/.xitbox/persist/gemini

resources:
  memory: 4g
  cpus: 2
  pids: 4096

env:
  # Filter environment variables to a whitelist
  filter: true
  allow:
    - PATH
    - HOME
    - TERM
    - LANG
    - EDITOR
    # Agent-specific vars are injected automatically
    - ANTHROPIC_API_KEY
    - OPENAI_API_KEY
```

### Per-Project Overrides

A `.xitbox.yaml` in the project directory merges with the default config:

```yaml
# ~/myproject/.xitbox.yaml
network:
  allow:
    - api.mycompany.internal

filesystem:
  shares:
    - path: /home/user/company-secrets
      mode: ro
```

---

## 7. Supported Agents (First-Class)

xitbox automatically detects and configures persistence for:

| Agent | Config Dir | Notes |
|-------|-----------|-------|
| Claude Code | `~/.claude/` | Maps to `~/.xitbox/persist/claude/` |
| Codex CLI | `~/.codex/` | Maps to `~/.xitbox/persist/codex/` |
| Aider | `~/.aider/` | Maps to `~/.xitbox/persist/aider/` |
| Cline | `~/.cline/` | Maps to `~/.xitbox/persist/cline/` |
| Gemini CLI | `~/.gemini/` | Maps to `~/.xitbox/persist/gemini/` |

> **Note:** OpenCode is intentionally not supported. Its TUI (built on the `opentui` library) silently exits with code 255 over SSH/VM transports — same symptom as [opencode issues #6119](https://github.com/sst/opencode/issues/6119) and [#24475](https://github.com/sst/opencode/issues/24475). The same problems have been reported upstream on Gnome Terminal and other non-kitty hosts, so the root cause is in the library, not in xitbox. For opencode, run `opencode web` outside xitbox.

Users can add custom agents via config.

---

## 8. Future Considerations (Out of Scope for V1)

- **Inter-sandbox communication** — Shared Unix socket directory or lightweight message bus for agents in different sandboxes to coordinate
- **macOS Apple Container backend** — For macOS 26+ users who want native microVMs instead of Lima
- **Windows support** — WSL2 backend
- **Content-aware blocking** — Block outbound requests that contain secrets (regex/entropy scanning)
  - **Auto-allow mode** — Interactive prompt when a new domain is hit (like a firewall popup)
  - **Snapshot/restore** — Save sandbox state and resume later
  - **Session SSH support** — Per-session SSH key sharing for git push (see bead Code-80f)

---

## 9. Non-Goals

- **Perfect jail / threat-proof isolation** — We use process-level sandboxing, not hardware VMs. A kernel exploit or sandbox escape is out of scope.
- **TLS inspection / MITM** — We trust the whitelist. Inspecting encrypted traffic adds complexity and breaks certificate pinning.
- **Container/Docker integration** — Agents cannot run Docker inside the sandbox. Use a VM-based tool if you need that.
- **GUI applications** — No display forwarding. CLI agents only.
- **Command whitelisting/blacklisting** — We don't block `rm -rf /`. Filesystem isolation is the protection.
- **SSH key sharing by default** — SSH keys are never mounted unless explicitly enabled per-session (deferred to bead Code-80f).
