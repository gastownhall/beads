package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/proxiedopen"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// resolveAndProbeDolt is proxiedopen.ResolveAndProbeDolt with the CLI's
// advisory gate: quiet, or --json, suppresses the unverifiable-version
// advisory (errors still return).
//
// quiet is a parameter rather than a read of the global quietFlag because bd
// init defines its own local --quiet flag that shadows the persistent one:
// during `bd init --proxied-server -q` the global quietFlag is false while the
// user very much asked for quiet.
func resolveAndProbeDolt(ctx context.Context, errPrefix string, quiet bool) (string, error) {
	return proxiedopen.ResolveAndProbeDolt(ctx, errPrefix, cliAdvisory(quiet))
}

// cliAdvisory is where the proxied open prints advisories: stderr, unless
// quiet or --json, matching this package's convention of keeping JSON-mode
// stdout/stderr free of advisory chatter (see e.g. tips.go, metrics.go).
func cliAdvisory(quiet bool) io.Writer {
	if quiet || jsonOutput {
		return nil
	}
	return os.Stderr
}

// sqlServerUOWTopology is proxiedopen.Topology under the name the CLI's
// server-mode resolver and bd serve use.
type sqlServerUOWTopology = proxiedopen.Topology

// rootProviderOptions is the CLI-side half of the open-posture policy: it
// turns the root pre-run's classifications into the uow.ProviderOption slice
// the proxied-server provider is opened with. Extracted (rather than inlined
// at the call site) so the wiring has something to unit test; the previous
// inline form had no test that would fail if a refactor dropped it.
//
// They are not exclusive but preview is stronger, and passing several would
// say different things about an open that can only have one posture: preview
// neither creates nor migrates, while readOnly opens normally and only changes
// how a refused migration is handled. Preview wins, then readOnly. reconcile
// (bd dolt commit) changes nothing about the open; it only tells events-journal
// activation that the command writes no beads (uow.OpensForBeadWrites).
func rootProviderOptions(preview, readOnly, reconcile bool) []uow.ProviderOption {
	switch {
	case preview:
		return []uow.ProviderOption{uow.WithPreview()}
	case readOnly:
		return []uow.ProviderOption{uow.WithReadOnly()}
	case reconcile:
		return []uow.ProviderOption{uow.WithWorkingSetReconcile()}
	default:
		return nil
	}
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
	return proxiedopen.Open(ctx, beadsDir, cliProxiedOptions(databaseOverride, proxiedopen.AssertWorkspaceIdentity, proxiedopen.RefuseFreshStore, opts))
}

// newProxiedServerUOWProviderAdopting skips that assertion. Two callers
// legitimately have no workspace identity to assert:
//
//   - server-wide database maintenance, which is not scoped to one project's
//     database;
//   - withQuiescedProxiedProvider (backup_proxied_server.go), the post-restore
//     reopen. A restore is precisely the operation after which the workspace's
//     recorded project id and the database's may legitimately differ, and it is
//     the connection whose job is to reconcile them — asserting the pre-restore
//     identity would refuse the one open that can fix the mismatch. The full
//     reasoning is at that function's doc comment.
//
// Neither may create the store, so both keep the fresh-store refusal
// (proxiedFreshStoreRefusal). Maintenance has nothing to create. A restore
// into a deleted root is refused (by the root pre-run's open, before it gets
// here), and the refusal names the way back: `bd init --proxied-server`,
// then `bd backup restore --force <dir>`. Letting the restore's opens create
// the store instead would leave a minted empty database behind whenever the
// restore failed. `bd init --proxied-server` adopts the identity too,
// through newProxiedServerUOWProviderInitializing, the one constructor that
// may also CREATE the store.
//
// Anything else reaching this constructor is bypassing an identity assertion
// that exists to stop a workspace writing into another project's database. Add
// a third entry here, with its reason, or use newProxiedServerUOWProvider.
func newProxiedServerUOWProviderAdopting(ctx context.Context, beadsDir, databaseOverride string, opts ...uow.ProviderOption) (uow.UnitOfWorkProvider, error) {
	return proxiedopen.Open(ctx, beadsDir, cliProxiedOptions(databaseOverride, proxiedopen.AdoptWorkspaceIdentity, proxiedopen.RefuseFreshStore, opts))
}

// newProxiedServerUOWProviderInitializing is `bd init --proxied-server`'s
// open. It ADOPTS the identity the database carries (asserting the
// locally-minted placeholder would reject every correct init, and
// --team-server adopts the identity the shared database already holds), and
// it is allowed to create the managed-local store, because creating it is
// what init is for. Every other open refuses an empty root
// (proxiedFreshStoreRefusal); this is the only constructor that does not, so
// a new caller here is a new way to mint an empty database under a
// workspace's proxied-server metadata — name its reason.
func newProxiedServerUOWProviderInitializing(ctx context.Context, beadsDir, databaseOverride string, opts ...uow.ProviderOption) (uow.UnitOfWorkProvider, error) {
	return proxiedopen.Open(ctx, beadsDir, cliProxiedOptions(databaseOverride, proxiedopen.AdoptWorkspaceIdentity, proxiedopen.AllowFreshStore, opts))
}

// cliProxiedOptions is the CLI's proxiedopen.Options: the posture its caller
// names, and the advisory gate the root command's --quiet/--json select.
func cliProxiedOptions(databaseOverride string, identity proxiedopen.IdentityPosture, fresh proxiedopen.FreshStorePosture, opts []uow.ProviderOption) proxiedopen.Options {
	return proxiedopen.Options{
		DatabaseOverride: databaseOverride,
		Identity:         identity,
		FreshStore:       fresh,
		ProviderOptions:  opts,
		Advisory:         cliAdvisory(quietFlag),
	}
}

// identity and fresh-store postures, under the names this package's tests use.
const (
	assertWorkspaceIdentity = proxiedopen.AssertWorkspaceIdentity
	adoptWorkspaceIdentity  = proxiedopen.AdoptWorkspaceIdentity
)

// proxiedProviderOptions is proxiedopen.ProxiedProviderOptions; see
// TestProxiedProviderOptions.
func proxiedProviderOptions(opts []uow.ProviderOption) []uow.ProviderOption {
	return proxiedopen.ProxiedProviderOptions(opts)
}

// serverLogProviderOptions is proxiedopen.ServerLogProviderOptions.
func serverLogProviderOptions(opts []uow.ProviderOption, isCustomLog bool) []uow.ProviderOption {
	return proxiedopen.ServerLogProviderOptions(opts, isCustomLog)
}

// newSQLServerUOWProvider is the single funnel every unit-of-work provider bd
// builds passes through — the proxied CLI path AND `bd serve`'s own provider
// for server-mode workspaces (proxiedopen.OpenTopology). Activation is applied
// there, by the two constructors it dispatches to, each alongside its own
// open; doing it at the CALL SITES instead is what left `bd serve` writing
// mutations into an empty journal while reporting success. See the note at
// the top of events_journal.go.
func newSQLServerUOWProvider(ctx context.Context, beadsDir string, topology sqlServerUOWTopology, opts ...uow.ProviderOption) (uow.UnitOfWorkProvider, error) {
	return proxiedopen.OpenTopology(ctx, beadsDir, topology, proxiedopen.Options{ProviderOptions: opts, Advisory: cliAdvisory(quietFlag)})
}

// newExternalProxiedServerUOWProvider opens an EXTERNAL topology through the
// same funnel; tests use it to reach the external constructor directly.
func newExternalProxiedServerUOWProvider(ctx context.Context, beadsDir string, topology sqlServerUOWTopology, opts ...uow.ProviderOption) (uow.UnitOfWorkProvider, error) {
	if topology.External == nil {
		return nil, fmt.Errorf("newExternalProxiedServerUOWProvider: topology has no external server")
	}
	return newSQLServerUOWProvider(ctx, beadsDir, topology, opts...)
}

// resolveProxiedServerUOWTopology is proxiedopen.ResolveProxiedTopology.
func resolveProxiedServerUOWTopology(beadsDir, databaseOverride string, posture proxiedopen.IdentityPosture) (sqlServerUOWTopology, error) {
	return proxiedopen.ResolveProxiedTopology(beadsDir, databaseOverride, posture)
}

// proxiedFreshStoreRefusal is proxiedopen.FreshStoreRefusal: a managed-local
// open whose resolved root holds no Dolt database is refused rather than
// minting an empty one. init_proxied_server.go and the root pre-run's
// recovery hints call it ahead of the open.
func proxiedFreshStoreRefusal(beadsDir string, topology sqlServerUOWTopology) error {
	return proxiedopen.FreshStoreRefusal(beadsDir, topology)
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
// The caller is responsible for reporting topology.Database as the served
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
		Database: database,
		External: external,
		// The proxy this topology builds must outlive every quiet period the
		// process it serves can have, so it gets no idle timeout at all.
		//
		// The 30s default does not suit this proxy. The only client it will
		// ever have is bd serve, whose pool releases its last connection after
		// ConnMaxIdleTime (20s, servePoolLimits) of no requests; a finite-idle
		// proxy then sees zero clients and exits with its OS-assigned port.
		// The provider recovers from that (#7295): every new physical
		// connection rediscovers the proxy, spawning a replacement on a
		// possibly different port. But each quiet period would then cost the
		// next request a proxy and backend restart, so this topology keeps one
		// proxy for its whole life instead.
		//
		// The tradeoff is a child that outlives serve, and it is deliberate: a
		// never-idle proxy is already a supported configuration
		// (`bd init --proxied-server --proxied-server-idle-timeout 0`), it is
		// reused rather than duplicated by the next serve, and reaping it here
		// would be worse — the endpoint API reports neither whether this
		// process spawned the proxy nor whether another serve has since adopted
		// it, so a shutdown reap could take the database out from under a
		// concurrent serve. `bd dolt stop` does not reap it in these modes.
		ProxyIdle:    proxy.IdleTimeoutNever,
		RootPassword: conn.ServerPassword,
	}, nil
}
