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

	"github.com/steveyegge/beads/internal/config"
)

const ConfigFileName = "metadata.json"

type Config struct {
	Database string `json:"database"`
	Backend  string `json:"backend,omitempty"` // Storage backend: "dolt" (default); legacy "postgres"/"mysql"/"sqlite" values are rejection tombstones. Read via GetBackend().

	// Deletions configuration
	DeletionsRetentionDays int `json:"deletions_retention_days,omitempty"` // 0 means use default (3 days)

	// Dolt connection mode configuration (bd-dolt.2.2)
	// "embedded" (default for standalone) runs Dolt in-process.
	// "server" connects to an external dolt sql-server (required for orchestrator / multi-writer).
	DoltMode           string `json:"dolt_mode,omitempty"`            // "embedded" (default) or "server"
	DoltServerHost     string `json:"dolt_server_host,omitempty"`     // Server host (default: 127.0.0.1)
	DoltServerPort     int    `json:"dolt_server_port,omitempty"`     // Server port (default: 3307)
	DoltServerSocket   string `json:"dolt_server_socket,omitempty"`   // Unix domain socket path (overrides host/port)
	DoltServerUser     string `json:"dolt_server_user,omitempty"`     // MySQL user (default: root)
	DoltDatabase       string `json:"dolt_database,omitempty"`        // SQL database name (default: beads)
	DoltServerTLS      bool   `json:"dolt_server_tls,omitempty"`      // Enable TLS for server connections (required for Hosted Dolt)
	DoltDataDir        string `json:"dolt_data_dir,omitempty"`        // Custom dolt data directory (absolute path; default: .beads/dolt)
	DoltRemotesAPIPort int    `json:"dolt_remotesapi_port,omitempty"` // Dolt remotesapi port for federation (default: 8080)
	// Note: Password should be set via BEADS_DOLT_PASSWORD env var for security

	// Deprecated backend fields are retained only to round-trip metadata written by
	// the short-lived PostgreSQL/MySQL implementations. They are not connection
	// configuration for current builds; store selection rejects those backends.
	PostgresDSN    string `json:"postgres_dsn,omitempty"`
	PostgresSchema string `json:"postgres_schema,omitempty"`
	MySQLDSN       string `json:"mysql_dsn,omitempty"`
	MySQLDatabase  string `json:"mysql_database,omitempty"`

	// Deprecated: retained only to round-trip metadata written by the removed
	// SQLite backend (backend="sqlite"); store selection rejects that backend.
	SQLitePath string `json:"sqlite_path,omitempty"` // database file, relative to the beads dir (default beads.db)

	// Project identity — unique ID generated at bd init time.
	// Used to detect cross-project data leakage when a client connects
	// to the wrong Dolt server (GH#2372).
	ProjectID string `json:"project_id,omitempty"`

	GlobalDoltDatabase string `json:"global_dolt_database,omitempty"`
	GlobalProjectID    string `json:"global_project_id,omitempty"`

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

	saved := *c
	if filepath.IsAbs(saved.DoltDataDir) {
		saved.DoltDataDir = ""
	}

	data, err := json.MarshalIndent(&saved, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	// Write-temp-then-rename: a plain os.WriteFile truncates in place, so a
	// concurrent Load can observe an empty or partial metadata.json and feed
	// store selection a corrupt config. Rename within the same directory is
	// atomic, so readers see either the old or the new file, never a torn one.
	if err := writeFileAtomic(configPath, data, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// writeFileAtomic writes data to a temp file in path's directory and renames
// it over path, so concurrent readers never observe a truncated or partial
// file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
	BackendDolt     = "dolt"
	BackendPostgres = "postgres"
	BackendMySQL    = "mysql"
	BackendSQLite   = "sqlite"
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

// CapabilitiesForBackend returns capabilities for a backend string. Embedded Dolt
// is single-process-only; use Config.GetCapabilities() to account for
// Dolt server and proxied-server modes.
func CapabilitiesForBackend(_ string) BackendCapabilities {
	return BackendCapabilities{SingleProcessOnly: true}
}

// GetCapabilities returns the backend capabilities for this config.
// Unlike CapabilitiesForBackend(string), this considers Dolt server mode
// (and proxied-server mode) which support multi-process access.
func (c *Config) GetCapabilities() BackendCapabilities {
	backend := c.GetBackend()
	if backend == BackendDolt && (c.IsDoltServerMode() || c.IsDoltProxiedServerMode()) {
		return BackendCapabilities{SingleProcessOnly: false}
	}
	return CapabilitiesForBackend(backend)
}

// IsSupportedBackend reports whether backend selects an implementation shipped by
// beads. The empty value is the legacy/default spelling of Dolt.
func IsSupportedBackend(backend string) bool {
	return backend == "" || backend == BackendDolt
}

// GetBackend returns the configured storage backend. PostgreSQL, MySQL, and
// SQLite remain recognizable here so workspaces created by earlier builds can
// fail loudly at store selection instead of silently falling back to an empty
// Dolt database.
// Empty and explicit Dolt retain the established Dolt behavior. GetBackend keeps
// the historical Dolt fallback for unknown values, so storage-selection callers
// must check IsSupportedBackend(c.Backend) before opening or creating storage.
func (c *Config) GetBackend() string {
	if c != nil {
		switch c.Backend {
		case BackendPostgres:
			return BackendPostgres
		case BackendMySQL:
			return BackendMySQL
		case BackendSQLite:
			return BackendSQLite
		}
	}
	return BackendDolt
}

// GetSQLitePath returns the SQLite database file path (relative to the beads dir, or
// absolute). Empty means the default (beads.db).
func (c *Config) GetSQLitePath() string {
	if c == nil {
		return ""
	}
	return c.SQLitePath
}

// Dolt mode constants
const (
	DoltModeEmbedded      = "embedded"
	DoltModeServer        = "server"
	DoltModeProxiedServer = "proxied-server"
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
//
// Checks (in priority order):
//  1. BEADS_DOLT_SERVER_MODE=1 env var
//  2. BEADS_DOLT_SHARED_SERVER env var (shared-server implies server mode)
//  3. dolt_mode field in metadata.json (project-local, explicit)
//  4. dolt.mode in config.yaml (user-global fallback, only when metadata.json has no mode)
//
// Runtime env vars take precedence over persisted metadata.json to prevent
// stale dolt_mode=embedded from overriding active server intent (GH#2949).
func (c *Config) IsDoltServerMode() bool {
	if c.GetBackend() != BackendDolt {
		return false
	}
	if os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" {
		return true
	}
	// Shared-server mode implies server-backed storage. Check env var
	// directly to avoid circular import with doltserver package.
	if v := os.Getenv("BEADS_DOLT_SHARED_SERVER"); v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	if c.DoltMode != "" {
		// metadata.json has an explicit mode — respect it over config.yaml
		return strings.ToLower(c.DoltMode) == DoltModeServer
	}
	// Fall back to config.yaml dolt.mode setting (no metadata.json mode set)
	if mode := config.GetYamlConfig("dolt.mode"); strings.EqualFold(mode, "server") {
		return true
	}
	return false
}

func (c *Config) IsDoltProxiedServerMode() bool {
	if c.GetBackend() != BackendDolt {
		return false
	}
	return strings.ToLower(c.DoltMode) == DoltModeProxiedServer
}

// GetDoltMode returns the Dolt connection mode, defaulting to server.
func (c *Config) GetDoltMode() string {
	if c.DoltMode == "" {
		return DoltModeEmbedded
	}
	return c.DoltMode
}

// GetDoltServerHost returns the Dolt server host.
// Priority: BEADS_DOLT_SERVER_HOST env var > metadata.json dolt_server_host
// > config.yaml / global config dolt.host > DefaultDoltServerHost.
// The config.yaml layer mirrors the dolt.port fix (GH#2073) so a shared
// team / user-level Dolt server can be configured once without per-clone
// metadata.json edits.
func (c *Config) GetDoltServerHost() string {
	if h := os.Getenv("BEADS_DOLT_SERVER_HOST"); h != "" {
		return h
	}
	if c.DoltServerHost != "" {
		return c.DoltServerHost
	}
	if h := config.GetYamlConfig("dolt.host"); h != "" {
		return h
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
func (c *Config) GetDoltServerPort() int {
	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	if p := os.Getenv("BEADS_DOLT_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	if c.DoltServerPort > 0 {
		return c.DoltServerPort
	}
	return DefaultDoltServerPort
}

// GetDoltServerSocket returns the Dolt server Unix domain socket path.
// Checks BEADS_DOLT_SERVER_SOCKET env var first, then config. Empty means use TCP.
func (c *Config) GetDoltServerSocket() string {
	if s := os.Getenv("BEADS_DOLT_SERVER_SOCKET"); s != "" {
		return s
	}
	return c.DoltServerSocket
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

// GetDoltCredentialCommand returns the server credential command:
// BEADS_DOLT_CREDENTIAL_COMMAND. Empty means no command — the static
// BEADS_DOLT_SERVER_USER / dolt_server_user path applies. The command's stdout is a
// short-lived token (bare, or a {token,expirationTimestamp} / {access_token,expires_in}
// envelope) presented as the connection username to an authenticating gateway server,
// which verifies it and routes to the database. It is deliberately read from the
// environment only, NOT metadata.json: a metadata-sourced command is arbitrary code run
// on open, so persisting it waits on a workspace-trust gate.
func (c *Config) GetDoltCredentialCommand() string {
	return os.Getenv("BEADS_DOLT_CREDENTIAL_COMMAND")
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

// GetGlobalDoltDatabase returns the global database name for shared-server mode.
// Returns empty string if no global database is configured.
func (c *Config) GetGlobalDoltDatabase() string {
	return c.GlobalDoltDatabase
}

func (c *Config) GetGlobalProjectID() string {
	return c.GlobalProjectID
}

// GetDoltServerPassword returns the Dolt server password.
// Checks in order:
//  1. BEADS_DOLT_PASSWORD env var (highest priority, existing behavior)
//  2. Credentials file lookup by [host:port] section
//     (path from BEADS_CREDENTIALS_FILE env var, or ~/.config/beads/credentials)
//  3. Empty string (no password)
//
// Note: uses the port from configfile (metadata.json / env var), which may differ
// from the resolved runtime port (doltserver port file). If you have the resolved
// port, prefer GetDoltServerPasswordForPort for correct credentials file lookup.
func (c *Config) GetDoltServerPassword() string {
	return c.GetDoltServerPasswordForPort(c.GetDoltServerPort())
}

// GetDoltServerPasswordForPort returns the Dolt server password using an explicit
// port for the credentials file lookup. Use this when the resolved runtime port
// (from doltserver.DefaultConfig) differs from the configfile port (metadata.json).
//
// This avoids a mismatch where metadata.json says port 3308 (tunnel) but the
// doltserver port file says 3307 (local), causing the credentials file lookup
// to use the wrong [host:port] section.
func (c *Config) GetDoltServerPasswordForPort(port int) string {
	if p := os.Getenv("BEADS_DOLT_PASSWORD"); p != "" {
		return p
	}
	host := c.GetDoltServerHost()
	if p := LookupCredentialsPassword(host, port); p != "" {
		return p
	}
	return ""
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
