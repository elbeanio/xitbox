# xitbox — Configuration Model

> **Status:** Design discussion, not yet implemented.
> **Last update:** 2026-06-05

This document captures the configuration model for xitbox: a three-level
hierarchy (global / agent / project), the corporate vs. home environment
problem, the persist-dir security model, and the design rationale — including
the ideas we considered and rejected.

---

## 1. Goals

- **Default-deny everything.** The user opts in to network, filesystem, and
  env access. Nothing leaks by accident.
- **Predictable override semantics.** A user can answer "where does this
  rule come from?" without reading code.
- **Reproducible across machines.** A config that works on the office laptop
  works the same on the home desktop, modulo a single flag.
- **No escalation via the project dir.** The agent never reads its own jail
  terms.

---

## 2. The Three-Level Hierarchy

xitbox loads config from three locations, merged in this order (later wins):

```
global   <   agent   <   project
```

- **Global** (`~/.xitbox/config.yaml`) — defaults. Network deny list,
  default allow list (github.com, *.github.com, pypi.org, …), filesystem
  defaults, resource limits, env filter on/off.
- **Agent** (`~/.xitbox/agents/<agent>/config.yaml`) — per-agent overrides.
  Anthropic API host for claude, aider-specific Python needs, etc.
- **Project** (`~/.xitbox/projects/<project>/config.yaml`) — per-project
  overrides. Internal API allow list, project-specific shares, project deny
  list, etc.

**There is no fourth "environment" layer.** Environment is a CLI flag
(see §3).

### Why no `.xitbox.yaml` in the project dir

The first instinct is to put project config in the project (`./.xitbox.yaml`).
We do not. The agent has read access to the project dir, which means it
reads its own jail terms — and any future "auto-allow" feature becomes a
prompt-injection target. Project config lives at
`~/.xitbox/projects/<project>/` instead, outside the sandbox's reach.

Hard rule: **xitbox never reads any config from the project dir.** If a
`.xitbox.yaml` is found in the cwd, xitbox errors out rather than silently
honoring it.

---

## 3. Environment: CLI Flag, Not Config Layer

The user has two environments: **home** (open internet) and **corporate**
(behind a corp proxy with internal registries and a custom CA chain). This
is not unique to xitbox — every developer tool faces it. We model it as a
required CLI flag, not a config layer:

```bash
xitbox --env=corporate claude
XITBOX_ENV=corporate xitbox claude
```

- **No default.** No `environment: home` field in the global config.
  Persisting an active environment in YAML is a footgun: the user toggles
  it once and forgets, or shares a config online with the wrong value baked
  in.
- **Required, not optional.** If `--env` is unset, xitbox uses no env
  (just global + agent + project). Most home use is "no env".
- **Per-shell via `XITBOX_ENV`.** `export XITBOX_ENV=corporate` in the
  office's `.zshrc` makes it sticky for that shell. Unset it for home.

### Why not `environment: home` in YAML?

- Hidden state. Users look at the CLI / help text, not their YAML.
- Forgotten switches. Toggle once, run for months in the wrong env.
- Shared-config footgun. Copy-paste from a gist = wrong env for the
  copier.

### Auto-discovery of corp proxy

The corporate proxy URL is usually not something the user types. It's
*discovered* from:

- macOS system proxy (`scutil --proxy`)
- Linux desktop env (`$HTTP_PROXY` set by the login session)
- Group policy / MDM push
- SSO client (Zscaler, Netskope, etc.) — sets the env var transparently

So xitbox reads `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` from the host
environment automatically and uses it as the Guardian's upstream proxy.
The user does not type their corp proxy URL into xitbox config. If
`HTTP_PROXY` is set, that's where the traffic goes after Guardian's
allow/deny check.

A `guardian_upstream_proxy:` field exists only as an explicit override
for cases where the OS env doesn't carry the right value (rare).

---

## 4. Directory Layout

```
~/.xitbox/
├── config.yaml                      # global defaults
├── agents/
│   ├── claude/
│   │   ├── config.yaml              # agent-specific overrides
│   │   └── persist/                 # bind-mounted to ~/.<agent> in VM
│   ├── gemini/
│   │   ├── config.yaml
│   │   └── persist/
│   └── ...
├── projects/
│   ├── myapp/
│   │   └── config.yaml              # project overrides
│   └── otherapp/
│       └── config.yaml
├── environments/
│   ├── home.yaml                    # optional, explicit home overrides
│   └── corporate.yaml               # corp proxy, CA, internal registries
└── logs/
    └── denied.jsonl
```

### Layout principles

- **One top-level dir per concern.** `agents/`, `projects/`, `environments/`,
  `logs/`. Easy to back up, easy to wipe per-agent, no surprises.
- **Per-agent subdir owns both config and persist.** `~/.xitbox/agents/claude/`
  is the canonical "claude state" — config, persistence, and (later) cache
  all live there.
- **Flat, no nesting beyond one level.** `~/.xitbox/<thing>/<name>/...`. No
  recursive categories.

### Migration note

We currently have `~/.xitbox/persist/<agent>/` (used). Pre-release, so
restructuring is free, but a one-shot migration moves existing data from
`~/.xitbox/persist/<agent>/` → `~/.xitbox/agents/<agent>/persist/`.

---

## 5. Merge Semantics

The merge is field-specific. We do not use a single "deep merge" — that
produces surprising behavior for lists.

| Field type | Merge semantic | Example |
|---|---|---|
| **Scalars** (`default_policy`, `cwd`, `memory`, etc.) | Last-wins | `cwd: rw` (global) overridden by `cwd: ro` (project) |
| **Maps** (`env.allow`, `network.allow` keyed lists) | Deep merge (key-level last-wins) | — |
| **Lists** (`network.allow`, `network.deny_list`, `filesystem.shares`) | **Concat, then deduplicate** | `allow: [a, b]` (global) + `allow: [b, c]` (project) → `[a, b, c]` |
| **Resources** (`memory`, `cpus`, `pids`) | Last-wins | — |
| **`ca` / `trust_bundle`** | Last-wins (one bundle) | — |
| **`guardian_upstream_proxy`** | Last-wins (one proxy) | — |

### Why concat for `allow` / `deny_list`?

A user wants the project's allow list to be *additive* to the global
allow list. They don't want to re-list `github.com` and `*.github.com` in
every project. But they want to be able to deny something at the project
level. The cleanest model: allow is union (concat + dedupe), deny list
is also union, and deny wins over allow at lookup time. That gives:

- `deny_list_extra:` at the project level adds new denies (e.g. known
  exfil destinations in this project's threat model)
- `allow:` is naturally additive
- The Guardian check is: blocked if (in deny_list) OR (not in allow) —
  deny list is checked first, so it always wins

### Merge order (recap)

```
effective = global
           + environment(name)        # if --env=<name> is set
           + agent(<name>)
           + project(<name>)
```

Each layer is a complete, well-formed config (or empty). Missing files
are skipped, not errors. A malformed file is a hard error (xitbox won't
start with a broken config).

---

## 6. Per-Project Identification

How does xitbox know which project you're in?

- **Default: git remote URL.** If the cwd is inside a git repo with a
  remote, use the remote (e.g. `git@github.com:acme/myapp.git` →
  project name `github.com-acme-myapp`).
- **Fallback: git toplevel dir basename.** No remote, use the basename of
  `git rev-parse --show-toplevel`.
- **Last resort: hash of absolute path.** Reproducible, collision-free,
  ugly. User should run `xitbox project init` to set a real name.
- **Override: `--name=<name>`** on `xitbox project init`.

### `xitbox project init` is friendly

```bash
$ cd ~/work/myapp
$ xitbox project init
✓ Detected git remote: git@github.com:acme/myapp.git
✓ Project name: github.com-acme-myapp
✓ Created ~/.xitbox/projects/github.com-acme-myapp/config.yaml
  (edit it to add project-specific rules)
```

If the user runs `xitbox claude` from an unregistered project, xitbox
**does not error out**. It auto-creates a stub config at
`~/.xitbox/projects/<derived-name>/config.yaml` and proceeds. The user
can edit later. This matches `git init`'s friendliness.

---

## 7. Corporate Environment: Proxy, CA, Registries

A corporate environment has four concerns:

1. **Corp proxy** — auto-discovered from `$HTTP_PROXY` (see §3). Not in
   xitbox config.
2. **CA chain** — needs to be installed inside the VM, not just symlinked.
3. **Internal package registries** — npm, pip, go, rubygems, crates.
4. **Internal API endpoints** — internal LLM proxies, internal services.

### 7.1 CA chain

```yaml
# ~/.xitbox/environments/corporate.yaml
network:
  ca: ~/work/certs/corp-ca-bundle.pem
```

The field is `ca`, not `ca_bundle`. On startup, xitbox:

1. Reads the bundle from the host.
2. Copies it into the VM at `/usr/local/share/ca-certificates/xitbox-corp.crt`.
3. Runs `update-ca-certificates` inside the VM.
4. Sets per-tool env vars as needed:
   - `SSL_CERT_FILE` (most CLI tools)
   - `NODE_EXTRA_CA_CERTS` (Node / npm)
   - `PIP_CERT_BUNDLE` (pip)
   - `GIT_SSL_CAINFO` (git)

The user types one path. xitbox handles the per-tool mess.

### 7.2 Internal registries

```yaml
# ~/.xitbox/environments/corporate.yaml
env:
  inject:
    NPM_CONFIG_REGISTRY: https://registry.corp.example.com
    PIP_INDEX_URL: https://pypi.corp.example.com/simple
    GOPROXY: https://goproxy.corp.example.com,direct
    GONOSUMCHECK: "*"
```

These get injected into the agent's env, on top of the env-filter allow
list. We do not add them to the user's global allow list — they're
environment-specific.

### 7.3 Internal API endpoints

```yaml
# ~/.xitbox/environments/corporate.yaml
network:
  allow:
    - llm-proxy.corp.example.com
    - registry.corp.example.com
    - pypi.corp.example.com
```

Merged into the agent's allow list (additive, per §5).

### 7.4 Internal LLM endpoint example

If the user is behind a corporate Claude proxy:

```yaml
# ~/.xitbox/agents/claude/config.yaml
network:
  allow:
    - llm-proxy.corp.example.com    # corp proxy, NOT api.anthropic.com
env:
  allow:
    - ANTHROPIC_BASE_URL              # override API endpoint
    - ANTHROPIC_API_KEY
```

The project layer can override `ANTHROPIC_BASE_URL` per-project for
experimentation.

---

## 8. The Persist-Dir Security Problem

The agent's persist dir (`~/.xitbox/agents/<agent>/persist/`, mounted at
`~/.<agent>` in the VM) is a read/write side channel into the agent's
config. Risks:

- **API key leakage.** The agent reads `auth.json` / equivalent files in
  the persist dir. Env filtering is a sieve — anything in a file is game.
- **Skill / plugin exfiltration.** A compromised or buggy plugin can read
  other plugins' configs from the persist dir.
- **Configuration tampering.** The agent can modify its own skills, install
  new ones, change settings. Persists across sandboxes.
- **Cross-agent contamination.** Each agent's persist dir is separate, but
  the model is "agent has full r/w to its config", which is the wrong
  default for a security tool.

### Three mitigations, in increasing strength

**Light — read-only mount + tmpfs session state.**

Mount the persist dir **read-only**. Agent's in-session writes go to a
tmpfs inside the VM. On exit, xitbox diffs the tmpfs against the persist
dir and prompts the user to commit / discard changes. Slow but safe.

**Medium — per-subdir r/w modes.**

Mount only the subdirs the agent needs read+write to (e.g.
`~/.claude/projects/`, `~/.claude/sessions/`) read-write. Mount the rest
(`skills/`, `agents/`, `auth.json`) read-only. Requires knowing each
agent's internal layout, but it's a per-agent config concern.

```yaml
# ~/.xitbox/agents/claude/config.yaml
filesystem:
  persist_mounts:
    - source: projects     # rw — agent needs to write session state
    - source: sessions     # rw
    - source: skills       # ro — read your skills, don't modify
    - source: agents       # ro
    - source: auth.json    # ro — agent needs the key, not write access
```

**Heavy — no persistence, install from manifest.**

Don't persist skills/plugins at all. The user maintains a manifest:

```yaml
# ~/.xitbox/agents/claude/manifest.yaml
skills:
  - source: github.com/anthropics/skills/commit-review
  - source: ~/work/my-skills/private-skill-1
plugins:
  - name: anthropic-official
```

Each sandbox start, xitbox installs the manifest into a fresh persist
dir. Reproducible, auditable, no drift, no "the agent modified its own
config while I wasn't looking".

**Recommendation:** start with **medium** (per-subdir r/w modes, default
deny for the persist dir). Move to **heavy** if the per-agent
subdir-permission config gets too painful to maintain.

### Why we don't just keep the whole dir r/w

Because it directly contradicts the threat model. The persist dir
contains secrets (auth tokens), shared state (skills, plugins), and
user-defined behavior (custom slash commands, agent definitions).
Letting the agent modify these is giving the agent root on its own jail.

---

## 9. `xitbox config show`: Non-Negotiable

The merge code will have bugs. The only way to debug is to print the
effective config with provenance. Ship `xitbox config show` (or
`xitbox config diff`) **with the config hierarchy**, not later.

```bash
$ xitbox config show --env=corporate claude
# Loaded from:
#   ~/.xitbox/config.yaml
#   ~/.xitbox/environments/corporate.yaml
#   ~/.xitbox/agents/claude/config.yaml
#   ~/.xitbox/projects/github.com-acme-myapp/config.yaml

network:
  default_policy: deny                  # global
  deny_list: [api.openai.com, ...]      # global
  allow:
    - github.com                        # global
    - pypi.org                          # global
    - llm-proxy.corp.example.com        # env:corporate
    - registry.corp.example.com         # env:corporate
  ca: ~/work/certs/corp-ca-bundle.pem   # env:corporate

filesystem:
  cwd: rw                               # global
  shares: []                            # (none in any layer)

env:
  filter: true                          # global
  allow: [PATH, HOME, TERM, ...]        # global + agent
  inject:
    NPM_CONFIG_REGISTRY: https://...    # env:corporate

guardian:
  upstream_proxy: http://proxy.corp:8080  # auto-detected from $HTTP_PROXY
```

Every line annotated with its source. "Why is this allowed?" → look at
the annotation. This is the only sustainable way to debug a 3-layer
merge.

---

## 10. Rejected Approaches (and Why)

This section captures ideas we considered and threw out. Reading the
failures is as important as reading the design.

### 10.1 Four-layer config: `global < environment < agent < project`

First proposal. The named environment was a config layer with its own
file, e.g. `environment: home` in global config.

**Rejected because:**
- Persisting active environment in YAML is a footgun (forgotten switches,
  shared-config copy-paste, hidden state).
- Four layers is too many. Environments are a property of the *runtime*
  (which network am I on right now), not a static config layer.
- CLI flag is more explicit, easier to script, harder to get wrong.

**Replacement:** environments are a CLI flag (`--env=NAME`), not a config
layer. The env *file* (if present) supplies environment-specific defaults
that merge in alongside agent and project.

### 10.2 `proxy_env` injected into the agent's environment

First proposal. Set `HTTP_PROXY=http://corp-proxy:8080` in the agent's
env, and the agent uses the corp proxy directly.

**Rejected because:**
- This bypasses the Guardian entirely. The agent's `api.openai.com` call
  goes straight to the corp proxy, uninspected. Defeats the entire
  point of xitbox.
- `proxy_env` is a misnomer — it's not "env for the agent", it's
  "Guardian's upstream".

**Replacement:** corp proxy is Guardian's upstream. The agent always
talks to Guardian; Guardian optionally chains through the corp proxy.
Auto-discovered from `$HTTP_PROXY` so the user never types it.

### 10.3 `ca_bundle: <path>` as a one-shot config field

First proposal. The user points at their corp CA bundle, xitbox does
the rest.

**Rejected because:**
- One field is not enough. Different tools honor different env vars
  (`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `PIP_CERT_BUNDLE`,
  `GIT_SSL_CAINFO`, …). Setting only `SSL_CERT_FILE` works for ~30% of
  tools.
- The bundle needs to be installed into the VM's system trust store
  (`update-ca-certificates`), not just symlinked.

**Replacement:** the field is `ca: <path>`. xitbox copies the bundle
into the VM, runs `update-ca-certificates`, and sets the per-tool env
vars. The user types one path.

### 10.4 `environment: home` persisted in global YAML

First proposal. The global config had an `environment:` field that
selected which env file to load.

**Rejected because:**
- Hidden state. Users look at the CLI, not their YAML.
- Forgotten switches. Toggle once, run for months in the wrong env.
- Shared-config footgun. Copy-paste from a gist = wrong env for the
  copier.

**Replacement:** environment is a required CLI flag or env var. No
persisted state.

### 10.5 Project config in the project dir (`./.xitbox.yaml`)

First instinct. The "standard" way (`.eslintrc`, `.prettierrc`, etc.).

**Rejected because:**
- Agent has read access to the project dir.
- The agent reads its own jail terms. Any future auto-allow feature
  becomes a prompt-injection target.
- Even worse: agent has *write* access. A malicious or compromised agent
  can edit its own config to widen permissions for next time.

**Replacement:** project config lives at
`~/.xitbox/projects/<project>/config.yaml`. xitbox never reads from the
project dir. If a `.xitbox.yaml` is found in cwd, xitbox errors out.

### 10.6 Per-agent config (CLAUDE.md, skills) mirrored in `~/.xitbox/projects/`

First instinct. The user said "skills, plugins, MCP servers — these are
usually shared across projects but might have project-specific bits."

**Rejected because:**
- The agent's own per-project config (CLAUDE.md, project-level skills)
  *belongs* in the project dir. The agent is designed to read it from
  cwd. We don't need to mirror it.
- We were conflating two things: the *agent's* project config (which
  lives in the project) and the *sandbox policy* for the project
  (which lives in `~/.xitbox/projects/`).

**Replacement:** xitbox only ever handles sandbox policy (network,
filesystem, env, resources). Agent-level per-project config (CLAUDE.md,
skills, etc.) stays in the project dir and is read by the agent
normally. The two are separate concerns.

### 10.7 One VM per `(agent, project)` pair

First instinct. "Isolation = VM, and projects need isolation too."

**Rejected because:**
- The VM boundary is for isolating the *agent from your host* — SSH
  keys, browser cookies, host env. Not for isolating projects from
  each other.
- VM-per-(agent, project) at 5 projects = 5 VMs × 4 agents = 20 VMs
  × 400MB each = 8GB of disk for no real security benefit.
- Switching projects means restarting the VM, which is slow.

**Replacement:** one VM per agent (`xitbox-claude`, `xitbox-gemini`,
…). Project config is *data* loaded on the host at run time, used to
configure mounts, env, network rules. The VM is project-agnostic.
Switching projects = restarting the agent inside the same VM.

### 10.8 Project identified by directory basename

First instinct. `cd ~/work/myapp; xitbox project init` → project
`myapp`.

**Rejected because:**
- Two projects named `backend` in different orgs collide.
- Git worktrees have different basenames than the main checkout.
- `myapp` is not unique. `github.com-acme-myapp` is.

**Replacement:** default to git remote URL → project name. Fallback to
git toplevel basename. Last resort: hash of absolute path. Explicit
`--name=` override on `xitbox project init`.

### 10.9 Persist dir mounted r/w (the current behavior)

First instinct. The agent needs to write to its config dir, so mount
it r/w.

**Rejected because:**
- The agent has full r/w to its own secrets, skills, and config. Any
  compromise of the agent = compromise of its own jail.
- A buggy skill can read sibling skills' configs.
- The agent can edit its own config to widen permissions (add a skill,
  install a backdoored plugin, change a setting) without the user
  noticing.

**Replacement:** per-subdir r/w modes (medium mitigation in §8) as the
default. Agent has r/w only to the subdirs it needs (sessions,
projects). Skills, plugins, auth files are ro or not mounted.

### 10.10 Required `--env` (no default, even for home)

Considered. Force the user to type `--env=home` every time at home.

**Rejected because:**
- Home is the default case. Forcing a flag is pure friction.
- `XITBOX_ENV=corporate` in the corp `.zshrc` already makes the office
  case zero-friction. At home, the user sets nothing and gets the
  sensible default (no env).
- The user *can* set `XITBOX_ENV=home` explicitly if they want. It's
  not forbidden — it's just not the default.

**Replacement:** `--env` is optional. If unset, no env-specific rules
are applied. The user opts in to environments by name.

---

## 11. Open Questions

Items still to settle before implementation:

1. **Persist-dir model: per-subdir r/w vs. read-only + tmpfs session.**
   Per-subdir is more flexible but requires per-agent knowledge of the
   config layout. Read-only is simpler but kills live-edit workflows.
   *Default: per-subdir r/w with conservative defaults (sessions rw,
   everything else ro). User can override.*

2. **What goes in `~/.xitbox/environments/home.yaml`?** Most "home" rules
   are auto-discovered from the OS env. Do we need a `home.yaml` at all?
   *Default: not required. Only create if you need explicit home
   overrides.*

3. **`xitbox config show` syntax.** Plain YAML with `# source` comments,
   or a dedicated TUI? *Default: YAML to stdout, with comments. Add
   TUI later if needed.*

4. **Project registration friction.** Should `xitbox project init` be
   required, or do we always auto-create? *Default: auto-create with
   sensible name, no error.*

5. **Cross-agent auth.** Some users use the same API key for multiple
   agents. Should the global `env.allow` list cover this, or do we
   duplicate per agent? *Default: agent-level `env.allow` is the
   source of truth, but project config can override.*

6. **Registry overrides — env var vs. tool config.** Setting
   `NPM_CONFIG_REGISTRY` works for npm but not for yarn / pnpm. Same
   problem as the CA bundle. Do we centralize registry overrides, or
   punt to "configure your tools in their own configs"? *Default:
   inject env vars, document the limitations, fix per-tool as users
   report issues.*

7. **Migration of existing `~/.xitbox/persist/<agent>/` dirs.** One-shot
   script to move into `~/.xitbox/agents/<agent>/persist/`. *Default:
   ship the script, run on first startup with `--migrate`.*

---

## 12. Implementation Order

When we're ready to build this:

1. **Directory layout + migration script.** Move `persist/<agent>/` →
   `agents/<agent>/persist/`. Add `agents/<agent>/config.yaml` and
   `projects/<name>/config.yaml` (empty for now).
2. **Multi-source config loader.** Load global + env + agent + project,
   apply merge semantics from §5.
3. **`xitbox config show`.** The merge code is useless without
   visibility. Ship the debug command with the loader.
4. **Project identification.** `xitbox project init`, auto-creation
   from cwd.
5. **Environment flag.** `XITBOX_ENV` / `--env=`, load from
   `environments/<name>.yaml`, inject env vars, set Guardian upstream.
6. **CA bundle handling.** Copy-into-VM, `update-ca-certificates`, per-
   tool env vars.
7. **Persist-dir r/w modes.** Per-subdir mounts, conservative defaults.
8. **Migration of existing config defaults** in
   `pkg/config/config.go` (move from a single `DefaultConfig()` to a
   layered loader).
