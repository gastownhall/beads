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
	// createIfMissing: whether this open may create the database when the
	// probe finds none; see providerOptions.createIfMissing.
	createIfMissing bool
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
	// createIfMissing: whether the open path may create the database when
	// the probe finds none. The default is false: a missing database is a
	// not-found error, never a silent create (#2189). Only a caller that is
	// explicitly initializing a workspace (bd init) should pass true. The
	// default is deliberately the safe behavior so a future caller that
	// forgets the option cannot re-open the implicit-create hole.
	createIfMissing bool
	// noDatabaseBind opens a server-wide maintenance connection; see
	// WithNoDatabaseBind.
	noDatabaseBind bool
}

// WithPreview opens the provider for a non-mutating preview command.
func WithPreview() ProviderOption {
	return func(o *providerOptions) { o.preview = true }
}

// WithCreateIfMissing sets whether the open path may create the database
// when the probe finds none; see providerOptions.createIfMissing.
func WithCreateIfMissing(create bool) ProviderOption {
	return func(o *providerOptions) { o.createIfMissing = create }
}

// WithNoDatabaseBind opens a server-wide maintenance connection: the open
// neither probes, creates, USEs, nor migrates the configured database, and
// the returned provider has no default database bound. Only server-scoped
// operations (MaintenanceProvider.RunNonTx issuing statements like SHOW
// DATABASES or DROP DATABASE) are meaningful on such a provider; per-database
// UnitOfWork/Tx use requires a bound database. Used by
// `bd dolt clean-databases`, which must work precisely when the configured
// database has been dropped server-side.
func WithNoDatabaseBind() ProviderOption {
	return func(o *providerOptions) { o.noDatabaseBind = true }
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

// CreateIfMissingForTest reports the create-if-missing policy a set of
// provider options resolves to. Test support only (policy-wiring tests in
// cmd/bd assert which policy each call site passes).
func CreateIfMissingForTest(opts ...ProviderOption) bool {
	return applyProviderOptions(opts).createIfMissing
}

// NoDatabaseBindForTest reports whether a set of provider options resolves
// to a server-wide, no-database-bind open. Test support only.
func NoDatabaseBindForTest(opts ...ProviderOption) bool {
	return applyProviderOptions(opts).noDatabaseBind
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

func (p *doltSQLProvider) initSchema(ctx context.Context, database string) error {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	// This budget must outwait a peer holding the migration lock through a
	// full cold-start migration pass (every migration + a Dolt commit each),
	// not just a transient blip — it grows as migrations accumulate.
	bo.MaxElapsedTime = 60 * time.Second
	// Fresh-bootstrap ownership proof for the #4566 guard self-heal
	// (gastownhall/beads#5012): a database the probe finds missing is created
	// with a bare CREATE DATABASE (no IF NOT EXISTS), so the server
	// arbitrates creation
	// atomically — success proves THIS init created the database, and an
	// already-exists refusal (1007) proves it did not. Only the proven
	// creator captures and passes a one-shot FreshBootstrapHealCapability: on
	// a database this init
	// created, a retry attempt that finds dirty tables can only be seeing a
	// previous attempt's own half-applied migration step (a session that
	// died between a step's SQL and its per-step Dolt commit — the "busy
	// buffer" shape on a loaded shared server), never pre-existing user
	// data, so the migrate call may discard that debris and converge instead
	// of failing the init permanently. The capability also binds that proof to
	// the endpoint, server UUID, database, and initial HEAD, preventing a stale
	// creator from resetting a drop/recreated name. A concurrent initializer
	// that loses the create race keeps the guard's refusal unchanged. `created`
	// is sticky across retry attempts for availability re-creation; reset
	// authority is captured only in the exact CREATE-winning attempt and is
	// never inferred again from probing or CREATE IF NOT EXISTS.
	created := false
	var bootstrapHeal *schema.FreshBootstrapHealCapability
	prepareBootstrap := func(ctx context.Context, conn *sql.Conn) (*schema.FreshBootstrapHealCapability, error) {
		ddl := db.NewDDLSQLRepository(conn)
		justCreated := false
		if created {
			// Re-assert on retries so a database dropped between attempts
			// (e.g. a concurrent clean-databases) is recreated rather than
			// failing the USE below.
			if err := ddl.CreateDatabaseIfNotExists(ctx, database); err != nil {
				return nil, &bootstrapPreparationError{err: fmt.Errorf("uow: creating database: %w", err)}
			}
		} else {
			// Probe before any DDL: an existing database is opened without a
			// CREATE attempt, so an account with no server CREATE privilege
			// is not denied (Error 1105) opening a database that is already
			// there. The bare CREATE below still arbitrates creation of a
			// missing database exactly as before.
			exists, err := ddl.DatabaseExists(ctx, database)
			if err != nil {
				if isSerializationError(err) {
					return nil, &bootstrapPreparationError{
						err:       fmt.Errorf("uow: probing database: %w", err),
						retryable: true,
					}
				}
				return nil, &bootstrapPreparationError{err: fmt.Errorf("uow: probing database: %w", err)}
			}
			if !exists {
				if !p.createIfMissing {
					return nil, &bootstrapPreparationError{err: fmt.Errorf("uow: database %q not found on Dolt server; run 'bd init' to create a new database, or 'bd bootstrap' to restore an existing project", database)}
				}
				switch err := ddl.CreateDatabase(ctx, database); {
				case err == nil:
					created = true
					justCreated = true
				case isDatabaseExistsError(err):
					// A concurrent initializer won the create race: not ours,
					// heal stays off.
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
		}
		if err := ddl.UseDatabase(ctx, database); err != nil {
			return nil, &bootstrapPreparationError{err: fmt.Errorf("uow: switching to database: %w", err)}
		}
		if justCreated {
			// Capture authority only in the same attempt that won the exact bare
			// CREATE. A later retry may re-create a missing database for
			// availability, but it must never infer ownership of a replacement
			// incarnation from CREATE IF NOT EXISTS.
			var err error
			bootstrapHeal, err = schema.CaptureFreshBootstrapHealCapability(
				ctx, conn, p.serverEndpoint, database,
			)
			if err != nil {
				return nil, &bootstrapPreparationError{err: fmt.Errorf("uow: capture fresh database identity: %w", err)}
			}
		}
		return bootstrapHeal, nil
	}
	return backoff.Retry(func() error {
		conn, err := p.db.Conn(ctx)
		if err != nil {
			if isSerializationError(err) {
				return fmt.Errorf("uow: pin connection: %w", err)
			}
			return backoff.Permanent(fmt.Errorf("uow: pin connection: %w", err))
		}
		defer conn.Close()

		ddl := db.NewDDLSQLRepository(conn)
		if p.teamServer {
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
			// Identity is checked only after the schema check proves the
			// metadata table exists at this binary's version.
			if err := checkTeamServerIdentity(ctx, conn, database, p.expectedProjectID); err != nil {
				if isSerializationError(err) {
					return fmt.Errorf("uow: team-server identity check: %w", err)
				}
				return backoff.Permanent(err)
			}
			return nil
		}
		if p.preview {
			// Preview open: attach to the database that is already there and
			// stop. No CreateDatabase, no MigrateUpWithLock — a --dry-run or
			// --inspect that migrated the workspace before rendering its plan
			// would be the exact side effect the flag exists to prevent.
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
		if _, err := schema.MigrateUpWithLock(ctx, conn, database,
			schema.WithLockedPreparation(p.serverEndpoint, prepareBootstrap)); err != nil {
			return classifyInitSchemaError(err)
		}
		return nil
	}, backoff.WithContext(bo, ctx))
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
	if opts.noDatabaseBind {
		// Server-wide maintenance open (WithNoDatabaseBind): no probe, no
		// create, no USE, no migrate — the configured database may not even
		// exist. The connection carries no default database.
		dbConn, err := openDB(ctx, buildDSN(ep, "", rootUser, rootPassword, tlsConfigName))
		if err != nil {
			return nil, err
		}
		return &doltSQLProvider{
			defaultBranch: defaultBranch,
			db:            dbConn,
		}, nil
	}

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
		createIfMissing:   opts.createIfMissing,
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
		createIfMissing:   opts.createIfMissing,
	}, nil
}
