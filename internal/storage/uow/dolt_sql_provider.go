package uow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v4"
	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	db "github.com/steveyegge/beads/internal/storage/domain/db"
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
	_ UnitOfWorkProvider = (*doltSQLProvider)(nil)
	_ TxProvider         = (*doltSQLProvider)(nil)
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

	return &doltServerTx{
		conn: conn,
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
	// (gastownhall/beads#5012): the first attempt issues a bare CREATE
	// DATABASE (no IF NOT EXISTS), so the server arbitrates creation
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
			switch err := ddl.CreateDatabase(ctx, database); {
			case err == nil:
				created = true
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
