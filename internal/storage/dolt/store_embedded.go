//go:build cgo

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cenkalti/backoff/v4"
	embedded "github.com/dolthub/driver"
)

const embeddedOpenMaxElapsed = 30 * time.Second

func newEmbeddedOpenBackoff() backoff.BackOff {
	// BackOff implementations are stateful; always return a fresh instance.
	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = embeddedOpenMaxElapsed

	return bo
}

// newEmbeddedMode creates a DoltStore using the embedded Dolt engine (requires CGO).
// Embedded-only: creates the local directory and acquires the access lock.
// In server mode, the database lives on the remote dolt sql-server;
// creating a local dolt/ directory would shadow the server connection
// with an empty embedded db (see bd-vyr).
func newEmbeddedMode(ctx context.Context, cfg *Config) (*DoltStore, error) {
	// Guard: if the path is an existing regular file (e.g., beads.db from SQLite era),
	// MkdirAll will fail confusingly. Give a clear error instead.
	if info, statErr := os.Stat(cfg.Path); statErr == nil && !info.IsDir() {
		return nil, fmt.Errorf("database path %q is a file, not a directory — run 'bd migrate --to-dolt' to migrate from SQLite", cfg.Path)
	}

	if err := os.MkdirAll(cfg.Path, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// IMPORTANT: Use an absolute path for embedded DSNs.
	//
	// The embedded driver sets its internal filesystem working directory to Config.Directory
	// and also passes the directory path through to lower layers. If we pass a relative path,
	// the working-directory stacking can effectively double it (e.g. ".beads/dolt/.beads/dolt").
	absPath, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Acquire advisory flock before opening dolt (embedded mode only).
	// This prevents multiple bd processes from competing for dolt's internal LOCK file.
	// Set BD_SKIP_ACCESS_LOCK=1 to bypass flock for testing whether Dolt's internal
	// locking is sufficient. See bd-39gso for testing plan.
	var accessLock *AccessLock
	if cfg.OpenTimeout > 0 && os.Getenv("BD_SKIP_ACCESS_LOCK") == "" {
		exclusive := !cfg.ReadOnly
		accessLock, err = AcquireAccessLock(absPath, exclusive, cfg.OpenTimeout)
		if err != nil {
			return nil, fmt.Errorf("failed to acquire dolt access lock: %w", err)
		}
	}

	initDSN := fmt.Sprintf(
		"file://%s?commitname=%s&commitemail=%s",
		absPath, cfg.CommitterName, cfg.CommitterEmail,
	)
	dbDSN := fmt.Sprintf(
		"file://%s?commitname=%s&commitemail=%s&database=%s",
		absPath, cfg.CommitterName, cfg.CommitterEmail, cfg.Database,
	)

	configureRetries := func(c *embedded.Config) {
		// Enable driver open retries for embedded usage.
		c.BackOff = newEmbeddedOpenBackoff()
	}

	// UOW 1: ensure database exists. Skip in read-only mode.
	if !cfg.ReadOnly {
		if err := withEmbeddedDolt(ctx, initDSN, configureRetries, func(ctx context.Context, db *sql.DB) error {
			_, err := db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", cfg.Database))

			return err
		}); err != nil {
			if accessLock != nil {
				accessLock.Release()
			}

			return nil, fmt.Errorf("failed to create dolt database: %w", err)
		}
	}

	// UOW 2: initialize schema (idempotent). Skip in read-only mode.
	if !cfg.ReadOnly {
		if err := withEmbeddedDolt(ctx, dbDSN, configureRetries, func(ctx context.Context, db *sql.DB) error {
			return initSchemaOnDB(ctx, db)
		}); err != nil {
			if accessLock != nil {
				accessLock.Release()
			}

			return nil, fmt.Errorf("failed to initialize schema: %w", err)
		}
	}

	// Open the store connection (fresh connector for subsequent work).
	db, connStr, connector, err := openEmbeddedConnection(dbDSN)
	if err != nil {
		if accessLock != nil {
			accessLock.Release()
		}

		return nil, err
	}

	// Test connection
	// IMPORTANT: In embedded mode, do not use a caller-supplied ctx to open the first
	// underlying connection. Many tests (and some call sites) pass contexts that are
	// canceled shortly after New() returns; the embedded driver derives a session context
	// from Connect(ctx) and reuses it across statements. We force the initial connection
	// to be created with a non-canceling context to avoid poisoning the connection pool.
	if err := db.PingContext(context.Background()); err != nil {
		// Ensure we don't leak filesystem locks if embedded open fails after creating a connector.
		_ = db.Close()
		_ = connector.Close()
		if accessLock != nil {
			accessLock.Release()
		}

		return nil, fmt.Errorf("failed to ping Dolt database: %w", err)
	}

	store := &DoltStore{
		db:                db,
		dbPath:            absPath,
		connStr:           connStr,
		embeddedConnector: connector,
		committerName:     cfg.CommitterName,
		committerEmail:    cfg.CommitterEmail,
		remote:            cfg.Remote,
		branch:            "main",
		remoteUser:        cfg.RemoteUser,
		remotePassword:    cfg.RemotePassword,
		readOnly:          cfg.ReadOnly,
		serverMode:        false,
		accessLock:        accessLock,
	}

	return store, nil
}

// openEmbeddedConnection opens a connection using the embedded Dolt driver
func openEmbeddedConnection(dsn string) (*sql.DB, string, *embedded.Connector, error) {
	openCfg, err := embedded.ParseDSN(dsn)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to parse Dolt DSN: %w", err)
	}
	openCfg.BackOff = newEmbeddedOpenBackoff()

	connector, err := embedded.NewConnector(openCfg)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to create Dolt connector: %w", err)
	}
	db := sql.OpenDB(connector)

	// Configure connection pool
	// Dolt embedded mode is single-writer
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	// NOTE: connector must be closed by the caller to release filesystem locks.
	// DoltStore.Close() will handle this.
	return db, dsn, connector, nil
}
