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
				// LLM providers — agents need these to function.
				// Corporate users who want to force routing through an internal
				// proxy should move these to deny_list in their config.
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
			AgentPersistence: map[string]string{
				"claude": defaultPersistPath("claude"),
				"aider":  defaultPersistPath("aider"),
				"codex":  defaultPersistPath("codex"),
				"cline":  defaultPersistPath("cline"),
				"gemini": defaultPersistPath("gemini"),
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

	// Load default config if it exists
	defaultPath := DefaultConfigPath()
	if _, err := os.Stat(defaultPath); err == nil {
		data, err := os.ReadFile(defaultPath)
		if err != nil {
			return nil, fmt.Errorf("read default config: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse default config: %w", err)
		}
	}

	// Load project config if it exists
	if projectDir != "" {
		projectPath := filepath.Join(projectDir, ".xb.yaml")
		if _, err := os.Stat(projectPath); err == nil {
			data, err := os.ReadFile(projectPath)
			if err != nil {
				return nil, fmt.Errorf("read project config: %w", err)
			}
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parse project config: %w", err)
			}
		}
	}

	// TODO: apply CLI overrides

	return cfg, nil
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
