// Package sqlite is the SQLite backend for bd: the shared sqlkit.Store bundled with
// the SQLite dialect and a no-op readiness strategy, plus the non-Dolt markers. It is
// a pure-Go, file-based, embedded backend (modernc.org/sqlite; no CGO, no server).
package sqlite

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlkit"
)

// Backend is the metadata.json backend identifier for this store.
const Backend = "sqlite"

// Store is the SQLite backend: *sqlkit.Store + the generated typed-unsupported shell.
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

// Config configures a SQLite backend. DSN is a modernc.org/sqlite file DSN.
type Config struct{ DSN string }

// New builds the SQLite backend from its strategy bundle: the sqlite dialect and a
// no-op readiness strategy (issueops maintains is_blocked in-tx, like Dolt).
func New(ctx context.Context, cfg Config) (*Store, error) {
	base, err := sqlkit.New(ctx, sqlkit.Config{
		Dialect:   sqliteDialect{dsn: cfg.DSN},
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

// Commit and variants are no-ops on SQLite (writes are already durable per-tx).
func (s *Store) Commit(_ context.Context, _ string) error                { return nil }
func (s *Store) CommitWithConfig(_ context.Context, _ string) error      { return nil }
func (s *Store) CommitMergeResolution(_ context.Context, _ string) error { return nil }

var (
	_ storage.DoltStorage           = (*Store)(nil)
	_ storage.NonCommitGraphBackend = (*Store)(nil)
)
