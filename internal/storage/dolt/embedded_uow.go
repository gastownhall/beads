//go:build cgo

package dolt

import (
	"context"
	"database/sql"
	"errors"

	embedded "github.com/dolthub/driver"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

func ignoreContextCanceled(err error) error {
	if err == nil {
		return nil
	}
	// Dolt engine close paths may surface context.Canceled from background goroutines / shutdown plumbing.
	// For unit-of-work helpers, we treat that as non-fatal cleanup noise.
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// withEmbeddedDolt executes exactly one unit of work using a single embedded Dolt connector.
//
// Lifecycle:
//  1) ParseDSN
//  2) configure (e.g. enable retries by setting cfg.BackOff)
//  3) NewConnector
//  4) sql.OpenDB(connector)
//  5) PingContext(ctx) to force open (and retries)
//  6) fn(ctx, db)
//  7) db.Close()
//  8) connector.Close() to release filesystem locks
//
// IMPORTANT: This helper does not wrap or modify ctx (no timeouts). The embedded driver derives
// a session context from the Connect(ctx) context and reuses it across statements.
func withEmbeddedDolt(
	ctx context.Context,
	dsn string,
	configure func(cfg *embedded.Config),
	fn func(ctx context.Context, db *sql.DB) error,
) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return errors.New("withEmbeddedDolt: fn is required")
	}

	cfg, err := embedded.ParseDSN(dsn)
	if err != nil {
		return err
	}
	if configure != nil {
		configure(&cfg)
	}

	connector, err := embedded.NewConnector(cfg)
	if err != nil {
		return err
	}
	db := sql.OpenDB(connector)

	defer func() {
		// Close DB first (stops pool activity), then close the connector to release engine locks.
		// Use CloseWithTimeout to prevent indefinite hangs (same issue as DoltStore.Close).
		cerr := errors.Join(
			ignoreContextCanceled(doltutil.CloseWithTimeout("db", db.Close)),
			ignoreContextCanceled(doltutil.CloseWithTimeout("connector", connector.Close)),
		)
		if err == nil {
			err = cerr
		} else {
			err = errors.Join(err, cerr)
		}
	}()

	// Force open (and retries if configured) before entering the unit of work.
	if err := db.PingContext(ctx); err != nil {
		return err
	}

	return fn(ctx, db)
}

