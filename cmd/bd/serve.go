package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/contextinfo"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// serveCmdName is the command name, shared with the root command's post-run
// policy (runsPostCommandMaintenance) so the exclusion cannot drift from the
// command it names.
const serveCmdName = "serve"

// providerCloseTimeout bounds the shutdown close of a provider serve built
// itself. It is not the drain budget — the server has already drained by then.
const providerCloseTimeout = 10 * time.Second

var (
	serveAddr             string
	serveAllowNonLoopback bool
)

var serveCmd = &cobra.Command{
	Use:   serveCmdName,
	Short: "Serve the beads HTTP API over loopback",
	Long: `Serve the beads HTTP API — the same work surface the CLI answers, for
automation clients that would otherwise fork a bd subprocess per call.

The wire contract is described by an OpenAPI document (/v0); GET
/v0/beads/context reports which operations this build actually implements.

DEPLOYMENT

  Pass an explicit port. The default 127.0.0.1:0 takes an ephemeral one, which
  is right for ad-hoc and test use — where the bound address printed on stdout
  is read immediately — but carries no mutual exclusion: two serves against one
  workspace then run side by side on different ports with no way to enumerate
  them. On a fixed port the second one fails to bind, which is the intended
  behavior. Concurrent serves are data-safe either way; claims are arbitrated
  in the SQL server.

  Run it under a supervisor. bd shuts down gracefully on SIGHUP as well as
  SIGINT and SIGTERM, so closing the terminal of a foreground bd serve stops it.

PROBES

  GET /healthz is LIVENESS only: it answers from the process and never touches
  the database, so it stays green while the database is unreachable. For
  readiness use GET /v0/beads/ready?limit=1 — a real query, where 200 means
  ready and 503 means live but not ready.

WHAT THIS DOES NOT DO

  No authentication and no TLS. The trust model is the loopback boundary, which
  is the same one the database behind it already relies on. --allow-non-loopback
  extends the surface to every network peer that can reach the address; nothing
  else about the server changes.

  Hooks do not fire. A hook is a user-controlled subprocess per mutation: in a
  concurrent server that is an unbounded latency multiplier and an orphaned
  child at shutdown, and its working-directory-derived hook lookup is
  meaningless in a server process. A CLI claim runs on_update; an HTTP claim
  does not.

  The per-command auto-commit machinery does not run. Durability is per request:
  a successful claim commits inside its own transaction, exactly as a proxied
  CLI claim does today.

  An actor on an HTTP request is caller-asserted provenance for the audit trail,
  not authenticated identity — the same thing it has always been on the CLI,
  where any local process can pass any --actor.

  It does not run under --readonly, and refuses to start rather than binding.
  Every server it binds publishes the issue-claim operation, and the capability
  set it advertises is a property of the build rather than of the flags on the
  process that started it — so a read-only server would advertise a write it
  could never land.

DESTRUCTIVE OPERATIONS

  POST /v0/beads/issues:sweep deletes closed beads in bulk — the operation
  behind bd purge and bd prune — and nothing it deletes comes back. It shares
  the library surface those commands call, so it inherits their guards: pinned
  beads are never swept, and a durable sweep with neither a cutoff nor an id
  pattern is refused rather than clearing every closed bead in the workspace.
  Combined with the trust model above, that means anyone who can reach this
  address can erase closed work; bind it accordingly.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServe()
	},
}

func init() {
	registerServeFlags(serveCmd)
	rootCmd.AddCommand(serveCmd)
}

// registerServeFlags declares bd serve's own flags.
//
// It is a named function rather than a block in init so a test that runs the
// command in-process can put the flag set back the way it found it: cobra
// merges every inherited persistent flag into a command's own FlagSet the first
// time it parses one, and that mutation outlives the run.
func registerServeFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&serveAddr, "addr", "127.0.0.1:0",
		"Address to bind as IP:PORT; the host must be a numeric IP literal, and port 0 takes an ephemeral port")
	cmd.Flags().BoolVar(&serveAllowNonLoopback, "allow-non-loopback", false,
		"Permit a bind beyond loopback. bd serve has no authentication: every peer that can reach the address gets full read and claim access")
}

func runServe() error {
	// Flag validation first: it depends on nothing about the workspace, so the
	// refusal for a bad --addr is the same in every mode.
	if _, err := httpapi.ValidateBindAddr(serveAddr, serveAllowNonLoopback); err != nil {
		return HandleError("%v", err)
	}
	if readonlyMode {
		return HandleError("%v", errServeReadonly())
	}

	cwd, err := os.Getwd()
	if err != nil {
		return HandleError("cannot resolve working directory: %v", err)
	}
	info, err := contextinfo.NewContextProvider(cwd, Version).ContextUseCase().GetContextInfo(rootCtx)
	if err != nil {
		return HandleError("cannot resolve workspace context: %v", err)
	}

	// The classification reads the workspace's own configuration, so it runs
	// after the context resolves rather than before it. A directory with no
	// workspace at all therefore reports that it has none, instead of reporting
	// the embedded refusal for a workspace nobody found.
	db, err := serveDatabaseSource(info.BeadsDir)
	if err != nil {
		return HandleError("%v", err)
	}

	if serveAllowNonLoopback {
		fmt.Fprintf(os.Stderr,
			"bd serve: WARNING: --allow-non-loopback binds %s beyond loopback. "+
				"This API has no authentication and no TLS: any peer that can reach it can read every issue, claim work as any actor, "+
				"bulk-delete closed beads, and delete any bead it can name.\n",
			serveAddr)
	}

	if db.source == serveSourceStore {
		// Nothing to create. PersistentPreRunE already opened this workspace
		// through the same backends.Lookup dispatch every ordinary bd command
		// opens it with, so the store bd list and bd create answer from is the
		// store sitting in front of this server — one creation path, which is
		// the whole point of this arm. Opening a second handle here would
		// double the pools and self-conflict with any backend that takes an
		// exclusive workspace lock.
		//
		// Closing it is the root command's business too: PersistentPostRunE
		// runs after this function returns, which is after the server has
		// fully drained, so no request can reach a closed store. runServe must
		// not close it.
		roles, err := serveIssueRoles(store)
		if err != nil {
			return HandleError("bd serve: %v", err)
		}
		return serveListen(httpapi.Config{
			Addr:              serveAddr,
			AllowNonLoopback:  serveAllowNonLoopback,
			Reader:            roles.reader,
			Claimer:           roles.claimer,
			Settings:          roles.settings,
			Stats:             roles.stats,
			CycleDetector:     roles.cycles,
			EdgeReader:        roles.edges,
			BlockingAnnotator: roles.blocking,
			TreeWalker:        roles.tree,
			ReadyCounter:      roles.readyCounter,
			Querier:           roles.querier,
			Sweeper:           roles.sweeper,
			Deleter:           roles.deleter,
			BatchCreator:      roles.batchCreator,
			Memories:          roles.memories,
			Workspace:         info,
			SchemaVersion:     JSONSchemaVersion,
			Mode:              serveResolvedMode(info, db),
		})
	}

	provider := uowProvider
	if provider == nil {
		// Server, external-server and shared-server workspaces: PersistentPreRunE
		// builds a DoltStore for those and no unit-of-work provider, so serve
		// builds its own from the same connection settings the store used.
		//
		// On this arm that store stays open for the life of the process and
		// serve never touches it. It is not free either: its pool holds an idle
		// connection or two against the very server this process is about to
		// pool twenty more on. Worth knowing when sizing a shared Dolt server's
		// max_connections.
		topology, err := resolveServerModeUOWTopology(rootCtx, info.BeadsDir)
		if err != nil {
			return HandleError("bd serve: %v", err)
		}
		// GET /v0/beads/context is the one endpoint automation is told to trust
		// for this server's identity, and its Database comes from metadata.json,
		// which knows nothing about --global. Report the database the provider
		// actually opened, or the handshake names one database while every
		// operation answers from another.
		//
		// The store source needs no such override: backend.Open consumes the
		// same metadata.json GetContextInfo just read, so the handshake and the
		// operations share one source of truth by construction.
		info.Database = topology.database
		p, err := newSQLServerUOWProvider(rootCtx, info.BeadsDir, topology)
		if err != nil {
			return HandleError("bd serve: %v", err)
		}
		defer func() {
			// By the time this runs the signal context is already canceled, so a
			// close that inherited it could not do any of its work.
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(rootCtx), providerCloseTimeout)
			defer cancel()
			if err := p.Close(closeCtx); err != nil {
				fmt.Fprintf(os.Stderr, "bd serve: closing the unit-of-work provider: %v\n", err)
			}
		}()
		provider = p
	}

	return serveListen(httpapi.Config{
		Addr:             serveAddr,
		AllowNonLoopback: serveAllowNonLoopback,
		Provider:         provider,
		Workspace:        info,
		SchemaVersion:    JSONSchemaVersion,
		Mode:             serveResolvedMode(info, db),
	})
}

// serveListen binds and runs. It is where the two database sources converge:
// everything past the source is the same server.
//
// Graceful shutdown rides the signal context the root command already sets up
// (SIGINT/SIGTERM/SIGHUP). A proxied provider is closed where every proxied
// command closes it, in PersistentPostRunE — which in proxied mode does nothing
// else; the provider serve built for a server-mode workspace is closed by
// runServe's own defer, and a registered backend's store by the same
// PersistentPostRunE that opened it. None of those paths runs the auto-commit,
// export or push maintenance: proxied mode never had it, and serve is excluded
// from it by name (runsPostCommandMaintenance, cmd/bd/main.go).
func serveListen(cfg httpapi.Config) error {
	srv, err := httpapi.Listen(cfg)
	if err != nil {
		return HandleError("%v", err)
	}
	return srv.Serve(rootCtx)
}

// serveSource names which of httpapi.Config's two database sources a workspace
// is served from.
type serveSource int

const (
	// serveSourceProvider is the unit-of-work provider: one unit of work per
	// request, timed into that request's uow_ms and drawn from a pool bd serve
	// bounds itself. Every Dolt SQL-server topology takes it.
	serveSourceProvider serveSource = iota
	// serveSourceStore is the role set, taken off the store the root command
	// already opened. A registered backend's facade is a store rather than a
	// unit-of-work provider, so this is the source it has.
	serveSourceStore
)

// String names the source. It exists so a failed comparison prints "store" or
// "provider" rather than an integer.
func (s serveSource) String() string {
	if s == serveSourceStore {
		return "store"
	}
	return "provider"
}

// serveDatabase is the resolved answer to the one question bd serve asks about
// a workspace before it binds anything: where does this server read and claim
// from.
type serveDatabase struct {
	// source is which of httpapi.Config's two database sources to build.
	source serveSource
	// backend is the registered backend's name on the store source, and empty
	// on the provider source.
	backend string
}

// serveDatabaseSource classifies the workspace. It is both the mode gate and
// the wiring decision, in one function, so the two can never disagree about one
// workspace.
//
// THE REGISTRY IS CONSULTED FIRST, and that ordering is not a preference.
// PersistentPreRunE dispatches the store open on backends.Lookup before
// anything looks at Dolt mode, so a registered workspace opens its registered
// store even with BEADS_DOLT_SHARED_SERVER=1 exported. Resolving it the other
// way here would build a Dolt unit-of-work provider over a non-Dolt store and
// answer HTTP from a different database than the CLI reaches in the same
// directory. Registry-first is also what closes the !cgo corner, where
// isEmbeddedMode is a constant false and a registered workspace would otherwise
// be handed to the Dolt provider and fail with a misleading Dolt error — or
// connect to a defaulted host and serve the wrong database.
//
// EMBEDDED DOLT IS PERMANENT, and this is the only place that refusal lives.
// Its commit protocol runs outside the SQL transaction on a separate
// connection, so the per-request atomicity this server's contract states would
// be a lie there. That is a property of the backend rather than of what has
// been built so far, which is also why no unit-of-work provider for it exists
// or will. Nothing downstream will catch a bypass: internal/httpapi cannot see
// the backend behind a role, and every store publishes every role accessor
// whatever it is. TestServeNamesOneDatabaseSourcePerServerItBuilds pins that
// the roles are only ever reached through here.
func serveDatabaseSource(beadsDir string) (serveDatabase, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		// Never classify past an unreadable metadata.json. The classification's
		// default is the embedded refusal, so falling back would refuse a
		// workspace whose real backend nobody managed to read.
		return serveDatabase{}, fmt.Errorf("load %s: %w", configfile.ConfigPath(beadsDir), err)
	}
	if backend := normalizeLoadedConfig(cfg).GetBackend(); backends.Registered(backend) {
		return serveDatabase{source: serveSourceStore, backend: backend}, nil
	}
	if isEmbeddedMode() {
		return serveDatabase{}, errServeEmbedded()
	}
	return serveDatabase{source: serveSourceProvider}, nil
}

// errServeReadonly refuses `bd --readonly serve`.
//
// AHEAD OF THE WORKSPACE, deliberately: every server this command builds
// publishes the same operation set, claim included, so the answer cannot depend
// on which database source the workspace resolves to. Putting it here is also
// what makes it one answer rather than two — the two sources degraded
// differently, and both silently.
//
//   - On the STORE source the root command opens the workspace through
//     backend.OpenReadOnly and serve takes its claimer off that store. The
//     server bound, GET /v0/beads/context went on advertising `issues.claim`
//     (the capability set is derived from the route table and knows nothing
//     about a CLI flag), and every claim answered 500 with the issue left open.
//   - On the PROVIDER source serve builds its own unit-of-work provider from
//     the workspace's connection settings, which carries no read-only posture,
//     so `--readonly` bought the operator nothing and every claim landed.
//     (Proxied mode never got here: the root pre-run already refuses strict
//     readonly for it.)
//
// REFUSING RATHER THAN NARROWING THE SURFACE. Dropping `issues.claim` from a
// read-only server's advertised capabilities would be a wire change — that list
// is the documented pre-flight a client checks — and it would make one
// operation's presence depend on a flag on the process that happened to start
// the server, which no client can discover before connecting. bd already
// answers this question the same way one layer down, where a backend that
// cannot guarantee mutation-free access is turned away rather than opened
// anyway (backendSupportsStrictReadonly, cmd/bd/main.go).
//
// The value is read from the global rather than a flag lookup because
// `readonly` is also a config key, and PersistentPreRunE has already folded
// both into readonlyMode by the time any RunE runs.
func errServeReadonly() error {
	return errors.New("bd serve is unavailable under strict readonly (--readonly, or readonly in config): " +
		"every server it binds publishes the issue-claim operation, and refusing to start is the only honest " +
		"answer — a server that advertised a claim it could never land would be worse than no server")
}

// errServeEmbedded is the PERMANENT refusal. The message says what the
// workspace is and what serve needs, and promises nothing further: the reason
// is the embedded backend's commit protocol (see serveDatabaseSource), which no
// amount of provider or role plumbing changes.
//
// The mode belongs in the message, not in ErrUnsupported.Backend: that field is
// documented as a BACKEND name and is the embryo of the pluggable-backend error
// taxonomy, so putting a topology string in it would hand every downstream
// errors.As a mixed backend/mode vocabulary.
func errServeEmbedded() error {
	return fmt.Errorf("%w: bd serve requires a Dolt SQL server; this workspace uses embedded Dolt",
		&storage.ErrUnsupported{Op: "serve", Backend: "embedded-dolt"})
}

// serveIssueRoles takes the roles this server answers from off the store the
// root command already opened.
//
// ONE PEEL, and never storage.UnwrapStore. bd's chain is
// HookFiringStore -> InstrumentedStorage -> raw, and every decorator publishes
// its own roles — that is what the accessors are for — so store.IssueClaimer()
// returns a claimer that runs the workspace's on_update script for every claim
// it lands. This server documents that hooks do not fire, so the hook layer has
// to come off; the telemetry layer beneath it must not, or every request this
// process serves goes unspanned and untimed. httpapi.Listen refuses a
// hook-firing role rather than trusting this comment, so getting it wrong is a
// startup error rather than a silent subprocess per claim.
//
// The assertion is conditional because a BD_NO_HOOKS=1 workspace has no hook
// layer to peel.
//
// It returns the WHOLE set httpapi.Config requires; Listen refuses a partial
// set (see checkDatabaseSource), so a role missing here is a startup failure
// rather than a nil dereference on the first request that reaches it.
func serveIssueRoles(src storage.DoltStorage) (serveRoles, error) {
	var roles serveRoles
	if src == nil {
		// A set of nil roles would reach Listen as "no database source" —
		// true, and useless. Name the condition that actually happened.
		return roles, errors.New("no store is open for this workspace")
	}
	if hooked, ok := src.(*storage.HookFiringStore); ok {
		src = hooked.Unwrap()
	}

	// Each entry binds one Config field to the accessor that fills it, and
	// names itself in the failure.
	type binding struct {
		name string
		get  func() error
	}
	for _, b := range []binding{
		{"issue reader", func() (err error) { roles.reader, err = src.IssueReader(); return }},
		{"issue claimer", func() (err error) { roles.claimer, err = src.IssueClaimer(); return }},
		{"workspace config", func() (err error) { roles.settings, err = src.WorkspaceConfig(); return }},
		{"stats reporter", func() (err error) { roles.stats, err = src.StatsReporter(); return }},
		{"cycle detector", func() (err error) { roles.cycles, err = src.CycleDetector(); return }},
		{"edge reader", func() (err error) { roles.edges, err = src.EdgeReader(); return }},
		{"blocking annotator", func() (err error) { roles.blocking, err = src.BlockingAnnotator(); return }},
		{"tree walker", func() (err error) { roles.tree, err = src.TreeWalker(); return }},
		{"ready counter", func() (err error) { roles.readyCounter, err = src.ReadyCounter(); return }},
		{"querier", func() (err error) { roles.querier, err = src.Querier(); return }},
		{"sweeper", func() (err error) { roles.sweeper, err = src.Sweeper(); return }},
		{"deleter", func() (err error) { roles.deleter, err = src.Deleter(); return }},
		{"batch creator", func() (err error) { roles.batchCreator, err = src.BatchCreator(); return }},
		{"memories", func() (err error) { roles.memories, err = src.Memories(); return }},
	} {
		if err := b.get(); err != nil {
			return serveRoles{}, fmt.Errorf("%s: %w", b.name, err)
		}
	}
	return roles, nil
}

// serveRoles is the store-shaped database source, assembled once before Listen.
// It is deliberately NOT an httpapi.Config: the gate test in serve_test.go
// requires every httpapi.Config literal in this package to sit in a function
// that consulted serveDatabaseSource.
type serveRoles struct {
	reader       issueops.Reader
	claimer      issueops.Claimer
	settings     issueops.WorkspaceConfig
	stats        issueops.StatsReporter
	cycles       issueops.CycleDetector
	edges        issueops.EdgeReader
	blocking     issueops.BlockingAnnotator
	tree         issueops.TreeWalker
	readyCounter issueops.ReadyCounter
	querier      issueops.Querier
	sweeper      issueops.Sweeper
	deleter      issueops.Deleter
	batchCreator issueops.BatchCreator
	// memories is the one role here that is not an issueops role: the memory
	// plane is user data riding in the config table under its own merge class,
	// so it has its own leaf package.
	memories memoryops.Memories
}

// serveResolvedMode labels the topology for the startup log line. Cosmetic —
// nothing dispatches on it — but the managed/external distinction is worth
// naming: an external dolt sql-server shares its max_connections budget with
// every other bd process pointed at it, and this server's pool is a claim on
// that budget.
//
// A registered backend is named instead of being given a Dolt mode, which it
// no longer has: GetContextInfo projects the Dolt-derived identity only for the
// Dolt backend (internal/storage/domain/context.go), so info.DoltMode is empty
// here and " (external dolt)" would read as a bare parenthetical about a
// topology this workspace is not on.
func serveResolvedMode(info domain.ContextInfo, db serveDatabase) string {
	if db.source == serveSourceStore {
		return db.backend + " (registered backend)"
	}
	if !usesProxiedServer() {
		// Server, external-server and shared-server: serve fronts the running
		// dolt sql-server rather than starting one, so from this process the
		// server is external even when Beads is what started it.
		return info.DoltMode + " (external dolt)"
	}
	client, err := configfile.LoadProxiedServerClientInfo(info.BeadsDir)
	if err == nil && client != nil && client.External != nil {
		return info.DoltMode + " (external dolt)"
	}
	return info.DoltMode + " (managed dolt)"
}
