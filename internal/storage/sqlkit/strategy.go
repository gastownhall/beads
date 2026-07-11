// Package sqlkit is the single, shared SQL-family implementation of bd's storage
// semantics. Every SQL backend (Dolt, Postgres, SQLite, MySQL, …) is a strategy
// bundle over this one implementation:
//
//   - Dialect            — the SQL flavor (a translating driver or native emit)
//   - ReadinessStrategy  — how the is_blocked projection is kept current
//   - ClaimStrategy      — how a ready issue is atomically claimed
//
// A concrete backend embeds *Store, inherits every method, and overrides only
// the handful it specializes (a "method override") or adds capability-interface
// methods for operations that don't exist in the core contract (e.g. Dolt
// history). The query/graph/ordering/id-mint/cycle semantics live here once, in
// internal/storage/issueops, and are never re-derived per backend.
package sqlkit

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Dialect opens the backend's *sql.DB and names it. The shared layer emits
// canonical (MySQL-dialect) SQL; a Dialect whose driver translates that text to
// the backend's flavor is how a non-MySQL SQL engine joins the family without
// changing the shared layer (see internal/storage/pgdialect for Postgres).
type Dialect interface {
	Name() string
	Open(ctx context.Context) (*sql.DB, error)
}

// ReadinessStrategy keeps the denormalized is_blocked column consistent after a
// mutation. It runs inside the same write transaction, so the mutation and its
// reprojection commit atomically.
type ReadinessStrategy interface {
	AfterWrite(ctx context.Context, tx issueops.DBTX) error
}

// ClaimStrategy selects and claims one ready issue. The wedge uses the shared
// read-then-update path (default); a backend may override with, e.g.,
// SELECT … FOR UPDATE SKIP LOCKED for lock-free multi-writer claims. Wired in
// the claim slice.
type ClaimStrategy interface {
	ClaimReady(ctx context.Context, tx issueops.DBTX, filter types.WorkFilter, actor string) (*types.Issue, error)
}

// NoopReadiness performs no reprojection. It is the default: every shared
// issueops mutation helper already maintains the is_blocked column targeted and
// in-transaction (exactly like the Dolt reference, which wires no readiness
// strategy at all), so a per-write full-graph recompute is pure redundant cost.
// A backend opts IN to extra reprojection by supplying SyncReadiness explicitly.
type NoopReadiness struct{}

func (NoopReadiness) AfterWrite(_ context.Context, _ issueops.DBTX) error {
	return nil
}

// SyncReadiness recomputes is_blocked synchronously, in-transaction, on every
// mutation via the full-graph fixpoint. Because the issueops helpers already
// keep is_blocked current, this is only useful as an explicit opt-in repair
// projection; it is no longer the nil-default.
type SyncReadiness struct{}

func (SyncReadiness) AfterWrite(ctx context.Context, tx issueops.DBTX) error {
	_, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
	return err
}
