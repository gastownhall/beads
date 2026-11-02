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
	"github.com/steveyegge/beads/internal/storage/backendnames"
)

const ConfigFileName = "metadata.json"

type Config struct {
	Database string `json:"database"`
	Backend  string `json:"backend,omitempty"` // Storage backend: "dolt" (default), a registered extension, or a legacy rejection tombstone. Read via GetBackend().

	// GraphMode identifies the workspace's data model, independently of its
	// storage backend. An absent marker preserves existing Issue workspaces.
	// This build opens only GraphModeDependency workspaces (see GetGraphMode);
	// the marker is carried so a Save never strips it from a workspace this
	// build must refuse.
	GraphMode string `json:"graph_mode,omitempty"`

	// Graph workspace binding, written by builds that create graph_mode
	// workspaces. This build never reads them, and missing or mismatched
	// values never authorize implicit bootstrap or migration. A Save carries
	// these known fields only, with the tags graph-preview builds declare: a
	// zero graph_schema_version and a false graph_ready are omitted, as those
	// builds omit them, and a graph_* key outside this set is not kept.
	GraphScopeURL      string `json:"graph_scope_url,omitempty"`
	GraphAuthorityID   string `json:"graph_authority_id,omitempty"`
	GraphWorkspace     string `json:"graph_workspace,omitempty"`
	GraphSchemaVersion int    `json:"graph_schema_version,omitempty"`
	GraphReady         bool   `json:"graph_ready,omitempty"`

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
	DoltTeamServer     bool   `json:"dolt_team_server,omitempty"`     // Schema is managed by beads-team-server (bts); bd never runs migrations (proxied-server mode only)
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

// LegacyConfigFileName is the pre-metadata.json name of a workspace's
// metadata file. Load migrates it into metadata.json; LoadForDiscovery reads
// it in place.
const LegacyConfigFileName = "config.json"

// MetadataFilePath returns the path of the file beadsDir's metadata is read
// from: the legacy config.json when metadata.json is absent and config.json
// exists (the file LoadForDiscovery reads, and Load would migrate), and
// ConfigPath otherwise. Messages that name the file holding a setting use it,
// so a refusal made before any migration names the file the user must edit.
// It only stats; an empty beadsDir returns ConfigPath("").
func MetadataFilePath(beadsDir string) string {
	path := ConfigPath(beadsDir)
	if beadsDir == "" {
		return path
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return path
	}
	legacyPath := filepath.Join(beadsDir, LegacyConfigFileName)
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath
	}
	return path
}

func Load(beadsDir string) (*Config, error) {
	configPath := ConfigPath(beadsDir)

	data, err := os.ReadFile(configPath) // #nosec G304 - controlled path from config
	if os.IsNotExist(err) {
		// Try legacy config.json location (migration path)
		legacyPath := filepath.Join(beadsDir, LegacyConfigFileName)
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

// LoadForDiscovery reads workspace metadata without migrating or rewriting it.
//
// Store admission uses this only to classify a workspace before a command can
// open storage. In particular, legacy config.json remains in place so a failed
// admission cannot turn a recoverable old workspace into a partially migrated
// one. Unknown JSON fields remain tolerated here because callers use the
// result only to decide whether to issue a conservative refusal; normal store
// selection keeps its existing validation.
func LoadForDiscovery(beadsDir string) (*Config, error) {
	data, err := os.ReadFile(ConfigPath(beadsDir)) // #nosec G304 -- beadsDir is caller-selected workspace state
	if os.IsNotExist(err) {
		data, err = os.ReadFile(filepath.Join(beadsDir, LegacyConfigFileName)) // #nosec G304 -- legacy workspace state
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading legacy config: %w", err)
		}
	} else if err != nil {
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

// IsSupportedBackend reports whether backend selects Dolt or a backend
// registered by this binary. The empty value is the legacy/default spelling
// of Dolt. OSS registers no alternate backends.
func IsSupportedBackend(backend string) bool {
	return backend == "" || backend == BackendDolt || backendnames.Has(backend)
}

// GetBackend returns the configured storage backend. PostgreSQL, MySQL, and
// SQLite remain recognizable here so workspaces created by earlier builds can
// fail loudly at store selection instead of silently falling back to an empty
// Dolt database.
// Registered extension names are returned unchanged. Empty and explicit Dolt
// retain the established Dolt behavior. GetBackend keeps the historical Dolt
// fallback for unknown values, so storage-selection callers must check
// IsSupportedBackend(c.Backend) before opening or creating storage.
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
		if backendnames.Has(c.Backend) {
			return c.Backend
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
//  3. BEADS_DOLT_SERVER_HOST env var set to a non-localhost value (GH#3545):
//     an operator who configures a remote host but forgets the mode flag
//     would otherwise silently fall through to embedded storage. Setting
//     the env var to an explicit localhost value (or empty string) is
//     treated as a deliberate suppression of host-based inference, not
//     merely "no signal" — it also suppresses the struct/config.yaml host
//     inference below.
//  4. dolt_mode field in metadata.json (explicit) — always wins over any
//     persisted or configured host, since it is the operator's explicit
//     statement of intent.
//  5. The non-localhost host GetDoltServerHost dials (GH#3545), when no
//     explicit dolt_mode is set and env didn't suppress inference: the
//     config.yaml dolt.host, which outranks metadata.json dolt_server_host
//     (GH#5961) unless both name the same server, and is gated by an
//     explicit config.yaml dolt.mode; otherwise the metadata.json host,
//     which a config.yaml dolt.mode does not gate. See
//     HostImpliesServerMode.
//  6. dolt.mode in config.yaml (user-global fallback, only when
//     metadata.json has no mode)
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
	// Host-based inference (GH#3545): a configured non-localhost host
	// implies server mode. Centralized in HostImpliesServerMode so the
	// storage-mode resolver (here) and the lifecycle resolver
	// (doltserver.ResolveServerMode) cannot disagree.
	if c.HostImpliesServerMode() {
		return true
	}
	if c.DoltMode != "" {
		// metadata.json has an explicit mode — respect it over any
		// persisted host and over config.yaml.
		return strings.ToLower(c.DoltMode) == DoltModeServer
	}
	// NOTE: deliberately no PortImpliesServerMode call here. This resolver
	// selects the storage backend for every data command, so honoring a bare
	// BEADS_DOLT_SERVER_PORT here has a materially wider blast radius than the
	// lifecycle-only question doltserver.resolveServerMode's check 2c answers.
	// The two therefore still disagree for an unset/embedded dolt_mode plus a
	// bare port env var and no other server signal. That gap is real, narrow
	// and tracked: GH#6181 owns whether this resolver should extend to the
	// port signal, after the call-site audit such a change needs.
	// Fall back to config.yaml dolt.mode setting (no metadata.json mode set)
	return strings.EqualFold(config.GetYamlConfig("dolt.mode"), "server")
}

// HostImpliesServerMode evaluates host-based server-mode inference
// (GH#3545) against the EFFECTIVE host precedence chain (env >
// config.yaml > metadata.json, mirroring GetDoltServerHost), so that a
// lower-priority host that GetDoltServerHost would ignore can never
// drive the inference. Rules, in order:
//
//   - Proxied-server workspaces are exempt: IsDoltServerMode and
//     IsDoltProxiedServerMode are mutually exclusive (the
//     proxied-to-server migration depends on it).
//   - A non-empty BEADS_DOLT_SERVER_HOST decides alone: a non-localhost
//     value implies server mode even over explicit dolt_mode=embedded
//     (runtime env beats stale metadata, GH#2949); an explicit
//     localhost value suppresses inference from lower-priority hosts —
//     matching GetDoltServerHost, which then dials locally. An EMPTY
//     env value is ignored (behaves as unset), again matching
//     GetDoltServerHost: treating empty as suppression would select
//     embedded storage while the effective dial host stays remote —
//     exactly the split-storage failure this inference fixes.
//   - An explicit metadata.json dolt_mode disables inference from
//     persisted/configured hosts.
//   - A metadata.json dolt_server_host decides alone when config.yaml
//     names no dolt.host or names the same server (SameServerHost): it is
//     the host GetDoltServerHost dials, and a config.yaml dolt.mode does
//     not suppress it, so a user-level dolt.mode cannot flip a workspace
//     whose metadata names the server it uses to embedded storage.
//   - Otherwise a config.yaml dolt.host outranks the metadata host (it is
//     the host GetDoltServerHost dials, GH#5961) and decides, unless an
//     explicit config.yaml dolt.mode disables inference from it.
//
// "Localhost" here is IsLocalHostString, the machine-locality question, not
// the ownership one (BdRunsServerAt): the IPv6 loopback in any spelling is
// this machine and infers nothing, while another loopback address such as
// 127.0.0.2 infers server mode, as a host on another machine does.
func (c *Config) HostImpliesServerMode() bool {
	if strings.EqualFold(c.DoltMode, DoltModeProxiedServer) {
		return false
	}
	if h := os.Getenv("BEADS_DOLT_SERVER_HOST"); h != "" {
		return !IsLocalHostString(h)
	}
	if c.DoltMode != "" {
		return false
	}
	h := config.GetYamlConfig("dolt.host")
	if c.DoltServerHost != "" && (h == "" || SameServerHost(h, c.DoltServerHost)) {
		return !IsLocalHostString(c.DoltServerHost)
	}
	if h == "" || config.GetYamlConfig("dolt.mode") != "" {
		return false
	}
	return !IsLocalHostString(h)
}

// PortImpliesServerMode evaluates port-based server-mode inference: an
// explicitly named server port means an orchestrator is pointing this
// workspace at a server address it manages, so beads cannot own that
// server's lifecycle. Rules, in order, mirroring HostImpliesServerMode:
//
//   - Proxied-server workspaces are exempt. A proxied workspace reaches its
//     server through the proxy, so an ambient port does not describe its
//     lifecycle and must not reclassify it.
//   - Otherwise the env vars decide, BEADS_DOLT_SERVER_PORT first and the
//     legacy BEADS_DOLT_PORT second, matching GetDoltServerPort's own
//     precedence. A live env var in the current process outranks an explicit
//     dolt_mode, exactly as the host env var does in HostImpliesServerMode --
//     the env check there also runs before the DoltMode suppression and can
//     return first (GH#2949: runtime env beats stale persisted metadata).
//
// There is deliberately no persisted-port tier here. HostImpliesServerMode
// suppresses its weaker persisted/config.yaml host signals on an explicit
// DoltMode; the port equivalent, metadata.json's dolt_server_port, is read by
// a later check in doltserver.resolveServerMode that an explicit
// dolt_mode=embedded already returns before. There is nothing for a
// downstream guard to protect.
//
// NOTE: IsDoltServerMode does NOT consult this predicate. See the note there.
func (c *Config) PortImpliesServerMode() bool {
	if strings.EqualFold(c.DoltMode, DoltModeProxiedServer) {
		return false
	}
	return doltServerPortFromEnv() > 0
}

// doltServerPortFromEnv returns the server port named by
// BEADS_DOLT_SERVER_PORT or, failing that, the legacy BEADS_DOLT_PORT --
// mirroring Config.GetDoltServerPort's env precedence without needing a
// loaded Config. Returns 0 when neither names a valid positive integer.
func doltServerPortFromEnv() int {
	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}
	if p := os.Getenv("BEADS_DOLT_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}
	return 0
}

func (c *Config) IsDoltProxiedServerMode() bool {
	if c.GetBackend() != BackendDolt {
		return false
	}
	return strings.ToLower(c.DoltMode) == DoltModeProxiedServer
}

// IsTeamServerManaged reports whether the database's schema and identity are
// owned by beads-team-server (bts). Only meaningful in proxied-server mode.
func (c *Config) IsTeamServerManaged() bool {
	return c.IsDoltProxiedServerMode() && c.DoltTeamServer
}

// GetDoltMode returns the Dolt connection mode, defaulting to server.
// GetDoltMode returns the EFFECTIVE mode: when no mode is persisted but
// host-based inference (GH#3545) selects server mode, it reports
// "server" so effective-context consumers (bd context, integrations)
// agree with IsDoltServerMode instead of advertising an embedded mode
// next to a remote server endpoint.
func (c *Config) GetDoltMode() string {
	if c.DoltMode == "" {
		if c.HostImpliesServerMode() {
			return DoltModeServer
		}
		return DoltModeEmbedded
	}
	return c.DoltMode
}

// GetDoltServerHost returns the Dolt server host.
// Priority: BEADS_DOLT_SERVER_HOST env var > config.yaml / global config
// dolt.host > metadata.json dolt_server_host (including a server.json
// central host merged into it) > DefaultDoltServerHost.
// The config.yaml layer mirrors the dolt.port fix (GH#2073) so a shared
// team / user-level Dolt server can be configured once without per-clone
// metadata.json edits. It outranks metadata.json because metadata.json is
// git-tracked: a host one operator persisted there travels to every clone,
// and pairing it with a config.yaml port dials an endpoint neither file
// declares. The port chain demoted its metadata.json tier for the same
// reason (GH#2372); host followed in GH#5961.
func (c *Config) GetDoltServerHost() string {
	if h := os.Getenv("BEADS_DOLT_SERVER_HOST"); h != "" {
		return h
	}
	if h := config.GetYamlConfig("dolt.host"); h != "" {
		return h
	}
	if c.DoltServerHost != "" {
		return c.DoltServerHost
	}
	return DefaultDoltServerHost
}

// OutrankedServerHost reports a metadata.json dolt_server_host that the
// merged config.yaml dolt.host now outranks (GH#5961) where that changes what
// bd does: beadsDir's own metadata.json (read raw, so a server.json central
// host never counts) names a host, config.yaml names a different server
// (SameServerHost), BEADS_DOLT_SERVER_HOST does not outrank both, and the
// host matters, because cfg selects server mode or because the metadata host
// alone used to select it (no dolt_mode, a non-local host) and the config.yaml
// host no longer does. Explicit embedded and proxied-server workspaces never
// dial the host, so they are not reported. cfg is beadsDir's loaded config.
func OutrankedServerHost(beadsDir string, cfg *Config) (metaHost, yamlHost string, ok bool) {
	if cfg == nil || beadsDir == "" || os.Getenv("BEADS_DOLT_SERVER_HOST") != "" {
		return "", "", false
	}
	if strings.EqualFold(cfg.DoltMode, DoltModeEmbedded) || strings.EqualFold(cfg.DoltMode, DoltModeProxiedServer) {
		return "", "", false
	}
	raw, err := LoadForDiscovery(beadsDir)
	if err != nil || raw == nil || raw.DoltServerHost == "" {
		return "", "", false
	}
	metaHost = raw.DoltServerHost
	yamlHost = config.GetYamlConfig("dolt.host")
	if yamlHost == "" || SameServerHost(yamlHost, metaHost) {
		return "", "", false
	}
	if !cfg.IsDoltServerMode() && (cfg.DoltMode != "" || IsLocalHostString(metaHost)) {
		return "", "", false
	}
	return metaHost, yamlHost, true
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
	return ServerPasswordFor(c.GetDoltServerHost(), port)
}

// ServerPasswordFor is the password for the Dolt server at host and port:
// BEADS_DOLT_PASSWORD when set, else the credentials-file entry for exactly
// [host:port] (either spelling of an IPv6 host, see CredentialsSectionKeys),
// else "". doltserver.Endpoint.Password calls it once the port is final.
func ServerPasswordFor(host string, port int) string {
	if p := os.Getenv("BEADS_DOLT_PASSWORD"); p != "" {
		return p
	}
	return LookupCredentialsPassword(host, port)
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
// Checks BEADS_DOLT_DATA_DIR env var first, then config. Nil-safe, like
// GetBackend: rejection paths resolve the data dir for workspaces whose config
// may not have loaded, and the env override still applies to those.
func (c *Config) GetDoltDataDir() string {
	if d := os.Getenv("BEADS_DOLT_DATA_DIR"); d != "" {
		return d
	}
	if c == nil {
		return ""
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
