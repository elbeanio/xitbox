package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

// DefaultConfig returns the built-in default configuration.
func DefaultConfig() *Config {
	return &Config{
		Network: NetworkConfig{
			DefaultPolicy: "deny",
			// DenyList is empty by default — it is an override mechanism for
			// corporate deployments that want to block specific destinations
			// even if a user adds them to the allow list. Personal users
			// typically leave this empty.
			DenyList: []string{},
			Allow: []string{
				// Package registries and dev tooling
				"github.com",
				"*.github.com",
				"raw.githubusercontent.com",
				"api.github.com",
				"objects.githubusercontent.com",
				"codeload.github.com",
				"140.82.0.0/16",
				"registry.npmjs.org",
				"pypi.org",
				"pythonhosted.org",
				"crates.io",
				"golang.org",
				"go.dev",
				"proxy.golang.org",
				"sum.golang.org",
				"rubygems.org",
				// opencode
				"opencode.ai",
				"models.dev",
				// Anthropic auth and API — required for Claude Code login and API calls.
				"claude.ai",
				"*.claude.ai",
				"platform.claude.com",
				"api.anthropic.com",
				"api.openai.com",
				"generativelanguage.googleapis.com",
				"api.cohere.com",
				"api.mistral.ai",
				"api.groq.com",
				"api.together.xyz",
			},
			LogFile: defaultLogPath(),
			// Default env vars injected when ca_bundle is set.
			// Add your own toolchain vars here or in the config file.
			CABundleEnvVars: []string{
				"NODE_EXTRA_CA_CERTS", // Node.js
				"REQUESTS_CA_BUNDLE",  // Python requests / httpx
				"SSL_CERT_FILE",       // curl, OpenSSL-linked tools
				"CURL_CA_BUNDLE",      // curl (explicit)
				"GIT_SSL_CAINFO",      // git
			},
		},
		Filesystem: FilesystemConfig{
			CWD: "rw",
			AllowWrite: []string{
				"~/.claude.json",
			},
			DenyRead: []string{
				"~/.ssh",
				"~/.gnupg",
				"~/.aws/credentials",
				"~/.azure",
				"~/.config/gcloud",
				"~/.netrc",
				"~/.bash_history",
				"~/.zsh_history",
				"~/.local/share/keyrings",
				"~/.docker/config.json",
			},
			AgentPersistence: map[string]string{
				"claude":          defaultPersistPath("claude"),
				"aider":           defaultPersistPath("aider"),
				"codex":           defaultPersistPath("codex"),
				"cline":           defaultPersistPath("cline"),
				"gemini":          defaultPersistPath("gemini"),
				"opencode":        defaultPersistPath("opencode"),
				"opencode-config": defaultPersistPath("opencode-config"),
				"opencode-data":   defaultPersistPath("opencode-data"),
				"opencode-state":  defaultPersistPath("opencode-state"),
				"opencode-cache":  defaultPersistPath("opencode-cache"),
			},
		},
		Resources: ResourceConfig{
			Memory: "4g",
			CPUs:   2,
			PIDs:   4096,
		},
		Env: EnvConfig{
			Filter: true,
			Allow: []string{
				"PATH", "HOME", "TERM", "LANG", "EDITOR",
				"ANTHROPIC_API_KEY", "OPENAI_API_KEY",
				"OPENROUTER_API_KEY", "GEMINI_API_KEY",
				"GROQ_API_KEY", "XAI_API_KEY",
			},
		},
	}
}

// Config is the top-level configuration for xb.
type Config struct {
	Network    NetworkConfig    `yaml:"network"`
	Filesystem FilesystemConfig `yaml:"filesystem"`
	Resources  ResourceConfig   `yaml:"resources"`
	Env        EnvConfig        `yaml:"env"`
}

// NetworkConfig controls network access.
type NetworkConfig struct {
	DefaultPolicy string   `yaml:"default_policy"`
	DenyList      []string `yaml:"deny_list"`
	Allow         []string `yaml:"allow"`
	LogFile       string   `yaml:"log_file"`

	// UpstreamProxy forwards allowed traffic through a corporate proxy.
	// Supports http:// and https:// with optional basic auth:
	//   http://proxy.corp.internal:8080
	//   http://user:pass@proxy.corp.internal:8080
	UpstreamProxy string `yaml:"upstream_proxy,omitempty"`

	// CABundle is the path to a PEM certificate bundle to inject into the
	// sandbox environment. Used when a corporate proxy performs TLS inspection.
	CABundle string `yaml:"ca_bundle,omitempty"`

	// CABundleEnvVars lists the env vars to set to the CABundle path.
	// Defaults to the common set; extend this list for custom toolchains.
	CABundleEnvVars []string `yaml:"ca_bundle_env_vars,omitempty"`
}

// FilesystemConfig controls filesystem access.
type FilesystemConfig struct {
	CWD              string            `yaml:"cwd"`
	AllowWrite       []string          `yaml:"allow_write,omitempty"`
	DenyRead         []string          `yaml:"deny_read,omitempty"`
	Shares           []ShareConfig     `yaml:"shares,omitempty"`
	AgentPersistence map[string]string `yaml:"agent_persistence"`
}

// ShareConfig is an additional host path to expose.
type ShareConfig struct {
	Path string `yaml:"path"`
	Mode string `yaml:"mode"` // "ro" or "rw"
}

// ResourceConfig sets resource limits.
type ResourceConfig struct {
	Memory string `yaml:"memory"`
	CPUs   int    `yaml:"cpus"`
	PIDs   int    `yaml:"pids"`
}

// EnvConfig controls environment variable filtering.
type EnvConfig struct {
	Filter bool     `yaml:"filter"`
	Allow  []string `yaml:"allow"`
}

// Load reads and merges configuration from default, project, and optional override.
func Load(projectDir string, overrides map[string]interface{}) (*Config, error) {
	cfg := DefaultConfig()

	if _, err := os.Stat(DefaultConfigPath()); err == nil {
		cfg = loadAndMerge(cfg, DefaultConfigPath())
	}
	if projectDir != "" {
		p := filepath.Join(projectDir, ".xb.yaml")
		if _, err := os.Stat(p); err == nil {
			cfg = loadAndMerge(cfg, p)
		}
	}

	// TODO: apply CLI overrides

	return cfg, nil
}

// LoadUserOnly reads the user config file without merging with built-in defaults.
// Returns an empty Config if the file doesn't exist.
// Used by --allow to persist only user additions, not the full merged config.
func LoadUserOnly() *Config {
	var cfg Config
	data, err := os.ReadFile(DefaultConfigPath())
	if err != nil {
		return &cfg
	}
	_ = yaml.Unmarshal(data, &cfg)
	return &cfg
}

// loadAndMerge reads a YAML file and merges it into base.
// Scalar fields replace the base value; slice fields are appended (additive).
// This means users only need to list additional entries in their config,
// not replicate the built-in defaults.
func loadAndMerge(base *Config, path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		return base
	}
	var overlay Config
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		return base
	}

	// Scalar fields: overlay wins when set.
	if overlay.Network.DefaultPolicy != "" {
		base.Network.DefaultPolicy = overlay.Network.DefaultPolicy
	}
	if overlay.Network.LogFile != "" {
		base.Network.LogFile = overlay.Network.LogFile
	}
	if overlay.Network.UpstreamProxy != "" {
		base.Network.UpstreamProxy = overlay.Network.UpstreamProxy
	}
	if overlay.Network.CABundle != "" {
		base.Network.CABundle = overlay.Network.CABundle
	}
	if overlay.Filesystem.CWD != "" {
		base.Filesystem.CWD = overlay.Filesystem.CWD
	}
	if overlay.Resources.Memory != "" {
		base.Resources.Memory = overlay.Resources.Memory
	}
	if overlay.Resources.CPUs != 0 {
		base.Resources.CPUs = overlay.Resources.CPUs
	}
	if overlay.Resources.PIDs != 0 {
		base.Resources.PIDs = overlay.Resources.PIDs
	}
	if !overlay.Env.Filter {
		base.Env.Filter = false
	}

	// Slice fields: additive — append unique entries from the overlay.
	base.Network.Allow = appendUnique(base.Network.Allow, overlay.Network.Allow...)
	base.Network.DenyList = appendUnique(base.Network.DenyList, overlay.Network.DenyList...)
	base.Network.CABundleEnvVars = appendUnique(base.Network.CABundleEnvVars, overlay.Network.CABundleEnvVars...)
	base.Filesystem.AllowWrite = appendUnique(base.Filesystem.AllowWrite, overlay.Filesystem.AllowWrite...)
	base.Filesystem.DenyRead = appendUnique(base.Filesystem.DenyRead, overlay.Filesystem.DenyRead...)
	base.Filesystem.Shares = append(base.Filesystem.Shares, overlay.Filesystem.Shares...)
	base.Env.Allow = appendUnique(base.Env.Allow, overlay.Env.Allow...)

	// Map fields: overlay keys win.
	for k, v := range overlay.Filesystem.AgentPersistence {
		base.Filesystem.AgentPersistence[k] = v
	}

	return base
}

func appendUnique(base []string, extras ...string) []string {
	seen := make(map[string]bool, len(base))
	for _, v := range base {
		seen[v] = true
	}
	for _, v := range extras {
		if !seen[v] {
			base = append(base, v)
			seen[v] = true
		}
	}
	return base
}

// SaveDefault writes the config to the default location.
func (c *Config) SaveDefault() error {
	path := DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// DefaultConfigPath returns the platform-specific default config path.
func DefaultConfigPath() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "xb", "default.yaml")
	}
	// Linux and others
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "xb", "default.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "xb", "default.yaml")
}

// defaultLogPath returns the default log file path.
func defaultLogPath() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Logs", "xb", "denied.jsonl")
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "xb", "denied.jsonl")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "xb", "denied.jsonl")
}

// defaultPersistPath returns the default persistence directory for an agent.
func defaultPersistPath(agent string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".xb", "persist", agent)
}
