package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/contextinfo"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
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

// serveTokenFileEnv is the environment fallback for --auth-token-file, which an
// operator would otherwise have to thread through a container spec. It follows
// the BEADS_* convention the rest of the binary uses, and applies ONLY when the
// flag was not passed.
//
// It is read here and nowhere else. internal/httpapi never reads the
// environment, so the library, its tests and an embedded caller cannot be
// reconfigured by an exported variable in somebody's shell.
const (
	// #nosec G101 -- this is the NAME of an environment variable that holds a
	// file PATH. No credential appears in this source file, and none may: a
	// token reaches the process only by being read out of that file.
	serveTokenFileEnv = "BEADS_SERVE_TOKEN_FILE"
)

var (
	serveAddr             string
	serveAllowNonLoopback bool
	serveAuthTokenFile    string
	serveInsecureNoAuth   bool
	serveAllowedHosts     []string
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

AUTHENTICATION

  Optional, and off by default on loopback — where the trust model is the
  loopback boundary itself, the same one the database behind it already relies
  on. --auth-token-file turns it on: every operation except GET /healthz then
  requires an "Authorization: Bearer <token>" header, GET /v0/beads/context
  included, because it reports the repo root, beads directory and database name.

  The file holds ONE TOKEN PER LINE and every line is accepted. That is the
  rotation mechanism: write the new token alongside the old, roll the clients
  over, then delete the old line. The file is re-read while the server runs, so
  both the addition and the removal take effect within about a second and
  neither needs a restart. Write it atomically (a temp file plus rename; a
  Kubernetes secret mount already does this).

  There is deliberately no --auth-token flag. A credential passed as an
  argument is readable by every local user in the process listing.

  --allow-non-loopback REQUIRES a token file. Beyond loopback, reaching the
  address would otherwise be the whole authorization: any peer could read every
  issue and claim work as any actor. --insecure-no-auth is the explicit,
  auditable way to say you meant that anyway.

  The Host allowlist is what a service deployment usually trips over first. The
  DNS-rebinding check answers only to loopback spellings and the bind address,
  so a client dialing a service name gets 400 on every request; enumerate the
  names it dials with --allowed-host (repeatable). Matching is exact — no
  wildcards — and the startup log line prints the effective allowlist.

WHAT THIS DOES NOT DO

  No TLS. Even with a token, the credential and every issue body travel in
  plaintext, so a deployment beyond loopback has to supply confidentiality
  itself — a service mesh, or a network boundary you already trust.

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
		"Permit a bind beyond loopback. Requires --auth-token-file, since reaching the address would otherwise be the whole authorization")
	cmd.Flags().StringVar(&serveAuthTokenFile, "auth-token-file", "",
		"Require an Authorization: Bearer token from this file, one token per line, all accepted. Re-read while running, so rewriting it rotates tokens without a restart (env "+serveTokenFileEnv+")")
	cmd.Flags().BoolVar(&serveInsecureNoAuth, "insecure-no-auth", false,
		"Serve a non-loopback bind with NO authentication. Every peer that can reach the address gets full read and claim access")
	cmd.Flags().StringArrayVar(&serveAllowedHosts, "allowed-host", nil,
		"Additional Host header value to answer to, e.g. a service DNS name. Repeatable; matched exactly, with no wildcards")
}

// serveOptions is the part of a server's configuration that depends on NEITHER
// the workspace nor its database source: the operator's flags, and the
// environment fallbacks behind them.
//
// It is deliberately not an httpapi.Config. Every Config bd builds names the
// one database source it serves from, and that source is not known until the
// workspace has been classified — so a Config filled in this early would be a
// server configuration that cannot yet describe a server. The field names match
// Config's because they become those fields verbatim, in applyTo.
type serveOptions struct {
	Addr             string
	AllowNonLoopback bool
	InsecureNoAuth   bool
	AllowedHosts     []string
	Auth             *httpapi.TokenFileAuth
}

// applyTo writes the operator's choices onto the configuration a database arm
// built. It is the ONLY place they are set, so an arm can add a source but can
// never be the one that forgot the credential or the host allowlist.
func (o serveOptions) applyTo(cfg *httpapi.Config) {
	cfg.Addr = o.Addr
	cfg.AllowNonLoopback = o.AllowNonLoopback
	cfg.InsecureNoAuth = o.InsecureNoAuth
	cfg.AllowedHosts = o.AllowedHosts
	cfg.Auth = o.Auth
}

// resolveServeConfig turns the flags and their environment fallbacks into the
// parts of the server configuration that depend on NEITHER the workspace nor a
// listener.
//
// It is separate from runServe, and runs first, for the reason every other
// validation in this command does: a refusal for a posture that cannot be
// served must not depend on which workspace the operator happened to be
// standing in, and must not arrive after a database has been opened.
func resolveServeConfig() (serveOptions, error) {
	cfg := serveOptions{
		Addr:             serveAddr,
		AllowNonLoopback: serveAllowNonLoopback,
		InsecureNoAuth:   serveInsecureNoAuth,
		AllowedHosts:     serveAllowedHosts,
	}
	if _, err := httpapi.ValidateBindAddr(serveAddr, serveAllowNonLoopback); err != nil {
		return cfg, err
	}

	tokenFile := serveAuthTokenFile
	if tokenFile == "" {
		tokenFile = os.Getenv(serveTokenFileEnv)
	}
	if err := httpapi.ValidateAuthPosture(serveAllowNonLoopback, tokenFile != "", serveInsecureNoAuth); err != nil {
		return cfg, err
	}
	if tokenFile != "" {
		auth, err := httpapi.NewTokenFileAuth(tokenFile)
		if err != nil {
			return cfg, fmt.Errorf("--auth-token-file: %w", err)
		}
		cfg.Auth = auth
	}

	for _, host := range serveAllowedHosts {
		if err := httpapi.ValidateAllowedHost(host); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func runServe() error {
	// Flag validation first: it depends on nothing about the workspace, so the
	// refusal for a bad --addr or an unservable auth posture is the same in
	// every mode, and it lands before anything opens a database.
	opts, err := resolveServeConfig()
	if err != nil {
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
		journalEnabled := eventsjournal.EnabledFor(info.BeadsDir)
		roles, err := serveIssueRoles(store, journalEnabled)
		if err != nil {
			return HandleError("bd serve: %v", err)
		}
		defer startServeEventsJournalMaintenance(info.BeadsDir, store)()
		return serveListen(opts, httpapi.Config{
			Reader:            roles.reader,
			Claimer:           roles.claimer,
			BatchCloser:       roles.batchCloser,
			ReadyClaimer:      roles.readyClaimer,
			Releaser:          roles.releaser,
			Lifecycle:         roles.lifecycle,
			Settings:          roles.settings,
			Stats:             roles.stats,
			CycleDetector:     roles.cycles,
			EdgeReader:        roles.edges,
			GraphCounter:      roles.edgeCounter,
			Relations:         roles.relations,
			Commenter:         roles.commenter,
			BlockingAnnotator: roles.blocking,
			TreeWalker:        roles.tree,
			ReadyCounter:      roles.readyCounter,
			Counter:           roles.counter,
			Querier:           roles.querier,
			Sweeper:           roles.sweeper,
			Deleter:           roles.deleter,
			BatchCreator:      roles.batchCreator,
			DependencyEditor:  roles.dependencyEditor,
			MetadataCAS:       roles.metadataCAS,
			BatchApplier:      roles.batchApplier,
			Memories:          roles.memories,
			// Nil when this backend has no journal seam and the workspace never
			// asked for one; Listen requires it exactly when the flag below is
			// set, and serveIssueRoles has already refused the enabled case.
			EventsJournal: roles.eventsJournal,
			// GET /v0/beads/events refuses outright on a workspace that records
			// nothing, because a disabled journal and an empty one are one
			// answer at the data level. Resolved ONCE above, so the role
			// extraction, this flag and the maintenance ticker cannot disagree
			// about whether this workspace journals.
			EventsJournalEnabled: journalEnabled,
			Workspace:            info,
			SchemaVersion:        JSONSchemaVersion,
			Mode:                 serveResolvedMode(info, db),
		})
	}

	// Serve from the provider BENEATH the hook layer. `bd serve` documents that
	// it runs no hooks — a user-controlled subprocess per mutation is an
	// unbounded latency multiplier and an orphaned child at shutdown — while
	// proxied mode wires a notifying provider so the CLI's own writes keep
	// firing them. This is the unit-of-work twin of the
	// (*storage.HookFiringStore).Unwrap the store-shaped source takes.
	provider := uow.UnwrapProvider(uowProvider)
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

	defer startServeEventsJournalMaintenance(info.BeadsDir, provider)()

	return serveListen(opts, httpapi.Config{
		Provider: provider,
		// No EventsJournal field on this arm: the provider carries the journal
		// read as one of its own capability accessors, exactly as it carries
		// every other role Listen would otherwise need spelled out. Activation
		// is still the workspace's answer and still has to be handed in — the
		// provider knows how to READ the journal, not whether this workspace has
		// one.
		EventsJournalEnabled: eventsjournal.EnabledFor(info.BeadsDir),
		Workspace:            info,
		SchemaVersion:        JSONSchemaVersion,
		Mode:                 serveResolvedMode(info, db),
	})
}

// startServeEventsJournalMaintenance runs events-journal retention for the life
// of the server and returns the function that stops it.
//
// A server is not a command, so it is excluded from the per-command maintenance
// net (runsPostCommandMaintenance) — including the writer-pays auto-prune
// trigger that fires after a CLI mutation. Without this, the one topology that
// journals fastest, because it is the one accepting concurrent HTTP mutations
// for hours, would be the one that never prunes. A ticker is the honest shape
// for a process with no command boundary: it polls, and the same persisted
// watermark every CLI process reads decides whether a pass is actually due, so
// a server and the CLIs beside it share one schedule rather than three.
//
// The stop is deferred by the caller so it runs after the server has drained:
// maintenance and requests overlap freely (both are ordinary transactions), but
// nothing should still be deleting when the provider closes underneath it.
func startServeEventsJournalMaintenance(beadsDir string, source any) func() {
	if !eventsjournal.EnabledFor(beadsDir) || !eventsjournal.AutoPruneEnabledFor(beadsDir) {
		return func() {}
	}
	// The plumbing this server answers FROM, not whatever the root pre-run left
	// in a global: on the server-mode arm serve builds its own provider, and
	// maintaining a journal through a different handle than the one writing it
	// is how a topology ends up pruning the wrong database.
	runner := eventsJournalMaintenanceRunnerFor(source)
	if runner == nil {
		return func() {}
	}
	return eventsjournal.StartAutoPruneTicker(rootCtx, runner,
		eventsjournal.DefaultAutoPruneTickInterval,
		eventsJournalAutoPruneOptions(),
		reportEventsJournalAutoPrune)
}

// serveListen binds and runs. It is where the two database sources converge:
// everything past the source is the same server.
//
// The operator's options and the database source arrive separately because they
// are resolved at different times — the flags before anything opens a database,
// the source only once the workspace has been classified — and this is where
// they meet, once, for every arm.
//
// Graceful shutdown rides the signal context the root command already sets up
// (SIGINT/SIGTERM/SIGHUP). A proxied provider is closed where every proxied
// command closes it, in PersistentPostRunE — which in proxied mode does nothing
// else; the provider serve built for a server-mode workspace is closed by
// runServe's own defer, and a registered backend's store by the same
// PersistentPostRunE that opened it. None of those paths runs the auto-commit,
// export or push maintenance: proxied mode never had it, and serve is excluded
// from it by name (runsPostCommandMaintenance, cmd/bd/main.go).
func serveListen(opts serveOptions, cfg httpapi.Config) error {
	opts.applyTo(&cfg)

	// The posture warning belongs here rather than on either source arm: it
	// reports what this bind does not protect, which is a property of the
	// listener both arms share.
	warnServePosture(os.Stderr, opts)

	srv, err := httpapi.Listen(cfg)
	if err != nil {
		return HandleError("%v", err)
	}
	return srv.Serve(rootCtx)
}

// warnServePosture says out loud what this deployment does not protect.
//
// Two different warnings, because they are two different exposures. Running
// unauthenticated beyond loopback is the one that was always here, and the
// operator now has to have asked for it by name. Running authenticated beyond
// loopback is the remaining gap: the token itself crosses the network in
// plaintext on every request, so a network that can read it can replay it.
//
// Nothing is printed for the loopback default, which is what it has always
// been.
func warnServePosture(w io.Writer, opts serveOptions) {
	if !opts.AllowNonLoopback {
		return
	}
	if opts.Auth == nil {
		fmt.Fprintf(w,
			"bd serve: WARNING: --insecure-no-auth binds %s beyond loopback with no authentication. "+
				"Any peer that can reach it can read every issue and claim work as any actor.\n",
			opts.Addr)
		return
	}
	fmt.Fprintf(w,
		"bd serve: WARNING: %s is bound beyond loopback with bearer authentication but NO TLS. "+
			"Tokens and issue data travel in plaintext; deploy it inside a trusted network boundary.\n",
		opts.Addr)
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

// serveRoleSource is the surface serveIssueRoles reaches on the store, spelled
// out rather than taken as a whole storage.DoltStorage.
//
// THE POINT IS THE TEST STUBS. A stub that stands in for a store has to satisfy
// whatever this function asks for, and the only affordable way to satisfy a
// hundred-method interface is to embed it and leave it nil — which answers every
// accessor the stub forgot with a promoted method on a nil interface. That is a
// segfault inside the loop below rather than a compile error, and it has landed
// twice: once on GraphCounter in serve_source_test.go, and once on the same role
// in serve_store_identity_test.go, where no local -run pattern named the test
// and only a full-package CI shard found it.
//
// Named narrowly, a stub can DECLARE this set and assert it, so the next role
// added to the loop below is a build failure in the file that has to grow a
// method — with the method's name in the error.
//
// A whole store is one of these, which is what the assertion beneath it pins:
// the production call site passes exactly what it always did.
type serveRoleSource interface {
	IssueReader() (issueops.Reader, error)
	IssueClaimer() (issueops.Claimer, error)
	BatchCloser() (issueops.BatchCloser, error)
	ReadyClaimer() (issueops.ReadyClaimer, error)
	Releaser() (issueops.Releaser, error)
	IssueLifecycle() (issueops.Lifecycle, error)
	WorkspaceConfig() (issueops.WorkspaceConfig, error)
	StatsReporter() (issueops.StatsReporter, error)
	CycleDetector() (issueops.CycleDetector, error)
	EdgeReader() (issueops.EdgeReader, error)
	GraphCounter() (issueops.GraphCounter, error)
	IssueRelations() (issueops.Relations, error)
	Commenter() (issueops.Commenter, error)
	BlockingAnnotator() (issueops.BlockingAnnotator, error)
	TreeWalker() (issueops.TreeWalker, error)
	ReadyCounter() (issueops.ReadyCounter, error)
	Counter() (issueops.Counter, error)
	Querier() (issueops.Querier, error)
	Sweeper() (issueops.Sweeper, error)
	Deleter() (issueops.Deleter, error)
	BatchCreator() (issueops.BatchCreator, error)
	DependencyEditor() (issueops.DependencyEditor, error)
	MetadataCAS() (issueops.MetadataCAS, error)
	BatchApplier() (issueops.BatchApplier, error)
	Memories() (memoryops.Memories, error)
}

var _ serveRoleSource = storage.DoltStorage(nil)

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
func serveIssueRoles(src serveRoleSource, journalEnabled bool) (serveRoles, error) {
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
		{"batch closer", func() (err error) { roles.batchCloser, err = src.BatchCloser(); return }},
		{"ready claimer", func() (err error) { roles.readyClaimer, err = src.ReadyClaimer(); return }},
		{"issue releaser", func() (err error) { roles.releaser, err = src.Releaser(); return }},
		{"issue lifecycle", func() (err error) { roles.lifecycle, err = src.IssueLifecycle(); return }},
		{"workspace config", func() (err error) { roles.settings, err = src.WorkspaceConfig(); return }},
		{"stats reporter", func() (err error) { roles.stats, err = src.StatsReporter(); return }},
		{"cycle detector", func() (err error) { roles.cycles, err = src.CycleDetector(); return }},
		{"edge reader", func() (err error) { roles.edges, err = src.EdgeReader(); return }},
		{"graph counter", func() (err error) { roles.edgeCounter, err = src.GraphCounter(); return }},
		{"issue relations", func() (err error) { roles.relations, err = src.IssueRelations(); return }},
		{"commenter", func() (err error) { roles.commenter, err = src.Commenter(); return }},
		{"blocking annotator", func() (err error) { roles.blocking, err = src.BlockingAnnotator(); return }},
		{"tree walker", func() (err error) { roles.tree, err = src.TreeWalker(); return }},
		{"ready counter", func() (err error) { roles.readyCounter, err = src.ReadyCounter(); return }},
		{"counter", func() (err error) { roles.counter, err = src.Counter(); return }},
		{"querier", func() (err error) { roles.querier, err = src.Querier(); return }},
		{"sweeper", func() (err error) { roles.sweeper, err = src.Sweeper(); return }},
		{"deleter", func() (err error) { roles.deleter, err = src.Deleter(); return }},
		{"batch creator", func() (err error) { roles.batchCreator, err = src.BatchCreator(); return }},
		{"dependency editor", func() (err error) { roles.dependencyEditor, err = src.DependencyEditor(); return }},
		{"metadata cas", func() (err error) { roles.metadataCAS, err = src.MetadataCAS(); return }},
		{"batch applier", func() (err error) { roles.batchApplier, err = src.BatchApplier(); return }},
		{"memories", func() (err error) { roles.memories, err = src.Memories(); return }},
		{"events journal", func() error {
			// storage.UnwrapStore rather than the ONE peel above, and that is not
			// an exception to this function's rule — it is the rule applied to a
			// capability that no decorator publishes. The hook and telemetry
			// layers wrap ROLES; neither implements the journal seam, so the
			// assertion has to reach the concrete store or it finds nothing at
			// all. Nothing is skipped by going the whole way: there is no
			// journal decorator to peel past.
			//
			// A ROLE REACHED BY ASSERTION IS NOT A ROLE OUTSIDE THE RULES.
			// journalops.Journal is a facade role with a contract tier and a
			// per-leg lock like every other; what it has no accessor for is
			// the reason issueops.Importer has none either — the capability is
			// not on storage.DoltStorage's published surface, so the census
			// that would otherwise miss it reads SOURCE rather than reflecting
			// over accessors (backend/conformance/role_coverage_scan_test.go).
			// The assertion below is what a front door does with such a role,
			// not a shortcut around one.
			cursor, ok := serveJournalCursor(src)
			if ok {
				roles.eventsJournal = cursor
				return nil
			}
			// A backend that cannot read the journal is an ordinary backend
			// while the workspace records nothing — the journal is off by
			// default, and eventsjournal.Apply takes exactly this deal when it
			// binds activation at open time. Only an ENABLED workspace has
			// asked for something this backend cannot do, and the message is
			// the one `bd events` prints for the same condition.
			if journalEnabled {
				return fmt.Errorf("storage backend does not support the events journal")
			}
			return nil
		}},
	} {
		if err := b.get(); err != nil {
			return serveRoles{}, fmt.Errorf("%s: %w", b.name, err)
		}
	}
	return roles, nil
}

// serveJournalCursor is storage.UnwrapStore reached from serveRoleSource, which
// is narrower than the whole store UnwrapStore takes. A source that is not a
// store has no decorator to peel — the test stubs are exactly that — so it is
// asked for the seam as it stands.
//
// It survives the journal's promotion to a facade role (journalops.Journal,
// which storage.EventsJournalCursor aliases) unchanged, and deliberately: a
// role with no accessor is reached exactly this way. issueops.Importer set the
// precedent — one accessor, none on the store interface — and the apparatus
// that keeps such a role honest is the conformance census, which parses the
// facade packages for declarations instead of reflecting over the accessors a
// store hands out. There is no accessor for this function to have been
// replaced by.
func serveJournalCursor(src serveRoleSource) (storage.EventsJournalCursor, bool) {
	raw := any(src)
	if store, ok := src.(storage.DoltStorage); ok {
		raw = storage.UnwrapStore(store)
	}
	cursor, ok := raw.(storage.EventsJournalCursor)
	return cursor, ok
}

// serveRoles is the store-shaped database source, assembled once before Listen.
// It is deliberately NOT an httpapi.Config: the gate test in serve_test.go
// requires every httpapi.Config literal in this package to sit in a function
// that consulted serveDatabaseSource.
type serveRoles struct {
	reader  issueops.Reader
	claimer issueops.Claimer
	// batchCloser closes many issues as one transaction, behind
	// POST /v0/beads/issues:batchClose. Its accessor does NOT recurse through
	// the hook decorator today, so it is taken off the peeled store with the
	// rest for uniformity rather than out of necessity.
	batchCloser issueops.BatchCloser
	// readyClaimer is the atomic take of ready work, behind
	// POST /v0/beads/issues:claimNext. Its accessor does NOT recurse through
	// the hook decorator today, so it is taken off the peeled store with the
	// rest for uniformity rather than out of necessity.
	readyClaimer issueops.ReadyClaimer
	// releaser is the claim's inverse, behind
	// POST /v0/beads/issues/{id}:release. Its accessor recurses through the
	// hook decorator like the two below it — a release is an update, so
	// HookFiringStore.Releaser fires the workspace's on_update script — which
	// is why it comes off the PEELED store with the rest.
	releaser     issueops.Releaser
	lifecycle    issueops.Lifecycle
	settings     issueops.WorkspaceConfig
	stats        issueops.StatsReporter
	cycles       issueops.CycleDetector
	edges        issueops.EdgeReader
	edgeCounter  issueops.GraphCounter
	relations    issueops.Relations
	commenter    issueops.Commenter
	blocking     issueops.BlockingAnnotator
	tree         issueops.TreeWalker
	readyCounter issueops.ReadyCounter
	counter      issueops.Counter
	querier      issueops.Querier
	sweeper      issueops.Sweeper
	deleter      issueops.Deleter
	batchCreator issueops.BatchCreator
	// dependencyEditor is the second role here whose accessor recurses through
	// the hook decorator, so taking it off the peeled store is not optional:
	// HookFiringStore.DependencyEditor fires the workspace's update hook per
	// edited source issue, and this server documents that hooks do not fire.
	dependencyEditor issueops.DependencyEditor
	// metadataCAS is the conditional single-key metadata write. Its accessor
	// recurses through the hook decorator, so the ONE peel above is what keeps
	// this server from running the workspace's on_update script per swap.
	metadataCAS issueops.MetadataCAS
	// batchApplier is the role that makes the ONE peel above matter most, and
	// the arithmetic is what makes it worth its own sentence. Its hook wrapper
	// fires FOUR vocabularies from one call — on_create for every created item,
	// on_update for every changed update AND once per distinct edge source, and
	// the close hooks for every close that landed — so one hundred-item plan
	// served from an unpeeled applier is up to a hundred of the workspace's own
	// subprocesses spawned inside a single HTTP request, holding a write
	// transaction open while they run. Every other role here costs at most one
	// per mutation.
	batchApplier issueops.BatchApplier
	// memories is the one role here that is not an issueops role: the memory
	// plane is user data riding in the config table under its own merge class,
	// so it has its own leaf package.
	memories memoryops.Memories
	// eventsJournal is the only role here that comes from a TYPE ASSERTION
	// rather than an accessor, because the journal is not part of DoltStorage's
	// published surface: it is a replay feed over engine state on a
	// dolt_ignored table that the two concrete stores implement and a backend
	// may not. Missing it is a startup error rather than a route that 500s,
	// which is the same deal every other role here takes.
	//
	// It IS a role — journalops.Journal, which storage.EventsJournalCursor
	// aliases — with its own contract tier and per-leg lock. Accessorless is
	// the issueops.Importer shape, not an exemption; see serveJournalCursor.
	eventsJournal storage.EventsJournalCursor
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
