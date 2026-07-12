package sqlkit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// ErrStoreClosed is returned when a method is called after Close.
var ErrStoreClosed = errors.New("store is closed")

// Store is the shared SQL-family store. It delegates every operation to the
// backend-neutral internal/storage/issueops layer over a *sql.Tx, and is
// parameterized by a strategy bundle. Concrete backends embed *Store.
//
// NOTE: this encodes the strategy-bundle template. It implements a
// representative slice of the core surface (config, ready-work, close); the
// remaining ~50 delegation methods are mechanical fill-in (they mirror the
// internal/storage/dolt/*.go bodies, minus the Dolt-only residue) and land in
// the "implement-set" slice.
type Store struct {
	db        *sql.DB
	dialect   Dialect
	readiness ReadinessStrategy
	claim     ClaimStrategy

	closed atomic.Bool
}

// Config is the strategy bundle a backend supplies to build a Store.
type Config struct {
	Dialect   Dialect
	Readiness ReadinessStrategy // defaults to NoopReadiness{} when nil
	Claim     ClaimStrategy     // optional until the claim slice
}

// New opens the backend via its Dialect and returns the shared Store.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Dialect == nil {
		return nil, errors.New("sqlkit: nil Dialect")
	}
	db, err := cfg.Dialect.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlkit: open %s: %w", cfg.Dialect.Name(), err)
	}
	readiness := cfg.Readiness
	if readiness == nil {
		readiness = NoopReadiness{}
	}
	return &Store{db: db, dialect: cfg.Dialect, readiness: readiness, claim: cfg.Claim}, nil
}

// DB exposes the underlying pool for diagnostics/migrations.
func (s *Store) DB() *sql.DB { return s.db }

// DialectName reports the backend's dialect ("postgres", "sqlite", …).
func (s *Store) DialectName() string { return s.dialect.Name() }

// Close closes the pool. Idempotent.
func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	return s.db.Close()
}

// withReadTx runs fn in a read transaction that is always rolled back.
func (s *Store) withReadTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin read tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// withWriteTx runs fn and commits. For writes that cannot change blocked-ness
// (config, metadata).
func (s *Store) withWriteTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.closed.Load() {
		return ErrStoreClosed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write tx: %w", err)
	}
	// A panicking callback must not pin the pooled connection; Rollback after
	// Commit (or the explicit Rollback below) is a no-op.
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit write tx: %w", err)
	}
	return nil
}

// withMutationTx runs fn then the readiness strategy in ONE transaction, so an
// issue/dependency mutation and its is_blocked reprojection commit atomically.
func (s *Store) withMutationTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return s.readiness.AfterWrite(ctx, tx)
	})
}

// --- representative core methods (delegating to issueops through the dialect) ---

// SetConfig persists a config key/value.
func (s *Store) SetConfig(ctx context.Context, key, value string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := issueops.SetConfigInTx(ctx, tx, key, value); err != nil {
			return err
		}
		// Sync the normalized custom-status/type tables, matching the embedded-Dolt
		// reference. The sync parses+validates the value (a malformed one errors and
		// rolls back the whole write tx) and is what GetCustomStatuses/GetCustomTypes
		// read back ORDER BY name.
		switch key {
		case "status.custom":
			if err := issueops.SyncCustomStatusesTable(ctx, tx, value); err != nil {
				return fmt.Errorf("syncing custom_statuses table: %w", err)
			}
		case "types.custom":
			if err := issueops.SyncCustomTypesTable(ctx, tx, value); err != nil {
				return fmt.Errorf("syncing custom_types table: %w", err)
			}
		}
		return nil
	})
}

// GetConfig reads a config value ("" if absent).
func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	var v string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		v, e = issueops.GetConfigInTx(ctx, tx, key)
		return e
	})
	return v, err
}

// GetAllConfig returns all config key/values.
func (s *Store) GetAllConfig(ctx context.Context) (map[string]string, error) {
	var m map[string]string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		m, e = issueops.GetAllConfigInTx(ctx, tx)
		return e
	})
	return m, err
}

// GetReadyWork returns issues that are ready to work (no open blockers).
func (s *Store) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	var out []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var e error
		out, e = issueops.GetReadyWorkInTx(ctx, tx, filter)
		return e
	})
	return out, err
}

// CloseIssue closes an issue and reprojects blocked-ness in the same tx.
func (s *Store) CloseIssue(ctx context.Context, id, reason, actor, session string) error {
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		_, err := issueops.CloseIssueInTx(ctx, tx, id, reason, actor, session)
		return err
	})
}
