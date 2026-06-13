# xb — Configuration

---

## Current Config Format

Config is a single YAML file. xb loads two sources in order, with later values winning:

1. **Default config** — `~/.config/xb/default.yaml` (Linux) or `~/Library/Application Support/xb/default.yaml` (macOS)
2. **Project config** — `.xitbox.yaml` in the current working directory (mounted read-only inside the sandbox)

List fields (`allow`, `deny_list`, `ca_bundle_env_vars`) are **concatenated** — project values add to defaults, not replace them.

### Full config reference

```yaml
network:
  default_policy: deny           # only "deny" is supported

  # Always blocked, checked before the allowlist
  deny_list:
    - api.openai.com
    - api.anthropic.com
    - generativelanguage.googleapis.com
    - api.cohere.com
    - api.mistral.ai

  # Allowed destinations (domains, globs, CIDRs)
  allow:
    - github.com
    - '*.github.com'
    - registry.npmjs.org
    - pypi.org
    - crates.io
    - golang.org
    - 10.0.0.0/8

  # JSONL audit log path
  log_file: ~/.local/share/xb/denied.jsonl

  # Forward allowed traffic through a corporate HTTP proxy
  # Supports basic auth: http://user:pass@proxy.corp.internal:8080
  upstream_proxy: http://proxy.corp.internal:8080

  # Path to a PEM CA bundle to inject into sandbox env
  ca_bundle: /etc/corp-ca.pem

  # Env vars to set to the ca_bundle path.
  # Default list is below; extend for custom toolchains.
  # This list is concatenated with the default, not replaced.
  ca_bundle_env_vars:
    - NODE_EXTRA_CA_CERTS    # Node.js
    - REQUESTS_CA_BUNDLE     # Python requests/httpx
    - SSL_CERT_FILE          # curl, OpenSSL tools
    - CURL_CA_BUNDLE         # curl
    - GIT_SSL_CAINFO         # git
    - MY_CUSTOM_CA_VAR       # add your own

filesystem:
  cwd: rw    # "rw" (default) or "ro"

  # Additional host paths to expose inside the sandbox
  shares:
    - path: /home/user/shared-lib
      mode: ro    # or rw
    - path: /tmp/agent-cache
      mode: rw

  # Agent config persistence dirs.
  # These get write access inside the sandbox.
  # Defaults are auto-detected by command name.
  agent_persistence:
    claude: ~/.xb/persist/claude    # legacy — now ~/.claude directly
    gemini: ~/.xb/persist/gemini    # legacy — now ~/.gemini directly

resources:
  memory: 4g
  cpus: 2
  pids: 4096

env:
  filter: true    # strip env to the allowlist below
  allow:
    - PATH
    - HOME
    - TERM
    - LANG
    - EDITOR
    - ANTHROPIC_API_KEY
    - OPENAI_API_KEY
    - GEMINI_API_KEY
```

### Project config example (`.xitbox.yaml`)

```yaml
network:
  allow:
    - api.mycompany.internal

filesystem:
  shares:
    - path: /home/user/company-certs
      mode: ro
```

---

## Security Notes

### `.xitbox.yaml` in project directory

`.xitbox.yaml` is mounted **read-only** inside the sandbox. The sandboxed process can read it (useful for debugging what's allowed) but cannot modify it to broaden allow rules for future runs.

However, xb reads it from the host filesystem before the sandbox starts. A malicious `.xitbox.yaml` committed to a repo (e.g. `allow: ["*"]`) would be loaded if you run xb in that directory. Treat `.xitbox.yaml` in code review like any other security-relevant file.

### Config precedence for deny_list

The deny list always wins over the allowlist regardless of source. Even if a project `.xitbox.yaml` adds a domain to `allow`, if it's also in the (default) `deny_list`, it remains blocked. To unblock a deny-listed domain, you must remove it from `deny_list` in your default config.

---

## Planned (Not Yet Implemented)

The following config features have been designed but not built:

### Per-agent config files

Agent-specific overrides at `~/.xb/agents/<agent>/config.yaml` would allow setting per-agent network rules, env vars, etc. without affecting other agents.

### `xb --config show`

Print the effective merged config with provenance annotations (which file each value came from). Useful for debugging merge behavior.

### Environment presets

Named environment configs (e.g. `~/.xb/environments/corporate.yaml`) that can be activated via env var (`XB_ENV=corporate`) rather than editing the default config. Designed for developers who switch between home and corporate networks.

### `xb --check` / `xb doctor`

Dependency check and setup verification command. Currently replaced by informational error messages at startup.
