package configfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const ConfigFileName = "metadata.json"

type Config struct {
	Database    string `json:"database"`
	JSONLExport string `json:"jsonl_export,omitempty"`
	Backend     string `json:"backend,omitempty"` // "sqlite" (default) or "dolt"

	// Deletions configuration
	DeletionsRetentionDays int `json:"deletions_retention_days,omitempty"` // 0 means use default (3 days)

	// Dolt connection mode configuration
	// Server mode connects to an external dolt sql-server via MySQL protocol.
	// The "embedded" value is accepted for backward compatibility but treated as "server".
	DoltMode       string `json:"dolt_mode,omitempty"`        // "server" (only mode supported)
	DoltServerHost string `json:"dolt_server_host,omitempty"` // Server host (default: 127.0.0.1)
	DoltServerPort int    `json:"dolt_server_port,omitempty"` // Server port (default: 3307)
	DoltServerUser string `json:"dolt_server_user,omitempty"` // MySQL user (default: root)
	DoltDatabase   string `json:"dolt_database,omitempty"`    // SQL database name (default: beads)
	// Note: Password should be set via BEADS_DOLT_PASSWORD env var for security

	// Stale closed issues check configuration
	// 0 = disabled (default), positive = threshold in days
	StaleClosedIssuesDays int `json:"stale_closed_issues_days,omitempty"`

	// Deprecated: LastBdVersion is no longer used for version tracking.
	// Version is now stored in .local_version (gitignored) to prevent
	// upgrade notifications firing after git operations reset metadata.json.
	// bd-tok: This field is kept for backwards compatibility when reading old configs.
	LastBdVersion string `json:"last_bd_version,omitempty"`
}

func DefaultConfig() *Config {
	return &Config{
		Database:    "beads.db",
		JSONLExport: "issues.jsonl", // Canonical name (bd-6xd)
	}
}

func ConfigPath(beadsDir string) string {
	return filepath.Join(beadsDir, ConfigFileName)
}

func Load(beadsDir string) (*Config, error) {
	configPath := ConfigPath(beadsDir)

	data, err := os.ReadFile(configPath) // #nosec G304 - controlled path from config
	if os.IsNotExist(err) {
		// Try legacy config.json location (migration path)
		legacyPath := filepath.Join(beadsDir, "config.json")
		data, err = os.ReadFile(legacyPath) // #nosec G304 - controlled path from config
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading legacy config: %w", err)
		}

		// Migrate: parse legacy config, save as metadata.json, remove old file
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing legacy config: %w", err)
		}

		// Save to new location
		if err := cfg.Save(beadsDir); err != nil {
			return nil, fmt.Errorf("migrating config to metadata.json: %w", err)
		}

		// Remove legacy file (best effort)
		_ = os.Remove(legacyPath)

		return &cfg, nil
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
	backend := c.GetBackend()

	if backend == BackendDolt {
		// For dolt backend, always use "dolt" as the directory name.
		// The Database field is irrelevant for dolt — data always lives at .beads/dolt/.
		// Stale values like "town", "wyvern", "beads_rig" caused split-brain (see DOLT-HEALTH-P0.md).
		if filepath.IsAbs(c.Database) {
			return c.Database
		}
		return filepath.Join(beadsDir, "dolt")
	}

	// SQLite (default)
	db := strings.TrimSpace(c.Database)
	if db == "" {
		db = "beads.db"
	}
	if filepath.IsAbs(db) {
		return db
	}
	return filepath.Join(beadsDir, db)
}

func (c *Config) JSONLPath(beadsDir string) string {
	if c.JSONLExport == "" {
		return filepath.Join(beadsDir, "issues.jsonl")
	}
	return filepath.Join(beadsDir, c.JSONLExport)
}

// DefaultDeletionsRetentionDays is the default retention period for deletion records.
const DefaultDeletionsRetentionDays = 3

// GetDeletionsRetentionDays returns the configured retention days, or the default if not set.
func (c *Config) GetDeletionsRetentionDays() int {
	if c.DeletionsRetentionDays <= 0 {
		return DefaultDeletionsRetentionDays
	}
	return c.DeletionsRetentionDays
}

// GetStaleClosedIssuesDays returns the configured threshold for stale closed issues.
// Returns 0 if disabled (the default), or a positive value if enabled.
func (c *Config) GetStaleClosedIssuesDays() int {
	if c.StaleClosedIssuesDays < 0 {
		return 0
	}
	return c.StaleClosedIssuesDays
}

// Backend constants
const (
	BackendSQLite = "sqlite"
	BackendDolt   = "dolt"
)

// BackendCapabilities describes behavioral constraints for a storage backend.
//
// This is intentionally small and stable: callers should use these flags to decide
// whether to enable features like daemon/RPC/autostart and process spawning.
type BackendCapabilities struct {
	// SingleProcessOnly indicates the backend must not be accessed from multiple
	// Beads OS processes concurrently (no daemon mode, no RPC client/server split,
	// no helper-process spawning).
	SingleProcessOnly bool
}

// CapabilitiesForBackend returns capabilities for a backend string.
// Unknown backends are treated conservatively as single-process-only.
func CapabilitiesForBackend(backend string) BackendCapabilities {
	switch strings.TrimSpace(strings.ToLower(backend)) {
	case "", BackendSQLite:
		return BackendCapabilities{SingleProcessOnly: false}
	case BackendDolt:
		// Dolt uses server mode which supports multi-writer access
		return BackendCapabilities{SingleProcessOnly: false}
	default:
		return BackendCapabilities{SingleProcessOnly: true}
	}
}

// GetCapabilities returns the backend capabilities for this config.
func (c *Config) GetCapabilities() BackendCapabilities {
	return CapabilitiesForBackend(c.GetBackend())
}

// GetBackend returns the configured backend type, defaulting to SQLite.
func (c *Config) GetBackend() string {
	if c.Backend == "" {
		return BackendSQLite
	}
	return c.Backend
}

// Dolt mode constants
const (
	DoltModeEmbedded = "embedded"
	DoltModeServer   = "server"
)

// Default Dolt server settings
const (
	DefaultDoltServerHost = "127.0.0.1"
	DefaultDoltServerPort = 3307 // Use 3307 to avoid conflict with MySQL on 3306
	DefaultDoltServerUser = "root"
	DefaultDoltDatabase   = "beads"
)

// IsDoltServerMode returns true if Dolt should connect via sql-server.
// Always returns true for Dolt backends (embedded mode has been removed).
func (c *Config) IsDoltServerMode() bool {
	return c.GetBackend() == BackendDolt
}

// GetDoltMode returns the Dolt connection mode. Always returns "server".
func (c *Config) GetDoltMode() string {
	return DoltModeServer
}

// GetDoltServerHost returns the Dolt server host.
// Checks BEADS_DOLT_SERVER_HOST env var first, then config, then default.
func (c *Config) GetDoltServerHost() string {
	if h := os.Getenv("BEADS_DOLT_SERVER_HOST"); h != "" {
		return h
	}
	if c.DoltServerHost != "" {
		return c.DoltServerHost
	}
	return DefaultDoltServerHost
}

// GetDoltServerPort returns the Dolt server port.
// Checks BEADS_DOLT_SERVER_PORT env var first, then config, then default.
func (c *Config) GetDoltServerPort() int {
	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	if c.DoltServerPort > 0 {
		return c.DoltServerPort
	}
	return DefaultDoltServerPort
}

// GetDoltServerUser returns the Dolt server MySQL user.
// Checks BEADS_DOLT_SERVER_USER env var first, then config, then default.
func (c *Config) GetDoltServerUser() string {
	if u := os.Getenv("BEADS_DOLT_SERVER_USER"); u != "" {
		return u
	}
	if c.DoltServerUser != "" {
		return c.DoltServerUser
	}
	return DefaultDoltServerUser
}

// GetDoltDatabase returns the Dolt SQL database name.
// Checks BEADS_DOLT_SERVER_DATABASE env var first, then config, then default.
func (c *Config) GetDoltDatabase() string {
	if d := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); d != "" {
		return d
	}
	if c.DoltDatabase != "" {
		return c.DoltDatabase
	}
	return DefaultDoltDatabase
}
