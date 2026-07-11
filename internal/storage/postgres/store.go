// Package postgres is the Postgres backend for bd: the shared sqlkit.Store
// bundled with the Postgres dialect and a readiness strategy, plus the non-Dolt
// markers. It is a strategy bundle over the shared SQL-family implementation —
// no storage semantics are re-derived here.
package postgres

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlkit"
)

// Backend is the metadata.json backend identifier for this store.
const Backend = "postgres"

// Store is the Postgres backend. It embeds *sqlkit.Store, so it inherits every
// core method and overrides only what Postgres specializes (nothing yet — the
// readiness projection and FOR UPDATE SKIP LOCKED claim are later strategy
// swaps that override GetReadyWork/ClaimReadyIssue without touching sqlkit).
type Store struct {
	*sqlkit.Store
	// unsupportedDoltStorage (generated, unsupported_gen.go) provides the
	// capability surface Postgres does not implement — VC/history/remote/sync/
	// compaction/federation and a few non-demo core methods — each returning a
	// typed *storage.ErrUnsupported. It is generated as the exact complement of
	// *sqlkit.Store's method set, so there is no ambiguous-selector overlap.
	unsupportedDoltStorage
}

// Compile-time guard on the generated skip list: the composite backend (real
// *sqlkit.Store methods plus the typed-unsupported shell) must fully satisfy
// storage.DoltStorage. This fails the build on skip-list drift — a skipped
// method sqlkit does not actually implement (missing method), or a method both
// implemented and stubbed (ambiguous selector) — so no method can silently
// become "unsupported".
var _ storage.DoltStorage = (*Store)(nil)

// Config configures a Postgres backend.
type Config struct {
	DSN    string // password merged from env at open; never persisted to metadata.json
	Schema string // per-workspace schema, pinned via search_path
}

// New builds the Postgres backend from its strategy bundle: the pg dialect
// (translating driver) and a no-op readiness strategy (the shared issueops
// helpers already maintain is_blocked targeted and in-transaction, matching the
// Dolt reference; SyncReadiness would only add a redundant per-write recompute).
func New(ctx context.Context, cfg Config) (*Store, error) {
	base, err := sqlkit.New(ctx, sqlkit.Config{
		Dialect:   pgDialect{dsn: cfg.DSN, searchPath: cfg.Schema},
		Readiness: sqlkit.NoopReadiness{},
	})
	if err != nil {
		return nil, err
	}
	return &Store{Store: base}, nil
}

// CommitGraphUnsupported is the negative maintenance marker (see
// storage.NonCommitGraphBackend): it tells cmd/bd's PostRun to skip Dolt-only
// maintenance. Only non-Dolt backends implement it, so the default path is
// unaffected.
func (s *Store) CommitGraphUnsupported() bool { return true }

// CommitPending is a no-op on a non-version-controlled backend; write paths
// invoke it opportunistically and must not error.
func (s *Store) CommitPending(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// Commit and its variants are no-ops on Postgres, NOT unsupported: a Dolt
// "commit" flushes the working set to history, but every sqlkit write already
// committed its own SQL transaction, so the data is durable. Command paths call
// store.Commit() opportunistically after writes; returning nil lets those writes
// stand instead of erroring on a history operation that has no PG meaning.
func (s *Store) Commit(_ context.Context, _ string) error                { return nil }
func (s *Store) CommitWithConfig(_ context.Context, _ string) error      { return nil }
func (s *Store) CommitMergeResolution(_ context.Context, _ string) error { return nil }

// Compile-time proof the Postgres backend satisfies the full storage seam
// (core methods via *sqlkit.Store, the rest via the generated shell) and the
// negative maintenance marker.
var (
	_ storage.DoltStorage           = (*Store)(nil)
	_ storage.NonCommitGraphBackend = (*Store)(nil)
)
