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
	// creator passes WithFreshBootstrapHeal: on a database this init
	// created, a retry attempt that finds dirty tables can only be seeing a
	// previous attempt's own half-applied migration step (a session that
	// died between a step's SQL and its per-step Dolt commit — the "busy
	// buffer" shape on a loaded shared server), never pre-existing user
	// data, so the migrate call may discard that debris and converge instead
	// of failing the init permanently. A concurrent initializer that loses
	// the create race keeps the guard's refusal unchanged. `created` is
	// sticky across retry attempts: it is set exactly when this init's
	// CREATE succeeded, which no later attempt can re-learn from probing.
	created := false
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
		if created {
			// Re-assert on retries so a database dropped between attempts
			// (e.g. a concurrent clean-databases) is recreated rather than
			// failing the USE below.
			if err := ddl.CreateDatabaseIfNotExists(ctx, database); err != nil {
				return backoff.Permanent(fmt.Errorf("uow: creating database: %w", err))
			}
		} else {
			switch err := ddl.CreateDatabase(ctx, database); {
			case err == nil:
				created = true
			case isDatabaseExistsError(err):
				// Pre-existing (or a concurrent initializer won the create
				// race): not ours, heal stays off.
			case isSerializationError(err):
				return fmt.Errorf("uow: creating database: %w", err)
			default:
				return backoff.Permanent(fmt.Errorf("uow: creating database: %w", err))
			}
		}
		if err := ddl.UseDatabase(ctx, database); err != nil {
			return backoff.Permanent(fmt.Errorf("uow: switching to database: %w", err))
		}

		var migrateOpts []schema.MigrateLockOption
		if created {
			migrateOpts = append(migrateOpts, schema.WithFreshBootstrapHeal())
		}
		if _, err := schema.MigrateUpWithLock(ctx, conn, database, migrateOpts...); err != nil {
			if isSerializationError(err) || schema.IsMigrationLockError(err) {
				return fmt.Errorf("uow: migrate: %w", err)
			}
			return backoff.Permanent(fmt.Errorf("uow: migrate: %w", err))
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

func openAndInitSchema(ctx context.Context, ep proxy.Endpoint, database, rootUser, rootPassword, tlsConfigName string) (UnitOfWorkProvider, error) {
	initDB, err := openDB(ctx, buildDSN(ep, "", rootUser, rootPassword, tlsConfigName))
	if err != nil {
		return nil, err
	}

	initProvider := &doltSQLProvider{
		defaultBranch: defaultBranch,
		db:            initDB,
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
		defaultBranch: defaultBranch,
		db:            dbConn,
	}, nil
}
