// Package mysql is the MySQL backend for bd: the shared sqlkit.Store bundled with
// the identity MySQL dialect and a no-op readiness strategy, plus the non-Dolt
// markers. bd's canonical SQL is MySQL-dialect, so unlike Postgres there is no
// translating driver — the only backend-specific work is a curated MySQL-8 schema
// (Dolt is more permissive than stock MySQL 8 at the DDL level).
package mysql

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlkit"
)

// Backend is the metadata.json backend identifier for this store.
const Backend = "mysql"

// Store is the MySQL backend. It embeds *sqlkit.Store (inheriting every core method)
// and the generated typed-unsupported shell for the non-core surface.
type Store struct {
	*sqlkit.Store
	unsupportedDoltStorage
}

// Compile-time guard on the generated skip list: the composite backend (real
// *sqlkit.Store methods plus the typed-unsupported shell) must fully satisfy
// storage.DoltStorage. This fails the build on skip-list drift — a skipped
// method sqlkit does not actually implement (missing method), or a method both
// implemented and stubbed (ambiguous selector) — so no method can silently
// become "unsupported".
var _ storage.DoltStorage = (*Store)(nil)

// Config configures a MySQL backend. DSN targets the per-workspace database; the
// password is merged from env at open and never persisted to metadata.json.
type Config struct {
	DSN string
}

// New builds the MySQL backend from its strategy bundle: the identity mysql dialect
// and a no-op readiness strategy (issueops already maintains is_blocked in-tx, exactly
// like the Dolt reference).
func New(ctx context.Context, cfg Config) (*Store, error) {
	base, err := sqlkit.New(ctx, sqlkit.Config{
		Dialect:   mysqlDialect{dsn: cfg.DSN},
		Readiness: sqlkit.NoopReadiness{},
	})
	if err != nil {
		return nil, err
	}
	return &Store{Store: base}, nil
}

// CommitGraphUnsupported tells cmd/bd's PostRun to skip Dolt-only maintenance.
func (s *Store) CommitGraphUnsupported() bool { return true }

// CommitPending is a no-op on a non-version-controlled backend.
func (s *Store) CommitPending(_ context.Context, _ string) (bool, error) { return false, nil }

// Commit and its variants are no-ops on MySQL, not unsupported: every sqlkit write
// already committed its own SQL transaction, so the data is durable. Command paths
// call store.Commit() opportunistically; returning nil lets those writes stand.
func (s *Store) Commit(_ context.Context, _ string) error                { return nil }
func (s *Store) CommitWithConfig(_ context.Context, _ string) error      { return nil }
func (s *Store) CommitMergeResolution(_ context.Context, _ string) error { return nil }

var (
	_ storage.DoltStorage           = (*Store)(nil)
	_ storage.NonCommitGraphBackend = (*Store)(nil)
)
