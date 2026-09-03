package uow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	db "github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

const (
	defaultBranch           = "main"
	defaultProxyIdleTimeout = 30 * time.Second
)

type doltSQLProvider struct {
	defaultBranch string
	db            *sql.DB
	// serverEndpoint is the exact transport endpoint used by the migration
	// connection. Fresh-bootstrap reset authority is bound to it together with
	// the Dolt server UUID, database name, and initial HEAD.
	serverEndpoint string
	// teamServer: schema is owned by beads-team-server (bts) — bd never
	// creates the database or migrates, only verifies the schema version.
	teamServer bool
	// expectedProjectID is the calling workspace's project identity, asserted
	// against the team-server database on open. Empty means "no assertion
	// available" (bd init, which adopts; server-wide maintenance; a workspace
	// predating project identity) and skips the check.
	expectedProjectID string
	// preview: the command that opened this provider is an explicitly
	// non-mutating preview (--dry-run, --inspect). The open creates no
	// database and applies no migration; see providerOptions.preview.
	preview bool
	// eventsJournalEnabled activates the durable events journal for THIS
	// provider instance only. See SetEventsJournalEnabled.
	eventsJournalEnabled atomic.Bool
}

// SetEventsJournalEnabled activates the durable events journal for every unit
// of work this provider begins from now on. Per instance, never process-global:
// a process can hold several providers at once, and enabling one must not
// enable the rest.
//
// Emission itself lives at the issueops seam that the domain/db repositories
// call, but the seam only emits for a transaction activation is BOUND to (see
// BeginTx). Without that binding the uow plumbing writes mutations while
// journaling nothing — the failure is invisible, because the code runs and the
// write lands and the journal is simply empty.
func (p *doltSQLProvider) SetEventsJournalEnabled(enabled bool) {
	p.eventsJournalEnabled.Store(enabled)
}

type bootstrapPreparationError struct {
	err       error
	retryable bool
}

func (e *bootstrapPreparationError) Error() string {
	return e.err.Error()
}

func (e *bootstrapPreparationError) Unwrap() error {
	return e.err
}

func classifyInitSchemaError(err error) error {
	var preparationErr *bootstrapPreparationError
	if errors.As(err, &preparationErr) {
		if preparationErr.retryable {
			return fmt.Errorf("uow: bootstrap preparation: %w", err)
		}
		return backoff.Permanent(err)
	}
	if isSerializationError(err) || schema.IsMigrationLockError(err) {
		return fmt.Errorf("uow: migrate: %w", err)
	}
	return backoff.Permanent(fmt.Errorf("uow: migrate: %w", err))
}

// ProviderOption tunes how a SQL-server unit-of-work provider opens. Options
// are variadic so the existing constructor call sites — every one of which
// wants the ordinary mutating open — stay unchanged.
type ProviderOption func(*providerOptions)

type providerOptions struct {
	// preview opens for a command that promised not to mutate anything
	// (--dry-run, --inspect). Such a command must reach its own RunE before
	// anything writes, so the open may neither CREATE DATABASE nor run
	// MigrateUpWithLock: both happen during root pre-run, before the flag the
	// user passed has had any effect. An absent or behind database is
	// reported by the preview's own query rather than repaired implicitly —
	// the same contract embeddeddolt.OpenForPreviewCommand gives the embedded
	// path.
	preview bool
}

// WithPreview opens the provider for a non-mutating preview command.
func WithPreview() ProviderOption {
	return func(o *providerOptions) { o.preview = true }
}

func applyProviderOptions(opts []ProviderOption) providerOptions {
	var resolved providerOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&resolved)
		}
	}
	return resolved
}

var (
	_ UnitOfWorkProvider              = (*doltSQLProvider)(nil)
	_ TxProvider                      = (*doltSQLProvider)(nil)
	_ storage.EventsJournalConfigurer = (*doltSQLProvider)(nil)
)

func (p *doltSQLProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	return NewUOW(ctx, p)
}

func (p *doltSQLProvider) Close(ctx context.Context) error {
	if p.db == nil {
		return nil
	}
	db := p.db
	p.db = nil
	return db.Close()
}

func (p *doltSQLProvider) BeginTx(ctx context.Context) (Tx, error) {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("uow: pin connection: %w", err)
	}

	_, err = conn.ExecContext(ctx, "START TRANSACTION;")
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("uow: failed to start transaction: %w", err)
	}

	// Bind journal activation to the connection this unit of work is pinned to,
	// AFTER START TRANSACTION so the seq allocation's UPDATE and the SELECT that
	// must observe it are inside one transaction on one session. The scope is
	// released when the connection is (doltServerTx.releaseConn / poisonConn),
	// so an entry cannot outlive its transaction.
	return &doltServerTx{
		conn:              conn,
		clearJournalScope: issueops.ScopeEventsJournalTransaction(conn, p.eventsJournalEnabled.Load()),
	}, nil
}

// selectProbeDatabase lets schema's pre-lock convergence probe reach the
// database on a session that is not yet on one. openAndInitSchema pins its
// schema-init pool with an EMPTY DSN database, so without this the probe reads
// NULL from DATABASE(), declines, and every invocation queues on the
// server-wide migration lock it exists to skip.
//
// The USE MUST remain the DDL repository's own UseDatabase — the exact
// statement prepareBootstrap issues below. That identity is the reason the
// probe may skip locked preparation when it reports converged: preparation's
// contribution on an existing database is precisely this USE (its bare CREATE
// DATABASE can only have failed with "database exists" and captured no heal
// authority). Reimplementing the statement here, or letting the two quote
// identifiers differently, would silently break that argument.
//
// It is injected rather than reached for from schema/: domain/db's own test
// suite migrates a scratch database with schema, so a schema -> domain/db
// import compiles as a library and then fails the domain/db TEST build with
// "import cycle not allowed in test". uow already depends on both, so the
// dependency is legal exactly here.
func selectProbeDatabase(ctx context.Context, conn schema.DBConn, database string) (string, error) {
	quoted, err := db.QuoteIdentifier(database)
	if err != nil {
		return "", err
	}
	if err := db.NewDDLSQLRepository(conn).UseDatabase(ctx, database); err != nil {
		return "", err
	}
	return quoted, nil
}

func (p *doltSQLProvider) initSchema(ctx context.Context, database string) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	// This budget must outwait a peer holding the migration lock through a
	// full cold-start migration pass (every migration + a Dolt commit each),
	// not just a transient blip — it grows as migrations accumulate.
	bo.MaxElapsedTime = 60 * time.Second
	// One preparer per initSchema call carries the sticky fresh-bootstrap state
	// (created/heal) across every backoff attempt; see bootstrapPreparer.
	preparer := &bootstrapPreparer{provider: p, database: database}
	return backoff.Retry(func() error {
		return p.initSchemaAttempt(ctx, database, preparer)
	}, backoff.WithContext(bo, ctx))
}

// initSchemaAttempt runs a single backoff attempt of initSchema. It pins a
// connection and fans out to the team-server, preview, or migrating open path.
// Each path returns a bare error for a serialization retry and backoff.Permanent
// for a terminal failure.
func (p *doltSQLProvider) initSchemaAttempt(ctx context.Context, database string, preparer *bootstrapPreparer) error {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		if isSerializationError(err) {
			return fmt.Errorf("uow: pin connection: %w", err)
		}
		return backoff.Permanent(fmt.Errorf("uow: pin connection: %w", err))
	}
	defer conn.Close()

	if p.teamServer {
		return p.verifyTeamServerSchema(ctx, conn, database)
	}
	if p.preview {
		return p.attachPreviewDatabase(ctx, conn, database)
	}
	if _, err := schema.MigrateUpWithLock(ctx, conn, database,
		schema.WithDatabaseSelector(selectProbeDatabase),
		schema.WithLockedPreparation(p.serverEndpoint, preparer.prepare)); err != nil {
		return classifyInitSchemaError(err)
	}
	return nil
}

// verifyTeamServerSchema is the team-server open path: the schema is owned by
// beads-team-server (bts), so bd never creates the database or migrates. It
// attaches to the existing database and verifies the schema version, then the
// project identity — identity is checked only after the schema check proves the
// metadata table exists at this binary's version.
func (p *doltSQLProvider) verifyTeamServerSchema(ctx context.Context, conn *sql.Conn, database string) error {
	ddl := db.NewDDLSQLRepository(conn)
	if err := ddl.UseDatabase(ctx, database); err != nil {
		if isSerializationError(err) {
			return fmt.Errorf("uow: switching to database: %w", err)
		}
		return backoff.Permanent(fmt.Errorf(
			"uow: database %q not found — the schema is managed by beads-team-server; ask your operator to run 'bts init' first: %w",
			database, err))
	}
	if err := checkTeamServerSchema(ctx, conn, database); err != nil {
		if isSerializationError(err) {
			return fmt.Errorf("uow: team-server schema check: %w", err)
		}
		return backoff.Permanent(err)
	}
	if err := checkTeamServerIdentity(ctx, conn, database, p.expectedProjectID); err != nil {
		if isSerializationError(err) {
			return fmt.Errorf("uow: team-server identity check: %w", err)
		}
		return backoff.Permanent(err)
	}
	return nil
}

// attachPreviewDatabase is the preview open path: attach to the database that is
// already there and stop. No CreateDatabase, no MigrateUpWithLock — a --dry-run
// or --inspect that migrated the workspace before rendering its plan would be the
// exact side effect the flag exists to prevent.
func (p *doltSQLProvider) attachPreviewDatabase(ctx context.Context, conn *sql.Conn, database string) error {
	ddl := db.NewDDLSQLRepository(conn)
	if err := ddl.UseDatabase(ctx, database); err != nil {
		if isSerializationError(err) {
			return fmt.Errorf("uow: switching to database: %w", err)
		}
		return backoff.Permanent(fmt.Errorf(
			"uow: database %q not found — preview commands (--dry-run, --inspect) never create or migrate a database; run the command without the preview flag first: %w",
			database, err))
	}
	return nil
}

// bootstrapPreparer carries the sticky fresh-bootstrap state across the backoff
// retry attempts of a single initSchema call and runs as MigrateUpWithLock's
// locked-preparation callback (see prepare).
//
// Fresh-bootstrap ownership proof for the #4566 guard self-heal
// (gastownhall/beads#5012): the first attempt issues a bare CREATE DATABASE (no
// IF NOT EXISTS), so the server arbitrates creation atomically — success proves
// THIS init created the database, and an already-exists refusal (1007) proves it
// did not. Only the proven creator captures and passes a one-shot
// FreshBootstrapHealCapability: on a database this init created, a retry attempt
// that finds dirty tables can only be seeing a previous attempt's own
// half-applied migration step (a session that died between a step's SQL and its
// per-step Dolt commit — the "busy buffer" shape on a loaded shared server),
// never pre-existing user data, so the migrate call may discard that debris and
// converge instead of failing the init permanently. The capability also binds
// that proof to the endpoint, server UUID, database, and initial HEAD, preventing
// a stale creator from resetting a drop/recreated name. A concurrent initializer
// that loses the create race keeps the guard's refusal unchanged.
type bootstrapPreparer struct {
	provider *doltSQLProvider
	database string
	// created is sticky across retry attempts for availability re-creation;
	// reset authority is captured only in the exact CREATE-winning attempt and is
	// never inferred again from probing or CREATE IF NOT EXISTS.
	created bool
	// heal is the one-shot capability captured in the CREATE-winning attempt and
	// re-returned unchanged on later attempts.
	heal *schema.FreshBootstrapHealCapability
}

// prepare is MigrateUpWithLock's locked-preparation callback. It (re)creates and
// selects the target database and, only in the attempt that won the bare CREATE,
// captures the fresh-bootstrap heal capability. See bootstrapPreparer.
func (b *bootstrapPreparer) prepare(ctx context.Context, conn *sql.Conn) (*schema.FreshBootstrapHealCapability, error) {
	ddl := db.NewDDLSQLRepository(conn)
	justCreated := false
	if b.created {
		// Re-assert on retries so a database dropped between attempts
		// (e.g. a concurrent clean-databases) is recreated rather than
		// failing the USE below.
		if err := ddl.CreateDatabaseIfNotExists(ctx, b.database); err != nil {
			return nil, &bootstrapPreparationError{err: fmt.Errorf("uow: creating database: %w", err)}
		}
	} else {
		switch err := ddl.CreateDatabase(ctx, b.database); {
		case err == nil:
			b.created = true
			justCreated = true
		case isDatabaseExistsError(err):
			// Pre-existing (or a concurrent initializer won the create
			// race): not ours, heal stays off.
		case isSerializationError(err):
			// Only the initial bare CREATE preserves its historical
			// serialization retry classification. The later sticky CREATE,
			// USE, and identity capture remain permanent regardless of
			// their nested driver error.
			return nil, &bootstrapPreparationError{
				err:       fmt.Errorf("uow: creating database: %w", err),
				retryable: true,
			}
		default:
			return nil, &bootstrapPreparationError{err: fmt.Errorf("uow: creating database: %w", err)}
		}
	}
	if err := ddl.UseDatabase(ctx, b.database); err != nil {
		return nil, &bootstrapPreparationError{err: fmt.Errorf("uow: switching to database: %w", err)}
	}
	if justCreated {
		// Capture authority only in the same attempt that won the exact bare
		// CREATE. A later retry may re-create a missing database for
		// availability, but it must never infer ownership of a replacement
		// incarnation from CREATE IF NOT EXISTS.
		var err error
		b.heal, err = schema.CaptureFreshBootstrapHealCapability(
			ctx, conn, b.provider.serverEndpoint, b.database,
		)
		if err != nil {
			return nil, &bootstrapPreparationError{err: fmt.Errorf("uow: capture fresh database identity: %w", err)}
		}
	}
	return b.heal, nil
}

func buildDSN(ep proxy.Endpoint, database, user, password, tlsConfigName string) string {
	return util.DoltServerDSN{
		Host:            ep.Host,
		Port:            ep.Port,
		User:            user,
		Password:        password,
		Database:        database,
		TLSConfigName:   tlsConfigName,
		ClientFoundRows: true,
	}.String()
}

func openDB(ctx context.Context, dsn string) (*sql.DB, error) {
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("uow: open db: %w", err)
	}
	if err := conn.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("uow: ping db: %w", err), conn.Close())
	}
	return conn, nil
}

func openAndInitSchema(ctx context.Context, ep proxy.Endpoint, database, rootUser, rootPassword, tlsConfigName string, teamServer bool, expectedProjectID string, opts providerOptions) (UnitOfWorkProvider, error) {
	initDB, err := openDB(ctx, buildDSN(ep, "", rootUser, rootPassword, tlsConfigName))
	if err != nil {
		return nil, err
	}

	initProvider := &doltSQLProvider{
		defaultBranch:     defaultBranch,
		db:                initDB,
		serverEndpoint:    "tcp:" + ep.Address(),
		teamServer:        teamServer,
		expectedProjectID: expectedProjectID,
		preview:           opts.preview,
	}

	if err := initProvider.initSchema(ctx, database); err != nil {
		_ = initDB.Close()
		return nil, fmt.Errorf("uow: init schema: %w", err)
	}

	if err := initDB.Close(); err != nil {
		return nil, fmt.Errorf("uow: close init db: %w", err)
	}

	dbConn, err := openDB(ctx, buildDSN(ep, database, rootUser, rootPassword, tlsConfigName))
	if err != nil {
		return nil, err
	}

	return &doltSQLProvider{
		defaultBranch:     defaultBranch,
		db:                dbConn,
		serverEndpoint:    "tcp:" + ep.Address(),
		teamServer:        teamServer,
		expectedProjectID: expectedProjectID,
		preview:           opts.preview,
	}, nil
}
