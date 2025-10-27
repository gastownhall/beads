package configfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const ConfigFileName = "config.json"

type Config struct {
	Database     string `json:"database"`
	Version      string `json:"version"`
	JSONLExport  string `json:"jsonl_export,omitempty"`
}

func DefaultConfig(version string) *Config {
	return &Config{
		Database:    "beads.db",
		Version:     version,
		JSONLExport: "beads.jsonl",
	}
}

func ConfigPath(beadsDir string) string {
	return filepath.Join(beadsDir, ConfigFileName)
}

func Load(beadsDir string) (*Config, error) {
	configPath := ConfigPath(beadsDir)
	
	data, err := os.ReadFile(configPath) // #nosec G304 - controlled path from config
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	
	return &cfg, nil
}

func (c *Config) Save(beadsDir string) error {
	configPath := ConfigPath(beadsDir)
	
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	
	return nil
}

func (c *Config) DatabasePath(beadsDir string) string {
	return filepath.Join(beadsDir, c.Database)
}

func (c *Config) JSONLPath(beadsDir string) string {
	if c.JSONLExport == "" {
		return filepath.Join(beadsDir, "beads.jsonl")
	}
	return filepath.Join(beadsDir, c.JSONLExport)
}
