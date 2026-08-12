package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// doltVersionWarnOnce ensures the dolt-version advisory (old/unverifiable
// version) is printed at most once per process, even though
// resolveAndProbeDolt can run repeatedly against the same resolved binary
// (retries, multiple UOW providers opened in one command, or bd init
// --proxied-server's own preflight followed by the UOW provider it opens a
// few lines later). Repeating the same advisory on every call would just be
// noise once the operator has seen it. Cross-process dedup (each bd
// invocation is a new process) is handled separately by the probe cache's
// warn stamp — see doltversion.ProbeWithPolicyCached / MarkWarned.
var doltVersionWarnOnce sync.Once

// doltProbeCachePath is where the cross-process dolt probe cache lives: the
// binary being probed is machine-scoped, so the cache is too (user cache
// dir, not the workspace). Empty on any error, which makes the cached probe
// fall back to uncached behavior — never a reason to fail a command.
func doltProbeCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "beads", "dolt-probe.json")
}

// resolveAndProbeDolt resolves the external dolt binary (env override >
// sidecar > PATH) and hardened-probes it, printing the version-recommendation
// advisory (if any) at most once per process via doltVersionWarnOnce.
//
// quiet suppresses the advisory (errors still return). It is a parameter
// rather than a read of the global quietFlag because bd init defines its own
// local --quiet flag that shadows the persistent one: during
// `bd init --proxied-server -q` the global quietFlag is false while the
// user very much asked for quiet.
//
// bd init --proxied-server's own preflight (init_proxied_server.go) and
// newManagedProxiedServerUOWProvider both need exactly this
// resolve+probe+warn sequence. A single non-quiet `bd init --proxied-server`
// invocation runs the preflight and then opens a UOW provider a few lines
// later, so before this was shared, an old/unverifiable dolt printed the
// identical advisory twice and forked `dolt version` twice. errPrefix is
// used to keep call-site-specific error context (e.g. "bd init
// --proxied-server" vs "newProxiedServerUOWProvider") in the wrapped error.
// The probed identity is deliberately not returned: its only consumer (the
// auto_gc_behavior.archive_level gate) was removed upstream from
// proxied-server mode, and PR-2's revalidation consumer will re-widen the
// signature when it lands.
func resolveAndProbeDolt(ctx context.Context, errPrefix string, quiet bool) (doltBin string, err error) {
	doltBin, doltSrc, err := doltversion.Resolve(doltversion.ResolveOptions{
		EnvValue: doltversion.ReadEnvOverride(),
		// SidecarValue is a hook point only in this PR — the clone-local
		// sidecar setting itself lands in PR-2.
		SidecarValue: "",
	})
	if err != nil {
		return "", fmt.Errorf(
			"%s: resolving dolt binary (source: %s): %w; install from https://docs.dolthub.com/introduction/installation",
			errPrefix, doltSrc, err,
		)
	}
	// Cached: a fingerprint hit (same real path, size, mtime as the last
	// successful probe) replays the result for the cost of a stat instead of
	// forking `dolt version` on every command — this call sits on the
	// store-open hot path of essentially every bd command in managed
	// proxied-server mode.
	cachePath := doltProbeCachePath()
	res, err := doltversion.ProbeWithPolicyCached(ctx, doltBin, cachePath, time.Now())
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// The probe was killed because OUR context was canceled (user
			// hit Ctrl-C, outer operation gave up) — dolt itself was never
			// shown to be broken, so the install hint would mislead.
			return doltBin, fmt.Errorf(
				"%s: probing dolt binary %q (source: %s): %w",
				errPrefix, doltBin, doltSrc, err,
			)
		}
		return doltBin, fmt.Errorf(
			"%s: probing dolt binary %q (source: %s): %w; install from https://docs.dolthub.com/introduction/installation",
			errPrefix, doltBin, doltSrc, err,
		)
	}
	// Gated on both quietFlag and jsonOutput, matching this package's
	// convention of keeping JSON-mode stdout/stderr free of advisory chatter
	// (see e.g. tips.go, metrics.go). WarnDue adds the cross-process gate:
	// the advisory repeats at most once per day (per the probe cache's
	// stamp), not on every bd invocation.
	if res.Warning != nil && !quiet && !jsonOutput && res.WarnDue {
		doltVersionWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", res.Warning.Message())
			doltversion.MarkWarned(cachePath, time.Now())
		})
	}
	return doltBin, nil
}

// sqlServerUOWTopology is everything the two unit-of-work providers need that
// differs between workspaces: which database, whose schema, where the proxy
// listens, and — when the Dolt SQL server is not one Beads starts itself —
// how to reach it.
//
// It exists so the two ways a workspace can describe its SQL server (the
// proxied-server sidecar, and server mode's Dolt connection settings) meet in
// one shape before anything is constructed. The construction below is then
// identical for both, which is the point: a proxied workspace and a server-mode
// workspace differ in where the answer is written down, not in what is built.
type sqlServerUOWTopology struct {
	database   string
	teamServer bool
	// expectedProjectID is the workspace identity a team-server open asserts
	// against the shared database. Empty means nothing to assert: either the
	// caller adopts (see newProxiedServerUOWProviderAdopting), or the workspace
	// is not team-server managed. Server mode never sets teamServer, so it
	// leaves this empty too.
	expectedProjectID string
	proxyPort         int
	proxyIdle         time.Duration
	external          *configfile.ExternalDoltConfig
	rootPassword      string
}

// previewProviderOptions is the CLI-side half of the preview policy: it turns
// the root pre-run's previewMode bool into the uow.ProviderOption slice the
// proxied-server provider is opened with. Extracted (rather than inlined at
// the call site) so the wiring — preview=true must produce uow.WithPreview(),
// preview=false must produce nothing — has something to unit test; the
// previous inline form had no test that would fail if a refactor dropped it.
func previewProviderOptions(preview bool) []uow.ProviderOption {
	if !preview {
		return nil
	}
	return []uow.ProviderOption{uow.WithPreview()}
}

// newProxiedServerUOWProvider opens the proxied-server provider and, in
// team-server mode, asserts that the shared bts-managed database is serving
// THIS workspace's project (gastownhall/beads: the proxied-path sibling of the
// gateway's DoltStore.verifyProjectIdentity guard).
//
// opts carry the open's posture that is not workspace topology — today only
// uow.WithPreview(), which the root pre-run passes for --dry-run/--inspect so
// a proxied preview does not create or migrate the database before the
// command's own RunE runs.
func newProxiedServerUOWProvider(ctx context.Context, beadsDir, databaseOverride string, opts ...uow.ProviderOption) (uow.UnitOfWorkProvider, error) {
	return openProxiedServerUOWProvider(ctx, beadsDir, databaseOverride, assertWorkspaceIdentity, opts...)
}

// newProxiedServerUOWProviderAdopting skips that assertion. Only two callers
// legitimately have no workspace identity to assert: `bd init --team-server`,
// which ADOPTS the identity the shared database already carries (asserting the
// locally-minted placeholder would reject every correct init), and server-wide
// database maintenance, which is not scoped to one project's database.
func newProxiedServerUOWProviderAdopting(ctx context.Context, beadsDir, databaseOverride string, opts ...uow.ProviderOption) (uow.UnitOfWorkProvider, error) {
	return openProxiedServerUOWProvider(ctx, beadsDir, databaseOverride, adoptWorkspaceIdentity, opts...)
}

// identityPosture selects whether a proxied open asserts the workspace's
// project identity against the database or adopts whatever it finds.
type identityPosture bool

const (
	assertWorkspaceIdentity identityPosture = false
	adoptWorkspaceIdentity  identityPosture = true
)

func openProxiedServerUOWProvider(ctx context.Context, beadsDir, databaseOverride string, posture identityPosture, opts ...uow.ProviderOption) (uow.UnitOfWorkProvider, error) {
	if beadsDir == "" {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: beadsDir must be set")
	}
	topology, err := resolveProxiedServerUOWTopology(beadsDir, databaseOverride, posture)
	if err != nil {
		return nil, err
	}
	return newSQLServerUOWProvider(ctx, beadsDir, topology, opts...)
}

// newSQLServerUOWProvider is the single funnel every unit-of-work provider bd
// builds passes through — the proxied CLI path AND `bd serve`'s own provider
// for server-mode workspaces. Activation is applied by the two constructors it
// dispatches to, each alongside its own open; doing it at the CALL SITES
// instead is what left `bd serve` writing mutations into an empty journal while
// reporting success. See the note at the top of events_journal.go.
func newSQLServerUOWProvider(ctx context.Context, beadsDir string, topology sqlServerUOWTopology, opts ...uow.ProviderOption) (uow.UnitOfWorkProvider, error) {
	if topology.external != nil {
		return newExternalProxiedServerUOWProvider(ctx, beadsDir, topology, opts...)
	}
	return newManagedProxiedServerUOWProvider(ctx, beadsDir, topology, opts...)
}

// resolveProxiedServerUOWTopology reads a proxied-server workspace's topology
// out of metadata.json and the proxied-server sidecar. An ABSENT file is not
// fatal — both loads return (nil, nil) then, the defaults apply, and the
// provider construction below is where a workspace that cannot be reached
// actually fails. A file that EXISTS but cannot be read or parsed IS fatal
// (restores f880a985b, reverted in #4418; bd-aj3g5): swallowing it silently
// falls back to a fresh managed local database — reads return zero issues and
// writes land in the wrong database (split-brain) — and the identity assertion
// below silently degrades to no assertion at all.
func resolveProxiedServerUOWTopology(beadsDir, databaseOverride string, posture identityPosture) (sqlServerUOWTopology, error) {
	persisted, err := configfile.Load(beadsDir)
	if err != nil {
		return sqlServerUOWTopology{}, fmt.Errorf(
			"corrupt workspace config %s: %w — refusing to fall back to a fresh database; repair or remove the file to proceed",
			configfile.ConfigPath(beadsDir), err)
	}
	topology := sqlServerUOWTopology{database: configfile.DefaultDoltDatabase}
	if persisted != nil {
		topology.database = persisted.GetDoltDatabase()
		topology.teamServer = persisted.IsTeamServerManaged()
		if posture == assertWorkspaceIdentity {
			topology.expectedProjectID = persisted.ProjectID
			// An empty local identity would reach checkTeamServerIdentity as
			// "nothing to assert" — indistinguishable from the adoption path,
			// which would silently disable the guard. A team-server workspace
			// always has an adopted ProjectID after a successful init, so an
			// empty one here is a broken workspace, not a legacy one.
			if topology.teamServer && topology.expectedProjectID == "" {
				return sqlServerUOWTopology{}, fmt.Errorf(
					"newProxiedServerUOWProvider: this team-server workspace has no project identity in %s; re-run 'bd init --team-server' to adopt the identity provisioned in the shared database (it never writes to that database)",
					configfile.ConfigFileName)
			}
		}
	}
	if databaseOverride != "" {
		topology.database = databaseOverride
	}

	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return sqlServerUOWTopology{}, fmt.Errorf(
			"corrupt proxied-server sidecar %s: %w — refusing to fall back to a fresh database; repair or remove the file to proceed",
			configfile.ProxiedServerClientInfoPath(beadsDir), err)
	}
	if info != nil {
		topology.proxyPort = info.Port
		topology.proxyIdle = info.IdleTimeout
		if info.External != nil {
			topology.external = info.External
			topology.rootPassword = os.Getenv(configfile.ExternalDoltPasswordEnvVar)
		}
	}
	return topology, nil
}

// serverModeSocketProbeTimeout is how long the socket-first / TCP-fallback
// probe waits, matching the store open's budget so both consumers make the same
// call on the same workspace.
const serverModeSocketProbeTimeout = 500 * time.Millisecond

type socketTransportResolver func(socket, host string, port int, timeout time.Duration) string

// errServeGatewayCredential refuses a workspace that authenticates to Dolt with
// a minted credential.
//
// The command mints a SHORT-LIVED token that is presented as the connection
// username. A command resolves it once and exits; a server would pin that token
// for its whole lifetime and start failing authentication at an hour nobody is
// watching. Refuse while the workspace still says so.
//
// It is deliberately a plain error, not a storage.ErrUnsupported like the
// embedded refusal: that type names a BACKEND, and the backend here is plain
// dolt, which bd serve supports. Typing this as "serve is unsupported on dolt"
// would be a worse lie than an untyped error.
func errServeGatewayCredential() error {
	return fmt.Errorf(
		"this workspace authenticates to Dolt with a credential command (BEADS_DOLT_CREDENTIAL_COMMAND), " +
			"which mints a short-lived token; bd serve holds one connection identity for the life of the " +
			"process and cannot refresh it")
}

// resolveServerModeUOWTopology reads the topology for a workspace whose Dolt
// SQL server Beads does not front with a proxy of its own: `bd init --server`
// (whether the server is one Beads auto-starts or one the operator runs), and
// shared-server mode.
//
// Such a workspace has no proxied-server sidecar to read the topology from, so
// it comes from the same Dolt connection settings the CLI's store open uses —
// resolveDoltServerConnection, so the HTTP server and a CLI command in this
// workspace reach the same server as the same identity. The one corner where
// that guarantee is narrower than it sounds is a workspace with no
// metadata.json, where main.go resolves nothing and leaves the connection to
// dolt.applyResolvedConfig; the ladders agree, but they are not the same call.
//
// The server is ALWAYS described as external, including when Beads started it:
// the local-server provider would spawn a second `dolt sql-server` on the data
// directory the first one already holds an exclusive write lock on, which Dolt
// refuses outright ("database is locked by another dolt process"). One server,
// fronted — never a second one.
//
// The caller is responsible for reporting topology.database as the served
// workspace's database: it is the one place --global is applied, and the
// handshake must not name a database the provider did not open.
func resolveServerModeUOWTopology(ctx context.Context, beadsDir string) (sqlServerUOWTopology, error) {
	return resolveServerModeUOWTopologyWithTransportResolver(ctx, beadsDir, dolt.ResolveSocketTransport)
}

func resolveServerModeUOWTopologyWithTransportResolver(ctx context.Context, beadsDir string, resolveTransport socketTransportResolver) (sqlServerUOWTopology, error) {
	if beadsDir == "" {
		return sqlServerUOWTopology{}, fmt.Errorf("resolveServerModeUOWTopology: beadsDir must be set")
	}
	fileCfg, err := configfile.Load(beadsDir)
	if err != nil {
		return sqlServerUOWTopology{}, fmt.Errorf("load %s: %w", configfile.ConfigPath(beadsDir), err)
	}
	// The database is resolved here rather than from the connection because the
	// no-metadata.json corner below has to answer it differently.
	database := configfile.DefaultDoltDatabase
	if fileCfg == nil {
		// No metadata.json at all. The store open in this same process takes the
		// defaults and carries on (shared-server mode reaches exactly that), so
		// refusing here would refuse a workspace the CLI just opened — and it
		// takes the LITERAL default database name (main.go's cfg == nil branch),
		// not GetDoltDatabase, so BEADS_DOLT_SERVER_DATABASE is ignored there.
		// Matching that is the point: honoring the env var on this side alone
		// would have one process serving two different databases over HTTP and
		// over the CLI, silently. The env var not reaching the CLI's branch is a
		// real gap, but it is that branch's to close.
		fileCfg = configfile.DefaultConfig()
	} else {
		database = fileCfg.GetDoltDatabase()
	}

	// Decide the gateway refusal from the CONFIGURATION, before anything runs
	// the operator's command. resolveDoltServerConnection would otherwise
	// execute it — spawning a subprocess, minting a token, and spending
	// whatever the credential endpoint rate-limits or audits — only for the
	// refusal below to throw the result away. PersistentPreRunE already ran it
	// once for the DoltStore serve never touches; a refused `bd serve` must not
	// make that twice.
	if fileCfg.GetDoltCredentialCommand() != "" {
		return sqlServerUOWTopology{}, errServeGatewayCredential()
	}

	conn := &dolt.Config{BeadsDir: beadsDir, ServerMode: true}
	if err := resolveDoltServerConnection(ctx, beadsDir, fileCfg, conn); err != nil {
		return sqlServerUOWTopology{}, err
	}
	if conn.Gateway {
		// Unreachable while a configured command is the only thing that can set
		// Gateway (dolt.ApplyGatewayCredential is its sole writer, and the check
		// above already refused that case). It stays because this is a
		// fail-closed boundary: if the credential ladder ever grows a second
		// source, the refusal must not quietly stop applying.
		return sqlServerUOWTopology{}, errServeGatewayCredential()
	}

	external := &configfile.ExternalDoltConfig{User: conn.ServerUser}
	// Socket-first / TCP-fallback, the same policy dolt's own server-mode open
	// applies (dolt.ResolveSocketTransport): a configured socket that is not
	// currently connectable must not block a workspace whose server answers over
	// TCP. Without this, serve fails to construct in a workspace where every CLI
	// command succeeds.
	socket := resolveTransport(conn.ServerSocket, conn.ServerHost, conn.ServerPort, serverModeSocketProbeTimeout)
	switch {
	case socket != "":
		external.Socket = socket
	case conn.ServerPort > 0:
		external.Host = conn.ServerHost
		external.Port = conn.ServerPort
	default:
		// Port 0 means no source resolved one — for a Beads-managed server that
		// is the case where nothing has started it yet, and the port file it
		// would write does not exist.
		return sqlServerUOWTopology{}, fmt.Errorf(
			"cannot determine the Dolt server port for %s; start the server with `bd dolt start`, "+
				"or set the port explicitly (BEADS_DOLT_SERVER_PORT or dolt.port in config.yaml)", beadsDir)
	}
	external.TLSRequired = conn.ServerTLS

	// --global selects the shared-server global database, exactly as it does for
	// the store the CLI opens (main.go rejects the flag outside shared-server
	// mode); without this the HTTP surface would answer from the project
	// database while the CLI in the same process answered from the global one.
	if globalFlag && doltserver.IsSharedServerMode() {
		database = doltserver.GlobalDatabaseName
	}

	return sqlServerUOWTopology{
		database: database,
		external: external,
		// The proxy this topology builds must outlive every quiet period the
		// process it serves can have, so it gets no idle timeout at all.
		//
		// The 30s default cannot work here. The only client this proxy will
		// ever have is bd serve, whose pool releases its last connection after
		// ConnMaxIdleTime (5m) of no requests; a finite-idle proxy then sees
		// zero clients and exits, taking with it the OS-assigned port the
		// provider's DSN pinned at construction. Nothing re-resolves that
		// endpoint — GetCreateDatabaseProxyServerEndpoint runs once, above — so
		// serve would go on answering /healthz with no database left to answer
		// anything else from, unrecoverable without a restart.
		//
		// The tradeoff is a child that outlives serve, and it is deliberate: a
		// never-idle proxy is already a supported configuration
		// (`bd init --proxied-server --proxied-server-idle-timeout 0`), it is
		// reused rather than duplicated by the next serve, and reaping it here
		// would be worse — the endpoint API reports neither whether this
		// process spawned the proxy nor whether another serve has since adopted
		// it, so a shutdown reap could take the database out from under a
		// concurrent serve. `bd dolt stop` does not reap it in these modes.
		proxyIdle:    proxy.IdleTimeoutNever,
		rootPassword: conn.ServerPassword,
	}, nil
}

// newExternalProxiedServerUOWProvider fronts a Dolt SQL server this process
// neither started nor owns. "Proxied" in the name is the PROXY it builds, not
// the workspace mode: since bd-emv a server-mode workspace lands here too, and
// the paths it resolves (root, log) are the same ones proxied mode uses because
// both modes root their server at the same directory.
func newExternalProxiedServerUOWProvider(ctx context.Context, beadsDir string, topology sqlServerUOWTopology, opts ...uow.ProviderOption) (p uow.UnitOfWorkProvider, err error) {
	defer func() { p, err = activateEventsJournalProvider(ctx, beadsDir, p, err) }()
	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := validateProxiedServerRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	logPath, isCustomLog, err := resolveProxiedServerLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := validateProxiedServerLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}

	if err := os.MkdirAll(rootPath, config.BeadsDirPerm); err != nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: mkdir %s: %w", rootPath, err)
	}

	return uow.NewExternalDoltServerUOWProvider(
		ctx,
		rootPath,
		topology.database,
		logPath,
		*topology.external,
		topology.external.ResolvedUser(),
		topology.rootPassword,
		topology.proxyPort,
		topology.proxyIdle,
		topology.teamServer,
		topology.expectedProjectID,
		opts...,
	)
}

func newManagedProxiedServerUOWProvider(ctx context.Context, beadsDir string, topology sqlServerUOWTopology, opts ...uow.ProviderOption) (p uow.UnitOfWorkProvider, err error) {
	defer func() { p, err = activateEventsJournalProvider(ctx, beadsDir, p, err) }()
	// Resolve and hardened-probe the external dolt binary before spawning
	// it: an env/sidecar override that is explicitly named but broken
	// should fail loudly here rather than surface as a confusing spawn
	// failure downstream. See internal/doltversion for the resolution
	// precedence (env > sidecar > PATH) and probe hardening (timeout,
	// output cap, pre-exec validation).
	//
	// The error label names this function rather than the proxied-server
	// entry point: since upstream's server-mode path (bd serve ->
	// newSQLServerUOWProvider) also lands here, a hardcoded
	// "newProxiedServerUOWProvider" would mislabel a `bd serve` failure as a
	// proxied-server one.
	doltBin, err := resolveAndProbeDolt(ctx, "newManagedProxiedServerUOWProvider", quietFlag)
	if err != nil {
		return nil, err
	}

	rootPath, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve root path: %w", err)
	}
	if err := validateProxiedServerRootPath(rootPath); err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server root (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
	}

	configPath, err := ensureProxiedServerConfig(beadsDir)
	if err != nil {
		return nil, err
	}

	logPath, isCustomLog, err := resolveProxiedServerLogPath(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("newProxiedServerUOWProvider: resolve log path: %w", err)
	}
	if isCustomLog {
		if err := validateProxiedServerLogPath(logPath); err != nil {
			return nil, fmt.Errorf("newProxiedServerUOWProvider: proxied server log (from env or %s): %w", configfile.ProxiedServerClientInfoFileName, err)
		}
	}

	return uow.NewDoltServerUOWProvider(
		ctx,
		rootPath,
		topology.database,
		logPath,
		configPath,
		proxy.BackendLocalServer,
		"root",
		"", // proxy is loopback-only, no auth
		doltBin,
		topology.proxyPort,
		topology.proxyIdle,
		topology.teamServer,
		topology.expectedProjectID,
		opts...,
	)
}
