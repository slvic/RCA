package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	LLM struct {
		APIKey string `yaml:"api_key"`
		Model  string `yaml:"model"`
	} `yaml:"llm"`

	MCPServers map[string]MCPServerConfig `yaml:"mcp_servers"`

	Webhook struct {
		Port              int           `yaml:"port"`
		DedupTTL          time.Duration `yaml:"dedup_ttl"`
		AllowedSeverities []string      `yaml:"allowed_severities"`
	} `yaml:"webhook"`

	Investigation struct {
		Timeout      time.Duration `yaml:"timeout"`
		BaselineDays int           `yaml:"baseline_days"`
		AnomalyZScore float64      `yaml:"anomaly_zscore"`
	} `yaml:"investigation"`

	Output struct {
		SlackWebhookURL string `yaml:"slack_webhook_url"`
	} `yaml:"output"`
}

type MCPServerConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	applyDefaults(&cfg)
	applyEnvOverrides(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.LLM.Model == "" {
		cfg.LLM.Model = "claude-sonnet-4-20250514"
	}
	if cfg.Webhook.Port == 0 {
		cfg.Webhook.Port = 8080
	}
	if cfg.Webhook.DedupTTL == 0 {
		cfg.Webhook.DedupTTL = 10 * time.Minute
	}
	if len(cfg.Webhook.AllowedSeverities) == 0 {
		cfg.Webhook.AllowedSeverities = []string{"critical", "page"}
	}
	if cfg.Investigation.Timeout == 0 {
		cfg.Investigation.Timeout = 5 * time.Minute
	}
	if cfg.Investigation.BaselineDays == 0 {
		cfg.Investigation.BaselineDays = 7
	}
	if cfg.Investigation.AnomalyZScore == 0 {
		cfg.Investigation.AnomalyZScore = 3.0
	}
	for name, srv := range cfg.MCPServers {
		if srv.Timeout == 0 {
			srv.Timeout = 30 * time.Second
			cfg.MCPServers[name] = srv
		}
	}
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("SLACK_WEBHOOK_URL"); v != "" {
		cfg.Output.SlackWebhookURL = v
	}
}

func validate(cfg *Config) error {
	if cfg.LLM.APIKey == "" {
		return fmt.Errorf("llm.api_key is required (or set ANTHROPIC_API_KEY)")
	}
	return nil
}
