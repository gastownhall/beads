//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Compile-time assertion that the embedded store provides the CAS capability.
var _ storage.ConditionalWriter = (*EmbeddedDoltStore)(nil)

// CompareAndSetMetadataKey implements storage.ConditionalWriter for the embedded
// store. Writes are serialized by a fresh per-operation connection (withConn),
// so no serialization-conflict retry is required.
func (s *EmbeddedDoltStore) CompareAndSetMetadataKey(ctx context.Context, id, key string, expected *string, newValue, actor string) (*types.Issue, error) {
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.CompareAndSetMetadataKeyInTx(ctx, tx, id, key, expected, newValue, actor)
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// CompareAndClearMetadataKey implements storage.ConditionalWriter for the
// embedded store — the guarded release symmetric with CompareAndSetMetadataKey.
func (s *EmbeddedDoltStore) CompareAndClearMetadataKey(ctx context.Context, id, key, expected, actor string) (*types.Issue, error) {
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.CompareAndClearMetadataKeyInTx(ctx, tx, id, key, expected, actor)
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// UpdateIssueIfMatch implements storage.ConditionalWriter for the embedded store —
// whole-row optimistic concurrency. Writes are serialized by withConn, so the
// guarded UPDATE's zero-row miss is the terminal precondition signal (no
// commit-time serialization retry is required, as for the metadata CAS).
func (s *EmbeddedDoltStore) UpdateIssueIfMatch(ctx context.Context, id string, updates map[string]interface{}, expectedRevision *int64, actor string) (*types.Issue, error) {
	err := s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := issueops.UpdateIssueIfMatchInTx(ctx, tx, id, updates, expectedRevision, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// DeleteIssueIfMatch implements storage.ConditionalWriter for the embedded store —
// a guarded delete.
func (s *EmbeddedDoltStore) DeleteIssueIfMatch(ctx context.Context, id string, expectedRevision *int64) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.DeleteIssueIfMatchInTx(ctx, tx, id, expectedRevision)
	})
}

// CloseIssueIfMatch implements storage.ConditionalWriter for the embedded store —
// a guarded close. Writes are serialized by withConn, so the guarded UPDATE's
// zero-row miss is the terminal precondition signal (no commit-time retry needed).
func (s *EmbeddedDoltStore) CloseIssueIfMatch(ctx context.Context, id, reason, actor, session string, expectedRevision *int64) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := issueops.CloseIssueIfMatchInTx(ctx, tx, id, reason, actor, session, expectedRevision)
		return err
	})
}
