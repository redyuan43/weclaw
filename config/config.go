package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Config holds the application configuration.
type Config struct {
	DefaultAgent    string                 `json:"default_agent"`
	APIAddr         string                 `json:"api_addr,omitempty"`
	SaveDir         string                 `json:"save_dir,omitempty"`
	MediaServiceURL string                 `json:"media_service_url,omitempty"`
	Agents          map[string]AgentConfig `json:"agents"`
	Bridge          BridgeConfig           `json:"bridge,omitempty"`
}

// BridgeConfig controls optional A2A bridge forwarding for selected chats.
type BridgeConfig struct {
	Enabled           bool     `json:"enabled,omitempty"`
	NodeID            string   `json:"node_id,omitempty"`
	ListenAddr        string   `json:"listen_addr,omitempty"`
	PublicBaseURL     string   `json:"public_base_url,omitempty"`
	PeerNodeID        string   `json:"peer_node_id,omitempty"`
	PeerBaseURL       string   `json:"peer_base_url,omitempty"`
	LocalUserID       string   `json:"local_user_id,omitempty"`
	LocalAgentAliases []string `json:"local_agent_aliases,omitempty"`
	PeerAgentAliases  []string `json:"peer_agent_aliases,omitempty"`
	PeerUserAliases   []string `json:"peer_user_aliases,omitempty"`
	OutboundPrefix    string   `json:"outbound_prefix,omitempty"`
	RequestTimeoutS   int      `json:"request_timeout_seconds,omitempty"`
}

// AgentConfig holds configuration for a single agent.
type AgentConfig struct {
	Type         string            `json:"type"`
	Command      string            `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
	Aliases      []string          `json:"aliases,omitempty"`
	Cwd          string            `json:"cwd,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Model        string            `json:"model,omitempty"`
	SystemPrompt string            `json:"system_prompt,omitempty"`
	Endpoint     string            `json:"endpoint,omitempty"`
	APIKey       string            `json:"api_key,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	MaxHistory   int               `json:"max_history,omitempty"`
}

// BuildAliasMap builds a map from custom alias to agent name from all agent configs.
func BuildAliasMap(agents map[string]AgentConfig) map[string]string {
	reserved := map[string]bool{
		"info": true, "help": true, "new": true, "clear": true, "cwd": true,
	}

	m := make(map[string]string)
	for name, cfg := range agents {
		for _, alias := range cfg.Aliases {
			if reserved[alias] {
				log.Printf("[config] WARNING: alias %q for agent %q conflicts with built-in command, ignored", alias, name)
				continue
			}
			if existing, ok := m[alias]; ok {
				log.Printf("[config] WARNING: alias %q is defined by both %q and %q, using %q", alias, existing, name, name)
			}
			m[alias] = name
		}
	}

	for alias, target := range m {
		if _, isAgent := agents[alias]; isAgent && alias != target {
			log.Printf("[config] WARNING: alias %q (-> %q) shadows agent key %q", alias, target, alias)
		}
	}

	return m
}

// DefaultConfig returns an empty configuration.
func DefaultConfig() *Config {
	return &Config{
		Agents: make(map[string]AgentConfig),
		Bridge: BridgeConfig{RequestTimeoutS: 10},
	}
}

// ConfigPath returns the path to the config file.
func ConfigPath() (string, error) {
	if custom := os.Getenv("WECLAW_CONFIG_PATH"); custom != "" {
		return custom, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "config.json"), nil
}

// Load loads configuration from disk and environment variables.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err != nil {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			loadEnv(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentConfig)
	}
	if cfg.Bridge.RequestTimeoutS == 0 {
		cfg.Bridge.RequestTimeoutS = 10
	}

	loadEnv(cfg)
	return cfg, nil
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("WECLAW_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgent = v
	}
	if v := os.Getenv("WECLAW_API_ADDR"); v != "" {
		cfg.APIAddr = v
	}
	if v := os.Getenv("WECLAW_SAVE_DIR"); v != "" {
		cfg.SaveDir = v
	}
	if v := os.Getenv("WECLAW_MEDIA_SERVICE_URL"); v != "" {
		cfg.MediaServiceURL = v
	}
}

// Save saves the configuration to disk.
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0o600)
}
