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
	Backend     string `json:"backend,omitempty"` // always "dolt"

	// Deletions configuration
	DeletionsRetentionDays int `json:"deletions_retention_days,omitempty"` // 0 means use default (3 days)

	// Dolt connection mode configuration (bd-dolt.2.2)
	// Default is "embedded" (in-process). Server mode ("server") connects to an
	// external dolt sql-server and should only be used for high-concurrency scenarios.
	DoltMode       string `json:"dolt_mode,omitempty"`        // "embedded" (default) or "server"
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

		// Remove legacy file (best effort: migration already saved to new location)
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
	// Always use "dolt" as the directory name.
	// The Database field is irrelevant — data always lives at .beads/dolt/.
	// Stale values like "town", "wyvern", "beads_rig" caused split-brain (see DOLT-HEALTH-P0.md).
	if filepath.IsAbs(c.Database) {
		return c.Database
	}
	return filepath.Join(beadsDir, "dolt")
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
	BackendDolt = "dolt"
)

// BackendCapabilities describes behavioral constraints for a storage backend.
//
// This is intentionally small and stable: callers should use these flags to decide
// whether to enable features like RPC and process spawning.
//
// NOTE: The embedded Dolt driver is effectively single-writer at the OS-process level.
// Even if multiple goroutines are safe within one process, multiple processes opening
// the same Dolt directory concurrently can cause lock contention and transient
// "read-only" failures. Therefore, Dolt is treated as single-process-only.
type BackendCapabilities struct {
	// SingleProcessOnly indicates the backend must not be accessed from multiple
	// Beads OS processes concurrently (embedded mode is single-writer).
	SingleProcessOnly bool
}

// CapabilitiesForBackend returns capabilities for a backend string.
// Unknown backends are treated conservatively as single-process-only.
//
// Note: For Dolt, this returns SingleProcessOnly=true for embedded mode.
// Use Config.GetCapabilities() when you have the full config to properly
// handle server mode (which supports multi-process access).
func CapabilitiesForBackend(backend string) BackendCapabilities {
	switch strings.TrimSpace(strings.ToLower(backend)) {
	case "", BackendDolt:
		// Embedded Dolt is single-process-only.
		// Server mode is handled by Config.GetCapabilities().
		return BackendCapabilities{SingleProcessOnly: true}
	default:
		return BackendCapabilities{SingleProcessOnly: true}
	}
}

// GetCapabilities returns the backend capabilities for this config.
// Unlike CapabilitiesForBackend(string), this considers Dolt server mode
// which supports multi-process access.
func (c *Config) GetCapabilities() BackendCapabilities {
	backend := c.GetBackend()
	if backend == BackendDolt && c.IsDoltServerMode() {
		// Server mode supports multi-writer, so NOT single-process-only
		return BackendCapabilities{SingleProcessOnly: false}
	}
	return CapabilitiesForBackend(backend)
}

// GetBackend returns the configured backend type (always Dolt).
func (c *Config) GetBackend() string {
	return BackendDolt
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
// Server mode is opt-in for high-concurrency scenarios; embedded is the default.
// Checks the BEADS_DOLT_SERVER_MODE env var first, then falls back to the
// dolt_mode field in metadata.json. Only applies when backend is "dolt".
func (c *Config) IsDoltServerMode() bool {
	if os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" && c.GetBackend() == BackendDolt {
		return true
	}
	return c.GetBackend() == BackendDolt && strings.ToLower(c.DoltMode) == DoltModeServer
}

// GetDoltMode returns the Dolt connection mode, defaulting to embedded.
func (c *Config) GetDoltMode() string {
	if c.DoltMode == "" {
		return DoltModeEmbedded
	}
	return c.DoltMode
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
