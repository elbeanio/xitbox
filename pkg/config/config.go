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
			DenyList: []string{
				"api.openai.com",
				"api.anthropic.com",
				"generativelanguage.googleapis.com",
				"api.cohere.com",
				"api.mistral.ai",
				"api.groq.com",
				"api.together.xyz",
			},
			Allow: []string{
				"github.com",
				"*.github.com",
				"raw.githubusercontent.com",
				"registry.npmjs.org",
				"pypi.org",
				"pythonhosted.org",
				"crates.io",
				"golang.org",
				"go.dev",
				"proxy.golang.org",
				"sum.golang.org",
				"rubygems.org",
				"api.github.com",
				"objects.githubusercontent.com",
				"codeload.github.com",
				"140.82.0.0/16",
			},
			LogFile: defaultLogPath(),
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

// Config is the top-level configuration for xitbox.
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
		projectPath := filepath.Join(projectDir, ".xitbox.yaml")
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
		return filepath.Join(home, "Library", "Application Support", "xitbox", "default.yaml")
	}
	// Linux and others
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "xitbox", "default.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "xitbox", "default.yaml")
}

// defaultLogPath returns the default log file path.
func defaultLogPath() string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Logs", "xitbox", "denied.jsonl")
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "xitbox", "denied.jsonl")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "xitbox", "denied.jsonl")
}

// defaultPersistPath returns the default persistence directory for an agent.
func defaultPersistPath(agent string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".xitbox", "persist", agent)
}
