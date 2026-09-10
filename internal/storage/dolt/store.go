// Package dolt implements the storage interface using Dolt (versioned MySQL-compatible database).
//
// Dolt provides native version control for SQL data with cell-level merge, history queries,
// and federation via Dolt remotes. The database itself is version-controlled.
//
// Dolt capabilities:
//   - Native version control (commit, push, pull, branch, merge)
//   - Time-travel queries via AS OF and dolt_history_* tables
//   - Cell-level merge for conflict resolution
//   - Multi-writer via dolt sql-server (federation, pure Go)
//
// All operations require a running dolt sql-server. Connect via MySQL protocol (pure Go).
package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	mysql "github.com/go-sql-driver/mysql"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/gittraceenv"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// DefaultSQLPort is the default port for dolt sql-server.
const DefaultSQLPort = 3307

// testDatabasePrefixes are name prefixes that indicate a test database.
// Used by isTestDatabaseName to prevent test databases from being created
// on the production Dolt server (Clown Shows #12-#18).
//
// Origin of each prefix:
//   - testdb_     : applyConfigDefaults derives this for BEADS_TEST_MODE=1
//     without an explicit Database (FNV hash of cfg.Path).
//   - beads_test  : convention for hand-written integration tests.
//   - beads_pt    : property-test fixtures.
//   - beads_vr    : version-roundtrip / migration fixtures.
//   - doctest_    : `bd doctor` self-check fixtures.
//   - doctortest_ : older `bd doctor` fixture name (kept for back-compat).
//   - benchdb_    : per-bench scratch DBs (cmd/bd/template_test.go
//     newTemplateBenchmarkStore, format `benchdb_<unixnano>`). Added by
//     AD-01 (be-c5p).
//
// This list is the firewall side of the test/prod split. Two sibling lists
// must converge with it (be-avn): cmd/bd/dolt.go:staleDatabasePrefixes (used
// by `bd dolt clean-databases`) and the formula-side `gc dolt cleanup`
// stale-prefix list. Any prefix added here must be mirrored to those lists,
// or stale fixtures will leak past clean-up.
var testDatabasePrefixes = []string{
	"testdb_",
	"beads_test",
	"beads_pt",
	"beads_vr",
	"doctest_",
	"doctortest_",
	"benchdb_",
}

// isTestDatabaseName returns true if the database name matches known test patterns.
// This is a pattern-based firewall — it does not rely on environment variables.
func isTestDatabaseName(name string) bool {
	for _, prefix := range testDatabasePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// productionPortReasons returns human-readable labels for each rule that
// flags cfg.ServerPort as a production port. An empty slice means the port
// is not detected as production.
//
// Detection sources, in order:
//  1. cfg.ServerPort == DefaultSQLPort (legacy default 3307). Unconditional —
//     never suppressed by BEADS_TEST_SERVER=1. The well-known Dolt default
//     port is the single highest-confidence production signal, and a
//     dedicated test server opting out of the other heuristics still must
//     not bind to it.
//  2. BEADS_PRODUCTION_PORT env var, parsed to int, matches cfg.ServerPort.
//  3. cfg.BeadsDir/dolt-server.port file present and contains cfg.ServerPort.
//
// Rules 2 and 3 are suppressed when BEADS_TEST_SERVER=1: they are
// heuristics (an env var or an on-disk port file, either of which can be
// stale or misconfigured) rather than the fixed default port, so an
// operator's explicit opt-in into a dedicated test-server lane is honored
// for those two only.
//
// Rule 3 deliberately does not fall back to filepath.Dir(cfg.Path) when
// BeadsDir is empty — the port-resolution chain in applyConfigDefaults
// already does that fallback for resolution purposes, but using it here
// would treat any cfg.Path under a directory that happens to contain a
// stray dolt-server.port file (e.g. /tmp/dolt-server.port from a leaked
// dev server) as production. Test fixtures commonly set cfg.Path under
// /tmp without a real BeadsDir; only an explicitly set BeadsDir is
// considered authoritative for the production check.
//
// All rules read deterministic state (constant, env, on-disk port file).
// No state is mutated. Multiple rules can match; the panic message lists all.
func productionPortReasons(cfg *Config) []string {
	if cfg == nil || cfg.ServerPort <= 0 {
		return nil
	}
	var reasons []string
	if cfg.ServerPort == DefaultSQLPort {
		reasons = append(reasons, fmt.Sprintf("port %d == DefaultSQLPort", cfg.ServerPort))
	}
	// Rules 2 and 3 are the suppressible heuristics: honor the operator's
	// BEADS_TEST_SERVER=1 opt-in for a dedicated test server by skipping
	// them. Rule 1 above is intentionally evaluated before this check and
	// is never suppressed.
	if os.Getenv("BEADS_TEST_SERVER") == "1" {
		return reasons
	}
	if env := os.Getenv("BEADS_PRODUCTION_PORT"); env != "" {
		if p, err := strconv.Atoi(env); err == nil && p > 0 && p == cfg.ServerPort {
			reasons = append(reasons, fmt.Sprintf("BEADS_PRODUCTION_PORT=%d matches", p))
		}
	}
	if cfg.BeadsDir != "" {
		if p := doltserver.ReadPortFile(cfg.BeadsDir); p > 0 && p == cfg.ServerPort {
			reasons = append(reasons, fmt.Sprintf("%s/%s contains %d", cfg.BeadsDir, doltserver.PortFileName, p))
		}
	}
	return reasons
}

// isProductionPort reports whether cfg.ServerPort matches any production-port
// indicator. Pure at call time — port resolution itself happens earlier in
// applyConfigDefaults; this helper only inspects already-resolved state.
//
// BEADS_TEST_SERVER=1 narrows detection to Rule 1 only (port ==
// DefaultSQLPort): the operator has explicitly opted into the dedicated
// test-server lane (e.g. a per-test container, an external test port), which
// suppresses the BEADS_PRODUCTION_PORT and dolt-server.port heuristics
// (Rules 2 and 3, see productionPortReasons). Rule 1 stays unconditional —
// a test server must never bind to the well-known default port 3307,
// opt-in or not. The database-name firewall in New is a separate AD-01
// defense with its own independent BEADS_TEST_SERVER=1 opt-out; it is not
// affected by this function.
//
// See productionPortReasons for the three detection sources and the
// suppression rule.
func isProductionPort(cfg *Config) bool {
	return len(productionPortReasons(cfg)) > 0
}

// autoStartRefs tracks in-process reference counts for auto-started dolt
// sql-server processes, keyed by resolved server directory. When the count
// drops to zero, the server is stopped. This prevents test-started servers
// from leaking (GH#2542) while allowing multiple stores to share one server.
// Normal repo-local auto-starts are intentionally not tracked here: those
// servers should stay up like an explicit `bd dolt start`, rather than being
// torn down at the end of each command.
var autoStartRefs struct {
	mu sync.Mutex
	m  map[string]int
}

func autoStartAcquire(serverDir string) {
	autoStartRefs.mu.Lock()
	defer autoStartRefs.mu.Unlock()
	if autoStartRefs.m == nil {
		autoStartRefs.m = make(map[string]int)
	}
	autoStartRefs.m[serverDir]++
}

// autoStartAcquireExisting increments the refcount for serverDir only when the
// current process is already tracking that auto-started server. This lets later
// stores share the same test-owned server without taking ownership of servers
// started by other processes.
func autoStartAcquireExisting(serverDir string) bool {
	autoStartRefs.mu.Lock()
	defer autoStartRefs.mu.Unlock()
	if autoStartRefs.m == nil || autoStartRefs.m[serverDir] <= 0 {
		return false
	}
	autoStartRefs.m[serverDir]++
	return true
}

// autoStartRelease decrements the refcount for serverDir and stops the server
// when it reaches zero. Returns any error from stopping the server.
// If the server is already stopped (e.g. killed externally, or never started),
// the ErrServerNotRunning sentinel is silently absorbed to avoid false
// "failed to stop" warnings (GH#2670).
func autoStartRelease(serverDir string) error {
	autoStartRefs.mu.Lock()
	defer autoStartRefs.mu.Unlock()
	if autoStartRefs.m == nil {
		return nil
	}
	autoStartRefs.m[serverDir]--
	if autoStartRefs.m[serverDir] <= 0 {
		delete(autoStartRefs.m, serverDir)
		// Stop is idempotent: returns ErrServerNotRunning (possibly joined
		// with cleanup errors) when the server is already gone. Strip the
		// sentinel but propagate any real cleanup failures.
		return doltserver.IgnoreNotRunning(doltserver.Stop(serverDir))
	}
	return nil
}

// undoRejectedAutoStart cleans up the side effects of a speculative
// auto-start that newServerMode's fail-closed checks (GH#4052) decided not
// to use.
//
// It restores the port file to the pre-call snapshot: EnsureRunningDetailed
// writes serverDir's port file with the new server's actual port before
// either fail-closed check runs (Start()'s writePortFile, or the
// adopt-existing-server path's EnsurePortFile), and that port file is the
// second-highest-precedence port source. Left in place, it would let a
// second, identical invocation resolve the port file instead of the
// authoritative source that just failed, adopt the server we declined to
// use, and silently succeed — permanently disarming the guard after exactly
// one invocation.
//
// When we spawned the server ourselves (startedByUs), it also stops it: we
// have decided not to use it, so leaving a stray dolt process running is an
// unrequested side effect. When autoStartedDir is set, the server is
// refcount-tracked (the test/test-database path via autoStartAcquire); that
// path already stops the server once the refcount reaches zero, so
// autoStartRelease is used instead of a direct Stop to avoid pulling the rug
// out from under another store instance sharing the same auto-started
// server. An adopted pre-existing server (startedByUs == false) is left
// running — we didn't start it, so we don't stop it, but its port file
// write must still be undone.
//
// Best-effort throughout: cleanup failures are reported on stderr but never
// returned, so they cannot mask the caller's fail-closed error, which is the
// one that matters.
func undoRejectedAutoStart(serverDir string, startedByUs bool, autoStartedDir string, snap doltserver.PortFileSnapshot, snapErr error) {
	if startedByUs {
		if autoStartedDir != "" {
			if err := autoStartRelease(autoStartedDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to stop rejected auto-started dolt server: %v\n", err)
			}
		} else if err := doltserver.IgnoreNotRunning(stopRejectedAutoStartedServer(serverDir)); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to stop rejected auto-started dolt server: %v\n", err)
		}
	}
	if snapErr != nil {
		// The pre-call snapshot itself failed (e.g. a permissions error
		// reading an existing file) — restoring a zero-value snapshot here
		// could wrongly delete a port file we never actually read. Leave the
		// port file alone rather than guess.
		return
	}
	if err := doltserver.RestorePortFile(serverDir, snap); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to restore port file after rejected auto-start: %v\n", err)
	}
}

// shouldStopAutoStartedServerOnClose reports whether an auto-started server
// should be treated as test-owned cleanup state instead of a normal repo-local
// server. In real repos, auto-start should behave like a persistent helper
// server, not a single-command subprocess.
func shouldStopAutoStartedServerOnClose(cfg *Config) bool {
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		return true
	}
	return isTestDatabaseName(cfg.Database)
}

// Compile-time interface checks.
var _ storage.DoltStorage = (*DoltStore)(nil)
var _ storage.RawDBAccessor = (*DoltStore)(nil)
var _ storage.StoreLocator = (*DoltStore)(nil)
var _ storage.ActiveDatabaseSizer = (*DoltStore)(nil)
var _ storage.LifecycleManager = (*DoltStore)(nil)
var _ storage.PendingCommitter = (*DoltStore)(nil)
var _ storage.GarbageCollector = (*DoltStore)(nil)
var _ storage.Flattener = (*DoltStore)(nil)
var _ storage.Compactor = (*DoltStore)(nil)
var _ storage.SchemaMigrator = (*DoltStore)(nil)
var _ storage.ExternalRefHistoryQuerier = (*DoltStore)(nil)
var _ storage.EventsJournalConfigurer = (*DoltStore)(nil)

// DoltStore implements the Storage interface using Dolt
type DoltStore struct {
	db       *sql.DB
	dbPath   string      // Path to Dolt data directory (server root, e.g. .beads/dolt/)
	beadsDir string      // Path to .beads directory (parent of dbPath)
	database string      // Database name (subdirectory under dbPath)
	closed   atomic.Bool // Tracks whether Close() has been called
	// eventsJournalEnabled activates the durable events journal for THIS store
	// instance only (storage.EventsJournalConfigurer); never process-global.
	eventsJournalEnabled atomic.Bool
	connStr              string       // Connection string for reconnection
	cfg                  *Config      // Config this store was opened with (rebuildPoolAfterMigration)
	serverEndpoint       string       // Exact endpoint bound to bootstrap reset authority
	mu                   sync.RWMutex // Protects concurrent access
	readOnly             bool         // True if opened in read-only mode
	credentialKey        []byte       // Random encryption key for federation credentials

	// localActiveDatabaseDir is the exact active database directory when this
	// store instance has authoritative local filesystem access. It is resolved
	// once at construction; empty means sizing is unsupported for this instance.
	localActiveDatabaseDir string

	customStatusDetailedCache []types.CustomStatus
	customStatusCache         []string
	customStatusCached        bool
	customTypeCache           []string
	customTypeCached          bool
	infraTypeCache            map[string]bool
	infraTypeCached           bool
	cacheMu                   sync.Mutex

	// OTel span attribute cache (avoids per-call allocation)
	spanAttrsOnce  sync.Once
	spanAttrsCache []attribute.KeyValue

	// Circuit breaker for Dolt server connections
	breaker *circuitBreaker

	// Version control config
	committerName  string
	committerEmail string
	remote         string // Default remote for push/pull
	branch         string // Current branch
	remoteUser     string // Remote auth user for Hosted Dolt push/pull (optional)
	remotePassword string // Remote auth password for Hosted Dolt push/pull (optional)
	serverMode     bool   // true when connected to external dolt sql-server (not embedded)

	// autoStartedServerDir is set when this store triggered a dolt sql-server
	// auto-start. Close() uses it to stop the server when the last store
	// referencing it is closed (tracked via autoStartRefs).
	autoStartedServerDir string
}

// Config holds Dolt database configuration
type Config struct {
	Path           string // Path to Dolt database directory
	BeadsDir       string // Path to .beads directory (for server auto-start when Path is custom)
	CommitterName  string // Git-style committer name
	CommitterEmail string // Git-style committer email
	Remote         string // Default remote name (e.g., "origin")
	Database       string // Database name within Dolt (default: "beads")
	ReadOnly       bool   // Open in read-only mode (skip schema init)
	Preview        bool   // Non-mutating preview: embedded opens skip schema init and refuse writes

	// LenientOpen opens the store leniently: embedded mode only. A migration
	// gate refusal (#4259) or a dirty-working-set refusal (#4566) skips the
	// migration instead of failing the open. Set for working-set-reconcile
	// commands (bd dolt commit, bd vc commit; #4566), whose entire purpose is
	// to clear the working set that the migration would otherwise refuse to
	// touch. Ignored in server mode.
	LenientOpen bool

	// Server connection options
	ServerSocket   string // Unix domain socket path (overrides Host/Port when set)
	ServerHost     string // Server host (default: 127.0.0.1)
	ServerPort     int    // Server port (default: 3307)
	ServerUser     string // MySQL user (default: root)
	ServerPassword string // MySQL password (default: empty, can be set via BEADS_DOLT_PASSWORD)
	ServerTLS      bool   // Enable TLS for server connections (required for Hosted Dolt)

	// ServerPortSource records which step of doltserver's port-resolution
	// chain (or the caller-explicit/env-var reads in applyConfigDefaults)
	// produced ServerPort. Zero value (doltserver.PortSourceUnset) when
	// ServerPort was never resolved from a source (i.e. left 0). A caller
	// that presets ServerPort before applyConfigDefaults runs gets
	// PortSourceCallerExplicit stamped in. Consulted by newServerMode's
	// auto-start path to decide whether silently retargeting to a different
	// port is safe (GH#4052).
	ServerPortSource doltserver.PortSource

	// ServerPortSharedServer mirrors doltserver.Config.PortSharedServer:
	// true when ServerPort was resolved via shared-server mode
	// (BEADS_DOLT_SHARED_SERVER=1). In shared-server mode, auto-start's
	// EnsureRunningDetailed(resolvedBeadsDir) always spins up a repo-local
	// server (a different database than the shared one), so a port change
	// here is never a benign refresh regardless of ServerPortSource —
	// consulted by newServerMode's auto-start path alongside
	// ServerPortSource.IsAuthoritative() (GH#4052).
	ServerPortSharedServer bool

	// Remote auth for Hosted Dolt push/pull (optional)
	// When set, Push/Pull use the --user flag and set DOLT_REMOTE_PASSWORD env var.
	RemoteUser     string // Hosted Dolt remote user (set via DOLT_REMOTE_USER env var)
	RemotePassword string // Hosted Dolt remote password (set via DOLT_REMOTE_PASSWORD env var)

	// SyncRemote holds the effective sync remote URL (from sync.remote
	// or deprecated sync.git-remote). Used for context-aware error hints.
	SyncRemote string

	// CreateIfMissing allows CREATE DATABASE when the target database does not
	// exist on the server. Only explicit initialization, migration, or new-board
	// creation paths should set this to true. Normal open paths leave it false,
	// which causes an error if the database is missing — preventing silent
	// creation of shadow databases on the wrong server.
	CreateIfMissing bool

	// ServerMode indicates this config targets an external dolt sql-server
	// rather than the embedded Dolt engine. Set by the store factory based
	// on metadata.json dolt_mode or BEADS_DOLT_SERVER_MODE env var.
	ServerMode bool

	// ProxiedServer indicates this config targets a per-workspace proxied
	// dolt sql-server (a parent proxy + a child dolt sql-server, both rooted
	// at <BeadsDir>/dolt). Mutually exclusive with ServerMode: the
	// proxied path owns its own connection details and does not consult
	// ServerHost/Port/Socket/User. Set by the store factory based on
	// metadata.json dolt_mode=proxied-server.
	ProxiedServer bool

	// Gateway indicates the server is an authenticating gateway server: a credential
	// command supplies a short-lived token as the connection username. bd treats such a
	// server as owning database routing and schema, so it connects with the project
	// database, skips the no-database admin probe, and never issues SHOW DATABASES /
	// CREATE DATABASE or schema DDL (drift check only, like ReadOnly). Set by
	// ApplyGatewayCredential, never by hand.
	Gateway bool

	// AutoStart enables transparent server auto-start when connection fails.
	// When true and the host is localhost, bd will start a dolt sql-server
	// automatically if one isn't running. Disabled under orchestrator (GT_ROOT set).
	AutoStart bool

	// DisableAutoStart suppresses implicit server startup even when standalone
	// defaults would enable it. Diagnostic paths use this to stay read-only.
	DisableAutoStart bool

	// MaxOpenConns overrides the connection pool size (0 = default 10).
	// Set to 1 for branch isolation in tests (DOLT_CHECKOUT is session-level).
	MaxOpenConns int

	// MaxIdleConns overrides the maximum number of idle pooled connections
	// (0 = default min(5, MaxOpenConns)). Higher values keep more connections
	// warm between queries, reducing NewConnection/ConnectionClosed churn.
	MaxIdleConns int

	// ConnMaxLifetime overrides how long a pooled connection may be reused
	// before the pool retires it (0 = default 1 hour). Long-lived daemons
	// should not use a short lifetime — every retire+reopen shows up as a
	// NewConnection event in dolt-server.log and churns the pool for no
	// benefit when the server is local and stable.
	ConnMaxLifetime time.Duration

	// ConnMaxIdleTime overrides how long a connection may sit idle in the pool
	// before the pool retires it (0 = default 20s). This must stay below the
	// dolt sql-server wait_timeout (currently 30s) so the pool retires an idle
	// connection before the server reaps it server-side; otherwise the next
	// query handed a server-reaped connection fails with "invalid connection".
	ConnMaxIdleTime time.Duration

	// PoolReadTimeout / PoolWriteTimeout override the per-I/O read/write
	// deadlines on shared-pool connections (0 = default 10s each; see
	// buildServerDSN). The default's fast-fail is right for a healthy local
	// server, but on an overloaded shared server it kills ordinary queries
	// mid-flight ("client connection went away", wy-b72dj/bd-vz0y9); raising
	// it is the intended relief valve for such deployments. Known-long
	// operations should not lean on this — route them through
	// execWithLongTimeout/openLongTimeoutConn instead.
	PoolReadTimeout  time.Duration
	PoolWriteTimeout time.Duration

	// PoolReadTimeoutFallback replaces the built-in 10s pool read deadline
	// ONLY when nothing else set PoolReadTimeout — not the caller, not
	// BEADS_DOLT_POOL_READ_TIMEOUT, not dolt.pool-read-timeout. It lets a
	// command whose ordinary statements are known to run long (bd import's
	// chunk commits, which a server-side auto_gc pause stretches past 10s —
	// wy-sbgucn) raise its own default without overriding an operator's
	// explicit choice. 0 = keep the built-in default.
	PoolReadTimeoutFallback time.Duration
}

// Defaults for the *sql.DB connection pool. Exported for tests/callers that
// want to reason about the out-of-the-box pool limits without having to read
// openServerConnection.
const (
	defaultMaxOpenConns    = 10
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = time.Hour
	// defaultConnMaxIdleTime keeps idle pooled connections shorter-lived than the
	// dolt sql-server wait_timeout (30s) so the pool retires an idle connection
	// before the server reaps it; this prevents the next read from picking up a
	// server-closed connection and failing with "invalid connection".
	defaultConnMaxIdleTime = 20 * time.Second
	// defaultPoolReadTimeout / defaultPoolWriteTimeout are the per-I/O
	// deadlines on shared-pool connections. Overridable via
	// Config.PoolReadTimeout/PoolWriteTimeout (BEADS_DOLT_POOL_READ_TIMEOUT /
	// BEADS_DOLT_POOL_WRITE_TIMEOUT, dolt.pool-read-timeout /
	// dolt.pool-write-timeout); the defaults themselves are deliberately
	// unchanged (bd-vz0y9).
	defaultPoolReadTimeout  = 10 * time.Second
	defaultPoolWriteTimeout = 10 * time.Second
)

// cliExecTimeout is the default maximum time to wait for dolt CLI
// push/pull/fetch operations. SSH transfers can hang indefinitely on network
// issues or SSH key prompts; this prevents the process from blocking forever.
// Large transfers can legitimately run longer (e.g. pushing a big chunk store
// to a cloud remote, or a transfer serialized behind a busy dolt sql-server
// that holds the database directory lock); set BEADS_CLI_TRANSFER_TIMEOUT to
// override.
const cliExecTimeout = 5 * time.Minute

// cliExecTimeoutEnv is the environment variable that overrides cliExecTimeout.
const cliExecTimeoutEnv = "BEADS_CLI_TRANSFER_TIMEOUT"

// cliExecWaitDelay bounds how long Wait/CombinedOutput may keep waiting after
// the transfer context expires. CommandContext kills only the direct dolt
// child; a grandchild (e.g. a cloud credential helper) that inherited the
// output pipes would otherwise keep Wait blocked indefinitely after the kill.
const cliExecWaitDelay = 10 * time.Second

// cliExecTimeoutDuration returns the configured CLI transfer timeout. The env
// var BEADS_CLI_TRANSFER_TIMEOUT overrides the compiled-in cliExecTimeout
// const; valid time.ParseDuration strings (e.g. "20m", "90s") or bare numbers
// treated as seconds (e.g. "90") are accepted. Unset or invalid values fall
// back to cliExecTimeout.
func cliExecTimeoutDuration() time.Duration {
	return timeoutFromEnv(cliExecTimeoutEnv, cliExecTimeout)
}

func withCLIExecTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, cliExecTimeoutDuration())
}

// timeoutFromEnv returns the duration configured in the named env var, falling
// back to fallback when the var is unset, unparsable, or non-positive. Valid
// time.ParseDuration strings (e.g. "2m", "90s") or bare numbers treated as
// seconds (e.g. "90") are accepted.
func timeoutFromEnv(env string, fallback time.Duration) time.Duration {
	return parseTimeout(os.Getenv(env), fallback)
}

// parseTimeout parses a duration setting, falling back to fallback when raw is
// empty, unparsable, or non-positive. Valid time.ParseDuration strings (e.g.
// "2m", "90s") or bare numbers treated as seconds (e.g. "90") are accepted.
func parseTimeout(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d > 0 {
			return d
		}
		return fallback
	}
	if d, err := time.ParseDuration(raw + "s"); err == nil {
		if d > 0 {
			return d
		}
		return fallback
	}
	return fallback
}

// fsckTimeout is the default maximum time to wait for dolt fsck to verify the
// local chunk store before a push. fsck reads local files only; 30 seconds is
// ample for small stores. Large stores may need more time; set
// BEADS_FSCK_TIMEOUT to override.
const fsckTimeout = 30 * time.Second

// fsckTimeoutEnv is the environment variable that overrides fsckTimeout.
const fsckTimeoutEnv = "BEADS_FSCK_TIMEOUT"

// fsckTimeoutDuration returns the configured fsck timeout. The env var
// BEADS_FSCK_TIMEOUT overrides the compiled-in fsckTimeout const; valid
// time.ParseDuration strings (e.g. "2m", "90s") or bare numbers treated as
// seconds (e.g. "90") are accepted. Unset or invalid values fall back to
// fsckTimeout.
func fsckTimeoutDuration() time.Duration {
	return timeoutFromEnv(fsckTimeoutEnv, fsckTimeout)
}

// Retry configuration for transient connection errors (stale pool connections,
// brief network issues, server restarts).
const serverRetryMaxElapsed = 30 * time.Second

func newServerRetryBackoff() backoff.BackOff {
	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = serverRetryMaxElapsed
	return bo
}

// isRetryableError returns true if the error is a transient connection error
// that should be retried in server mode.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if schema.IsMigrationLockError(err) {
		return true
	}
	// A decoded 1105 is a definite server response. Preserve the two explicit
	// server-startup recoveries below, but do not let any other 1105 enter the
	// general retry or circuit-breaker path just because its message happens to
	// contain connection-like wording.
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1105 {
		message := strings.ToLower(mysqlErr.Message)
		if strings.Contains(message, "no root value found") ||
			strings.Contains(message, "database is read only") {
			return true
		}
		return false
	}
	errStr := strings.ToLower(err.Error())
	// MySQL driver transient errors
	if strings.Contains(errStr, "driver: bad connection") {
		return true
	}
	if strings.Contains(errStr, "invalid connection") {
		return true
	}
	// Network transient errors (brief blips, not persistent failures)
	if strings.Contains(errStr, "broken pipe") {
		return true
	}
	if strings.Contains(errStr, "connection reset") {
		return true
	}
	// Server restart: "connection refused" is transient — the server may
	// come back within the backoff window (30s). Retrying here prevents
	// a brief server outage from cascading into permanent failures.
	if strings.Contains(errStr, "connection refused") {
		return true
	}
	// Dolt read-only mode: under load, Dolt may enter read-only mode with
	// "cannot update manifest: database is read only". This clears after
	// a server restart, so it's worth retrying.
	if strings.Contains(errStr, "database is read only") {
		return true
	}
	// MySQL error 2013: mid-query disconnect
	if strings.Contains(errStr, "lost connection") {
		return true
	}
	// MySQL error 2006: idle connection timeout
	if strings.Contains(errStr, "gone away") {
		return true
	}
	// Go net package timeout on read/write
	if strings.Contains(errStr, "i/o timeout") {
		return true
	}
	// Dolt server catalog race: after CREATE DATABASE, the server's in-memory
	// catalog may not have registered the new database yet. The immediately
	// following USE (implicit via DSN) fails with "Unknown database". This is
	// transient and resolves once the catalog refreshes. (GH-1851)
	if strings.Contains(errStr, "unknown database") {
		return true
	}
	// Dolt internal race: after CREATE DATABASE, information_schema queries
	// on the new database may fail with "no root value found in session" if
	// the server hasn't finished initializing the database's root value.
	// This is transient and resolves on retry.
	if strings.Contains(errStr, "no root value found") {
		return true
	}
	return false
}

// isLockError returns true if the error indicates a Dolt lock contention problem.
// These can occur when the Dolt server's storage layer is locked by another
// process or a stale LOCK file was left behind by a crashed server.
func isLockError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "database is locked") ||
		strings.Contains(errStr, "lock file") ||
		strings.Contains(errStr, "noms lock") ||
		strings.Contains(errStr, "locked by another dolt process")
}

// wrapLockError wraps lock-related errors with actionable guidance.
// Non-lock errors and nil are returned unchanged.
func wrapLockError(err error) error {
	if !isLockError(err) {
		return err
	}
	hint := lockProcessHint()
	return fmt.Errorf("%w\n\nThe Dolt database is locked.%s\n"+
		"Try: bd doctor --fix (clears stale locks), or kill the holding process.", err, hint)
}

// lockProcessHint tries to identify the process holding the database lock.
// Returns a hint string like " Process 12345 (bd) may be holding the lock."
// Returns empty string if identification fails or on unsupported platforms.
func lockProcessHint() string {
	// Look for other bd/dolt processes that might hold the lock
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// /proc not available (macOS, Windows, FreeBSD) — skip PID detection
		return ""
	}

	myPID := os.Getpid()
	var holders []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == myPID {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		cmd := string(cmdline)
		if strings.Contains(cmd, "bd") || strings.Contains(cmd, "dolt") {
			holders = append(holders, fmt.Sprintf("%d", pid))
		}
	}

	if len(holders) == 0 {
		return ""
	}
	if len(holders) == 1 {
		return fmt.Sprintf(" Process %s (bd/dolt) may be holding the lock.", holders[0])
	}
	return fmt.Sprintf(" Processes %s (bd/dolt) may be holding the lock.", strings.Join(holders, ", "))
}

// withRetry executes an operation with retry for transient errors.
// If a circuit breaker is configured, it checks the breaker before each attempt
// and records connection failures/successes to coordinate fail-fast across processes.
func (s *DoltStore) withRetry(ctx context.Context, op func() error) error {
	return s.withRetryClassified(ctx, op, isRetryableError)
}

// withTransactionSetupRetry extends the general connection retry policy with
// rollback-guaranteed transaction errors. Public transaction wrappers use it
// only before their callback begins; callback-entered errors are permanent.
func (s *DoltStore) withTransactionSetupRetry(ctx context.Context, op func() error) error {
	return s.withRetryClassified(ctx, op, func(err error) bool {
		return isRetryableError(err) || isDoltAutocommitRollbackError(err) || isSerializationError(err)
	})
}

type circuitWriteContextKey struct{}

func circuitWriteManaged(ctx context.Context) bool {
	_, ok := ctx.Value(circuitWriteContextKey{}).(struct{})
	return ok
}

// withCircuitWrite admits one externally visible write and records its
// terminal success. Nested retry helpers keep failure accounting but defer
// their success reset to this boundary.
func (s *DoltStore) withCircuitWrite(ctx context.Context, op func(context.Context) error) error {
	if circuitWriteManaged(ctx) {
		return op(ctx)
	}
	if s.breaker != nil && !s.breaker.Allow() {
		doltMetrics.circuitRejected.Add(ctx, 1)
		return ErrCircuitOpen
	}
	err := op(context.WithValue(ctx, circuitWriteContextKey{}, struct{}{}))
	if err == nil && s.breaker != nil {
		s.breaker.RecordSuccess()
	}
	return err
}

func (s *DoltStore) withRetryClassified(ctx context.Context, op func() error, retryable func(error) bool) error {
	// Circuit breaker: fail-fast if the server is known to be down.
	if !circuitWriteManaged(ctx) && s.breaker != nil && !s.breaker.Allow() {
		doltMetrics.circuitRejected.Add(ctx, 1)
		return ErrCircuitOpen
	}

	attempts := 0
	bo := newServerRetryBackoff()
	err := backoff.Retry(func() error {
		attempts++
		return s.classifyManagedRetry(ctx, op(), retryable)
	}, backoff.WithContext(bo, ctx))
	if attempts > 1 {
		doltMetrics.retryCount.Add(ctx, int64(attempts-1))
	}
	return err
}

// classifyManagedRetry maps one attempt's result to a backoff decision: nil to
// stop on success, a bare error to retry, or a backoff.Permanent to stop on
// failure. It owns the attempt's breaker accounting, deferring the success reset
// to an outer withCircuitWrite boundary when one is active (circuitWriteManaged).
func (s *DoltStore) classifyManagedRetry(ctx context.Context, err error, retryable func(error) bool) error {
	if err == nil {
		if !circuitWriteManaged(ctx) && s.breaker != nil {
			s.breaker.RecordSuccess()
		}
		return nil
	}
	// An already-permanent error (e.g. a callback-entered RunInTransaction
	// failure) is terminal and must not be re-wrapped.
	var permanent *backoff.PermanentError
	if errors.As(err, &permanent) {
		return err
	}
	// An indeterminate commit is never replayed — replay could double-apply —
	// but a connection loss still feeds the breaker before we stop.
	if errors.Is(err, ErrCommitIndeterminate) {
		if tripped := s.recordRetryFailure(ctx, err); tripped != nil {
			return tripped
		}
		return backoff.Permanent(err)
	}
	if retryable(err) {
		if tripped := s.recordRetryFailure(ctx, err); tripped != nil {
			return tripped
		}
		return err // backoff will retry
	}
	return backoff.Permanent(err) // non-retryable — stop immediately
}

// recordRetryFailure records a connection-level failure to the breaker. It
// returns a permanent "circuit breaker tripped" error when this failure trips
// the breaker — signaling the retry loop to stop — and nil otherwise, including
// when err is not a connection error or no breaker is configured.
func (s *DoltStore) recordRetryFailure(ctx context.Context, err error) error {
	if s.breaker == nil || !isConnectionError(err) {
		return nil
	}
	s.breaker.RecordFailure()
	if s.breaker.State() == circuitOpen {
		doltMetrics.circuitTrips.Add(ctx, 1)
		return backoff.Permanent(fmt.Errorf("%w (circuit breaker tripped)", err))
	}
	return nil
}

// doltTracer is the OTel tracer for SQL-level spans.
// It uses the global provider, which is a no-op until telemetry.Init() is called.
var doltTracer = otel.Tracer("github.com/steveyegge/beads/storage/dolt")

// doltMetrics holds OTel metric instruments for the dolt storage backend.
// Instruments are registered against the global delegating provider at init time,
// so they automatically forward to the real provider once telemetry.Init() runs.
var doltMetrics struct {
	retryCount           metric.Int64Counter
	lockWaitMs           metric.Float64Histogram
	circuitTrips         metric.Int64Counter
	circuitRejected      metric.Int64Counter
	serializationErrors  metric.Int64Counter
	writeRetries         metric.Int64Counter
	connAcquireMs        metric.Float64Histogram
	poolWaitCount        metric.Int64Counter
	poolWaitMs           metric.Float64Histogram
	claimVerifyLost      metric.Int64Counter
	claimVerifyRecovered metric.Int64Counter
	ignoredTxFreshPool   metric.Int64Counter
}

func init() {
	m := otel.Meter("github.com/steveyegge/beads/storage/dolt")
	doltMetrics.retryCount, _ = m.Int64Counter("bd.db.retry_count",
		metric.WithDescription("SQL operations retried due to server-mode transient errors"),
		metric.WithUnit("{retry}"),
	)
	doltMetrics.lockWaitMs, _ = m.Float64Histogram("bd.db.lock_wait_ms",
		metric.WithDescription("Time spent waiting to acquire database locks"),
		metric.WithUnit("ms"),
	)
	doltMetrics.circuitTrips, _ = m.Int64Counter("bd.db.circuit_trips",
		metric.WithDescription("Number of times the Dolt circuit breaker tripped open"),
		metric.WithUnit("{trip}"),
	)
	doltMetrics.circuitRejected, _ = m.Int64Counter("bd.db.circuit_rejected",
		metric.WithDescription("Requests rejected by open circuit breaker (fail-fast)"),
		metric.WithUnit("{request}"),
	)
	doltMetrics.serializationErrors, _ = m.Int64Counter("bd.db.serialization_errors",
		metric.WithDescription("Serialization failures (MySQL 1213/1205) before retry"),
		metric.WithUnit("{error}"),
	)
	doltMetrics.writeRetries, _ = m.Int64Counter("bd.write_retries_total",
		metric.WithDescription("Write-tx retries in withRetryTx (label: type=serialization|connection)"),
		metric.WithUnit("{retry}"),
	)
	doltMetrics.connAcquireMs, _ = m.Float64Histogram("bd.db.conn_acquire_ms",
		metric.WithDescription("Time to acquire a pooled connection for a Dolt transaction"),
		metric.WithUnit("ms"),
	)
	doltMetrics.poolWaitCount, _ = m.Int64Counter("bd.db.pool_wait_count",
		metric.WithDescription("Number of times a connection acquisition had to wait for the pool"),
		metric.WithUnit("{wait}"),
	)
	doltMetrics.poolWaitMs, _ = m.Float64Histogram("bd.db.pool_wait_ms",
		metric.WithDescription("Total time connections spent waiting due to pool exhaustion"),
		metric.WithUnit("ms"),
	)
	doltMetrics.claimVerifyLost, _ = m.Int64Counter("bd.claim_verify_lost_total",
		metric.WithDescription("Claim-family writes that reported success but failed verify-by-re-read (label: op=claim|unclaim)"),
		metric.WithUnit("{write}"),
	)
	doltMetrics.claimVerifyRecovered, _ = m.Int64Counter("bd.claim_verify_recovered_total",
		metric.WithDescription("Indeterminate claim-family commits resolved by re-read (label: op, outcome=applied|replayed)"),
		metric.WithUnit("{write}"),
	)
	doltMetrics.ignoredTxFreshPool, _ = m.Int64Counter("bd.db.ignored_tx_fresh_pool",
		metric.WithDescription("ignored-tx transactions that fell back to a dedicated single-connection pool instead of borrowing from the main pool"),
		metric.WithUnit("{tx}"),
	)
}

// registerPoolGauges registers observable gauges that report sql.DB pool stats
// on each OTel collection cycle. These are essential for diagnosing shared-server
// degradation under multi-worktree load (GH#3140).
func (s *DoltStore) registerPoolGauges() {
	m := otel.Meter("github.com/steveyegge/beads/storage/dolt")
	db := s.db

	m.Int64ObservableGauge("bd.db.pool_open", //nolint:errcheck,gosec
		metric.WithDescription("Current number of open connections (in-use + idle)"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(db.Stats().OpenConnections))
			return nil
		}),
	)
	m.Int64ObservableGauge("bd.db.pool_in_use", //nolint:errcheck,gosec
		metric.WithDescription("Connections currently in use"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(db.Stats().InUse))
			return nil
		}),
	)
	m.Int64ObservableGauge("bd.db.pool_idle", //nolint:errcheck,gosec
		metric.WithDescription("Idle connections in pool"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(db.Stats().Idle))
			return nil
		}),
	)
	m.Int64ObservableGauge("bd.db.pool_max_open", //nolint:errcheck,gosec
		metric.WithDescription("Maximum number of open connections (pool limit)"),
		metric.WithUnit("{connection}"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(db.Stats().MaxOpenConnections))
			return nil
		}),
	)
}

// doltSpanAttrs returns the fixed attributes shared by all SQL spans.
// Cached to avoid allocating on every call (hot path when telemetry is disabled
// still flows through no-op tracers).
func (s *DoltStore) doltSpanAttrs() []attribute.KeyValue {
	s.spanAttrsOnce.Do(func() {
		s.spanAttrsCache = []attribute.KeyValue{
			attribute.String("db.system", "dolt"),
			attribute.Bool("db.readonly", s.readOnly),
			attribute.Bool("db.server_mode", true), // TODO: update when embedded mode returns
		}
	})
	return s.spanAttrsCache
}

// spanSQL truncates a SQL string to keep spans readable.
func spanSQL(q string) string {
	if len(q) > 300 {
		return q[:300] + "…"
	}
	return q
}

// endSpan records an error (if any) and ends the span.
func endSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// execContext wraps a write statement in an explicit BEGIN/COMMIT to ensure
// durability when the Dolt server runs with autocommit disabled (the default
// when started with --no-auto-commit). Without this, writes remain in an
// ErrStoreClosed is returned when an operation is attempted on a closed store.
var ErrStoreClosed = errors.New("store is closed")

// withReadTx runs fn inside a transaction while holding the store's read-lock.
// Used for read operations that need a *sql.Tx to share issueops functions.
//
// The whole BeginTx+fn is wrapped in withRetry so a transient connection error
// (e.g. "invalid connection" when the dolt sql-server reaps a pooled connection
// that has been idle past its wait_timeout) is retried rather than surfaced to
// the caller. This is safe because fn is read-only and the transaction is always
// rolled back, so re-running the operation has no side effects.
func (s *DoltStore) withReadTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.withRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin read tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		return fn(tx)
	})
}

// execer is satisfied by both *sql.DB and *sql.Conn, letting pinStoreBranch
// share one implementation between withReadTxLongTimeout's one-shot *sql.DB
// and a single pinned *sql.Conn (see recomputeAllBlocked/recomputeBlockedTx).
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// pinStoreBranch reproduces the store's real active branch on conn. Branch
// checkout is Dolt session state, scoped to one physical connection — a
// fresh connection (from openLongTimeoutConn or db.Conn) defaults to the
// database's default branch rather than inheriting whatever branch the
// store's pooled session (s.db) is actually checked out to. Query s.db for
// its live active_branch() (preferred over the possibly-stale s.branch
// field, which is only ever set inside Checkout() and left behind by any
// checkout run directly against s.db, e.g. the test harness's
// per-test-branch isolation) and reproduce that checkout on conn before any
// read or write runs on it. Every caller that opens its own connection
// instead of using the shared pool (s.db) must call this before issuing any
// branch-sensitive query — see openLongTimeoutConn's callers.
//
// Caveat: the SELECT active_branch() read below is only well-defined when the
// pool behind s.db is effectively single-connection. Checkout() leases one
// connection from that pool (s.db.Conn), runs DOLT_CHECKOUT on it and returns
// it — the branch stays with that physical connection, because checkout is
// per-connection session state. The pool defaults to defaultMaxOpenConns (10,
// overridable by BEADS_DOLT_MAX_CONNS or dolt.max-conns), so on a genuinely
// multi-connection pool this read may be served by a sibling connection that
// never saw that checkout and still reports the branch it was opened with.
// The paths that rely on this pin run effectively single-connection —
// server-mode stores are pinned to MaxOpenConns=1 precisely because branch
// isolation is session-level (see iter_issues.go) — so the read is reliable
// in practice rather than by construction. The s.branch fallback does not
// close the gap either: it fires only when the query errors, not when it
// succeeds with another connection's answer.
func (s *DoltStore) pinStoreBranch(ctx context.Context, conn execer) error {
	var branch string
	if scanErr := s.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); scanErr == nil {
		if branch != "" {
			if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", branch); err != nil {
				return fmt.Errorf("checkout active branch %q: %w", branch, err)
			}
		}
	} else if s.branch != "" {
		// Fall back to the store's recorded branch rather than failing the
		// whole call outright.
		if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", s.branch); err != nil {
			return fmt.Errorf("checkout fallback branch %q: %w", s.branch, err)
		}
	}
	return nil
}

// withReadTxLongTimeout is like withReadTx but runs fn against a dedicated
// one-shot connection with a 5-minute read timeout (see openLongTimeoutConn)
// instead of the shared pool's 10s ReadTimeout (see buildServerDSN). Use for
// read queries that are known to legitimately run long, e.g. dolt_history_*
// system-table scans on issues with many revisions — the pooled 10s client
// timeout otherwise surfaces as an intermittent MySQL i/o timeout / invalid
// connection error (ga-ahnxx) well before the query would have finished on
// its own. Note this only removes the client-side ceiling: the Dolt server's
// own read_timeout_millis (often configured short to bound orphaned-connection
// pileup — see the comment next to it) still applies server-side and can
// independently abort a query whose
// per-row production stalls past that window.
func (s *DoltStore) withReadTxLongTimeout(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.withRetry(ctx, func() error {
		db, err := s.openLongTimeoutConn()
		if err != nil {
			return err
		}
		defer db.Close()
		// The fresh one-shot connection defaults to the default branch, not
		// whatever branch the store's pooled session (s.db) is actually
		// checked out to — reproduce it before starting the read tx.
		if err := s.pinStoreBranch(ctx, db); err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin read tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		return fn(tx)
	})
}

func (s *DoltStore) withRetryTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	// Keep circuit admission at the transaction retry boundary. Calling
	// withRetry from here would multiply retries and could replay a write after
	// an indeterminate commit.
	if !circuitWriteManaged(ctx) && s.breaker != nil && !s.breaker.Allow() {
		doltMetrics.circuitRejected.Add(ctx, 1)
		return ErrCircuitOpen
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = 5 * time.Second
	if s.serverMode {
		bo.MaxElapsedTime = 15 * time.Second
	}
	return backoff.Retry(func() error {
		err := s.withWriteTx(ctx, fn)
		if err == nil {
			if !circuitWriteManaged(ctx) && s.breaker != nil {
				s.breaker.RecordSuccess()
			}
			return nil
		}
		// Dolt's exact 1105 autocommit rollback proves the transaction did not
		// land. This is the only 1105 replayed, and withRetryTx is the boundary
		// that recreates the complete SQL transaction on every attempt.
		if isDoltAutocommitRollbackError(err) {
			doltMetrics.serializationErrors.Add(ctx, 1)
			doltMetrics.writeRetries.Add(ctx, 1, metric.WithAttributes(attribute.String("type", "serialization")))
			return err
		}
		// A commit result marked indeterminate may have landed before its
		// response was lost. Never replay the callback in that case.
		if errors.Is(err, ErrCommitIndeterminate) {
			err = s.recordDoltPublicationFailure(ctx, err)
			return backoff.Permanent(fmt.Errorf("write commit result indeterminate after connection loss (not retried to avoid double-apply): %w", err))
		}
		// Serialization failures (1213/1205) guarantee a server-side rollback,
		// so the write never landed — safe to replay at any phase.
		if isSerializationError(err) {
			doltMetrics.serializationErrors.Add(ctx, 1)
			doltMetrics.writeRetries.Add(ctx, 1, metric.WithAttributes(attribute.String("type", "serialization")))
			return err // retryable
		}
		// Connection failures reaching this branch happened before commit;
		// withWriteTx marks ambiguous commit response loss with the public
		// ErrCommitIndeterminate sentinel above.
		if isRetryableError(err) {
			doltMetrics.writeRetries.Add(ctx, 1, metric.WithAttributes(attribute.String("type", "connection")))
			if s.breaker != nil && isConnectionError(err) {
				s.breaker.RecordFailure()
				if s.breaker.State() == circuitOpen {
					doltMetrics.circuitTrips.Add(ctx, 1)
					return backoff.Permanent(fmt.Errorf("%w (circuit breaker tripped)", err))
				}
			}
			return err // pre-commit transient: retryable
		}
		return backoff.Permanent(err)
	}, backoff.WithContext(bo, ctx))
}

func (s *DoltStore) withWriteTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write tx: %w", err)
	}
	clearJournalScope := s.scopeEventsJournalTransaction(tx)
	defer clearJournalScope()
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return wrapSQLCommitError("commit write tx", err)
	}
	return nil
}

// SetEventsJournalEnabled activates the journal for this store instance only.
func (s *DoltStore) SetEventsJournalEnabled(enabled bool) {
	s.eventsJournalEnabled.Store(enabled)
}

func (s *DoltStore) scopeEventsJournalTransaction(tx *sql.Tx) func() {
	return issueops.ScopeEventsJournalTransaction(tx, s.eventsJournalEnabled.Load())
}

func (s *DoltStore) commitSQLTx(ctx context.Context, op string, tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return s.recordDoltPublicationFailure(ctx, wrapSQLCommitError(op, err))
	}
	return nil
}

// uncommitted implicit transaction that Dolt rolls back on connection close,
// causing silent data loss for callers that do not use db.BeginTx themselves.
func (s *DoltStore) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if s.closed.Load() {
		return nil, ErrStoreClosed
	}
	ctx, span := doltTracer.Start(ctx, "dolt.exec",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("db.operation", "exec"),
			attribute.String("db.statement", spanSQL(query)),
		)...),
	)
	var result sql.Result
	err := s.withRetry(ctx, func() error {
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return txErr
		}
		var execErr error
		result, execErr = tx.ExecContext(ctx, query, args...)
		if execErr != nil {
			_ = tx.Rollback()
			return execErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return wrapSQLCommitError("commit exec tx", commitErr)
		}
		return nil
	})
	finalErr := wrapLockError(err)
	endSpan(span, finalErr)
	return result, finalErr
}

// DB returns the underlying sql.DB connection for direct queries.
// Use sparingly — prefer the store's typed methods for normal operations.
func (s *DoltStore) DB() *sql.DB {
	return s.db
}

// RemoteName returns the configured default sync remote name ("origin" unless
// overridden), the remote Push/Pull target when no explicit remote is given.
func (s *DoltStore) RemoteName() string {
	return s.remote
}

// BackupAdd registers a Dolt backup destination.
func (s *DoltStore) BackupAdd(ctx context.Context, name, url string) error {
	return versioncontrolops.BackupAdd(ctx, s.db, name, url)
}

// BackupSync pushes the database to the named backup destination.
// Runs on a long-timeout connection: a sync to a remote destination
// streams the database and outlives the pool's 10s ReadTimeout.
func (s *DoltStore) BackupSync(ctx context.Context, name string) error {
	db, err := s.oneShotConn(0)
	if err != nil {
		return err
	}
	defer db.Close()
	return versioncontrolops.BackupSync(ctx, db, name)
}

// BackupRemove removes a configured Dolt backup destination.
func (s *DoltStore) BackupRemove(ctx context.Context, name string) error {
	return versioncontrolops.BackupRemove(ctx, s.db, name)
}

// BackupDatabase registers dir as a file:// Dolt backup remote and syncs
// the full database to it, preserving complete commit history.
func (s *DoltStore) BackupDatabase(ctx context.Context, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("backup destination does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup destination is not a directory: %s", dir)
	}

	backupURL, err := versioncontrolops.DirToFileURL(dir)
	if err != nil {
		return err
	}
	backupName := "backup_export"

	syncDB, err := s.oneShotConn(0)
	if err != nil {
		return err
	}
	defer syncDB.Close()

	// Register as a backup remote (idempotent — remove first if exists).
	_ = versioncontrolops.BackupRemove(ctx, s.db, backupName)
	if err := versioncontrolops.BackupAdd(ctx, s.db, backupName, backupURL); err != nil {
		// Another backup (e.g. "default" registered by `bd backup init`) may
		// already point to this URL. In that case, sync using the existing
		// remote name rather than failing.
		if conflict := versioncontrolops.ExtractAddressConflictName(err); conflict != "" {
			if syncErr := versioncontrolops.BackupSync(ctx, syncDB, conflict); syncErr != nil {
				return fmt.Errorf("sync to backup: %w", syncErr)
			}
			return nil
		}
		return fmt.Errorf("register backup remote: %w", err)
	}
	if err := versioncontrolops.BackupSync(ctx, syncDB, backupName); err != nil {
		return fmt.Errorf("sync to backup: %w", err)
	}
	return nil
}

// RestoreDatabase restores the database from a Dolt backup at dir.
// When force is true, an existing database is overwritten.
func (s *DoltStore) RestoreDatabase(ctx context.Context, dir string, force bool) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("backup source does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup source is not a directory: %s", dir)
	}

	backupURL, err := versioncontrolops.DirToFileURL(dir)
	if err != nil {
		return err
	}
	db, err := s.oneShotConn(0)
	if err != nil {
		return err
	}
	defer db.Close()
	return versioncontrolops.BackupRestore(ctx, db, backupURL, s.database, force)
}

// QueryContext wraps s.db.QueryContext with retry for transient errors.
// Exported so callers (e.g. backup) can run ad-hoc queries with retry
// instead of going through the raw *sql.DB.
func (s *DoltStore) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.queryContext(ctx, query, args...)
}

// queryContext wraps s.db.QueryContext with retry for transient errors.
func (s *DoltStore) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if s.closed.Load() {
		return nil, ErrStoreClosed
	}
	ctx, span := doltTracer.Start(ctx, "dolt.query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("db.operation", "query"),
			attribute.String("db.statement", spanSQL(query)),
		)...),
	)
	var rows *sql.Rows
	err := s.withRetry(ctx, func() error {
		// Close any Rows from a previous failed attempt to avoid leaking connections.
		if rows != nil {
			_ = rows.Close()
			rows = nil
		}
		var queryErr error
		rows, queryErr = s.db.QueryContext(ctx, query, args...)
		return queryErr
	})
	finalErr := wrapLockError(err)
	endSpan(span, finalErr)
	return rows, finalErr
}

// queryRowContext wraps s.db.QueryRowContext with retry for transient errors.
// The scan function receives the *sql.Row and should call .Scan() on it.
func (s *DoltStore) queryRowContext(ctx context.Context, scan func(*sql.Row) error, query string, args ...any) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	ctx, span := doltTracer.Start(ctx, "dolt.query_row",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("db.operation", "query_row"),
			attribute.String("db.statement", spanSQL(query)),
		)...),
	)
	finalErr := wrapLockError(s.withRetry(ctx, func() error {
		row := s.db.QueryRowContext(ctx, query, args...)
		return scan(row)
	}))
	endSpan(span, finalErr)
	return finalErr
}

// applyConfigDefaults fills in default values for unset Config fields.
func applyConfigDefaults(cfg *Config) {
	if cfg.Database == "" {
		// Check env var first — this is the highest-priority override and
		// must be consulted even when no config file was loaded.
		if d := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); d != "" {
			cfg.Database = d
		} else if os.Getenv("BEADS_TEST_MODE") == "1" && cfg.Path != "" {
			// Test mode: derive unique database name from path for isolation.
			// Each test creates a unique temp directory, so hashing the path
			// gives each test its own database on the shared test server.
			h := fnv.New64a()
			_, _ = h.Write([]byte(cfg.Path)) // hash.Hash.Write never returns an error
			cfg.Database = fmt.Sprintf("testdb_%x", h.Sum64())
		} else {
			fmt.Fprintf(os.Stderr, "warning: no database name configured; falling back to default %q\n", configfile.DefaultDoltDatabase)
			cfg.Database = configfile.DefaultDoltDatabase
		}
	}
	if cfg.CommitterName == "" {
		cfg.CommitterName = os.Getenv("GIT_AUTHOR_NAME")
		if cfg.CommitterName == "" {
			cfg.CommitterName = "beads"
		}
	}
	if cfg.CommitterEmail == "" {
		cfg.CommitterEmail = os.Getenv("GIT_AUTHOR_EMAIL")
		if cfg.CommitterEmail == "" {
			cfg.CommitterEmail = "beads@local"
		}
	}
	if cfg.Remote == "" {
		cfg.Remote = "origin"
	}

	// Server connection defaults (applied in server mode; embedded mode bypasses TCP)
	if cfg.ServerSocket == "" {
		cfg.ServerSocket = os.Getenv("BEADS_DOLT_SERVER_SOCKET")
	}
	if cfg.ServerHost == "" {
		// Host resolution: BEADS_DOLT_SERVER_HOST env > default 127.0.0.1.
		if h := os.Getenv("BEADS_DOLT_SERVER_HOST"); h != "" {
			cfg.ServerHost = h
		} else {
			cfg.ServerHost = "127.0.0.1"
		}
	}
	// Port resolution: caller-preset explicit ServerPort > BEADS_DOLT_SERVER_PORT
	// env (or legacy BEADS_DOLT_PORT) > BEADS_TEST_MODE guard > metadata config > default.
	// CRITICAL: BEADS_TEST_MODE=1 forces port 1 (immediate fail) if the resolved port
	// is the production port (DefaultSQLPort). This prevents test databases from leaking
	// onto production even when the port env var is set to 3307 by the orchestrator's beads module.
	// Only an explicit non-production port (e.g., 43211 for a test server)
	// overrides test mode — that's a deliberate test server assignment.
	if cfg.ServerPort != 0 && cfg.ServerPortSource == doltserver.PortSourceUnset {
		// The caller (e.g. `bd init --server-port`, or initGlobalDatabaseConfig's
		// copy-forward of it) already set an explicit port before this function
		// ran. That assertion outranks the ambient env vars below (be-wf9a.1).
		cfg.ServerPortSource = doltserver.PortSourceCallerExplicit
	}
	envPort := os.Getenv("BEADS_DOLT_SERVER_PORT")
	if envPort == "" {
		envPort = os.Getenv("BEADS_DOLT_PORT") // legacy fallback
	}
	// Also fires when ServerPort is already nonzero but its source isn't
	// authoritative (e.g. PortSourcePortFile from applyResolvedConfig's own
	// doltserver.DefaultConfig fallback above) — bd's own port-file
	// bookkeeping must not silently outrank an explicit env override
	// (be-9tju; regression of the hq-27t bug class). A genuinely
	// caller-explicit ServerPort (PortSourceCallerExplicit, stamped above)
	// still wins: IsAuthoritative() is true for it.
	if envPort != "" && (cfg.ServerPort == 0 || !cfg.ServerPortSource.IsAuthoritative()) {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			cfg.ServerPort = p
			// This env read happens before doltserver.DefaultConfig is
			// consulted below, but it is the same authoritative source
			// (BEADS_DOLT_SERVER_PORT / legacy BEADS_DOLT_PORT) — record it
			// so the auto-start fail-closed check in newServerMode sees it.
			cfg.ServerPortSource = doltserver.PortSourceEnv
		}
	}
	// If env var didn't provide a port, consult the full resolution chain:
	// port file > config.yaml > metadata.json (GH#2590).
	// Resolve from the owning .beads dir when available; cfg.Path is the Dolt
	// data path, not the config directory, and using it directly can miss the
	// repo-local port file or metadata.
	if cfg.ServerPort == 0 {
		resolveDir := cfg.BeadsDir
		if resolveDir == "" && cfg.Path != "" {
			resolveDir = filepath.Dir(cfg.Path)
		}
		if resolveDir != "" {
			if resolved := doltserver.DefaultConfig(resolveDir); resolved.Port > 0 {
				cfg.ServerPort = resolved.Port
				cfg.ServerPortSource = resolved.PortSource
				cfg.ServerPortSharedServer = resolved.PortSharedServer
			}
		}
	}
	// Port 0 means "not yet resolved" — auto-start (EnsureRunning) will
	// allocate an ephemeral port. Don't default to 3307 as that caused
	// cross-project data leakage (GH#2098, GH#2372).
	//
	// Test mode guard: force port 1 (immediate fail) if we'd hit production
	// or have no port, to prevent test databases leaking onto production.
	// Production-port detection is generalized via isProductionPort so cities
	// using non-3307 ports (BEADS_PRODUCTION_PORT or dolt-server.port) are
	// covered too.
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		if cfg.ServerPort == 0 || isProductionPort(cfg) {
			cfg.ServerPort = 1
		}
	}
	if cfg.ServerUser == "" {
		cfg.ServerUser = "root"
	}
	// Check environment variable for password (more secure than command-line)
	if cfg.ServerPassword == "" {
		cfg.ServerPassword = os.Getenv("BEADS_DOLT_PASSWORD")
	}

	// Remote credentials for Hosted Dolt push/pull (env vars take precedence)
	if cfg.RemoteUser == "" {
		cfg.RemoteUser = os.Getenv("DOLT_REMOTE_USER")
	}
	if cfg.RemotePassword == "" {
		cfg.RemotePassword = os.Getenv("DOLT_REMOTE_PASSWORD")
	}
}

// New creates a new Dolt storage backend.
// Connects to a running dolt sql-server via MySQL protocol (pure Go).
func New(ctx context.Context, cfg *Config) (*DoltStore, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("database path is required")
	}

	applyConfigDefaults(cfg)

	// Hard guard: tests must NEVER connect to the production Dolt server.
	// applyConfigDefaults rewrites a production port to 1 in BEADS_TEST_MODE=1
	// for fail-loud-but-continue behavior; this panic is defense-in-depth for
	// any path that bypasses or post-edits the rewrite. Generalized via
	// isProductionPort so non-3307 production deployments are covered.
	if os.Getenv("BEADS_TEST_MODE") == "1" && isProductionPort(cfg) {
		panic(buildTestModeProductionPortPanic(cfg))
	}

	// Database-name firewall: refuse to open a test-named database on any
	// server unless the operator opted in via BEADS_TEST_SERVER=1. This is
	// the second of two AD-01 defenses (the first is the production-port
	// guard above). Returning an error (not panic) lets tests assert on it.
	if isTestDatabaseName(cfg.Database) && os.Getenv("BEADS_TEST_SERVER") != "1" {
		addr := net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.ServerPort))
		if cfg.ServerSocket != "" {
			addr = cfg.ServerSocket
		}
		return nil, fmt.Errorf(
			"refusing to connect test database %q to server %s: "+
				"set BEADS_TEST_SERVER=1 on a dedicated test server, "+
				"or use test helpers in internal/storage/dolt/testserver",
			cfg.Database, addr)
	}

	return newServerMode(ctx, cfg)
}

// resolveLocalActiveDatabaseDir returns an authoritative local path only for
// server configurations whose storage ownership is known. It deliberately
// does not infer locality from Path, CLIDir, or filesystem existence: external
// servers may leave unrelated client-local directories at those locations.
func resolveLocalActiveDatabaseDir(cfg *Config) string {
	if cfg == nil || cfg.BeadsDir == "" || cfg.Database == "" ||
		cfg.Gateway || cfg.ProxiedServer || cfg.ServerSocket != "" ||
		cfg.ServerTLS || !isLocalHost(cfg.ServerHost) {
		return ""
	}

	// An endpoint supplied directly by the environment may be any server,
	// including a container or tunnel on localhost. It is not proof that this
	// process can inspect the server's data directory.
	if os.Getenv("BEADS_DOLT_SERVER_PORT") != "" || os.Getenv("BEADS_DOLT_PORT") != "" {
		return ""
	}

	if doltserver.IsSharedServerMode() {
		return filepath.Join(doltserver.ResolveDoltDir(cfg.BeadsDir), cfg.Database)
	}

	// Owned mode plus effective auto-start authority is the affirmative proof
	// that the configured data root belongs to this local beads instance.
	if !cfg.AutoStart || doltserver.ResolveServerMode(cfg.BeadsDir) != doltserver.ServerModeOwned {
		return ""
	}
	return filepath.Join(cfg.Path, cfg.Database)
}

// buildTestModeProductionPortPanic returns the multi-line panic message for
// the BEADS_TEST_MODE=1 + production-port hard-guard. Format follows
// AD-01 Wireframe 1: scannable header + database/path/server fields,
// list of detection rules that matched, and a fix block naming each
// supported escape hatch.
func buildTestModeProductionPortPanic(cfg *Config) string {
	addr := net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.ServerPort))
	if cfg.ServerSocket != "" {
		addr = cfg.ServerSocket
	}
	reasons := productionPortReasons(cfg)
	if len(reasons) == 0 {
		// Should be unreachable (caller checks isProductionPort first), but
		// keep the message coherent if it ever hits.
		reasons = []string{"production-port heuristic matched"}
	}
	var rules strings.Builder
	for _, r := range reasons {
		rules.WriteString("    - ")
		rules.WriteString(r)
		rules.WriteString("\n")
	}
	var fixLines strings.Builder
	fixLines.WriteString("    - point BEADS_DOLT_SERVER_PORT at a non-production port (test server)\n")
	fixLines.WriteString("    - or use test helpers in internal/storage/dolt/testserver\n")
	// BEADS_TEST_SERVER=1 does not suppress Rule 1 (port == DefaultSQLPort,
	// see productionPortReasons) — only list it as a fix when the port
	// itself isn't the reason this fired, so the message never claims an
	// opt-in that would not actually resolve this panic.
	if cfg.ServerPort != DefaultSQLPort {
		fixLines.WriteString("    - or set BEADS_TEST_SERVER=1 on the spawned test server's env\n")
	}
	return fmt.Sprintf(
		"refusing to connect: BEADS_TEST_MODE=1 but resolved server port is production\n\n"+
			"  database: %s\n"+
			"  path:     %s\n"+
			"  server:   %s\n"+
			"  detected as production via:\n"+
			"%s"+
			"  fix:\n"+
			"%s",
		cfg.Database,
		cfg.Path,
		addr,
		rules.String(),
		fixLines.String(),
	)
}

// dialProbe reports whether an address accepts a connection within timeout.
// Declared as a var (not a plain call) so unit tests can stub connectivity
// without a live Dolt server. Returns nil when the endpoint is reachable.
//
// Delegates to doltserver.ProbeSQLServer so the probe drains the MySQL
// handshake before closing (Close() then sends FIN, not RST) — applies to
// unix sockets too, since the protocol spoken over them is still MySQL.
// See gastownhall/beads#4132, #4133.
var dialProbe = func(network, addr string, timeout time.Duration) error {
	_, err := doltserver.ProbeSQLServer(network, addr, timeout)
	return err
}

// ResolveSocketTransport applies a socket-first / TCP-fallback policy and
// returns the effective unix socket path to use ("" means use TCP).
//
// A configured unix socket is a preference, not a hard requirement. Dolt's
// /tmp/mysql.sock is created only on some server start paths and is frequently
// absent while the server is fully reachable on its TCP port — when that
// happens, every socket-mode bd operation (and `gt mq submit`, cross-rig bead
// reads that route through bd) fails hard with no fallback (gt-28itz). This
// mirrors the conservative socket-first/TCP-fallback semantics already used on
// the gt-CLI side (internal/cmd/dolt_dsn.go localDoltSocketPath/buildDoltDSN).
//
// Returns the socket unchanged when: no socket is configured, the socket is
// connectable, or neither the socket nor TCP is reachable (the latter is left
// to the normal error path so its socket-specific hint still surfaces a true
// outage rather than masking it behind a TCP error).
//
// Exported because the store is no longer the only consumer: `bd serve` builds
// a unit-of-work provider against the same server from the same connection
// settings, and a transport policy that only one of them applies is a workspace
// where CLI commands work and the HTTP server cannot connect.
func ResolveSocketTransport(socket, host string, port int, timeout time.Duration) string {
	if socket == "" {
		return ""
	}
	if dialProbe("unix", socket, timeout) == nil {
		return socket // socket is live — keep using it
	}
	if port > 0 && dialProbe("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout) == nil {
		debug.Logf("dolt: socket %s unreachable, falling back to TCP %s\n", socket, net.JoinHostPort(host, strconv.Itoa(port)))
		return "" // socket down but TCP up — transparently fall back to TCP
	}
	return socket // both down (or no TCP port) — keep socket for the error path
}

// ensureRunningDetailed starts (or reuses) the repo-local auto-started dolt
// sql-server. Declared as a var (not a plain call) so unit tests can stub
// auto-start outcomes — including a retargeted port — without spawning a
// real dolt sql-server process.
var ensureRunningDetailed = doltserver.EnsureRunningDetailed

// stopRejectedAutoStartedServer stops a repo-local dolt sql-server that
// newServerMode's fail-closed checks (GH#4052) decided not to use. Declared
// as a var (matching ensureRunningDetailed above) so unit tests can stub it
// and assert whether it was invoked, without spawning or killing a real
// dolt sql-server process.
var stopRejectedAutoStartedServer = doltserver.Stop

// newServerMode creates a DoltStore connected to a running dolt sql-server.
// This path is pure Go and does not require CGO.
func newServerMode(ctx context.Context, cfg *Config) (*DoltStore, error) {
	breaker := initializeServerCircuitBreaker(cfg)

	// Circuit breaker: fail-fast if the server is known to be down.
	if breaker != nil && !breaker.Allow() {
		doltMetrics.circuitRejected.Add(ctx, 1)
		return nil, ErrCircuitOpen
	}

	// Tracks server dir if we auto-started a server (for cleanup in Close, GH#2542).
	var autoStartedDir string
	trackAutoStartedServer := !cfg.ReadOnly && shouldStopAutoStartedServerOnClose(cfg)
	resolvedBeadsDir := cfg.BeadsDir
	if resolvedBeadsDir == "" {
		resolvedBeadsDir = filepath.Dir(cfg.Path) // fallback: cfg.Path is .beads/dolt → parent is .beads/
	}
	serverDir := doltserver.ResolveServerDir(resolvedBeadsDir)

	// Socket-first / TCP-fallback (gt-28itz): a configured unix socket that
	// isn't currently connectable must not block operations when the server is
	// reachable over TCP. Normalizing here means the fail-fast dial below and
	// the DSN built in openServerConnection agree on the transport.
	cfg.ServerSocket = ResolveSocketTransport(cfg.ServerSocket, cfg.ServerHost, cfg.ServerPort, 500*time.Millisecond)

	// Fail-fast connectivity check before MySQL protocol initialization.
	// This gives an immediate, clear error if the Dolt server isn't running,
	// rather than waiting for MySQL driver timeouts.
	var addr string
	var conn net.Conn
	var dialErr error
	if cfg.ServerSocket != "" {
		addr = cfg.ServerSocket
		conn, dialErr = net.DialTimeout("unix", cfg.ServerSocket, 500*time.Millisecond)
	} else {
		addr = net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort))
		conn, dialErr = net.DialTimeout("tcp", addr, 500*time.Millisecond)
	}
	if dialErr != nil {
		// Auto-start: if enabled and connecting locally via TCP, start a server.
		// Socket mode is excluded — auto-start creates a TCP listener, not a
		// unix socket, so the DSN would still fail. Socket users are expected
		// to manage their own server lifecycle.
		canAutoStart := serverOpenCanAutoStart(cfg)
		if canAutoStart {
			// Snapshot the port file's exact pre-call state before letting
			// EnsureRunningDetailed write to it. Start() (and the
			// adopt-existing-server path) write serverDir's port file with
			// the actual listening port *inside* EnsureRunningDetailed —
			// before either fail-closed check below runs. Left in place, a
			// fail-closed return here still leaves that new port findable
			// via the port-file source (PortSourcePortFile, non-authoritative),
			// so a second, identical invocation would resolve the port file
			// instead of the authoritative source, adopt the server we just
			// declined to use, and silently succeed — permanently disarming
			// the guard after exactly one invocation (GH#4052 round 3). The
			// fail-closed branches below restore this snapshot before
			// returning so a retry re-triggers the same check.
			portFileSnap, snapErr := doltserver.SnapshotPortFile(serverDir)
			if snapErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not snapshot port file before auto-start: %v\n", snapErr)
			}
			port, startedByUs, startErr := ensureRunningDetailed(resolvedBeadsDir)
			if startErr != nil {
				return nil, fmt.Errorf("Dolt server unreachable at %s and auto-start failed: %w\n\n"+
					"To start manually: bd dolt start\n"+
					"To disable auto-start: set dolt.auto-start: false in .beads/config.yaml",
					addr, startErr)
			}
			// Only tests should stop auto-started servers on Close(). In normal
			// repo-local server mode, leaving the server up avoids endpoint churn
			// and circuit-breaker trips between commands.
			if startedByUs && trackAutoStartedServer {
				autoStartedDir = serverDir
				autoStartAcquire(autoStartedDir)
			}
			// Update port — EnsureRunning allocates an ephemeral port
			if port != cfg.ServerPort {
				if cfg.ServerPort > 0 {
					// A configured port is either an authoritative user
					// assertion (env var, project/global config.yaml,
					// metadata.json) or bd's own port-file bookkeeping
					// (GH#4052). Silently retargeting an authoritative port
					// can land the write on the wrong project's database
					// (e.g. a shared-server host serving multiple repos);
					// only bd's own bookkeeping is safe to replace without
					// confirmation.
					//
					// Shared-server mode is a second, orthogonal reason to
					// fail closed regardless of ServerPortSource: the
					// configured port resolved from the *shared* server
					// directory, but EnsureRunningDetailed(resolvedBeadsDir)
					// above always auto-starts a *repo-local* server — a
					// different database than the shared one. Retargeting
					// there is never a benign port refresh; it means the
					// shared server is down and bd just silently wrote
					// somewhere else instead (GH#4052, "Shared-server mode:
					// write commands report success when Dolt is
					// unreachable").
					if cfg.ServerPortSharedServer {
						undoRejectedAutoStart(serverDir, startedByUs, autoStartedDir, portFileSnap, snapErr)
						if breaker != nil {
							breaker.RecordFailure()
						}
						return nil, fmt.Errorf(
							"Shared Dolt server configured at port %d (source: %s) is unreachable; "+
								"auto-start started a repo-local server on port %d instead, but bd will "+
								"not silently write to it\n\n"+
								"A repo-local server is a different database than the shared one, so "+
								"using port %d here would silently write to the wrong database.\n\n"+
								"To proceed:\n"+
								"  - Restart the shared Dolt server: bd dolt start\n"+
								"  - Or check why it stopped responding on port %d before retrying",
							cfg.ServerPort, cfg.ServerPortSource, port, port, cfg.ServerPort)
					}
					if cfg.ServerPortSource.IsAuthoritative() {
						undoRejectedAutoStart(serverDir, startedByUs, autoStartedDir, portFileSnap, snapErr)
						if breaker != nil {
							breaker.RecordFailure()
						}
						return nil, fmt.Errorf(
							"Dolt server configured at port %d (source: %s) is unreachable; "+
								"auto-start started a new server on port %d, but bd will not "+
								"silently use a different port than the one you configured\n\n"+
								"The configured port may be pointing at a shared-server host "+
								"serving a different project's database; using port %d instead "+
								"could silently write to the wrong database.\n\n"+
								"To proceed:\n"+
								"  - Start the configured server manually: bd dolt start\n"+
								"  - Or remove/change the pinned port (env var, .beads/config.yaml "+
								"dolt.port, or global config) if port %d is stale",
							cfg.ServerPort, cfg.ServerPortSource, port, port, cfg.ServerPort)
					}
					fmt.Fprintf(os.Stderr, "Warning: Dolt server endpoint changed: port %d → %d (auto-start)\n", cfg.ServerPort, port)
					fmt.Fprintf(os.Stderr, "  Previous port was unreachable. If other tools expect port %d, they may see stale data.\n", cfg.ServerPort)
					fmt.Fprintf(os.Stderr, "  To pin a port: set dolt.port in .beads/config.yaml\n")
				}
				cfg.ServerPort = port
				addr = net.JoinHostPort(cfg.ServerHost, fmt.Sprintf("%d", cfg.ServerPort))
				breaker = maybeNewCircuitBreaker(cfg.ServerHost, cfg.ServerPort, cfg.Database)
			}
			// Retry connection with longer timeout (server just started)
			conn, dialErr = net.DialTimeout("tcp", addr, 2*time.Second)
			if dialErr != nil {
				// Release auto-start ref on connection failure
				if autoStartedDir != "" {
					_ = autoStartRelease(autoStartedDir)
				}
				if breaker != nil {
					breaker.RecordFailure()
				}
				return nil, fmt.Errorf("Dolt server auto-started but still unreachable at %s: %w\n\n"+
					"Check logs: %s", addr, dialErr, doltserver.LogPath(resolvedBeadsDir))
			}
		} else {
			if breaker != nil {
				breaker.RecordFailure()
			}
			var hint string
			if cfg.ServerSocket != "" {
				hint = fmt.Sprintf("The Dolt server is not listening on socket %s.\n"+
					"Ensure the server is started with --socket:\n"+
					"  dolt sql-server --socket %s\n"+
					"Auto-start is not supported in socket mode.",
					cfg.ServerSocket, cfg.ServerSocket)
			} else if isExternalServerHost(cfg.ServerHost) {
				// External (non-localhost) server: bd does not
				// manage it; "bd dolt start" would be wrong advice
				// (GH#3518). Suggest verifying the external server
				// instead.
				hint = fmt.Sprintf("Configured Dolt server at %s:%d is unreachable.\n"+
					"Verify the external server is running and reachable from this host:\n"+
					"  nc -zv %s %d  # or curl %s:%d for an HTTP-style check",
					cfg.ServerHost, cfg.ServerPort,
					cfg.ServerHost, cfg.ServerPort,
					cfg.ServerHost, cfg.ServerPort)
			} else if !cfg.AutoStart && doltserver.IsAutoStartDisabled() {
				hint = "Dolt server auto-start is disabled (dolt.auto-start: false).\n" +
					"Start the server manually:\n  bd dolt start"
			} else {
				hint = "The Dolt server may not be running. Try:\n  bd dolt start"
			}
			return nil, fmt.Errorf("Dolt server unreachable at %s: %w\n\n%s",
				addr, dialErr, hint)
		}
	}
	// Drain the MySQL handshake before closing so Close() sends FIN, not RST
	// (dolt sql-server crash risk otherwise, gastownhall/beads#4132, #4133).
	// This single close site covers both the initial successful dial above
	// and the post-auto-start retry dial in the branch just above.
	doltserver.DrainAndCloseProbe(conn)

	// If this process already owns a test-started auto-start server, later
	// stores sharing it must participate in the refcount so one Close() does
	// not stop the server out from under another open store.
	if autoStartedDir == "" && trackAutoStartedServer && autoStartAcquireExisting(serverDir) {
		autoStartedDir = serverDir
	}

	// TCP dial succeeded — record success to reset the breaker
	if breaker != nil {
		breaker.RecordSuccess()
	}

	// Server mode: connect via MySQL protocol to dolt sql-server
	db, connStr, dbFacts, err := openServerConnection(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Close the pool on any failure path below; cleared once ownership passes to the caller.
	storeReady := false
	defer func() {
		if !storeReady {
			_ = db.Close()
		}
	}()

	// Test connection
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping Dolt database: %w", err)
	}

	beadsDir := cfg.BeadsDir
	if beadsDir == "" && cfg.Path != "" {
		beadsDir = filepath.Dir(cfg.Path) // cfg.Path is .beads/dolt → parent is .beads/
	}

	store := &DoltStore{
		db:                     db,
		dbPath:                 cfg.Path,
		beadsDir:               beadsDir,
		database:               cfg.Database,
		localActiveDatabaseDir: resolveLocalActiveDatabaseDir(cfg),
		connStr:                connStr,
		cfg:                    cfg,
		serverEndpoint:         serverEndpointIdentity(cfg),
		breaker:                breaker,
		committerName:          cfg.CommitterName,
		committerEmail:         cfg.CommitterEmail,
		remote:                 cfg.Remote,
		branch:                 "main",
		remoteUser:             cfg.RemoteUser,
		remotePassword:         cfg.RemotePassword,
		serverMode:             true,
		readOnly:               cfg.ReadOnly,
		autoStartedServerDir:   autoStartedDir,
	}

	// Forward-drift guard runs on read-only AND writable opens. A binary older
	// than the database's schema cannot migrate it forward: MigrateUp no-ops
	// when the DB is already past the binary's latest migration (atLatest uses
	// >=), so without this guard a writable open would proceed and later queries
	// would fail with cryptic unknown-column errors instead of a clear
	// "upgrade bd" message (the stale-binary incident behind #4135/#4137).
	if err := schema.CheckForwardDrift(ctx, db); err != nil {
		return nil, err
	}

	// Identity verification runs whenever we did not just create the database
	// ourselves: the classic !CreateIfMissing "connect to an existing DB" path,
	// and — critically — the CreateIfMissing:true "init" path when the target
	// database turns out to already exist on the server. Without the latter,
	// `bd init` against a shared server silently adopts a foreign project's
	// existing database instead of failing, because CreateIfMissing:true used
	// to skip the check unconditionally regardless of whether anything was
	// actually created (GH#4637). A genuinely new database (dbAlreadyExisted
	// == false) still skips verification: there is nothing to compare against
	// yet, and the per-field soft-skips below already make this a no-op for a
	// brand-new database in the !CreateIfMissing case too.
	//
	// This must run BEFORE store.initSchema below: initSchema runs migrations
	// and Dolt commits against the database, so verifying first means a
	// foreign existing database is rejected before bd writes anything into
	// it, not after. On a database with no bd schema yet, GetMetadata errors
	// and the verifier soft-skips, so this ordering does not change behavior
	// for a genuinely fresh database.
	//
	// Gateway is effectively excluded from the alreadyExisted branch because
	// openServerConnection never probes existence for it (see the Gateway
	// branch below) and always returns alreadyExisted == false: gateway
	// identity is not enforced here at all. That is deliberate, not an
	// oversight — a hosted database's identity is server-authoritative, and
	// cmd/bd/init.go already implements the correct reconciliation for it via
	// resolveInitProjectID, adopting the server's _project_id onto a stale or
	// missing local one on every init, including re-init, which relies on
	// this open not hard-failing first. This gap is specific to Part A's
	// server-existence check; it does not reopen the separate "foreign
	// server, no bd database at all" gap tracked as Part B (mybd-y18b).
	if !cfg.CreateIfMissing || dbFacts.alreadyExisted {
		var verifyErr error
		if cfg.Database == doltserver.GlobalDatabaseName {
			verifyErr = store.verifyGlobalProjectIdentity(ctx, cfg.BeadsDir)
		} else {
			verifyErr = store.verifyProjectIdentity(ctx, cfg.BeadsDir)
		}
		if verifyErr != nil {
			return nil, verifyErr
		}
	}

	// A gateway server owns the schema: it provisions each project at its deployed bd
	// version, so a client must never run migrations (DDL) against it. Treat it like
	// ReadOnly for schema — the forward-drift guard above still protects a stale client
	// binary.
	if !cfg.ReadOnly && !cfg.Gateway {
		applied, err := store.initSchema(ctx, dbFacts.bootstrapHeal)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize schema: %w", err)
		}
		// initSchema runs migrations over a separate pool (openMigrationDB).
		// The Ping above already pinned a connection in store.db to the
		// pre-migration session root; without a rebuild, the first read
		// through that stale connection returns 0 rows / table-not-found
		// and does not self-heal on retry (be-itm5). Only a migrating open
		// (applied > 0) needs this — rebuildPoolAfterMigration no-ops otherwise.
		if err := store.rebuildPoolAfterMigration(ctx, applied); err != nil {
			return nil, fmt.Errorf("failed to rebuild pool after migration: %w", err)
		}
	}

	if isLocalHost(cfg.ServerHost) {
		beadsDir := cfg.BeadsDir
		if beadsDir == "" && cfg.Path != "" {
			beadsDir = filepath.Dir(cfg.Path)
		}
		_ = persistResolvedPortFile(cfg, beadsDir)
	}

	// All writers operate on main — transaction isolation via RunInTransaction
	// replaces the former branch-per-worker approach (BD_BRANCH).
	store.branch = "main"

	// Register observable pool gauges for diagnosing shared-server degradation (GH#3140).
	// These report sql.DB.Stats() on each OTel scrape — no-op when telemetry is off.
	store.registerPoolGauges()

	// Ownership of db transfers to the returned store; suppress the deferred
	// close above. Must be the last thing before the success return.
	storeReady = true
	return store, nil
}

var (
	cleanServerCircuitState = CleanStaleCircuitBreakerFiles
	newServerCircuitBreaker = maybeNewCircuitBreaker
	ensureResolvedPortFile  = doltserver.EnsurePortFile
)

func initializeServerCircuitBreaker(cfg *Config) *circuitBreaker {
	if cfg.DisableAutoStart || os.Getenv("BEADS_TEST_MODE") == "1" {
		return nil
	}
	// Clean stale circuit breaker files before checking — prevents leftover
	// state from previous sessions poisoning fresh writable opens (GH#2598).
	cleanServerCircuitState()
	return newServerCircuitBreaker(cfg.ServerHost, cfg.ServerPort, cfg.Database)
}

// serverOpenCanAutoStart reports whether a stopped managed dolt server may be
// auto-started for this open. This is keyed off DisableAutoStart (the strict
// --readonly signal threaded from policy.disableAutoStart in cmd/bd/main.go),
// not cfg.ReadOnly: ordinary classified-read commands (bd show, bd list, ...)
// also set cfg.ReadOnly but must still be able to auto-start a stopped
// managed server, per dolt_autostart_lifecycle_integration_test.go.
func serverOpenCanAutoStart(cfg *Config) bool {
	return !cfg.DisableAutoStart && cfg.AutoStart && cfg.Path != "" &&
		cfg.ServerSocket == "" && isLocalHost(cfg.ServerHost)
}

func persistResolvedPortFile(cfg *Config, beadsDir string) error {
	if cfg.DisableAutoStart || !shouldPersistResolvedPortFile() {
		return nil
	}
	return ensureResolvedPortFile(beadsDir, cfg.ServerPort)
}

func shouldPersistResolvedPortFile() bool {
	return os.Getenv("BEADS_DOLT_SERVER_PORT") == "" && os.Getenv("BEADS_DOLT_PORT") == ""
}

// verifyProjectIdentity checks that the database belongs to the expected project.
// If both the local metadata.json and the database have a project_id, they must match.
// Returns nil if verification passes or is not applicable (missing IDs = old setup).
func (s *DoltStore) verifyProjectIdentity(ctx context.Context, beadsDir string) error {
	if beadsDir == "" {
		return nil // can't verify without knowing beadsDir
	}

	// Load local project ID from metadata.json
	metaCfg, err := configfile.Load(beadsDir)
	if err != nil || metaCfg == nil {
		return nil // no local config — skip verification
	}
	localID := metaCfg.ProjectID
	if localID == "" {
		return nil // old-style metadata.json without project_id — skip
	}

	// Read project ID from database metadata table
	dbID, err := s.GetMetadata(ctx, "_project_id")
	if err != nil || dbID == "" {
		return nil // old database without project_id — skip
	}

	if localID != dbID {
		return fmt.Errorf(
			"PROJECT IDENTITY MISMATCH — refusing to connect\n\n"+
				"  Local project ID (metadata.json):  %s\n"+
				"  Database project ID:               %s\n\n"+
				"This means the Dolt server is serving a DIFFERENT project's database.\n"+
				"This can happen when:\n"+
				"  - Another project's server is running on the same port\n"+
				"  - The server restarted with a different data directory\n\n"+
				"To diagnose: bd dolt status\n"+
				"Do NOT run 'bd init' — your data likely exists, just on a different server.",
			localID, dbID)
	}
	return nil
}

func (s *DoltStore) verifyGlobalProjectIdentity(ctx context.Context, beadsDir string) error {
	if beadsDir == "" {
		return nil
	}

	metaCfg, err := configfile.Load(beadsDir)
	if err != nil || metaCfg == nil {
		return nil
	}
	expectedID := metaCfg.GlobalProjectID
	if expectedID == "" {
		return nil
	}

	dbID, err := s.GetMetadata(ctx, "_project_id")
	if err != nil || dbID == "" {
		return nil
	}

	if expectedID != dbID {
		return fmt.Errorf(
			"GLOBAL PROJECT IDENTITY MISMATCH — refusing to connect\n\n"+
				"  Expected global project ID (metadata.json): %s\n"+
				"  Database project ID:                        %s\n\n"+
				expectedID, dbID)
	}
	return nil
}

// isLocalHost returns true if the host refers to the local machine.
func isLocalHost(host string) bool {
	switch host {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

// isExternalServerHost reports whether host names a remote server for the
// purposes of connect-failure hints (GH#3518). Unlike isLocalHost it
// normalizes case/whitespace and treats 0.0.0.0 as local, matching the
// mode-inference classification in internal/configfile — the two must
// agree or an unreachable local server gets external-server advice with
// no "bd dolt start" recovery hint.
func isExternalServerHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", "localhost", "127.0.0.1", "::1", "[::1]", "0.0.0.0":
		return false
	}
	return true
}

// buildServerDSN constructs a MySQL DSN for connecting to a Dolt server.
// If database is empty, connects without selecting a database (for init operations).
// Adds ReadTimeout/WriteTimeout for long-lived connection pools.
func buildServerDSN(cfg *Config, database string) string {
	base := doltutil.ServerDSN{
		Socket:   cfg.ServerSocket,
		Host:     cfg.ServerHost,
		Port:     cfg.ServerPort,
		User:     cfg.ServerUser,
		Password: cfg.ServerPassword,
		Database: database,
		TLS:      cfg.ServerTLS,
	}
	// Parse the base DSN and add pool-specific timeouts.
	parsed, err := mysql.ParseDSN(base.String())
	if err != nil {
		return base.String()
	}
	parsed.ReadTimeout = defaultPoolReadTimeout
	if cfg.PoolReadTimeoutFallback > 0 {
		parsed.ReadTimeout = cfg.PoolReadTimeoutFallback
	}
	if cfg.PoolReadTimeout > 0 {
		parsed.ReadTimeout = cfg.PoolReadTimeout
	}
	parsed.WriteTimeout = defaultPoolWriteTimeout
	if cfg.PoolWriteTimeout > 0 {
		parsed.WriteTimeout = cfg.PoolWriteTimeout
	}
	return parsed.FormatDSN()
}

// execWithLongTimeout opens a one-shot database connection with readTimeout=5m
// and executes the given query. Push/pull operations can exceed the default
// readTimeout when the server performs network I/O to git remotes.
//
// The query is wrapped in an explicit transaction (BEGIN/COMMIT) so that
// DOLT_PULL merge operations succeed even when the server runs with
// autocommit=1. Without this, Dolt rejects merges under autocommit because
// it cannot expose conflict-resolution tables to the caller.
//
// Audited for be-b0am's fresh-connection branch hazard: safe — but the two
// callers are safe for different reasons, so the annotation names both.
// federation.go's CALL DOLT_PUSH(?, ?) names the refspec explicitly. Its
// CALL DOLT_FETCH(?) passes only the remote: with no refspec argument dolt
// falls back to the remote's configured refspecs (ParseRefSpecs ->
// GetRefSpecs), which are remote config rather than session state, and a
// fetch writes only remote-tracking refs, never the working branch. Neither
// depends on this fresh connection's default checkout.
func (s *DoltStore) execWithLongTimeout(ctx context.Context, query string, args ...any) error {
	cfg, err := mysql.ParseDSN(s.connStr)
	if err != nil {
		return fmt.Errorf("failed to parse DSN for long-timeout connection: %w", err)
	}
	cfg.ReadTimeout = 5 * time.Minute
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("failed to open long-timeout connection: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// execWithLongTimeoutNoTx executes a long-running Dolt stored procedure without
// an explicit transaction. Push operations do not need the pull/merge conflict
// handling above, and DOLT_PUSH has diverged from direct `dolt push` behavior
// when wrapped in a SQL transaction.
//
// Audited for be-b0am's fresh-connection branch hazard: safe. Every caller
// passes s.branch explicitly as a CALL DOLT_PUSH(...) arg, so this fresh
// connection's default checkout never matters.
func (s *DoltStore) execWithLongTimeoutNoTx(ctx context.Context, query string, args ...any) error {
	db, err := s.oneShotConn(5 * time.Minute)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, query, args...)
	return err
}

// oneShotConn opens a one-shot connection with the given read deadline
// (0 = no deadline), for callers that pass a DBConn into versioncontrolops.
// The pool's 10s ReadTimeout kills any server-side procedure that performs
// sustained network I/O; push/pull use 5m, while backup sync/restore use no
// deadline at all — a first sync to a remote destination (gs://) can exceed
// any fixed budget, and the server aborts the transfer when the client
// connection drops, so a too-short deadline can never converge by retrying.
// Caller closes.
func (s *DoltStore) oneShotConn(readTimeout time.Duration) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(s.connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse DSN for long-timeout connection: %w", err)
	}
	cfg.ReadTimeout = readTimeout
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("failed to open long-timeout connection: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// applyPoolLimits configures the pool on db using the sensible-default
// connection pool limits, overridden by any non-zero Config fields.
//
// These limits are deliberately oriented at long-lived daemons: a 1h
// connection lifetime lets the same physical MySQL connection be reused
// for thousands of queries, so dolt-server.log no longer shows a
// NewConnection/ConnectionClosed pair every few queries.
func applyPoolLimits(db *sql.DB, cfg *Config) {
	maxOpen := defaultMaxOpenConns
	if cfg.MaxOpenConns > 0 {
		maxOpen = cfg.MaxOpenConns
	}

	maxIdle := defaultMaxIdleConns
	if cfg.MaxIdleConns > 0 {
		maxIdle = cfg.MaxIdleConns
	}
	// MaxIdleConns must never exceed MaxOpenConns or database/sql silently
	// clamps it and we end up with a different pool shape than requested.
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}

	lifetime := defaultConnMaxLifetime
	if cfg.ConnMaxLifetime > 0 {
		lifetime = cfg.ConnMaxLifetime
	}

	idle := defaultConnMaxIdleTime
	if cfg.ConnMaxIdleTime > 0 {
		idle = cfg.ConnMaxIdleTime
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(lifetime)
	db.SetConnMaxIdleTime(idle)
}

// serverConnFacts reports what openServerConnection established about the
// target database while connecting. Creation and prior existence are
// deliberately NOT inverses: for a gateway database, existence is never
// probed, so neither is proven and each caller's gate fails closed.
type serverConnFacts struct {
	// created reports whether THIS call's bare CREATE DATABASE won the
	// ownership arbitration and actually created the database.
	created bool

	// bootstrapHeal carries one-shot reset authority bound to the exact
	// endpoint, server UUID, database, and initial HEAD captured after this
	// call created and successfully connected to a pristine database. A nil
	// capability always fails closed.
	bootstrapHeal *schema.FreshBootstrapHealCapability

	// alreadyExisted reports whether the database was proven to exist on the
	// server before this call: either the SHOW DATABASES probe found it, or
	// our CREATE DATABASE was refused with "database exists" (1007). Callers
	// use it to decide whether project-identity verification applies even
	// when CreateIfMissing is true (see the newServerMode gate around
	// verifyProjectIdentity, GH#4637).
	alreadyExisted bool
}

// openServerConnection connects to (and if needed creates) the target database
// on a dolt sql-server via MySQL protocol. See serverConnFacts for what the
// returned facts mean and why they are not a single bool.
func openServerConnection(ctx context.Context, cfg *Config) (*sql.DB, string, serverConnFacts, error) {
	connStr := buildServerDSN(cfg, cfg.Database)

	db, err := sql.Open("mysql", connStr)
	if err != nil {
		return nil, "", serverConnFacts{}, fmt.Errorf("failed to open Dolt server connection: %w", err)
	}

	// Configure the pool. *sql.DB is safe for concurrent use and manages its
	// own pool — the same Store reuses these connections across every query
	// for the lifetime of the daemon, rather than opening a fresh one each
	// time (which used to show up as endless NewConnection/ConnectionClosed
	// pairs in dolt-server.log).
	applyPoolLimits(db, cfg)

	// Close the pool on any failure path below; cleared at the success return.
	connReady := false
	defer func() {
		if !connReady {
			_ = db.Close()
		}
	}()

	// A gateway server owns database routing and existence, so bd does not probe or create
	// it: skip the no-database admin connection (and the SHOW DATABASES / CREATE DATABASE
	// it would run) and verify the project connection directly — a successful connect IS
	// the existence proof. connReady must be set before returning the pool, or the defer
	// above would close the *sql.DB we just handed the caller.
	if cfg.Gateway {
		if err := db.PingContext(ctx); err != nil {
			return nil, "", serverConnFacts{}, fmt.Errorf("failed to connect to gateway server %s:%d (database %q): %w",
				cfg.ServerHost, cfg.ServerPort, cfg.Database, err)
		}
		connReady = true
		// Neither fact is established for a gateway database: we did not
		// create it, and existence was never probed, so we cannot honestly
		// report alreadyExisted either. The zero value is what the
		// newServerMode caller relies on to skip its alreadyExisted-forced
		// identity check for Gateway. That is intentional, not a leftover
		// gap: gateway identity is reconciled (not enforced at open) by
		// cmd/bd/init.go's resolveInitProjectID, which adopts the
		// server-authoritative _project_id onto a stale or missing local one
		// on every init, including re-init. It is also correct for `created`:
		// an unproven creator must never arm fresh-bootstrap heal.
		return db, connStr, serverConnFacts{}, nil
	}

	// Ensure database exists (may need to create it)
	// First connect without database to create it
	initConnStr := buildServerDSN(cfg, "")
	initDB, err := sql.Open("mysql", initConnStr)
	if err != nil {
		return nil, "", serverConnFacts{}, fmt.Errorf("failed to open init connection: %w", err)
	}
	defer func() { _ = initDB.Close() }()

	// Validate database name to prevent SQL injection via backtick escaping
	if err := ValidateDatabaseName(cfg.Database); err != nil {
		return nil, "", serverConnFacts{}, fmt.Errorf("invalid database name %q: %w", cfg.Database, err)
	}

	// FIREWALL: Never create test databases on the production server.
	// This is the last line of defense against test pollution (Clown Shows #12-#18).
	// Pattern-based, not env-var-based — env vars can be misconfigured or missing.
	// Production-port detection generalized via isProductionPort so non-3307
	// production deployments are covered (AD-01).
	if isTestDatabaseName(cfg.Database) && isProductionPort(cfg) {
		return nil, "", serverConnFacts{}, fmt.Errorf(
			"REFUSED: will not CREATE DATABASE %q on production port %d — "+
				"this is a test database name on the production server (see DOLT-WAR-ROOM.md)",
			cfg.Database, cfg.ServerPort)
	}

	// Check if the database already exists before deciding whether to create it.
	// This prevents the shadow database bug: without CreateIfMissing, connecting
	// to a server that lacks the expected database is an error (not silent creation).
	// The result also feeds the caller's identity-verification gate: a
	// CreateIfMissing:true init that lands on an already-existing database
	// must still verify project identity (GH#4637) — only a database this
	// call creates from scratch is exempt.
	//
	// Uses SHOW DATABASES + iterate for exact match instead of SHOW DATABASES LIKE,
	// because LIKE treats _ and % as wildcards and Dolt does not support backslash
	// escaping. Database names like "beads_vulcan" contain underscores which would
	// match unrelated databases with LIKE.
	dbExists, checkErr := databaseExistsOnServer(ctx, initDB, cfg.Database)
	if checkErr != nil {
		return nil, "", serverConnFacts{}, fmt.Errorf("failed to check if database %q exists on server %s:%d: %w",
			cfg.Database, cfg.ServerHost, cfg.ServerPort, checkErr)
	}

	// created reports whether THIS call's CREATE DATABASE won the race and
	// actually created the database (the #5042 ownership-arbitration signal,
	// ported from internal/storage/uow/dolt_sql_provider.go's initSchema).
	// Only the proven creator may later arm schema.WithFreshBootstrapHeal:
	// on a database this call created, dirty tables a retry sees can only be
	// this same process's own half-applied migration step (a session that
	// died between a step's SQL and its per-step Dolt commit), never
	// pre-existing user data (gastownhall/beads#5012).
	created := false
	if !dbExists {
		if !cfg.CreateIfMissing {
			return nil, "", serverConnFacts{}, databaseNotFoundError(cfg)
		}

		// Bare CREATE DATABASE (no IF NOT EXISTS): the server arbitrates
		// creation atomically, so a nil error here proves THIS call created
		// the database. A concurrent initializer that loses the race gets
		// the same "database exists" (1007) refusal the old IF NOT EXISTS
		// form absorbed silently — still tolerated below, just no longer
		// ambiguous about who created it.
		_, err = initDB.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE `%s`", cfg.Database)) //nolint:gosec // G201: cfg.Database validated by ValidateDatabaseName above
		switch {
		case err == nil:
			created = true
		default:
			errLower := strings.ToLower(err.Error())
			if !strings.Contains(errLower, "database exists") && !strings.Contains(errLower, "1007") {
				// Check for connection refused - server likely not running
				if strings.Contains(errLower, "connection refused") || strings.Contains(errLower, "connect: connection refused") {
					return nil, "", serverConnFacts{}, fmt.Errorf("failed to connect to Dolt server at %s:%d: %w\n\nThe Dolt server may not be running. Try:\n  bd dolt start    # Start a local server\n  gt dolt start    # If using an orchestrator",
						cfg.ServerHost, cfg.ServerPort, err)
				}
				return nil, "", serverConnFacts{}, fmt.Errorf("failed to create database: %w", err)
			}
			// Lost the create race (or a benign TOCTOU with the dbExists
			// check above): not ours, heal stays off. The refusal IS proof of
			// existence, though — another process won the CREATE DATABASE
			// race between our SHOW DATABASES probe and this call — so
			// correct dbExists even though the probe reported false. The
			// caller's identity-verification gate depends on this being
			// accurate, not just on the initial probe.
			dbExists = true
		}
	}

	// Wait for the Dolt server's in-memory catalog to register the new database.
	// After CREATE DATABASE, there is a race where the server has created the
	// database on disk but hasn't updated its catalog yet. Pinging db (which
	// has the database in the DSN) will fail with "Unknown database" until the
	// catalog catches up. We retry with exponential backoff. (GH-1851)
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 100 * time.Millisecond
	bo.MaxElapsedTime = 10 * time.Second
	if err := backoff.Retry(func() error {
		pingErr := db.PingContext(ctx)
		if pingErr != nil && isRetryableError(pingErr) {
			return pingErr // retryable — backoff will retry
		}
		if pingErr != nil {
			return backoff.Permanent(pingErr)
		}
		return nil
	}, backoff.WithContext(bo, ctx)); err != nil {
		return nil, "", serverConnFacts{}, fmt.Errorf("database %q not available after CREATE DATABASE: %w", cfg.Database, err)
	}

	var bootstrapHeal *schema.FreshBootstrapHealCapability
	if created {
		conn, connErr := db.Conn(ctx)
		if connErr != nil {
			return nil, "", serverConnFacts{}, fmt.Errorf("capture fresh database identity: pin connection: %w", connErr)
		}
		bootstrapHeal, err = schema.CaptureFreshBootstrapHealCapability(
			ctx, conn, serverEndpointIdentity(cfg), cfg.Database,
		)
		_ = conn.Close()
		if err != nil {
			return nil, "", serverConnFacts{}, fmt.Errorf("capture fresh database identity: %w", err)
		}
	}

	connReady = true
	return db, connStr, serverConnFacts{
		created:        created,
		bootstrapHeal:  bootstrapHeal,
		alreadyExisted: dbExists,
	}, nil
}

// serverEndpointIdentity returns the exact configured transport endpoint. It
// is captured with fresh-bootstrap authority and must match the migration
// connection's endpoint before that authority can be consumed.
func serverEndpointIdentity(cfg *Config) string {
	if cfg.ServerSocket != "" {
		return "unix:" + cfg.ServerSocket
	}
	return "tcp:" + net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.ServerPort))
}

// databaseExistsOnServer checks if a database with the exact given name exists
// on the Dolt server. Uses SHOW DATABASES + iterate instead of SHOW DATABASES LIKE
// to avoid LIKE wildcard issues with underscores in database names.
func databaseExistsOnServer(ctx context.Context, db *sql.DB, name string) (bool, error) {
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var dbName string
		if err := rows.Scan(&dbName); err != nil {
			return false, err
		}
		if dbName == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

// initSchemaOnDB applies pending schema migrations. schema.MigrateUp tracks
// applied versions in schema_migrations and backfills legacy config-driven
// tables. Returns the number of migrations applied.
func initSchemaOnDB(ctx context.Context, db *sql.DB) (int, error) {
	return initSchemaOnDBWithBootstrapHeal(ctx, db, nil, "")
}

// initSchemaOnDBWithBootstrapHeal threads one-shot, incarnation-bound reset
// authority into the migration lock. A nil capability always fails closed.
func initSchemaOnDBWithBootstrapHeal(
	ctx context.Context,
	db *sql.DB,
	bootstrapHeal *schema.FreshBootstrapHealCapability,
	endpoint string,
) (int, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("schema: pin connection: %w", err)
	}
	defer conn.Close()

	var dbName string
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&dbName); err != nil {
		return 0, fmt.Errorf("schema: read database name: %w", err)
	}

	var opts []schema.MigrateLockOption
	if bootstrapHeal != nil {
		opts = append(opts, schema.WithFreshBootstrapHeal(bootstrapHeal, endpoint))
	}
	applied, err := schema.MigrateUpWithLock(ctx, conn, dbName, opts...)
	if err != nil {
		return applied, fmt.Errorf("schema migration: %w", err)
	}
	return applied, nil
}

func initSchemaOnDBWithRetry(ctx context.Context, db *sql.DB) (int, error) {
	return initSchemaOnDBWithRetryAndGate(ctx, db, nil)
}

// initSchemaOnDBWithRetryAndGate is initSchemaOnDBWithRetry with an optional
// pre-migration gate run INSIDE the retry loop. The gate's own reads
// (schema_migrations, dolt_remotes) can hit the same transient Dolt
// startup/catalog races the migration retry absorbs, so gate probe errors are
// retried with them instead of failing the open fast (bd-6dnrw.30); a
// *schema.RemoteMigrateGateError refusal stays permanent.
func initSchemaOnDBWithRetryAndGate(ctx context.Context, db *sql.DB, gate func(context.Context, *sql.DB) error) (int, error) {
	return initSchemaOnDBWithRetryAndGateBootstrapHeal(ctx, db, gate, nil, "")
}

// initSchemaOnDBWithRetryAndGateBootstrapHeal shares one capability across the
// outer retry loop. Once consumed, no later retry can issue another reset.
func initSchemaOnDBWithRetryAndGateBootstrapHeal(
	ctx context.Context,
	db *sql.DB,
	gate func(context.Context, *sql.DB) error,
	bootstrapHeal *schema.FreshBootstrapHealCapability,
	endpoint string,
) (int, error) {
	// Schema initialization for server mode is idempotent. Retry transient
	// Dolt startup/catalog races and contended migration-lock attempts so
	// concurrent bd processes converge instead of failing one unlucky waiter.
	schemaBO := backoff.NewExponentialBackOff()
	schemaBO.InitialInterval = 100 * time.Millisecond
	// Must exceed schema.MigrateUpWithLock's 5s GET_LOCK wait so a contended
	// schema migration can time out once and still retry.
	schemaBO.MaxElapsedTime = serverRetryMaxElapsed
	var applied int
	err := backoff.Retry(func() error {
		if gate != nil {
			if gateErr := gate(ctx, db); gateErr != nil {
				if !schema.IsRemoteMigrateGateError(gateErr) && isRetryableError(gateErr) {
					return gateErr
				}
				return backoff.Permanent(gateErr)
			}
		}
		var schemaErr error
		applied, schemaErr = initSchemaOnDBWithBootstrapHeal(ctx, db, bootstrapHeal, endpoint)
		if schemaErr != nil && isRetryableError(schemaErr) {
			return schemaErr
		}
		if schemaErr != nil {
			return backoff.Permanent(schemaErr)
		}
		return nil
	}, backoff.WithContext(schemaBO, ctx))
	return applied, err
}

func (s *DoltStore) initSchema(ctx context.Context, bootstrapHeal *schema.FreshBootstrapHealCapability) (int, error) {
	// Schema migrations can run arbitrarily long (e.g. full-table recomputes
	// such as the is_blocked backfill in migration 0047). The main connection
	// pool sets a 10s ReadTimeout (see buildServerDSN); a slow migration over
	// that pool aborts mid-flight with "i/o timeout" and leaves tables dirty,
	// which then blocks every subsequent migration attempt. Run the migration
	// pass over a dedicated connection with no read/write timeout. Cancellation
	// is governed by the caller's context, not a fixed deadline.
	migDB, err := s.openMigrationDB()
	if err != nil {
		return 0, err
	}
	defer migDB.Close()
	// #4259: refuse to silently apply pending migrations to a remote-backed,
	// already-initialized database — that is how two clones fork the schema.
	// The gate runs inside the retry loop, before each migration attempt: its
	// reads can hit transient startup/catalog races (retryable) while a gate
	// refusal is permanent and never retried into a migration.
	// Use the on-disk fallback: a freshly (auto-)started server can report an
	// empty dolt_remotes table even though remotes are persisted in .dolt/config
	// (GH#2315), so an SQL-only check would miss the remote on the first write
	// open after an upgrade.
	//
	// adopt injects the driver-side fast-forward ancestry primitives
	// (mybd-ae1i) so the smart gate can distinguish a losslessly
	// fast-forwardable remote-ahead case (smartAdoptFastForward) from the
	// plain destructive adopt, and auto-execute it: CheckRemoteMigrateGate*
	// calls FastForward and returns nil (proceed, nothing pending) once HEAD
	// has actually advanced; any execution failure (dirty working set raced
	// in, non-fast-forward, concurrent writer) falls back to the plain
	// destructive adopt directive instead of forcing the write.
	adopt := &schema.FastForwardAdopter{
		IsStrictAncestor: func(ctx context.Context, db schema.DBConn, ref string) (bool, error) {
			return versioncontrolops.LocalIsStrictAncestorOf(ctx, db, ref)
		},
		WorkingSetClean: func(ctx context.Context, db schema.DBConn) (bool, error) {
			return versioncontrolops.WorkingSetClean(ctx, db)
		},
		FastForward: func(ctx context.Context, db schema.DBConn, ref string) error {
			return versioncontrolops.FastForwardAdopt(ctx, db, ref)
		},
		// s.initSchema is only ever invoked from the writable-open path (the
		// caller guards it on !cfg.ReadOnly), so this is always false in
		// practice today — wired explicitly anyway so the adopter's safety
		// invariant (ReadOnly means "cannot write here") does not silently
		// depend on that external guard alone.
		ReadOnly: s.readOnly,
	}
	gate := func(ctx context.Context, db *sql.DB) error {
		return schema.CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(ctx, db, s.remote, s.hasPersistedCLIRemote, adopt)
	}
	applied, err := initSchemaOnDBWithRetryAndGateBootstrapHeal(ctx, migDB, gate, bootstrapHeal, s.serverEndpoint)
	return applied, err
}

// ApplySchemaMigrations runs idempotent schema migrations under the
// per-database advisory lock, with retry for transient lock contention.
// Implements storage.SchemaMigrator.
func (s *DoltStore) ApplySchemaMigrations(ctx context.Context) (int, error) {
	migDB, err := s.openMigrationDB()
	if err != nil {
		return 0, err
	}
	defer migDB.Close()
	return initSchemaOnDBWithRetry(ctx, migDB)
}

// openMigrationDB opens a one-off connection pool for schema migrations with no
// read/write timeout. Migrations may run far longer than the default 10s pool
// timeout, and timing out part-way leaves the database in a dirty, half-migrated
// state. The single connection is closed by the caller once migration completes.
func (s *DoltStore) openMigrationDB() (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(s.connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse DSN for migration connection: %w", err)
	}
	cfg.ReadTimeout = 0
	cfg.WriteTimeout = 0
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("failed to open migration connection: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// rebuildPoolAfterMigration replaces the main connection pool (s.db) after a
// migrating open. Migrations run over a separate one-off pool
// (openMigrationDB); a connection already pooled in s.db before migrations
// ran (e.g. the startup Ping in newServerMode) stays pinned to the
// pre-migration Dolt session root, so the first read through it returns 0
// rows / table-not-found and does not self-heal on retry (be-itm5). A
// non-migrating open (applied == 0 — the common re-open-of-an-
// already-migrated-database path) has no stale state to fix and must return
// before touching s.db or dialing anything.
func (s *DoltStore) rebuildPoolAfterMigration(ctx context.Context, applied int) error {
	if applied == 0 {
		return nil
	}

	newDB, err := sql.Open("mysql", s.connStr)
	if err != nil {
		return fmt.Errorf("rebuild pool after migration: %w", err)
	}
	applyPoolLimits(newDB, s.cfg)

	if err := newDB.PingContext(ctx); err != nil {
		_ = newDB.Close()
		return fmt.Errorf("rebuild pool after migration: %w", err)
	}

	old := s.db
	s.db = newDB
	return old.Close()
}

// IsClosed returns true if the store has been closed.
func (s *DoltStore) IsClosed() bool {
	return s.closed.Load()
}

// Close closes the database connection and removes any 0-byte noms LOCK files
// left behind by the embedded Dolt engine.
func (s *DoltStore) Close() error {
	s.closed.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.db != nil {
		if cerr := doltutil.CloseWithTimeout("db", s.db.Close); cerr != nil {
			// Timeout is non-fatal for cleanup - just log it
			if !errors.Is(cerr, context.Canceled) {
				err = errors.Join(err, cerr)
			}
		}
	}
	s.db = nil

	// Stop auto-started server when the last store referencing it closes.
	if s.autoStartedServerDir != "" {
		if stopErr := autoStartRelease(s.autoStartedServerDir); stopErr != nil {
			// Best-effort: don't mask other errors
			fmt.Fprintf(os.Stderr, "Warning: failed to stop auto-started dolt server: %v\n", stopErr)
		}
		s.autoStartedServerDir = ""
	}

	// WARNING: DO NOT remove, delete, or modify files inside Dolt's .dolt/
	// directory — including noms/LOCK files. These are Dolt-internal files.
	// Removing them WILL cause unrecoverable data corruption and data loss.
	// Dolt manages these files itself; external interference is never safe.

	return err
}

// Path returns the database directory path
func (s *DoltStore) Path() string {
	return s.dbPath
}

// IsReadOnly reports whether the store was opened in read-only mode. It is a
// test-facing accessor: production read-only enforcement comes from the
// read-only open mode itself (the readOnly field guards every write path), not
// from callers consulting this method. Tests such as
// TestDepRoutedTargetOpensReadOnly use it to assert that routed
// dependency/link target resolution opens a by-ID target read-only, so
// resolving it never opens a foreign project writable or runs open-time
// migrations into its history (bd-6dnrw.32, GH#3231).
func (s *DoltStore) IsReadOnly() bool {
	return s.readOnly
}

// CLIDir returns the directory for dolt CLI operations (push/pull/remote/fetch).
// The actual database lives in a subdirectory of Path() named after the database.
// Use this instead of Path() when running dolt CLI commands that target the
// actual database (e.g., remote add/remove, push, pull).
func (s *DoltStore) CLIDir() string {
	if s.serverMode && doltserver.IsSharedServerMode() && s.beadsDir != "" {
		return filepath.Join(doltserver.ResolveDoltDir(s.beadsDir), s.database)
	}
	if s.dbPath == "" {
		return ""
	}
	return filepath.Join(s.dbPath, s.database)
}

// ActiveDatabaseSize returns the approximate size of the active database.
// External server instances have no authoritative local path and report the
// capability as unsupported even if a stale client-local directory exists.
func (s *DoltStore) ActiveDatabaseSize(ctx context.Context) (int64, error) {
	if s.localActiveDatabaseDir == "" {
		return 0, &storage.ErrUnsupported{
			Op:      "ActiveDatabaseSize",
			Backend: "dolt-server",
		}
	}
	size, err := storage.MeasureDirectorySize(ctx, s.localActiveDatabaseDir)
	if err != nil {
		return 0, fmt.Errorf("measure active database directory %q: %w", s.localActiveDatabaseDir, err)
	}
	return size, nil
}

// DoltGC runs Dolt garbage collection to reclaim disk space.
// Pins a single connection to avoid session state loss on pooled *sql.DB.
func (s *DoltStore) DoltGC(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for gc: %w", err)
	}
	defer conn.Close()
	return versioncontrolops.DoltGC(ctx, conn)
}

// ListRemoteRefs returns the names of all cached remote-tracking refs.
func (s *DoltStore) ListRemoteRefs(ctx context.Context) ([]string, error) {
	return versioncontrolops.ListRemoteRefs(ctx, s.db)
}

// PruneRemoteRefs deletes all cached remote-tracking refs so a post-squash GC
// can reclaim the history they anchor (bd-agctw). Returns the deleted names.
func (s *DoltStore) PruneRemoteRefs(ctx context.Context) ([]string, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection for remote-ref prune: %w", err)
	}
	defer conn.Close()
	return versioncontrolops.PruneRemoteRefs(ctx, conn)
}

// ListTags returns the names of all Dolt tags.
func (s *DoltStore) ListTags(ctx context.Context) ([]string, error) {
	return versioncontrolops.ListTags(ctx, s.db)
}

// Flatten squashes all Dolt commit history into a single commit.
// Pins a single connection because the stored procedures (DOLT_CHECKOUT,
// DOLT_RESET, etc.) rely on session-scoped state that would be lost if
// steps execute on different pooled connections.
func (s *DoltStore) Flatten(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for flatten: %w", err)
	}
	defer conn.Close()
	return versioncontrolops.Flatten(ctx, conn)
}

// Compact squashes old Dolt commits while preserving recent ones.
// Pins a single connection for session-scoped stored procedures.
func (s *DoltStore) Compact(ctx context.Context, initialHash, boundaryHash string, oldCommits int, recentHashes []string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for compact: %w", err)
	}
	defer conn.Close()
	return versioncontrolops.Compact(ctx, conn, initialHash, boundaryHash, oldCommits, recentHashes)
}

// UnderlyingDB returns the underlying *sql.DB connection
func (s *DoltStore) UnderlyingDB() *sql.DB {
	return s.db
}

// =============================================================================
// Version Control Operations (Dolt-specific extensions)
// =============================================================================

func (s *DoltStore) commitAuthorString() string {
	return fmt.Sprintf("%s <%s>", s.committerName, s.committerEmail)
}

// configCommitMode controls how commitWorkingSet treats the config table, which
// holds both internal keys (issue_prefix) and synced user data (kv.* keys,
// including kv.memory.* persistent memories).
type configCommitMode int

const (
	// configExclude skips config entirely (GH#2455): a plain Commit must not
	// sweep a concurrent writer's half-applied issue_prefix change into an
	// unrelated commit.
	configExclude configCommitMode = iota
	// configIncludeUserKVOnly stages config for the pre-pull auto-commit, but
	// only when every dirty config row is this clone's own user KV data (the
	// kv.* namespace, which includes kv.memory.* memories). Any other dirty
	// config key — an internal key such as issue_prefix above all — aborts the
	// commit with operator guidance so the pull never auto-commits unsafe
	// config (GH#2455 + GH#2474).
	configIncludeUserKVOnly
	// configIncludeAll stages every dirty config row. Used only to conclude a
	// merge whose conflicts the operator resolved explicitly (bd federation
	// sync --strategy): that resolution is intentional, so a resolved
	// issue_prefix (or any config row) must be committed, not dropped.
	configIncludeAll
)

// Commit creates a Dolt commit with the given message.
//
// GH#2455: Stages all dirty tables EXCEPT config, then commits with '-m'.
// The old '-Am' approach staged ALL dirty tables including config, which
// swept up stale issue_prefix changes from concurrent operations. By
// excluding config from automatic staging, we prevent the corruption.
//
// Callers that intentionally modify config (e.g., CommitPending after
// 'bd config set') must call CommitWithConfig instead.
func (s *DoltStore) Commit(ctx context.Context, message string) error {
	return s.withCircuitWrite(ctx, func(ctx context.Context) error {
		return s.commitWorkingSet(ctx, message, configExclude)
	})
}

// commitBeforePull commits the working set ahead of a pull's merge, INCLUDING
// config. The pre-pull auto-commit (GH#2474) must include config because user
// KV data lives there as kv.* rows (persistent memories are the kv.memory.*
// subset) and Commit() deliberately skips config (GH#2455): without this those
// rows sit permanently uncommitted, so the "clean the working set before
// merging" step leaves config dirty and DOLT_MERGE refuses to start ("cannot
// merge with uncommitted changes").
//
// It includes ONLY this clone's own user kv.* rows: if any other config key is
// dirty (an internal key such as issue_prefix above all) it refuses rather than
// auto-committing it, so the stale-config corruption GH#2455 guards against is
// never re-opened by a pull. Auto-*resolution* of a config conflict stays
// narrower still — only convergent kv.memory.* keys (see
// configConflictsAreMemoryConvergent) — so widening the commit screen to the
// whole kv. namespace cannot auto-resolve a genuine kv.* conflict; it only stops
// generic `bd kv set` writes from wedging the pull. Config is staged explicitly
// (via DOLT_ADD in commitWorkingSet) because this path must screen dirty config
// rows before staging and admit only user kv.* data. GH#4412 also recorded an
// older live server-mode case where DOLT_COMMIT('-Am') did not stage config;
// CommitAll's pinned-server container test now proves that '-Am' stages config
// on the supported path. The explicit loop remains necessary for this path's
// narrower kv.* policy, not because CommitAll lacks server-mode coverage.
// Committing this clone's own kv.* rows as the merge basis is the same explicit,
// user-initiated action CommitPending ('bd dolt commit') already performs, so it
// does not widen the concurrent-writer race GH#2455 guards against.
func (s *DoltStore) commitBeforePull(ctx context.Context, message string) error {
	return s.commitWorkingSet(ctx, message, configIncludeUserKVOnly)
}

// CommitMergeResolution concludes a merge whose conflicts were resolved by an
// explicit operator strategy (bd federation sync --strategy / bd vc merge
// --strategy ours|theirs), committing the resolved working set INCLUDING config.
// Plain Commit excludes config (GH#2455), so a config-only resolution — exactly
// the case this change makes routine by syncing kv.* through config — would be
// silently dropped, leaving the merge unconcluded and re-wedging the next
// pull/sync. Unlike commitBeforePull it does not screen config keys: the operator
// chose this resolution, so whichever config rows it touched (issue_prefix
// included) are committed as-is. It satisfies storage.VersionControl so cmd/bd
// concludes bd vc merge --strategy through the same config-inclusive commit
// instead of the config-excluding Commit that would drop the resolution.
func (s *DoltStore) CommitMergeResolution(ctx context.Context, message string) error {
	return s.withCircuitWrite(ctx, func(ctx context.Context) error {
		return s.commitWorkingSet(ctx, message, configIncludeAll)
	})
}

// commitWorkingSet stages the dirty tables reported by dolt_status and commits
// them with '-m'. The config table is staged according to mode: configExclude
// skips it (GH#2455) so a concurrent writer's half-applied issue_prefix change
// is never swept into an unrelated commit; configIncludeUserKVOnly stages it for
// the pre-pull path but refuses when any non-kv. (internal) config key is dirty;
// configIncludeAll stages every dirty config row to conclude an explicit merge
// resolution.
func (s *DoltStore) commitWorkingSet(ctx context.Context, message string, mode configCommitMode) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.commit",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(s.doltSpanAttrs()...),
	)
	defer func() { endSpan(span, retErr) }()

	// Pin a single connection so all operations run on the same Dolt session.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Close()

	// GH#2455: stage each dirty table individually, skipping config unless the
	// mode opts it in, to avoid sweeping up stale issue_prefix changes from
	// concurrent operations. Exclude dolt_ignore'd tables (wisps, wisp_%, leases)
	// with the same anti-join HasCommittablePending uses: they surface in
	// dolt_status but are never stageable, and the fail-hard DOLT_ADD loop below
	// must see only tables it can actually stage. A dirty wisp or lease row is the
	// normal steady state; staging it depends on Dolt's version-specific
	// ignored-table DOLT_ADD behavior (a silent no-op on 2.2.0), so filtering here
	// keeps ordinary commits from failing whenever an ignored table is dirty.
	rows, err := conn.QueryContext(ctx, `
		SELECT s.table_name FROM dolt_status s
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`)
	if err != nil {
		// If dolt_status fails, fall back to nothing (rare edge case).
		return fmt.Errorf("failed to query dolt_status: %w", err)
	}
	var tables []string
	configDirty := false
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to scan dolt_status: %w", err)
		}
		if table == "config" {
			configDirty = true
			if mode == configExclude {
				continue
			}
		}
		tables = append(tables, table)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate dolt_status: %w", err)
	}

	// GH#2455 + GH#2474: the pre-pull auto-commit includes config so user kv.*
	// writes sync, but it must NOT auto-commit any internal (non-kv.) config key.
	// Refuse before staging anything so the merge is never concluded over an
	// unsafe config row; the operator commits those explicitly.
	if configDirty && mode == configIncludeUserKVOnly {
		if err := s.assertDirtyConfigUserKVOnly(ctx, conn); err != nil {
			return err
		}
	}

	if len(tables) == 0 {
		// A merge resolution with a clean working set is NOT a no-op: it is
		// the `--ours` case, where our values already stood and resolving the
		// conflict dirtied nothing. Returning here left is_merging true while
		// the caller reported "Merge committed", and the next pull re-wedged
		// on the unconcluded merge (wy-36ilm, caught by the F9 integration
		// test). Only the merge-conclusion mode takes this path: for the
		// other modes an empty working set really is nothing to commit.
		if mode == configIncludeAll {
			return s.concludeOpenMerge(ctx, conn, message)
		}
		return nil // Nothing to commit (all changes were config-only or dolt_ignore'd)
	}

	for _, table := range tables {
		if err := schema.DrainCall(ctx, conn, "CALL DOLT_ADD(?)", table); err != nil {
			return fmt.Errorf("failed to stage %s before commit: %w", table, err)
		}
	}

	// NOTE: In SQL procedure mode, Dolt defaults author to the authenticated SQL user
	// (e.g. root@localhost). Always pass an explicit author for deterministic history.
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?, '--author', ?)", message, s.commitAuthorString()); err != nil {
		if isDoltNothingToCommit(err) {
			return nil
		}
		return s.wrapDoltPublicationFailure(ctx, "failed to commit", err)
	}

	return nil
}

// commitWorkingSetAfterSQLCommit preserves the no-replay boundary for a Dolt
// publication that follows an already-visible SQL mutation. commitWorkingSet
// classifies DOLT_COMMIT response loss itself; this wrapper adds the same
// sentinel to earlier publication failures such as a lost DOLT_ADD response.
func (s *DoltStore) commitWorkingSetAfterSQLCommit(ctx context.Context, message string, mode configCommitMode) error {
	err := s.commitWorkingSet(ctx, message, mode)
	if err == nil || errors.Is(err, ErrCommitIndeterminate) || !isIndeterminateCommitResponse(err) {
		return err
	}
	return s.recordDoltPublicationFailure(ctx,
		fmt.Errorf("publish working set after SQL commit: %w: %w", err, ErrCommitIndeterminate))
}

// concludeOpenMerge commits an open merge whose resolution left the working
// set clean, so the merge is actually concluded rather than left open with
// nothing to show for it. It is a no-op when no merge is in progress, and it
// runs on the CALLER'S pinned connection because dolt's merge state is
// session state. isDoltNothingToCommit still absorbs the race where the merge
// closed between the status read and the commit.
func (s *DoltStore) concludeOpenMerge(ctx context.Context, conn *sql.Conn, message string) error {
	var merging bool
	if err := conn.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		// No merge status to read is no evidence of a merge — keep the old
		// "nothing to commit" behavior rather than failing a resolution.
		return nil //nolint:nilerr // diagnosis only; never a gate
	}
	if !merging {
		return nil
	}
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?, '--author', ?)", message, s.commitAuthorString()); err != nil {
		if isDoltNothingToCommit(err) {
			return nil
		}
		return s.wrapDoltPublicationFailure(ctx, "failed to conclude merge", err)
	}
	return nil
}

// assertDirtyConfigUserKVOnly returns an error unless every config row dirty in
// the working set is this clone's own user KV data (the kv.* namespace, which
// includes kv.memory.* memories). The pre-pull auto-commit opts config into the
// staged set so user KV writes sync and stop wedging DOLT_MERGE (GH#2474), but
// auto-committing an unrelated dirty internal config key such as issue_prefix
// would re-open the GH#2455 stale-config corruption — that is the operator's
// explicit `bd dolt commit` to make, not the pull's. Screening on the whole kv.
// namespace (not just kv.memory.*) un-wedges generic `bd kv set` writes too: a
// kv.* row is this clone's own data, exactly as safe to auto-commit as a memory,
// and a genuine kv.* merge conflict is still left for the operator because
// auto-resolution stays kv.memory.*-only (configConflictsAreMemoryConvergent).
// config's primary key is `key`, so dolt_diff exposes to_key/from_key; an add or
// delete leaves one side NULL, so COALESCE picks whichever key the change carries.
func (s *DoltStore) assertDirtyConfigUserKVOnly(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx,
		"SELECT COALESCE(to_key, from_key) FROM dolt_diff('HEAD', 'WORKING', 'config')")
	if err != nil {
		return fmt.Errorf("inspect dirty config before pull: %w", err)
	}
	defer rows.Close()

	var unsafe []string
	for rows.Next() {
		var key sql.NullString
		if err := rows.Scan(&key); err != nil {
			return fmt.Errorf("scan dirty config key: %w", err)
		}
		if key.Valid && !strings.HasPrefix(key.String, kvkeys.Prefix) {
			unsafe = append(unsafe, key.String)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate dirty config diff: %w", err)
	}
	if len(unsafe) > 0 {
		return fmt.Errorf("refusing to auto-commit %d dirty internal config key(s) before pull: %s; "+
			"only user %s* keys auto-commit before a pull (GH#2455) — commit or revert "+
			"these explicitly with `bd dolt commit` first", len(unsafe), strings.Join(unsafe, ", "), kvkeys.Prefix)
	}
	return nil
}

// CommitWithConfig creates a Dolt commit that includes the config table.
// Use this instead of Commit when the caller intentionally modified config
// (e.g., CommitPending after 'bd config set', 'bd init', or 'bd rename-prefix').
// GH#2455: Commit() excludes config to prevent sweeping up stale changes.
func (s *DoltStore) CommitWithConfig(ctx context.Context, message string) error {
	return s.withCircuitWrite(ctx, func(ctx context.Context) error {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("failed to acquire connection: %w", err)
		}
		defer conn.Close()

		if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)", message, s.commitAuthorString()); err != nil {
			if isDoltNothingToCommit(err) {
				return nil
			}
			return s.wrapDoltPublicationFailure(ctx, "failed to commit", err)
		}
		return nil
	})
}

// CommitAll creates a single Dolt commit of ALL uncommitted changes in the
// working set — config included — with the given message, and reports whether
// a commit actually landed. It is the storage entry point for the explicit
// operator commands (bd vc commit, bd dolt commit), which promise "any
// uncommitted changes in the working set":
//
//   - Unlike Commit, it stages config. Plain Commit deliberately excludes
//     config (GH#2455) to keep AUTOMATIC commits from sweeping a concurrent
//     writer's half-applied config change, but that made the explicit
//     commands silently skip out-of-band config dirt (a table modified by an
//     external writer, or by any path that bypasses bd's dirty-table
//     tracking) — a working set the doctor's dirty-working-set warning flags
//     while recommending exactly these commands as the remedy. An operator
//     explicitly asking to commit everything is the same trust level as
//     CommitPending, which has always included config for that reason.
//
//   - The committed bool is the atomic signal the CLI's HEAD-before/
//     HEAD-after comparison approximated (the committed-bool threading
//     tracked as bd mybd-z9h7j; CommitPending already had the shape). It
//     returns (false, nil) when nothing was committable — clean working set,
//     or only dolt_ignore'd tables such as wisps dirty ('-A' skips those) —
//     without the concurrent-writer misattribution race of comparing HEADs.
func (s *DoltStore) CommitAll(ctx context.Context, message string) (bool, error) {
	committed := false
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("failed to acquire connection: %w", err)
		}
		defer conn.Close()

		if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)", message, s.commitAuthorString()); err != nil {
			if isDoltNothingToCommit(err) {
				return nil
			}
			return s.wrapDoltPublicationFailure(ctx, "failed to commit", err)
		}
		committed = true
		return nil
	})
	return committed, err
}

// doltAddAndCommit stages the specified tables and commits on a pinned
// connection. This prevents DOLT_COMMIT('-Am') from sweeping up stale
// working set changes from concurrent operations (GH#2455). Every caller has
// already committed its SQL mutation, so any publication failure here has an
// indeterminate durable outcome and must not be replayed.
func (s *DoltStore) doltAddAndCommit(ctx context.Context, tables []string, commitMsg string) error {
	// Batch/off auto-commit (bd-4wamg): leave the writes in the working set
	// for a later explicit commit point (bd dolt commit / CommitPending),
	// matching doltAddAndCommitInTx.
	if issueops.VersionCommitDeferred(ctx) {
		return nil
	}
	return s.withCircuitWrite(ctx, func(ctx context.Context) error {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return s.recordDoltPublicationFailure(ctx,
				fmt.Errorf("acquire connection after SQL mutation: %w: %w", err, ErrCommitIndeterminate))
		}
		defer conn.Close()

		for _, table := range tables {
			if err := schema.DrainCall(ctx, conn, "CALL DOLT_ADD(?)", table); err != nil {
				return s.recordDoltPublicationFailure(ctx,
					fmt.Errorf("dolt add %s after SQL mutation: %w: %w", table, err, ErrCommitIndeterminate))
			}
		}

		// Skip the commit when nothing was actually staged (idempotent no-op
		// write), so Dolt does not log a server-side "nothing to commit" warning
		// on every reconcile-cadence call. The guard tests the STAGED set rather
		// than the whole working set because this helper stages only a fixed
		// table list — an unrelated dirty table must not trigger an empty '-m'
		// commit. A guard-read failure is NOT a publication failure: nothing has
		// been committed and nothing is indeterminate, so plain error return.
		staged, err := issueops.HasStagedChanges(ctx, conn)
		if err != nil {
			return fmt.Errorf("check staged changes before commit: %w", err)
		}
		if !staged {
			return nil
		}

		if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return s.recordDoltPublicationFailure(ctx,
				fmt.Errorf("dolt commit after SQL mutation: %w: %w", err, ErrCommitIndeterminate))
		}
		return nil
	})
}

func (s *DoltStore) wrapDoltPublicationFailure(ctx context.Context, op string, err error) error {
	return s.recordDoltPublicationFailure(ctx, wrapSQLCommitError(op, err))
}

// recordDoltPublicationFailure accounts once for an ambiguous connection loss
// at a Dolt publication boundary. Direct publication helpers stay outside
// withRetryTx; transaction-backed writes call this from withRetryTx itself.
func (s *DoltStore) recordDoltPublicationFailure(ctx context.Context, err error) error {
	if s.breaker == nil || !errors.Is(err, ErrCommitIndeterminate) || !isConnectionError(err) {
		return err
	}
	s.breaker.RecordFailure()
	if s.breaker.State() == circuitOpen {
		doltMetrics.circuitTrips.Add(ctx, 1)
		return fmt.Errorf("%w (circuit breaker tripped)", err)
	}
	return err
}

// HasCommittablePending reports whether the working set holds committable
// changes, excluding dolt_ignore'd tables (wisp and lease tables appear in
// dolt_status but can't be staged). Implements storage.PendingChangeDetector.
func (s *DoltStore) HasCommittablePending(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status s
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check status: %w", err)
	}
	return count > 0, nil
}

// CommitPending creates a single Dolt commit for all uncommitted changes in the working set.
// Returns (true, nil) if changes were committed, (false, nil) if there was nothing to commit,
// or (false, err) on failure. The commit message summarizes the accumulated changes by
// querying dolt_diff to count issue-level operations.
//
// This is the primary commit mechanism for batch mode, where multiple bd commands
// accumulate changes in the working set before committing at a logical boundary.
func (s *DoltStore) CommitPending(ctx context.Context, actor string) (bool, error) {
	dirty, err := s.HasCommittablePending(ctx)
	if err != nil {
		return false, err
	}
	if !dirty {
		return false, nil // Nothing to commit
	}

	msg := s.buildBatchCommitMessage(ctx, actor)
	// GH#2455: CommitPending is an explicit user action that should include ALL
	// pending changes, including config. CommitAll does exactly that, and maps
	// Dolt reporting "nothing to commit" despite the status pre-check (e.g.,
	// system tables or schema-only diffs) to an honest (false, nil) no-op.
	return s.CommitAll(ctx, msg)
}

// buildBatchCommitMessage generates a descriptive commit message summarizing
// what changed since the last commit by querying dolt_diff against HEAD.
// It reports issue-level create/update/delete counts and lists any other
// tables (labels, comments, events, etc.) that have uncommitted changes.
func (s *DoltStore) buildBatchCommitMessage(ctx context.Context, actor string) string {
	if actor == "" {
		actor = s.committerName
	}

	// Count issue-level changes by diff type
	var added, modified, removed int
	rows, err := s.db.QueryContext(ctx, `
		SELECT diff_type, COUNT(*) as cnt
		FROM dolt_diff('HEAD', 'WORKING', 'issues')
		GROUP BY diff_type
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var diffType string
			var count int
			if scanErr := rows.Scan(&diffType, &count); scanErr == nil {
				switch diffType {
				case "added":
					added = count
				case "modified":
					modified = count
				case "removed":
					removed = count
				}
			}
		}
		if rowErr := rows.Err(); rowErr != nil {
			// Best effort — proceed with whatever counts we gathered
			_ = rowErr
		}
	}

	// Check which other tables have uncommitted changes beyond issues.
	// This surfaces label, comment, event, and dependency changes that
	// would otherwise produce a generic fallback message.
	var otherTables []string
	statusRows, statusErr := s.db.QueryContext(ctx, `
		SELECT table_name FROM dolt_status s
		WHERE table_name != 'issues'
		AND NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)`)
	if statusErr == nil {
		defer statusRows.Close()
		for statusRows.Next() {
			var table string
			if scanErr := statusRows.Scan(&table); scanErr == nil {
				otherTables = append(otherTables, table)
			}
		}
		_ = statusRows.Err() // Best effort
	}

	// Build descriptive message
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d created", added))
	}
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", modified))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", removed))
	}

	if len(parts) == 0 && len(otherTables) == 0 {
		return fmt.Sprintf("bd: batch commit by %s", actor)
	}

	msg := fmt.Sprintf("bd: batch commit by %s", actor)
	if len(parts) > 0 {
		msg += " — " + strings.Join(parts, ", ")
	}
	if len(otherTables) > 0 {
		msg += fmt.Sprintf(" (+ %s)", strings.Join(otherTables, ", "))
	}
	return msg
}

// hasMatchingCLIRemote reports whether the local CLI directory contains the
// same remote URL that SQL reports. CLI push/pull/fetch run from CLIDir, so
// SQL visibility alone is not enough to route safely.
func (s *DoltStore) hasMatchingCLIRemote(remote, expectedURL string) bool {
	if expectedURL == "" {
		return false
	}
	cliDir := s.CLIDir()
	if cliDir == "" {
		return false
	}
	if !s.hasCLIDatabase() {
		return false
	}
	return doltutil.RemoteURLsMatch(doltutil.FindCLIRemote(cliDir, remote), expectedURL)
}

// hasCLIDatabase reports whether CLIDir points at an initialized Dolt database.
// SQL-capable routes use this as a CLI availability check and fall back to SQL
// when an external-server client has only a placeholder local directory.
func (s *DoltStore) hasCLIDatabase() bool {
	cliDir := s.CLIDir()
	if cliDir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(cliDir, ".dolt"))
	return err == nil && info.IsDir()
}

// ensureMatchingCLIRemote materializes the local CLI remote needed before
// subprocess push/pull/fetch routing. SQL remains the source of truth; the CLI
// remote is only the local transport surface that dolt subprocesses read.
func (s *DoltStore) ensureMatchingCLIRemote(remote, expectedURL string) error {
	if s.hasMatchingCLIRemote(remote, expectedURL) {
		return nil
	}
	cliDir := s.CLIDir()
	if expectedURL == "" {
		return fmt.Errorf("remote %q has an empty SQL URL", remote)
	}
	if cliDir == "" {
		return fmt.Errorf("remote %q (%s) requires CLI routing but no CLI directory is configured", remote, expectedURL)
	}
	if err := doltutil.EnsureCLIRemote(cliDir, remote, expectedURL); err != nil {
		return fmt.Errorf("materialize CLI remote %q (%s) in %s: %w", remote, expectedURL, cliDir, err)
	}
	if !s.hasMatchingCLIRemote(remote, expectedURL) {
		return fmt.Errorf("materialized CLI remote %q in %s, but its URL does not match SQL URL %q", remote, cliDir, expectedURL)
	}
	return nil
}

func (s *DoltStore) prepareDoltCLITransfer(ctx context.Context, remote string, creds *remoteCredentials, args ...string) (*exec.Cmd, context.Context, context.CancelFunc) {
	return prepareDoltCLITransferCommand(ctx, s.CLIDir(), creds, s.isS3Remote(ctx, remote), args...)
}

func prepareDoltCLITransferCommand(ctx context.Context, cliDir string, creds *remoteCredentials, s3Remote bool, args ...string) (*exec.Cmd, context.Context, context.CancelFunc) {
	ctx, cancel := withCLIExecTimeout(ctx)
	cmd := exec.CommandContext(ctx, "dolt", args...) // #nosec G204 -- fixed command with validated remote/ref args
	// CommandContext kills only the direct dolt child on expiry; a grandchild
	// (e.g. a cloud credential helper) holding the inherited output pipes
	// would otherwise keep Wait/CombinedOutput blocked forever after the kill.
	cmd.WaitDelay = cliExecWaitDelay
	cmd.Dir = cliDir
	creds.applyToCmd(cmd)
	if s3Remote {
		applyS3ChecksumEnvToCmd(cmd)
	}
	// Stderr-directed git tracing corrupts the transfer (see internal/gittraceenv);
	// mirrors withRemoteEnvGuards on the in-process path.
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = gittraceenv.ScrubEnv(base)
	return cmd, ctx, cancel
}

// prepareCLIRouteForGitProtocol reports whether the SQL-visible remote uses
// git wire protocol and prepares the matching local CLI remote before routing.
func (s *DoltStore) prepareCLIRouteForGitProtocol(ctx context.Context, remote string) (bool, error) {
	if s.CLIDir() == "" {
		return false, nil
	}
	if !s.hasCLIDatabase() {
		return false, nil
	}
	remotes, err := s.ListRemotes(ctx)
	if err != nil {
		return false, fmt.Errorf("list Dolt remotes before git-protocol routing: %w", err)
	}
	for _, r := range remotes {
		if r.Name == remote {
			if !doltutil.IsGitProtocolURL(r.URL) {
				return false, nil
			}
			if err := s.ensureMatchingCLIRemote(remote, r.URL); err != nil {
				return false, fmt.Errorf("remote %q uses git protocol and requires CLI routing: %w", remote, err)
			}
			return true, nil
		}
	}
	// Not visible in dolt_remotes — but that is not proof it is absent: a
	// freshly (auto-)started sql-server can report an empty dolt_remotes
	// while the remote is persisted on disk (GH#2118, wy-6k7f7). Recover the
	// persisted truth: a git-protocol remote routes over the CLI anyway, so
	// the push can proceed; a non-git remote would need the SQL route, which
	// the cold server would refuse with a bare "remote not found" — fail
	// with the cold-start explanation instead.
	for _, r := range s.PersistedRemoteInfos() {
		if r.Name != remote {
			continue
		}
		if !doltutil.IsGitProtocolURL(r.URL) {
			return false, fmt.Errorf("remote %q (%s) is persisted on disk but not yet visible to this sql-server (GH#2118 cold start); retry shortly, or restart the dolt sql-server if it persists", remote, r.URL)
		}
		if err := s.ensureMatchingCLIRemote(remote, r.URL); err != nil {
			return false, fmt.Errorf("remote %q uses git protocol and requires CLI routing: %w", remote, err)
		}
		return true, nil
	}
	return false, nil
}

// shouldUseCLIForGitProtocol is a compatibility wrapper for tests and older
// call sites. Prefer prepareCLIRouteForGitProtocol so mutation is explicit.
func (s *DoltStore) shouldUseCLIForGitProtocol(ctx context.Context, remote string) (bool, error) {
	return s.prepareCLIRouteForGitProtocol(ctx, remote)
}

// isGitProtocolRemote reports whether the SQL-visible remote uses git wire
// protocol and the same remote is available in the local CLI directory.
func (s *DoltStore) isGitProtocolRemote(ctx context.Context, remote string) bool {
	ok, err := s.prepareCLIRouteForGitProtocol(ctx, remote)
	if err != nil {
		log.Printf("warning: %v", err)
		return false
	}
	return ok
}

// mainRemoteCredentials returns credentials for the main remote, or nil if none.
func (s *DoltStore) mainRemoteCredentials() *remoteCredentials {
	if s.remoteUser == "" && s.remotePassword == "" {
		return nil
	}
	return &remoteCredentials{username: s.remoteUser, password: s.remotePassword}
}

// credentialsForRemote returns credentials only when the target remote is the
// default remote (s.remote). Non-default remotes get nil creds to avoid sending
// the wrong credentials to the wrong host.
func (s *DoltStore) credentialsForRemote(remote string) *remoteCredentials {
	if remote == s.remote {
		return s.mainRemoteCredentials()
	}
	return nil
}

// prePushFSCK runs dolt fsck --quiet to verify local chunk integrity before
// pushing. This prevents propagating Dolt remote corruption (dangling blob
// references) that arise when concurrent pushes race on the remote manifest.
//
// When multiple agents push simultaneously, one push's manifest update can
// land before another's chunks finish uploading, leaving a manifest that
// references chunks that were never stored. Any agent that then fetches and
// re-pushes that remote faithfully propagates the dangling reference.
//
// If CLIDir is empty or .dolt/noms does not exist, the check is skipped.
// Six outcomes are possible when fsck exits non-zero (see classifyFSCKFailure):
//   - non-empty output, could-not-open: skipped with a log warning.
//   - non-empty output, other: ErrDanglingReference — push aborted.
//   - parent context canceled: cancellation error — push aborted.
//   - parent context deadline exceeded: ErrFSCKTimeout (caller timeout) — push aborted.
//   - fsck own timeout: ErrFSCKTimeout (raise BEADS_FSCK_TIMEOUT) — push aborted.
//   - cancellation phrasing in output, no context error: cancellation error — push aborted.
func (s *DoltStore) prePushFSCK(ctx context.Context) error {
	dir := s.CLIDir()
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, ".dolt", "noms")); os.IsNotExist(err) {
		return nil
	}
	fsckCtx, cancel := context.WithTimeout(ctx, fsckTimeoutDuration())
	defer cancel()
	cmd := exec.CommandContext(fsckCtx, "dolt", "fsck", "--quiet") // #nosec G204 -- fixed command
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		output := strings.TrimSpace(string(out))
		if classified := classifyFSCKFailure(ctx.Err(), fsckCtx.Err(), output); classified != nil {
			return classified
		}
		log.Printf("pre-push fsck could not run, skipping integrity check: %s", output)
		return nil
	}
	return nil
}

// classifyFSCKFailure maps a failed dolt fsck exit into one of six outcomes,
// evaluated in priority order:
//
//	(a) Non-empty output → route by content: could-not-open → nil (caller logs
//	    and skips); cancellation phrasing (fsckOutputInterrupted) → fall
//	    through to the context-state branches below, because an interrupted
//	    fsck prints e.g. "context canceled" before dying and that text is not
//	    an integrity finding; any other content → ErrDanglingReference abort.
//	    Non-empty output means fsck actually ran and said something (--quiet
//	    fsck is silent until it finds a problem), so content wins over context
//	    state. This also closes the race where real corruption arrives at the
//	    deadline instant and would otherwise be masked as a timeout.
//
//	(b) Parent context canceled (Ctrl-C, caller abort) → plain cancellation
//	    error wrapping context.Canceled; neither ErrDanglingReference nor
//	    ErrFSCKTimeout (the store is not implicated).
//
//	(c) Parent context deadline exceeded → ErrFSCKTimeout, but guidance points
//	    at the CALLER's timeout (e.g. dolt.auto-push-timeout for auto-push)
//	    and explicitly notes BEADS_FSCK_TIMEOUT cannot extend it. Checked
//	    before fsck's own deadline because a fired parent context propagates
//	    cancellation into all child contexts, making both parentErr and fsckErr
//	    DeadlineExceeded simultaneously.
//
//	(d) fsck's own deadline exceeded, parent still running → ErrFSCKTimeout
//	    with dolt gc / CALL DOLT_GC() / BEADS_FSCK_TIMEOUT guidance.
//
//	(e) Cancellation phrasing in output but no recognized context error — the
//	    bd process (group) was killed out from under fsck, so neither context
//	    carries the reason. Plain cancellation error wrapping context.Canceled;
//	    the store is not implicated.
//
//	(f) Generic non-zero exit, empty output → ErrDanglingReference abort.
//
// Returning nil for the could-not-open case (branch a) distinguishes "fsck
// couldn't run at all" from "fsck ran and found a problem". Wrapping an
// open-failure as ErrDanglingReference misleads users (dolthub/dolt#10915);
// so does wrapping an interrupt's "context canceled" noise as corruption
// (same genus, observed when a background push's process group was killed).
func classifyFSCKFailure(parentErr, fsckErr error, output string) error {
	// (a) Non-empty output: fsck actually reported something; route by content.
	if output != "" {
		if fsckCouldNotOpen(output) {
			return nil
		}
		if !fsckOutputInterrupted(output) {
			return fmt.Errorf("%w: aborting push to prevent propagating corrupt chunks: %s",
				ErrDanglingReference, output)
		}
		// Cancellation phrasing: not an integrity finding — classify by
		// context state below.
	}
	// (b) Parent context canceled — user interrupt or caller abort.
	if errors.Is(parentErr, context.Canceled) {
		return fmt.Errorf("pre-push integrity check interrupted: %w", parentErr)
	}
	// (c) Caller's deadline expired during fsck. Point at the caller's timeout;
	// BEADS_FSCK_TIMEOUT cannot extend a deadline imposed by the caller.
	if errors.Is(parentErr, context.DeadlineExceeded) {
		return fmt.Errorf("%w: the surrounding operation's deadline expired during the "+
			"pre-push integrity check; to extend it, raise the caller timeout "+
			"(for auto-push: the dolt.auto-push-timeout config); "+
			"note that BEADS_FSCK_TIMEOUT cannot extend the caller deadline",
			ErrFSCKTimeout)
	}
	// (d) fsck's own per-call timeout expired (parent still running).
	if errors.Is(fsckErr, context.DeadlineExceeded) {
		return fmt.Errorf("%w: fsck did not complete within the configured timeout; "+
			"the push was aborted without checking integrity (the store is not necessarily corrupt); "+
			"large stores can be shrunk with `dolt gc` (or `CALL DOLT_GC()` on a running sql-server); "+
			"the timeout can be raised via the BEADS_FSCK_TIMEOUT environment variable",
			ErrFSCKTimeout)
	}
	// (e) Interrupted fsck whose cancellation reason lives only in the output
	// (process group killed: both contexts look healthy from here).
	if fsckOutputInterrupted(output) {
		return fmt.Errorf("pre-push integrity check interrupted: %w: %s",
			context.Canceled, output)
	}
	// (f) Generic failure with no output and no recognized context error.
	return fmt.Errorf("%w: aborting push to prevent propagating corrupt chunks",
		ErrDanglingReference)
}

// fsckCouldNotOpen reports whether dolt fsck output indicates the check
// could not run at all (as opposed to finding integrity problems). Matches
// the known error phrasings dolt emits before any integrity work begins.
func fsckCouldNotOpen(output string) bool {
	switch {
	case strings.Contains(output, "Could not open dolt database"):
		return true
	case strings.Contains(output, "repository state is invalid"):
		return true
	default:
		return false
	}
}

// fsckOutputInterrupted reports whether dolt fsck output is cancellation
// noise from a dying process rather than an integrity finding. An fsck
// interrupted mid-run (Ctrl-C, killed process group, expired deadline)
// prints the literal cancellation text to its combined output before
// exiting; treating that as a dangling-reference finding produces a false
// corruption report for a plain interrupt (bd-f2b15).
func fsckOutputInterrupted(output string) bool {
	for _, phrase := range []string{
		"context canceled",
		"context deadline exceeded",
		"signal: killed",
		"signal: terminated",
	} {
		if strings.Contains(output, phrase) {
			return true
		}
	}
	return false
}

// doltCLIPush shells out to `dolt push` from the database directory.
// Used for git-protocol remotes where CALL DOLT_PUSH times out through the SQL connection.
// If creds is non-nil, credentials are set on the subprocess environment only,
// avoiding process-wide env var races with concurrent goroutines.
func (s *DoltStore) doltCLIPush(ctx context.Context, remote string, force bool, creds *remoteCredentials) error {
	if err := s.prePushFSCK(ctx); err != nil {
		return err
	}
	args := []string{"push"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, remote, s.branch)
	cmd, transferCtx, cancel := s.prepareDoltCLITransfer(ctx, remote, creds, args...)
	defer cancel()
	applyNoGitHooksToCmd(cmd) // GH#3724
	out, err := cmd.CombinedOutput()
	if err != nil {
		return cliTransferError("dolt push", remote, transferCtx, out, err)
	}
	return nil
}

// cliTransferError wraps a failed CLI transfer, distinguishing a transfer that
// hit the bounded timeout (actionable: raise BEADS_CLI_TRANSFER_TIMEOUT, or
// check what holds the database directory busy) from an ordinary failure.
func cliTransferError(op, remote string, transferCtx context.Context, out []byte, err error) error {
	if errors.Is(transferCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s to %q timed out after %s (override with %s=<duration>; large transfers to cloud remotes can run long, and a busy dolt sql-server serving the database directory can stall CLI transfers): %s: %w",
			op, remote, cliExecTimeoutDuration(), cliExecTimeoutEnv, strings.TrimSpace(string(out)), err)
	}
	return fmt.Errorf("%s failed: %s: %w", op, strings.TrimSpace(string(out)), err)
}

// doltCLIPull shells out to `dolt pull` from the database directory.
// Used for git-protocol remotes where CALL DOLT_PULL times out through the SQL connection.
// If creds is non-nil, credentials are set on the subprocess environment only.
func (s *DoltStore) doltCLIPull(ctx context.Context, remote string, creds *remoteCredentials) error {
	cmd, transferCtx, cancel := s.prepareDoltCLITransfer(ctx, remote, creds, "pull", remote, s.branch)
	defer cancel()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return cliTransferError("dolt pull", remote, transferCtx, out, err)
	}
	return nil
}

// Push pushes commits to the remote.
// For git-protocol remotes (SSH, git+https://, git://), uses CLI `dolt push` to avoid MySQL connection timeouts.
// For non-SSH Hosted Dolt (remoteUser set), uses CALL DOLT_PUSH with --user authentication.
// For other remotes (DoltHub, S3, GCS, file), uses CALL DOLT_PUSH via SQL.
func (s *DoltStore) Push(ctx context.Context) (retErr error) {
	return s.pushToRemote(ctx, s.remote, false)
}

// ForcePush force-pushes commits to the remote, overwriting remote changes.
// Use when the remote has uncommitted changes in its working set.
// For git-protocol remotes (SSH, git+https://, git://), uses CLI `dolt push --force` to avoid MySQL connection timeouts.
func (s *DoltStore) ForcePush(ctx context.Context) (retErr error) {
	return s.pushToRemote(ctx, s.remote, true)
}

// PushRemote pushes commits to a named remote. Unlike Push(), which always
// uses the configured default remote (s.remote), PushRemote targets an
// explicit remote name. Credentials are only applied when the target remote
// matches the default remote; otherwise nil creds are used.
func (s *DoltStore) PushRemote(ctx context.Context, remote string, force bool) error {
	return s.pushToRemote(ctx, remote, force)
}

// pushToRemote is the internal implementation for all push operations.
// It routes through CLI or SQL based on the remote's protocol and credentials.
func (s *DoltStore) pushToRemote(ctx context.Context, remote string, force bool) (retErr error) {
	spanName := "dolt.push"
	if force {
		spanName = "dolt.force_push"
	}
	ctx, span := doltTracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.remote", remote),
			attribute.String("dolt.branch", s.branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	creds := s.credentialsForRemote(remote)
	// Git-protocol remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env, avoiding
	// process-wide env var races with concurrent goroutines.
	if useCLI, err := s.prepareCLIRouteForGitProtocol(ctx, remote); err != nil {
		return err
	} else if useCLI {
		return s.doltCLIPush(ctx, remote, force, creds)
	}
	// Credential CLI routing: when credentials are set and server is external,
	// route through CLI subprocess so credentials reach the dolt process via
	// cmd.Env (applyToCmd). The SQL path's withEnvCredentials sets process-wide
	// env vars that an external server cannot see.
	if useCLI, err := s.prepareCLIRouteForCredentials(ctx, remote, creds); err != nil {
		return err
	} else if useCLI {
		return s.doltCLIPush(ctx, remote, force, creds)
	}
	// Cloud auth CLI routing: when cloud storage env vars (AZURE_*, AWS_*,
	// etc.) are set and we're in server mode, route through CLI so the dolt
	// subprocess inherits the current env. The SQL server may not have these
	// vars if it was started in a different context (GH#6).
	if useCLI, err := s.prepareCLIRouteForCloudAuth(ctx, remote); err != nil {
		return err
	} else if useCLI {
		return s.doltCLIPush(ctx, remote, force, creds)
	}
	if useCLI, err := s.shouldUseCLIForLocalRemoteWithError(ctx, remote); err != nil {
		return err
	} else if useCLI {
		return s.doltCLIPush(ctx, remote, force, creds)
	}
	if s.remoteUser != "" && remote == s.remote {
		return withRemoteOperationEnv(creds, s.isS3Remote(ctx, remote), func() error {
			if force {
				if err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH('--force', '--user', ?, ?, ?)", s.remoteUser, remote, s.branch); err != nil {
					return fmt.Errorf("failed to force push to %s/%s: %w", remote, s.branch, err)
				}
			} else {
				if err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH('--user', ?, ?, ?)", s.remoteUser, remote, s.branch); err != nil {
					return fmt.Errorf("failed to push to %s/%s: %w", remote, s.branch, err)
				}
			}
			return nil
		})
	}
	return withRemoteOperationEnv(nil, s.isS3Remote(ctx, remote), func() error {
		if force {
			if err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH('--force', ?, ?)", remote, s.branch); err != nil {
				return fmt.Errorf("failed to force push to %s/%s: %w", remote, s.branch, err)
			}
		} else {
			if err := s.execWithLongTimeoutNoTx(ctx, "CALL DOLT_PUSH(?, ?)", remote, s.branch); err != nil {
				return fmt.Errorf("failed to push to %s/%s: %w", remote, s.branch, err)
			}
		}
		return nil
	})
}

// Pull pulls changes from the remote.
// Passes branch explicitly to avoid "did not specify a branch" errors.
// For git-protocol remotes (SSH, git+https://, git://), uses CLI `dolt pull` to avoid MySQL connection timeouts.
// For non-SSH Hosted Dolt (remoteUser set), uses CALL DOLT_PULL with --user authentication.
//
// If the pull results in merge conflicts on the metadata table only (e.g., from
// stale dolt_auto_push_* rows on multi-machine setups), the conflicts are
// automatically resolved using "theirs" strategy (GH#2466).
func (s *DoltStore) Pull(ctx context.Context) (retErr error) {
	return s.pullFromRemote(ctx, s.remote)
}

// PullRemote pulls changes from a named remote. Unlike Pull(), which always
// uses the configured default remote (s.remote), PullRemote targets an
// explicit remote name. Credentials are only applied when the target remote
// matches the default remote; otherwise nil creds are used.
func (s *DoltStore) PullRemote(ctx context.Context, remote string) error {
	return s.pullFromRemote(ctx, remote)
}

// pullFromRemote is the internal implementation for all pull operations.
// It routes through CLI or SQL based on the remote's protocol and credentials.
func (s *DoltStore) pullFromRemote(ctx context.Context, remote string) (retErr error) {
	return s.withCircuitWrite(ctx, func(ctx context.Context) error {
		return s.pullFromRemoteUnchecked(ctx, remote)
	})
}

func (s *DoltStore) pullFromRemoteUnchecked(ctx context.Context, remote string) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.pull",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.remote", remote),
			attribute.String("dolt.branch", s.branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()

	// GH#2474: Auto-commit pending changes before pull to prevent
	// "cannot merge with uncommitted changes" errors. Store initialization
	// (schema init, molecule loading, metadata writes) can dirty the working
	// set before the user's pull command runs.
	if !s.readOnly {
		if err := s.commitBeforePull(ctx, "auto-commit before pull"); err != nil {
			// "nothing to commit" is fine — working set is already clean
			if !isDoltNothingToCommit(err) {
				return fmt.Errorf("failed to commit pending changes before pull: %w", err)
			}
		}
	}

	// bd-6dnrw.3: capture the pre-pull commit of the branch this store reads so a
	// successful merge can recompute the denormalized is_blocked column for the
	// rows it changed. Read before the transport; an unreadable head degrades to
	// a full recompute.
	//
	// ga-ivaps Finding 3: read this unconditionally, including for read-only
	// stores. verifyPullLanded's cheap fast path — a head that moved is proof the
	// transport landed — needs it, and without it every pull that DID merge
	// something pays a network DOLT_FETCH round trip it could have skipped. (A
	// no-op pull moves no head and refreshes the tracking ref regardless, so the
	// saved round trip is on the merged pulls, never the no-op ones.)
	// recomputeBlockedAfterPull below still runs only for writable stores.
	//
	// ga-ivaps Finding 1 (attempt 2): read the tip of s.branch, not the session
	// HEAD. verifyPullLanded compares this against branchHash(ctx, s.branch) after
	// the pull, so reading the same branch here is what makes "the head moved"
	// mean THIS branch moved. On a multi-connection pool a query can land on a
	// connection still checked out to the database's default branch (the be-b0am
	// hazard), so GetCurrentCommit could report a different branch's head; when
	// that differs from the post-pull s.branch tip the fast path fires on a merge
	// that never reached s.branch and skips the very containment check meant to
	// catch it. branchHash reads dolt_branches, which is branch-global.
	preHead := ""
	if h, err := s.branchHash(ctx, s.branch); err == nil {
		preHead = h
	}

	if err := s.pullTransport(ctx, remote); err != nil {
		return err
	}

	// ga-ivaps: a route that returns nil having merged nothing is silent
	// divergence, so a transport's own "success" is not taken as proof the
	// merge landed. Checked before the recompute: recomputing derived state
	// over a merge that never arrived would report a second success on top of
	// the first.
	if err := s.verifyPullLanded(ctx, remote, preHead); err != nil {
		return err
	}

	if !s.readOnly {
		if err := s.recomputeBlockedAfterPull(ctx, preHead); err != nil {
			return fmt.Errorf("pull succeeded but is_blocked recompute failed: %w", err)
		}
	}
	return nil
}

// branchHash returns the commit hash at the tip of a local branch, or the empty
// string when the branch has no row. Reads dolt_branches, which is global to the
// database, so the answer does not depend on which branch the pooled connection
// happens to be sitting on.
func (s *DoltStore) branchHash(ctx context.Context, branch string) (string, error) {
	var hash string
	if err := s.db.QueryRowContext(ctx, "SELECT hash FROM dolt_branches WHERE name = ?", branch).Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return hash, nil
}

// verifyPullLanded reports whether the branch this store READS now contains the
// remote-tracking ref the pull just fetched. It is the post-condition that makes
// a pull which merged nothing distinguishable from one that had nothing to
// merge (ga-ivaps).
//
// WHY A POST-CONDITION AND NOT A BETTER TRANSPORT CHECK. Pull() has several
// routes (CLI subprocess, CALL DOLT_PULL, the fetch+merge fallback) and each
// reports success its own way; `dolt pull` exits 0 both when it merged and when
// it was already up to date, and CALL DOLT_PULL's (fast_forward, conflicts,
// message) row is discarded by the shared DrainCall helper. Rather than teach
// every route a new dialect, this asserts the one thing all of them promise:
// after a successful pull, the branch you read contains what was fetched.
//
// THE THREE OUTCOMES, AND WHY THE NO-OP STAYS QUIET:
//
//   - nothing to merge — the tracking ref equals the local head, so the local
//     branch trivially contains it. Returns nil, silently. This is the control:
//     a no-op pull must not become an error.
//   - merged — the local head is a descendant of the tracking ref, which still
//     contains it. Returns nil.
//   - reported success, merged nothing — the tracking ref moved and the local
//     branch did not follow it, so the merge landed somewhere the caller does
//     not read. Returns an error naming both hashes.
//
// WHY IT RE-FETCHES FIRST, which is the whole reason this works. Comparing the
// two refs as the transport left them catches only half the problem: a pull
// whose transport never reached THIS database leaves the tracking ref stale,
// and a stale tracking ref equals the local head, so the worst failure looks
// exactly like an honest no-op. Measured, not assumed — the ga-ivaps repro
// leaves remotes/origin/<branch> byte-identical to the local branch while the
// peer's commit sits unfetched on the remote. So the check refreshes the
// tracking ref itself, over the store's OWN connection, before comparing. That
// is what makes the comparison mean something: the ref it reads was written by
// this database, not by whatever the transport may or may not have done
// somewhere else. When the transport did land, the refresh is an
// already-up-to-date no-op.
//
// Fails OPEN on anything it cannot read or do. The refresh fetch can fail for
// reasons that say nothing about the pull (an external sql-server with no route
// to the remote, credentials scoped to the CLI subprocess), dolt_remote_branches
// carries no row for the tracking ref on remote types that do not maintain one,
// and a missing system table is not evidence of divergence. Turning "I cannot
// tell" into a failed pull would break working remotes to catch a broken one.
//
// KNOWN BLIND SPOT, one second wide, on git-backed remotes. Dolt's
// GitBlobstore.syncForRead skips the underlying git fetch when it last synced
// less than defaultSyncForReadTTL (1s) ago, and the blobstore is cached for the
// life of the sql-server. A pull issued inside that window — including the
// refresh above, which is served from the same cached mirror — cannot see a
// peer's commit pushed during it, so a pull that merged nothing is
// indistinguishable here from one that had nothing to merge and is reported as
// success. Bounded and upstream: the next pull outside the window sees the
// commit and merges it. Tests that push from a second process and then pull
// must wait the window out rather than depend on it.
//
// preHead is the branch head read before the transport ran, and is the cheap
// way out: a head that MOVED is itself proof the transport landed in this
// database on this branch, so the refresh below — the only part that costs a
// network round trip — is skipped for every pull that actually merged
// something. An empty preHead means it could not be read, which verifies.
func (s *DoltStore) verifyPullLanded(ctx context.Context, remote, preHead string) error {
	trackingRef := "remotes/" + remote + "/" + s.branch

	localHash, headErr := s.branchHash(ctx, s.branch)
	if headErr == nil && preHead != "" && localHash != "" && localHash != preHead {
		return nil
	}

	// Refresh the tracking ref through this database so the comparison below
	// reads a ref this connection wrote. Best-effort by design: a refresh that
	// cannot run leaves the weaker stale-ref comparison, which is still worth
	// making.
	//
	// ga-ivaps Finding (attempt 2): route the fetch over a long-timeout,
	// credential-aware connection instead of s.db. s.db carries the default ~10s
	// pool read timeout and none of the remote credentials the CLI-routed
	// (git-protocol, credential, cloud-auth) remotes need, so on exactly those
	// remotes the refresh would time out or auth-fail and the check would fall
	// back to the stale-ref comparison — fail open — for the transports most
	// likely to have dropped a merge. refreshTrackingRef mirrors the pull path's
	// own network calls, so the refresh reaches the same remotes the pull can.
	if err := s.refreshTrackingRef(ctx, remote); err != nil {
		log.Printf("warning: could not refresh %s to verify the pull landed: %v", trackingRef, err)
	}

	var remoteHash string
	if err := s.db.QueryRowContext(ctx,
		"SELECT hash FROM dolt_remote_branches WHERE name = ?", trackingRef).Scan(&remoteHash); err != nil {
		return nil
	}
	if remoteHash == "" {
		return nil
	}

	// DOLT_MERGE_BASE is the containment test: the base of (local, tracking)
	// is the tracking hash itself exactly when local already contains it —
	// whether local is equal to it (nothing to merge) or ahead of it (merged).
	var mergeBase sql.NullString
	if err := s.db.QueryRowContext(ctx,
		"SELECT DOLT_MERGE_BASE(?, ?)", s.branch, trackingRef).Scan(&mergeBase); err != nil {
		return nil
	}
	if !mergeBase.Valid || mergeBase.String == remoteHash {
		return nil
	}

	// ga-ivaps Finding 2 (attempt 2): the branch this database reads does not
	// contain the refreshed tracking ref, and the merge base splits that into two
	// states with very different recovery.
	//
	//   - mergeBase == localHash: local is a strict ANCESTOR of the tracking ref,
	//     so the remote merely moved ahead and a plain re-pull fast-forwards it.
	//     This is the ordinary "a peer pushed after our fetch" race — benign and
	//     self-correcting — so it is wrapped in ErrPullBehindFastForwardable and
	//     bd sync's loop retries it like a push race instead of hard-failing the
	//     tick (cmd/bd/sync.go). The message still names both hashes and "merged
	//     nothing", so a caller that surfaces it reads the same diagnosis.
	//   - otherwise: local and the tracking ref have genuinely diverged (their
	//     common ancestor is neither tip), which no re-pull can fast-forward away.
	//     That is the stuck-transport / split-brain signal, and it stays a hard
	//     error.
	//
	// The split fails safe: a true divergence can never be demoted to the
	// retryable class, because its common ancestor is by definition neither tip.
	// Distinguishing the benign race from a genuinely failed transport would
	// still need the remote tip as of the transport's own fetch, which git-backed
	// remotes never write into this database; the merge base is the best post-hoc
	// split available. Classified before the display fallback below rewrites an
	// empty localHash.
	behindFastForwardable := localHash != "" && mergeBase.String == localHash

	if localHash == "" {
		localHash = "unknown"
	}
	mergedNothing := fmt.Errorf("pull from %s/%s reported success but merged nothing into %s: %s is at %s while %s is at %s "+
		"(their common ancestor is %s), so the commits on the remote-tracking ref are not on the branch this "+
		"database reads. Most often another client pushed after this pull fetched and a re-run will merge it; "+
		"if the divergence survives repeated re-runs, the transport is not landing merges on this branch "+
		"(for example the dolt CLI directory and the sql-server are serving different databases or branches)",
		remote, s.branch, s.branch, s.branch, localHash, trackingRef, remoteHash, mergeBase.String)
	if behindFastForwardable {
		return fmt.Errorf("%w: %w", versioncontrolops.ErrPullBehindFastForwardable, mergedNothing)
	}
	return mergedNothing
}

// refreshTrackingRef fetches remote/s.branch into this database's
// remote-tracking refs over a dedicated long-timeout, credential-aware
// connection, so verifyPullLanded's comparison reads a tracking ref this
// database just wrote. It mirrors pullTransport's own network calls: a
// long-timeout connection (openLongTimeoutConn) wrapped in the remote's
// credential/S3 environment (withRemoteOperationEnv), which is what lets the
// refresh reach CLI-routed (git-protocol, credential, cloud-auth) remotes that
// the default s.db pool — short read timeout, no CLI credentials — cannot.
//
// Only the FETCH runs here, and DOLT_FETCH is branch-global: it advances
// remote-tracking refs and never touches the working branch, so the fresh
// connection's default-branch checkout — the be-b0am hazard that makes a merge
// on such a connection unsafe — does not apply. The containment reads in
// verifyPullLanded stay on s.db, where the short pool timeout is right: they are
// local, fast, and branch-parameterized.
func (s *DoltStore) refreshTrackingRef(ctx context.Context, remote string) error {
	db, err := s.openLongTimeoutConn()
	if err != nil {
		return err
	}
	defer db.Close()
	return withRemoteOperationEnv(s.credentialsForRemote(remote), s.isS3Remote(ctx, remote), func() error {
		return schema.DrainCall(ctx, db, "CALL DOLT_FETCH(?, ?)", remote, s.branch)
	})
}

// pullTransport routes one pull through CLI or SQL based on the remote's
// protocol and credentials, including the post-pull conflict auto-resolution
// each route carries. Split from pullFromRemote so every successful route
// funnels back through the is_blocked recompute.
func (s *DoltStore) pullTransport(ctx context.Context, remote string) error {
	_, err := s.pullTransportReporting(ctx, remote)
	return err
}

// pullTransportReporting is pullTransport plus the transport's own account of
// what it did. The CLI routes have none to give — `dolt pull` exits 0 whether
// it merged or was already up to date, and its stdout is discarded — so they
// report nothing; the SQL routes return what CALL DOLT_PULL (or the fallback's
// CALL DOLT_MERGE) said. See pullReport for what that does and does not prove.
func (s *DoltStore) pullTransportReporting(ctx context.Context, remote string) (pullReport, error) {
	creds := s.credentialsForRemote(remote)
	// Git-protocol remotes: use CLI to avoid MySQL connection timeout during transfer.
	// Must check before remoteUser — Hosted Dolt SSH remotes have remoteUser set
	// but still need CLI to avoid SQL connection timeout.
	// Credentials are passed directly to the subprocess via cmd.Env.
	if useCLI, err := s.prepareCLIRouteForGitProtocol(ctx, remote); err != nil {
		return pullReport{}, err
	} else if useCLI {
		// CLI pull leaves any conflicts in the working set; run the auto-resolver so
		// git-protocol remotes get the same audit-only dependency / metadata repair
		// as the SQL DOLT_PULL path (#4259).
		return pullReport{}, s.finishCLIPull(ctx, s.doltCLIPull(ctx, remote, creds))
	}
	// Credential CLI routing: mirrors git-protocol path, including post-pull
	// auto-resolution.
	if useCLI, err := s.prepareCLIRouteForCredentials(ctx, remote, creds); err != nil {
		return pullReport{}, err
	} else if useCLI {
		return pullReport{}, s.finishCLIPull(ctx, s.doltCLIPull(ctx, remote, creds))
	}
	// Cloud auth CLI routing (GH#6), including post-pull auto-resolution.
	if useCLI, err := s.prepareCLIRouteForCloudAuth(ctx, remote); err != nil {
		return pullReport{}, err
	} else if useCLI {
		return pullReport{}, s.finishCLIPull(ctx, s.doltCLIPull(ctx, remote, creds))
	}
	// Local file:// pulls intentionally stay on the SQL path. The matching CLI
	// guard is a push-only optimization; SQL pull keeps pullWithAutoResolve in
	// charge of metadata-only conflict repair.
	var report pullReport
	if s.remoteUser != "" && remote == s.remote {
		err := withRemoteOperationEnv(creds, s.isS3Remote(ctx, remote), func() error {
			var err error
			report, err = s.pullWithAutoResolveReporting(ctx, remote, "CALL DOLT_PULL('--user', ?, ?, ?)", s.remoteUser, remote, s.branch)
			if err != nil {
				return fmt.Errorf("failed to pull from %s/%s: %w", remote, s.branch, err)
			}
			return nil
		})
		return report, err
	}
	err := withRemoteOperationEnv(nil, s.isS3Remote(ctx, remote), func() error {
		var err error
		report, err = s.pullWithAutoResolveReporting(ctx, remote, "CALL DOLT_PULL(?, ?)", remote, s.branch)
		if err != nil {
			return fmt.Errorf("failed to pull from %s/%s: %w", remote, s.branch, err)
		}
		return nil
	})
	return report, err
}

// pullWithAutoResolve executes a DOLT_PULL query with long timeout and auto-resolves
// metadata-only merge conflicts using "theirs" strategy. This handles the common case
// where machine-local metadata rows (e.g., dolt_auto_push_*) diverge across clones
// and cause recurring merge conflicts on pull (GH#2466).
//
// Dolt may report merge conflicts in two ways:
//  1. DOLT_PULL itself returns an error (under autocommit)
//  2. DOLT_PULL succeeds but tx.Commit() fails (conflicts in working set)
//
// This method handles both by checking for conflicts after the pull call
// (whether it errored or not) and auto-resolving metadata-only conflicts.
// openLongTimeoutConn opens a dedicated single-connection *sql.DB to this store's
// database with a long read timeout, for merge/pull/conflict operations that can run
// longer than the default connection timeout. The caller must Close the returned DB.
func (s *DoltStore) openLongTimeoutConn() (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(s.connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse DSN for long-timeout connection: %w", err)
	}
	cfg.ReadTimeout = 5 * time.Minute
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("failed to open long-timeout connection: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// remote names the remote the query pulls from; the GH#3144 fetch+merge
// fallback targets it directly, so pulls from non-default remotes (PullRemote,
// federation peers) no longer fall back to s.remote.
func (s *DoltStore) pullWithAutoResolve(ctx context.Context, remote string, query string, args ...any) error {
	_, err := s.pullWithAutoResolveReporting(ctx, remote, query, args...)
	return err
}

// pullWithAutoResolveReporting is pullWithAutoResolve plus the row the pull (or
// the fetch+merge fallback) returned — the engine's own account of whether
// anything merged. See pullReport.
func (s *DoltStore) pullWithAutoResolveReporting(ctx context.Context, remote string, query string, args ...any) (pullReport, error) {
	var report pullReport
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		var err error
		report, err = s.pullWithAutoResolveUnchecked(ctx, remote, query, args...)
		return err
	})
	return report, err
}

func (s *DoltStore) pullWithAutoResolveUnchecked(ctx context.Context, remote string, query string, args ...any) (pullReport, error) {
	// Audited for be-b0am's fresh-connection branch hazard: NOT safe, and all
	// three callers share it. Passing a branch argument does not avoid it.
	//
	// DOLT_PULL's second positional arg names the remote ref to merge FROM
	// (dolt's doDoltPull binds it to remoteRefName); the merge TARGET is
	// always the session's current working branch (CWBHeadRef), which on this
	// fresh openLongTimeoutConn connection is the database's default branch,
	// never s.branch. So store.go's two callers, which pass s.branch to
	// CALL DOLT_PULL(...), pin only the source and still merge into the
	// default branch; federation.go's peer-pull route (CALL DOLT_PULL(?),
	// remote only) derives both source and target from that same default
	// branch. The GH#3144 fallback below has the same shape — DOLT_FETCH
	// names the remote ref, but CALL DOLT_MERGE(trackingRef) merges into the
	// current branch too.
	//
	// Left unfixed here deliberately: this is be-b0am's root cause on a
	// pull/merge path, and a merge landing on the wrong branch has a much
	// larger blast radius than a stale is_blocked flag, so it needs its own
	// regression test asserting the merge target rather than riding an
	// unrelated TDD cycle. Tracked as be-5ybd, which covers all three call
	// sites. The fix is s.pinStoreBranch(ctx, db) before BeginTx below.
	db, err := s.openLongTimeoutConn()
	if err != nil {
		return pullReport{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return pullReport{}, fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Allow commits with conflicts so we can inspect and resolve them.
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		return pullReport{}, fmt.Errorf("failed to set dolt_allow_commit_conflicts: %w", err)
	}
	// bd-6dnrw.4: a merge that violates a foreign key (e.g. one clone deleted
	// an issue while another inserted a child row referencing it) rolls the
	// whole transaction back before it can be inspected. Let it land in the
	// working set instead so tryRepairFKCascadeViolations can apply the
	// cascade semantics; the violation check before tx.Commit() below refuses
	// to commit anything the repair did not fully clear.
	if _, err := tx.ExecContext(ctx, "SET @@dolt_force_transaction_commit = 1"); err != nil {
		_ = tx.Rollback()
		return pullReport{}, fmt.Errorf("failed to set dolt_force_transaction_commit: %w", err)
	}

	// DOLT_PULL's row is the engine's only in-band account of what the pull
	// did: `dolt pull` on the CLI exits 0 whether it merged or was already up
	// to date, and so does this CALL. Capturing it costs nothing — the drain
	// is identical — and it is the difference between a caller that knows
	// nothing arrived and one that only knows no error occurred (ga-bq9zd).
	pullRow, pullErr := schema.CallReturningRow(ctx, tx, query, args...)
	report := parseMergeReport(pullRow)

	// GH#3144: When DOLT_PULL fails because upstream branch tracking is not
	// configured in repo_state.json (common when remote was added via
	// bd dolt remote add rather than bd bootstrap/dolt clone), fall back to
	// DOLT_FETCH + DOLT_MERGE which does not require tracking config.
	if pullErr != nil && isBranchTrackingError(pullErr) {
		if err := schema.DrainCall(ctx, tx, "CALL DOLT_FETCH(?, ?)", remote, s.branch); err != nil {
			_ = tx.Rollback()
			return pullReport{}, fmt.Errorf("fetch from %s/%s: %w", remote, s.branch, err)
		}
		trackingRef := remote + "/" + s.branch
		// The merge, not the pull, is now what happened — so its row replaces
		// the failed pull's report rather than adding to it.
		mergeRow, mergeErr := schema.CallReturningRow(ctx, tx, "CALL DOLT_MERGE(?)", trackingRef)
		report = parseMergeReport(mergeRow)
		// Retained deliberately even though DOLT_MERGE reports the ordinary
		// no-op as a MESSAGE with a nil error (measured: "Everything
		// up-to-date" and "cannot fast forward from a to b. a is ahead of b
		// already" both arrive that way). Dolt's squash path still returns
		// "Already up to date." as a real error, and that is what this catches.
		if mergeErr != nil && strings.Contains(mergeErr.Error(), "up to date") {
			mergeErr = nil
		}
		pullErr = mergeErr
	}

	return report, s.settleMergeInTx(ctx, tx, pullErr)
}

// settleMergeInTx finishes a pull/merge that ran in tx: it auto-resolves the
// safe conflict classes, repairs FK cascade violations (bd-6dnrw.4), and
// commits — or rolls back when anything needs the operator. pullErr is the
// pull/merge statement's own error; it is surfaced whenever nothing was
// resolved or repaired. The tx must have been opened with
// dolt_allow_commit_conflicts and dolt_force_transaction_commit set, which is
// why the violation gate here is mandatory: with the force flag on, committing
// without it would persist a violated working set.
func (s *DoltStore) settleMergeInTx(ctx context.Context, tx *sql.Tx, pullErr error) error {
	// Check for merge conflicts regardless of whether DOLT_PULL errored.
	// Some Dolt versions error on conflicts, others leave them in the working set.
	resolved, resolveErr := s.tryAutoResolveMergeConflicts(ctx, tx)
	if resolveErr != nil {
		_ = tx.Rollback()
		if pullErr != nil {
			return pullErr
		}
		return resolveErr
	}

	// bd-578h9.15: conflicts the resolver declined are the operator's. Capture
	// them BEFORE the rollback wipes merge state — a post-rollback GetConflicts
	// on a fresh transaction sees an empty set, which made PullFrom's
	// conflict-reporting contract dead code on the SQL route. The resolver
	// pre-screens every table before resolving any, so a declined resolve
	// leaves dolt_conflicts fully intact here.
	if !resolved {
		if conflicts, cErr := versioncontrolops.GetConflicts(ctx, tx); cErr == nil && len(conflicts) > 0 {
			_ = tx.Rollback()
			return &versioncontrolops.MergeConflictsError{Conflicts: conflicts, MergeErr: pullErr}
		}
	}

	// bd-6dnrw.4: repair FK cascade violations the merge produced (child rows
	// whose parent issue was deleted on the other clone). Unrepaired
	// violations MUST NOT be committed.
	repairedViol, hadViol, violErr := s.tryRepairFKCascadeViolations(ctx, tx)
	if violErr != nil {
		_ = tx.Rollback()
		if pullErr != nil {
			return pullErr
		}
		return violErr
	}
	if hadViol && !repairedViol {
		_ = tx.Rollback()
		if pullErr != nil {
			return pullErr
		}
		return fmt.Errorf("pull merge left constraint violations bd cannot auto-repair; inspect dolt_constraint_violations and resolve before retrying")
	}

	if pullErr != nil && !resolved && !repairedViol {
		// Pull failed for a non-conflict reason, or conflicts include non-metadata tables.
		_ = tx.Rollback()
		return pullErr
	}

	// Conclude the merge for resolved conflicts only now, after the FK repair:
	// DOLT_COMMIT refuses a violated working set, so a merge carrying both
	// classes could never settle when the resolver committed first (bd-578h9.14).
	if resolved {
		if err := versioncontrolops.CommitResolvedConflicts(ctx, tx); err != nil {
			_ = tx.Rollback()
			if pullErr != nil {
				return pullErr
			}
			return err
		}
	}

	return s.commitSQLTx(ctx, "commit pull merge settlement", tx)
}

// recomputeBlockedAfterPull recomputes the denormalized is_blocked column for
// the rows a pull's merge changed (bd-6dnrw.3) and commits the result.
// is_blocked is otherwise maintained only by local write paths, so a merge
// that brings in another clone's status or dependency changes leaves it stale
// and `bd ready` trusts it. fromCommit is the pre-pull HEAD; empty means it
// could not be read, which degrades to a full recompute. A pull that merged
// nothing (HEAD unchanged) is a no-op.
func (s *DoltStore) recomputeBlockedAfterPull(ctx context.Context, fromCommit string) error {
	return s.withCircuitWrite(ctx, func(ctx context.Context) error {
		return s.recomputeBlockedAfterPullUnchecked(ctx, fromCommit)
	})
}

func (s *DoltStore) recomputeBlockedAfterPullUnchecked(ctx context.Context, fromCommit string) error {
	if err := s.recomputeBlockedTx(ctx, fromCommit); err != nil {
		// The merge this recompute covers is already committed, so a plain
		// retry on the next pull would skip as "nothing merged" — leave a
		// marker so it widens its window instead (bd-578h9.11). Best-effort:
		// the recompute error is what matters.
		s.markBlockedRecomputePending(ctx, fromCommit)
		return err
	}
	// Derived state converges: every clone computes the same values from the
	// same merged graph, so committing is merge-safe. Commit no-ops when the
	// recompute changed nothing.
	if err := s.commitWorkingSetAfterSQLCommit(ctx, "bd: recompute is_blocked after pull", configExclude); err != nil && !isDoltNothingToCommit(err) {
		return fmt.Errorf("commit is_blocked recompute: %w", err)
	}
	return nil
}

// RecomputeAllBlocked recomputes is_blocked for every issue and wisp in one full
// pass and returns the number of rows it corrected. It is the mode-independent
// repair behind 'bd recompute-blocked' and 'bd doctor --fix' (bd-6dnrw.37): the
// scoped post-pull recompute is skipped when a re-pull merges nothing, so a
// recompute that failed after its merge committed — or a conflicted pull the
// operator resolved by hand — leaves is_blocked stale until this full pass runs.
// Idempotent: a consistent database corrects nothing.
func (s *DoltStore) RecomputeAllBlocked(ctx context.Context) (int, error) {
	var changed int
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		var err error
		changed, err = s.recomputeAllBlocked(ctx)
		return err
	})
	return changed, err
}

func (s *DoltStore) recomputeAllBlocked(ctx context.Context) (int, error) {
	// The full pass's batched UPDATEs carry five correlated EXISTS subqueries
	// each; on a loaded shared server a single batch can outlive the pool's
	// per-I/O deadline (default 10s, see buildServerDSN), killing the repair
	// with "i/o timeout" — and the retry dies the same way, so the owed
	// recompute never lands (bd-bn8jo). Run it on a dedicated long-timeout
	// connection like the other known-long maintenance ops.
	db, err := s.openLongTimeoutConn()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	// Pin a single physical connection for the whole operation (same
	// pattern as federation.go's filteredPushToPeer — branch state is
	// connection-scoped, so BeginTx must run on the exact connection that
	// was just checked out, not whatever the pool hands out next) and
	// reproduce the store's real active branch on it before the recompute
	// reads or writes anything; otherwise this fresh connection defaults to
	// Dolt's default branch instead of whatever the store is actually
	// checked out to (be-b0am).
	db.SetMaxIdleConns(0)
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire long-timeout connection: %w", err)
	}
	defer conn.Close()
	if err := s.pinStoreBranch(ctx, conn); err != nil {
		return 0, err
	}
	return s.recomputeAllBlockedWithDB(ctx, conn)
}

// txBeginner is satisfied by both *sql.DB and *sql.Conn, letting
// recomputeAllBlockedWithDB run either against a caller-owned pinned
// *sql.Conn (recomputeAllBlocked) or directly against a *sql.DB (tests).
type txBeginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

func (s *DoltStore) recomputeAllBlockedWithDB(ctx context.Context, db txBeginner) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin is_blocked recompute: %w", err)
	}
	// One shared body across every mode (guard a dirty graph, then recompute):
	// the guard refuses to derive and commit is_blocked from uncommitted
	// issue/dependency edits, and it runs inside THIS tx so it sees exactly the
	// working set the recompute reads (bd-6dnrw.37).
	changed, err := versioncontrolops.GuardedRecomputeAllBlockedInTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := s.commitSQLTx(ctx, "commit is_blocked recompute", tx); err != nil {
		return 0, err
	}
	if changed > 0 {
		// Stage only issues — the synced table is_blocked lives on (wisps are
		// dolt_ignore'd) — so an unrelated dirty working set is not swept in.
		if err := s.doltAddAndCommit(ctx, blockedRecomputeStagedTableList(),
			versioncontrolops.BlockedRecomputeCommitMsg); err != nil {
			return int(changed), err
		}
	}
	return int(changed), nil
}

// blockedRecomputeStagedTableList is versioncontrolops.BlockedRecomputeStagedTables
// in the ordered form doltAddAndCommit takes, so the staging set of the repair
// commit is defined in exactly one place for every mode.
func blockedRecomputeStagedTableList() []string {
	staged := versioncontrolops.BlockedRecomputeStagedTables()
	tables := make([]string, 0, len(staged))
	for table := range staged {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

// recomputeBlockedTx runs the post-merge is_blocked recompute in its own
// transaction. Like RecomputeAllBlocked it runs on a long-timeout connection:
// a heavy merge scopes the recompute over a large diff, and a pool-deadline
// kill here is what turns into the owed full recompute in the first place
// (bd-bn8jo).
func (s *DoltStore) recomputeBlockedTx(ctx context.Context, fromCommit string) error {
	db, err := s.openLongTimeoutConn()
	if err != nil {
		return err
	}
	defer db.Close()
	// See recomputeAllBlocked: pin a single physical connection and
	// reproduce the store's real active branch on it before the recompute
	// runs (be-b0am).
	db.SetMaxIdleConns(0)
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire long-timeout connection: %w", err)
	}
	defer conn.Close()
	if err := s.pinStoreBranch(ctx, conn); err != nil {
		return err
	}
	return s.recomputeBlockedTxWithDB(ctx, conn, fromCommit)
}

func (s *DoltStore) recomputeBlockedTxWithDB(ctx context.Context, db txBeginner, fromCommit string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin is_blocked recompute: %w", err)
	}
	if err := issueops.RecomputeIsBlockedAfterMergeInTx(ctx, tx, fromCommit); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.commitSQLTx(ctx, "commit is_blocked recompute", tx); err != nil {
		return err
	}
	return nil
}

// markBlockedRecomputePending best-effort records a failed post-merge
// is_blocked recompute (bd-578h9.11); see
// issueops.MarkIsBlockedRecomputePendingInTx.
func (s *DoltStore) markBlockedRecomputePending(ctx context.Context, fromCommit string) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	if err := issueops.MarkIsBlockedRecomputePendingInTx(ctx, tx, fromCommit); err != nil {
		_ = tx.Rollback()
		return
	}
	_ = tx.Commit()
}

// finishCLIPull runs the merge-conflict auto-resolver after a CLI-based pull
// (git-protocol, credentialed, or cloud-auth remotes). CLI `dolt pull` writes any
// merge conflicts into the shared working set but, unlike the SQL DOLT_PULL path,
// returns without a transaction we can inspect — so these remotes historically
// skipped the resolver entirely. With deterministic dependency ids (#4259) a
// same-edge conflict that differs only in audit columns is safe to auto-resolve, and
// the git remote topology in #4259 is exactly this CLI path; route it through the
// same resolver as the SQL path. pullErr is what doltCLIPull returned: a pull that
// fails *because* of conflicts is recoverable once they resolve, so we inspect the
// working set regardless and only surface pullErr when nothing was resolved.
func (s *DoltStore) finishCLIPull(ctx context.Context, pullErr error) error {
	if s.readOnly {
		// A read-only store cannot resolve or commit; surface the pull result as-is.
		return pullErr
	}
	resolved, resolveErr := s.autoResolveConflictsAfterCLIPull(ctx)
	if resolveErr != nil {
		if pullErr != nil {
			return pullErr
		}
		return resolveErr
	}
	if pullErr != nil && !resolved {
		// Pull failed for a non-conflict reason, or conflicts are not auto-resolvable;
		// leave them in the working set for the operator.
		return pullErr
	}
	return nil
}

// autoResolveConflictsAfterCLIPull inspects the working set and auto-resolves the
// conflict classes that are safe without operator input (#4259 audit-only dependency
// edges, GH#2466 metadata, GH#4698 issues-table LWW). It runs on a connection from
// the store pool (s.db) on
// purpose: those connections are on the same branch the CLI `dolt pull` merged into,
// whereas a separately opened connection would default to the base branch and never
// see the conflicts. The pull's
// network transfer already completed in the subprocess, so no long-timeout connection
// is needed for the local resolve. Returns (true, nil) only if all conflicts were
// resolved and committed; (false, nil) when there is nothing to resolve or a conflict
// needs the operator, leaving the working set untouched for manual resolution.
func (s *DoltStore) autoResolveConflictsAfterCLIPull(ctx context.Context) (bool, error) {
	// Pin a single connection: @@dolt_allow_commit_conflicts is session-scoped,
	// and setting it through a pooled transaction leaks it to whichever caller
	// drains that connection next. Reset it before releasing the connection; if
	// the reset cannot run, discard the connection rather than return it dirty.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to acquire connection: %w", err)
	}
	varSet := false
	defer func() {
		if varSet {
			if _, err := conn.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 0"); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
		_ = conn.Close()
	}()
	// Allow committing while conflicts exist so we can inspect and resolve them.
	if _, err := conn.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		return false, fmt.Errorf("failed to set dolt_allow_commit_conflicts: %w", err)
	}
	varSet = true
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	resolved, err := s.tryAutoResolveMergeConflicts(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	// bd-6dnrw.4: a CLI pull can also leave FK cascade violations in the
	// shared working set (child rows whose parent issue was deleted on the
	// other clone). Repair them like the SQL route does; unrepaired
	// violations roll back untouched for the operator.
	repairedViol, hadViol, violErr := s.tryRepairFKCascadeViolations(ctx, tx)
	if violErr != nil {
		_ = tx.Rollback()
		return false, violErr
	}
	if hadViol && !repairedViol {
		_ = tx.Rollback()
		return false, nil
	}
	if !resolved && !repairedViol {
		_ = tx.Rollback()
		return false, nil
	}
	// Conclude the merge for resolved conflicts only now, after the FK repair:
	// DOLT_COMMIT refuses a violated working set, so a merge carrying both
	// classes could never settle when the resolver committed first (bd-578h9.14).
	if resolved {
		if err := versioncontrolops.CommitResolvedConflicts(ctx, tx); err != nil {
			_ = tx.Rollback()
			return false, err
		}
	}
	if err := s.commitSQLTx(ctx, "commit resolved CLI pull conflicts", tx); err != nil {
		return false, err
	}
	return true, nil
}

// tryAutoResolveMergeConflicts auto-resolves merge conflicts that are safe to
// resolve without operator input (GH#2466 metadata, #4259 audit-only
// dependency edges, bd-6dnrw.29 schema_migrations vintage rows, GH#2474
// convergent kv.memory.* config rows, GH#4698 issues-table LWW by updated_at),
// returning (true, nil) only if ALL conflicts were resolved. The
// implementation is
// shared with the embedded pull path (bd-6dnrw.40); see
// versioncontrolops.TryAutoResolveMergeConflicts for the full contract.
func (s *DoltStore) tryAutoResolveMergeConflicts(ctx context.Context, tx *sql.Tx) (bool, error) {
	return versioncontrolops.TryAutoResolveMergeConflicts(ctx, tx)
}

// tryRepairFKCascadeViolations repairs the post-merge foreign-key constraint
// violations produced by the delete-vs-insert cascade hazard (bd-6dnrw.4).
// The caller's transaction must run with @@dolt_force_transaction_commit=1
// for the merge to survive long enough to be repaired, and must NOT commit
// when (repaired=false, had=true) — unrepaired violations are the operator's.
// The implementation is shared with the embedded pull path (bd-6dnrw.40); see
// versioncontrolops.TryRepairFKCascadeViolations for the full contract.
func (s *DoltStore) tryRepairFKCascadeViolations(ctx context.Context, tx *sql.Tx) (repaired, had bool, err error) {
	return versioncontrolops.TryRepairFKCascadeViolations(ctx, tx)
}

// Branch creates a new branch
func (s *DoltStore) Branch(ctx context.Context, name string) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.branch",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.branch", name),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for branch: %w", err)
	}
	defer conn.Close()
	return versioncontrolops.CreateBranch(ctx, conn, name)
}

// Checkout switches to the specified branch
func (s *DoltStore) Checkout(ctx context.Context, branch string) (retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.checkout",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.branch", branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for checkout: %w", err)
	}
	defer conn.Close()
	if err := versioncontrolops.CheckoutBranch(ctx, conn, branch); err != nil {
		return err
	}
	s.branch = branch
	return nil
}

// Merge merges the specified branch into the current branch.
// Returns any merge conflicts if present. Implements storage.VersionedStorage.
func (s *DoltStore) Merge(ctx context.Context, branch string) (conflicts []storage.Conflict, retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.merge",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.merge_branch", branch),
		)...),
	)
	defer func() { endSpan(span, retErr) }()

	// bd-578h9.11: like every pull path, a branch merge brings in writes that
	// bypassed the local is_blocked hooks; recompute after a conflict-free
	// merge. Conflicted merges defer to the caller's post-resolution hook
	// (Sync, bd vc merge --strategy) — recomputing over unresolved rows would
	// read garbage.
	preHead := ""
	if !s.readOnly {
		if h, err := s.GetCurrentCommit(ctx); err == nil {
			preHead = h
		}
	}

	conflicts, err := versioncontrolops.Merge(ctx, s.db, branch, s.commitAuthorString())
	if len(conflicts) > 0 {
		span.SetAttributes(attribute.Int("dolt.conflicts", len(conflicts)))
	}
	if err == nil && len(conflicts) == 0 && !s.readOnly {
		if rerr := s.recomputeBlockedAfterPull(ctx, preHead); rerr != nil {
			return conflicts, fmt.Errorf("merge succeeded but is_blocked recompute failed: %w", rerr)
		}
	}
	return conflicts, err
}

// MergeWithStrategy implements storage.StrategicMerger for `bd vc merge
// --strategy` (#4992). Merge (above) runs the bare CALL DOLT_MERGE on the
// shared pool: that is enough to detect a conflict-shaped autocommit
// rejection, but not to resolve one, because Dolt's conflict-tolerant session
// flags (@@dolt_allow_commit_conflicts, @@dolt_force_transaction_commit) are
// session state and the pool may hand a later statement a different
// connection. MergeWithStrategy instead pins a single connection — the same
// pattern Branch/Checkout use for stored procedures — for the whole
// merge/resolve/repair/commit sequence versioncontrolops.MergeWithStrategy
// runs.
//
// A resolved merge (conflicted or clean) always commits, so — unlike Merge,
// which skips the recompute for a still-conflicted merge — the is_blocked
// recompute always runs on success here.
func (s *DoltStore) MergeWithStrategy(ctx context.Context, branch, strategy string) (conflicts []storage.Conflict, retErr error) {
	ctx, span := doltTracer.Start(ctx, "dolt.merge_with_strategy",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(append(s.doltSpanAttrs(),
			attribute.String("dolt.merge_branch", branch),
			attribute.String("dolt.merge_strategy", strategy),
		)...),
	)
	defer func() { endSpan(span, retErr) }()

	preHead := ""
	if !s.readOnly {
		if h, err := s.GetCurrentCommit(ctx); err == nil {
			preHead = h
		}
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection for merge: %w", err)
	}
	conflicts, err = versioncontrolops.MergeWithStrategy(ctx, conn, branch, s.commitAuthorString(), strategy)
	// Release the pinned connection before the recompute: s.db's pool can be
	// configured with a single connection (setupTestStore's MaxOpenConns: 1
	// mirrors constrained production configs), and recomputeBlockedAfterPull
	// acquires its own connection — held past this point, conn would starve
	// it of the only one available.
	closeErr := conn.Close()
	if len(conflicts) > 0 {
		span.SetAttributes(attribute.Int("dolt.conflicts", len(conflicts)))
	}
	if err != nil {
		return conflicts, err
	}
	if closeErr != nil {
		return conflicts, fmt.Errorf("release merge connection: %w", closeErr)
	}
	if !s.readOnly {
		if rerr := s.recomputeBlockedAfterPull(ctx, preHead); rerr != nil {
			return conflicts, fmt.Errorf("merge succeeded but is_blocked recompute failed: %w", rerr)
		}
	}
	return conflicts, nil
}

// RecomputeBlockedAfterMerge recomputes the denormalized is_blocked column
// for the rows changed since fromCommit and commits the result — the hook a
// caller that resolved merge conflicts itself must run after committing the
// resolution (bd-578h9.11): conflicted merges skip the automatic recompute
// because unresolved rows would feed it garbage, and nothing else covers the
// merged-in writes. fromCommit is the pre-merge HEAD; empty degrades to a
// full-graph recompute.
func (s *DoltStore) RecomputeBlockedAfterMerge(ctx context.Context, fromCommit string) error {
	return s.recomputeBlockedAfterPull(ctx, fromCommit)
}

// CurrentBranch returns the current branch name
func (s *DoltStore) CurrentBranch(ctx context.Context) (string, error) {
	return versioncontrolops.CurrentBranch(ctx, s.db)
}

// DeleteBranch deletes a branch (used to clean up import branches)
func (s *DoltStore) DeleteBranch(ctx context.Context, branch string) error {
	return versioncontrolops.DeleteBranch(ctx, s.db, branch)
}

// Log returns recent commit history
func (s *DoltStore) Log(ctx context.Context, limit int) ([]CommitInfo, error) {
	return versioncontrolops.Log(ctx, s.db, limit)
}

// CommitInfo is an alias for storage.CommitInfo.
type CommitInfo = storage.CommitInfo

// HistoryEntry represents a row from dolt_history_* table
type HistoryEntry struct {
	CommitHash string
	Committer  string
	CommitDate time.Time
	// Issue data at that commit
	IssueData map[string]interface{}
}

// HasRemote checks if a Dolt remote with the given name exists.
func (s *DoltStore) HasRemote(ctx context.Context, name string) (bool, error) {
	var count int
	err := s.queryRowContext(ctx, func(row *sql.Row) error {
		return row.Scan(&count)
	}, "SELECT COUNT(*) FROM dolt_remotes WHERE name = ?", name)
	if err != nil {
		return false, fmt.Errorf("failed to check remote %s: %w", name, err)
	}
	return count > 0, nil
}

// AddRemote adds a Dolt remote
func (s *DoltStore) AddRemote(ctx context.Context, name, url string) error {
	_, err := s.db.ExecContext(ctx, "CALL DOLT_REMOTE('add', ?, ?)", name, url)
	if err != nil {
		return fmt.Errorf("failed to add remote %s: %w", name, err)
	}
	return nil
}

// Status returns the current Dolt status (staged/unstaged changes)
func (s *DoltStore) Status(ctx context.Context) (*DoltStatus, error) {
	return versioncontrolops.Status(ctx, s.db)
}

// DoltStatus is an alias for storage.Status.
type DoltStatus = storage.Status

// StatusEntry is an alias for storage.StatusEntry.
type StatusEntry = storage.StatusEntry
