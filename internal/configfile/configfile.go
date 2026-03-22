package configfile

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const ConfigFileName = "metadata.json"

type Config struct {
	Database string `json:"database"`
	Backend  string `json:"backend,omitempty"` // Deprecated: always "dolt". Kept for JSON compat.

	// Deletions configuration
	DeletionsRetentionDays int `json:"deletions_retention_days,omitempty"` // 0 means use default (3 days)

	// Dolt connection mode configuration (bd-dolt.2.2)
	// "embedded" (default for standalone) runs Dolt in-process.
	// "server" connects to an external dolt sql-server (required for orchestrator / multi-writer).
	DoltMode           string `json:"dolt_mode,omitempty"`            // "embedded" (default) or "server"
	DoltServerHost     string `json:"dolt_server_host,omitempty"`     // Server host (default: 127.0.0.1)
	DoltServerPort     int    `json:"dolt_server_port,omitempty"`     // Server port (default: 3307)
	DoltServerUser     string `json:"dolt_server_user,omitempty"`     // MySQL user (default: root)
	DoltDatabase       string `json:"dolt_database,omitempty"`        // SQL database name (default: beads)
	DoltServerTLS      bool   `json:"dolt_server_tls,omitempty"`      // Enable TLS for server connections (required for Hosted Dolt)
	DoltDataDir        string `json:"dolt_data_dir,omitempty"`        // Custom dolt data directory (absolute path; default: .beads/dolt)
	DoltRemotesAPIPort int    `json:"dolt_remotesapi_port,omitempty"` // Dolt remotesapi port for federation (default: 8080)
	// Note: Password should be set via BEADS_DOLT_PASSWORD env var for security

	// Project identity — unique ID generated at bd init time.
	// Used to detect cross-project data leakage when a client connects
	// to the wrong Dolt server (GH#2372).
	ProjectID string `json:"project_id,omitempty"`

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
		Database: "beads.db",
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

	// Strip absolute dolt_data_dir before saving — metadata.json is committed
	// to git and propagates to other clones, but absolute paths are
	// machine-specific and cause data-loss on other machines (GH#2251).
	// Users should set absolute paths via BEADS_DOLT_DATA_DIR env var instead.
	saved := *c
	if filepath.IsAbs(saved.DoltDataDir) {
		saved.DoltDataDir = ""
	}

	data, err := json.MarshalIndent(&saved, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

func (c *Config) DatabasePath(beadsDir string) string {
	// Check for custom dolt data directory (absolute path on a faster filesystem).
	// This is useful on WSL where .beads/ lives on NTFS (slow 9P mount) but
	// dolt data can be placed on native ext4 for 5-10x I/O speedup.
	if customDir := c.GetDoltDataDir(); customDir != "" {
		if filepath.IsAbs(customDir) {
			return customDir
		}
		return filepath.Join(beadsDir, customDir)
	}

	if filepath.IsAbs(c.Database) {
		return c.Database
	}
	// Always use "dolt" as the directory name.
	// Stale values like "town", "wyvern", "beads_rig" caused split-brain (see DOLT-HEALTH-P0.md).
	return filepath.Join(beadsDir, "dolt")
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
// NOTE: Multiple processes opening the same Dolt directory concurrently can
// cause lock contention and transient failures. Dolt is treated as
// single-process-only unless using server mode.
type BackendCapabilities struct {
	// SingleProcessOnly indicates the backend must not be accessed from multiple
	// Beads OS processes concurrently.
	SingleProcessOnly bool
}

// CapabilitiesForBackend returns capabilities for a backend string.
// Dolt is the only supported backend. Returns SingleProcessOnly=true by default;
// use Config.GetCapabilities() to properly handle server mode.
func CapabilitiesForBackend(_ string) BackendCapabilities {
	return BackendCapabilities{SingleProcessOnly: true}
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

// GetBackend returns the backend type. Always returns "dolt".
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
	DefaultDoltServerHost     = "127.0.0.1"
	DefaultDoltServerPort     = 3307 // Use 3307 to avoid conflict with MySQL on 3306
	DefaultDoltServerUser     = "root"
	DefaultDoltDatabase       = "beads"
	DefaultDoltRemotesAPIPort = 8080 // Default dolt remotesapi port for federation
)

// IsDoltServerMode returns true if Dolt should connect via sql-server.
// Server mode is the standard connection method.
// Checks the BEADS_DOLT_SERVER_MODE env var first, then falls back to the
// dolt_mode field in metadata.json. Only applies when backend is "dolt".
func (c *Config) IsDoltServerMode() bool {
	if os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" && c.GetBackend() == BackendDolt {
		return true
	}
	return c.GetBackend() == BackendDolt && strings.ToLower(c.DoltMode) == DoltModeServer
}

// GetDoltMode returns the Dolt connection mode, defaulting to server.
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

// Deprecated: Use doltserver.DefaultConfig(beadsDir).Port instead.
// This method falls back to 3307 which is wrong for standalone mode
// (where the port is an OS-assigned ephemeral port).
// Kept for backward compatibility with external consumers.
//
// GetDoltServerPort returns the Dolt server port.
// Checks BEADS_DOLT_SERVER_PORT env var first, then BEADS_DOLT_PORT (orchestrator sets this),
// then config, then default.
//
// BEADS_DOLT_PORT is only applied when the config host is localhost (127.0.0.1 or
// empty). When config explicitly points at a remote host (e.g. dolt.lan), the
// env var is skipped because it refers to the local Gas Town Dolt instance and
// would produce an invalid host:port combination (e.g. dolt.lan:3307).
func (c *Config) GetDoltServerPort() int {
	// BEADS_DOLT_SERVER_PORT is the explicit override — always wins.
	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	// BEADS_DOLT_PORT (from GT_DOLT_PORT) is a localhost hint. Only apply when
	// config host is localhost or unset; skip when config targets a remote host.
	if p := os.Getenv("BEADS_DOLT_PORT"); p != "" {
		if !c.isRemoteHost() {
			if port, err := strconv.Atoi(p); err == nil {
				return port
			}
		}
	}
	if c.DoltServerPort > 0 {
		return c.DoltServerPort
	}
	return DefaultDoltServerPort
}

// isRemoteHost returns true if the config explicitly sets a non-localhost host.
func (c *Config) isRemoteHost() bool {
	h := c.DoltServerHost
	if h == "" {
		return false
	}
	return h != "127.0.0.1" && h != "localhost" && h != "::1"
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

// GetDoltServerPassword returns the Dolt server password.
// Checks BEADS_DOLT_PASSWORD env var (password should never be stored in config files).
func (c *Config) GetDoltServerPassword() string {
	return os.Getenv("BEADS_DOLT_PASSWORD")
}

// GetDoltServerTLS returns whether TLS is enabled for server connections.
// Required for Hosted Dolt instances.
// Checks BEADS_DOLT_SERVER_TLS env var first ("1" or "true"), then config.
func (c *Config) GetDoltServerTLS() bool {
	if t := os.Getenv("BEADS_DOLT_SERVER_TLS"); t != "" {
		return t == "1" || strings.ToLower(t) == "true"
	}
	return c.DoltServerTLS
}

// GetDoltDataDir returns the custom dolt data directory path.
// When set, dolt stores its data in this directory instead of .beads/dolt/.
// This is useful on WSL where the project lives on a slow NTFS mount (9P)
// but dolt data can be placed on native ext4 for significantly better I/O.
// Checks BEADS_DOLT_DATA_DIR env var first, then config.
func (c *Config) GetDoltDataDir() string {
	if d := os.Getenv("BEADS_DOLT_DATA_DIR"); d != "" {
		return d
	}
	return c.DoltDataDir
}

// GetDoltRemotesAPIPort returns the Dolt remotesapi port used for federation.
// Checks BEADS_DOLT_REMOTESAPI_PORT env var first, then config, then default (8080).
func (c *Config) GetDoltRemotesAPIPort() int {
	if p := os.Getenv("BEADS_DOLT_REMOTESAPI_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	if c.DoltRemotesAPIPort > 0 {
		return c.DoltRemotesAPIPort
	}
	return DefaultDoltRemotesAPIPort
}

// GenerateProjectID creates a UUID v4 for project identity verification (GH#2372).
func GenerateProjectID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use timestamp + PID as a unique-enough identifier
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	}
	// Set version (4) and variant (RFC 4122)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
