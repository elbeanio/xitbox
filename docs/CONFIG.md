# xb — Configuration Reference

---

## Config locations

| Platform | User config | Project config |
|----------|------------|----------------|
| macOS | `~/Library/Application Support/xb/default.yaml` | `.xb.yaml` in project dir |
| Linux | `~/.config/xb/default.yaml` | `.xb.yaml` in project dir |

Run `xb --init` to write the default config to the platform path.

---

## Merge behaviour

Config is loaded additively across three scopes:

1. **Built-in defaults** (hardcoded)
2. **User config** (platform path above)
3. **Project config** (`.xb.yaml` in cwd, merged read-only inside sandbox)

**Slice fields** (`network.allow`, `network.deny_list`, `filesystem.allow_write`, `filesystem.deny_read`, `env.allow`, `network.ca_bundle_env_vars`) are **appended** across scopes — each scope adds unique entries to the previous. You only need to list your extras in project or user config, not replicate the full defaults.

**Scalar fields** (`default_policy`, `cwd`, `upstream_proxy`, etc.) are replaced by the most specific scope that sets them.

---

## Full config reference

```yaml
network:
  # "deny" is the only supported policy — all connections blocked by default.
  default_policy: deny

  # deny_list is always checked first. Matches here are blocked even if the
  # destination is also in the allow list. Use this in corporate deployments
  # to prevent users from accidentally re-enabling public LLM endpoints.
  deny_list: []

  # Allowed destinations. Supports exact domains, *.glob, and CIDRs.
  allow:
    - github.com
    - '*.github.com'
    - registry.npmjs.org
    - pypi.org
    - crates.io
    - golang.org
    - claude.ai           # Claude Code auth
    - '*.claude.ai'
    - platform.claude.com
    - api.anthropic.com
    - api.openai.com
    - 10.0.0.0/8          # internal CIDR example

  # JSONL audit log. Each line: {"ts":"...","dest":"...","action":"allow|deny","reason":"..."}
  log_file: ~/Library/Logs/xb/denied.jsonl  # macOS
  # log_file: ~/.local/share/xb/denied.jsonl  # Linux

  # Forward allowed traffic through a corporate HTTP proxy.
  # Supports basic auth: http://user:pass@proxy.corp.internal:8080
  upstream_proxy: http://proxy.corp.internal:8080

  # Path to a PEM CA bundle to inject into the sandbox environment.
  # Use when a corporate proxy performs TLS inspection.
  ca_bundle: /etc/corp-ca.pem

  # Env vars to set to the ca_bundle path inside the sandbox.
  # Additive: entries here are appended to the defaults below.
  # Default set (always included when ca_bundle is set):
  #   NODE_EXTRA_CA_CERTS, REQUESTS_CA_BUNDLE, SSL_CERT_FILE,
  #   CURL_CA_BUNDLE, GIT_SSL_CAINFO
  ca_bundle_env_vars:
    - MY_CUSTOM_CA_VAR    # add your toolchain's variable here

filesystem:
  # "rw" (default) or "ro" — controls the cwd mount mode.
  cwd: rw

  # Additional paths the sandboxed process can write to (macOS only — on Linux,
  # the host home is not mounted so these paths simply don't exist).
  # Additive: entries here are appended to the built-in defaults.
  # Built-in defaults: cwd, /tmp, /private/tmp, /private/var/folders/,
  #   and ~/.claude.json (for Claude Code atomic writes).
  # File paths use a regex prefix rule so atomic temp files (e.g.
  # ~/.claude.json.tmp.<pid>.<hash>) are also covered.
  allow_write:
    - ~/some/tool/state

  # Paths the sandboxed process cannot read (macOS only — on Linux these
  # paths are not mounted at all).
  # Additive: entries here are appended to the built-in defaults.
  # Built-in defaults: ~/.ssh, ~/.gnupg, ~/.aws/credentials, ~/.azure,
  #   ~/.config/gcloud, ~/.netrc, ~/.bash_history, ~/.zsh_history,
  #   ~/.local/share/keyrings, ~/.docker/config.json
  deny_read:
    - ~/.config/some-tool/token

  # Additional host paths to expose inside the sandbox.
  shares:
    - path: /home/user/shared-lib
      mode: ro    # or rw
    - path: /tmp/agent-cache
      mode: rw

  # Agent config persistence dirs (controls where agent settings are stored
  # and where write access is granted inside the sandbox).
  # Default dirs are auto-detected by command name.
  agent_persistence:
    claude: ~/.xb/persist/claude
    aider:  ~/.xb/persist/aider
    codex:  ~/.xb/persist/codex
    cline:  ~/.xb/persist/cline
    gemini: ~/.xb/persist/gemini

resources:
  memory: 4g   # not yet enforced — placeholder for future cgroups support
  cpus: 2
  pids: 4096

env:
  filter: true    # strip environment to the allowlist below
  # Additive: entries here are appended to the defaults.
  # Default set: PATH, HOME, TERM, LANG, EDITOR,
  #   ANTHROPIC_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY,
  #   GEMINI_API_KEY, GROQ_API_KEY, XAI_API_KEY
  allow:
    - MY_CUSTOM_VAR
```

---

## Project config example (`.xb.yaml`)

Only list what differs from the defaults — everything is additive.

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

## Corporate config example

```yaml
network:
  upstream_proxy: http://proxy.corp.internal:8080
  ca_bundle: /etc/corp-ca.pem
  ca_bundle_env_vars:
    - MY_INTERNAL_TOOL_CA

  # Block public LLM endpoints; deny_list overrides allow
  deny_list:
    - api.openai.com
    - api.anthropic.com
    - generativelanguage.googleapis.com

  allow:
    - llm-proxy.corp.internal
    - registry.corp.internal
```

---

## Security notes

### `.xb.yaml` in project directory

`.xb.yaml` is mounted **read-only** inside the sandbox. The sandboxed process can read it (useful for debugging) but cannot modify it to broaden allow rules for future runs.

However, xb reads it from the host filesystem before the sandbox starts. A malicious `.xb.yaml` committed to a repo (e.g. `allow: ["*"]`) would be loaded if you run xb in that directory. Treat `.xb.yaml` in code review like any other security-relevant file.

### `deny_list` vs `allow`

`deny_list` always takes precedence over `allow` regardless of which scope a rule comes from. To unblock a deny-listed domain, you must remove it from `deny_list` in your default config.

### macOS read protection

`deny_read` entries generate Seatbelt `(deny file-read*)` rules that are more specific than the `(allow default)` at the top of the profile, so they take effect. The default blocklist covers common credential files; extend it with `deny_read` in your config.

### Linux read protection

On Linux, the host home directory is not mounted into the bwrap sandbox at all. `deny_read` has no effect because the paths simply don't exist inside the sandbox. The protection is structural.
