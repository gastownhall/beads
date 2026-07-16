package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Compile-time assertion that DoltStore provides the CAS capability.
var _ storage.ConditionalWriter = (*DoltStore)(nil)

// runIssueWrite runs fn inside a retrying write transaction, then applies Dolt
// versioning (DOLT_ADD/DOLT_COMMIT) for durable issues and skips it for wisps
// (dolt-ignored tables carry no history).
//
// It routes through withRetryTx so that a Dolt serialization conflict (1213) —
// which is how two concurrent conditional writers to one bead collide at commit —
// replays fn. On replay the loser's guarded UPDATE re-evaluates against the
// winner's committed value and returns a *PreconditionFailedError (a
// non-serialization error, surfaced as-is). This is what turns a raw commit-time
// conflict into a clean compare-and-swap failure. A commit-phase connection loss
// is NOT retried (withRetryTx returns an indeterminate error) to avoid a
// double-apply.
func (s *DoltStore) runIssueWrite(ctx context.Context, id, commitMsg string, fn func(tx *sql.Tx) error) error {
	isWisp := s.isActiveWisp(ctx, id)
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if !isWisp {
			for _, table := range []string{"issues", "events"} {
				_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
			}
			if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
				commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
				return fmt.Errorf("dolt commit: %w", err)
			}
		}
		return nil
	})
}

// CompareAndSetMetadataKey implements storage.ConditionalWriter. It atomically
// sets a single metadata key iff its current value matches expected (nil =
// claim-once). Returns the post-write issue on success, or a
// *storage.PreconditionFailedError on a guard miss.
func (s *DoltStore) CompareAndSetMetadataKey(ctx context.Context, id, key string, expected *string, newValue, actor string) (*types.Issue, error) {
	err := s.runIssueWrite(ctx, id, fmt.Sprintf("bd: cas %s %s", id, key), func(tx *sql.Tx) error {
		return issueops.CompareAndSetMetadataKeyInTx(ctx, tx, id, key, expected, newValue, actor)
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// CompareAndClearMetadataKey implements storage.ConditionalWriter — the guarded
// release symmetric with CompareAndSetMetadataKey.
func (s *DoltStore) CompareAndClearMetadataKey(ctx context.Context, id, key, expected, actor string) (*types.Issue, error) {
	err := s.runIssueWrite(ctx, id, fmt.Sprintf("bd: cas-clear %s %s", id, key), func(tx *sql.Tx) error {
		return issueops.CompareAndClearMetadataKeyInTx(ctx, tx, id, key, expected, actor)
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// UpdateIssueIfMatch implements storage.ConditionalWriter — whole-row optimistic
// concurrency. It routes through runIssueWrite so a commit-time serialization
// conflict (1213) — how two concurrent writers to one bead collide — replays fn;
// on replay the loser's guarded UPDATE re-evaluates against the winner's committed
// revision and returns a clean *storage.PreconditionFailedError.
func (s *DoltStore) UpdateIssueIfMatch(ctx context.Context, id string, updates map[string]interface{}, expectedRevision *int64, actor string) (*types.Issue, error) {
	err := s.runIssueWrite(ctx, id, fmt.Sprintf("bd: update %s", id), func(tx *sql.Tx) error {
		if err := s.checkConditionalWriteGate(ctx, tx); err != nil {
			return err
		}
		_, err := issueops.UpdateIssueIfMatchInTx(ctx, tx, id, updates, expectedRevision, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetIssue(ctx, id)
}

// CloseIssueIfMatch implements storage.ConditionalWriter — a guarded close. Like
// UpdateIssueIfMatch it routes through runIssueWrite (which commits issues+events,
// exactly as CloseIssue does) so a commit-time 1213 replays and the loser's guarded
// close re-evaluates against the winner's committed revision, returning a clean
// *storage.PreconditionFailedError. An already-closed issue at the expected
// revision is an idempotent success.
func (s *DoltStore) CloseIssueIfMatch(ctx context.Context, id, reason, actor, session string, expectedRevision *int64) error {
	return s.runIssueWrite(ctx, id, fmt.Sprintf("bd: close %s", id), func(tx *sql.Tx) error {
		if err := s.checkConditionalWriteGate(ctx, tx); err != nil {
			return err
		}
		_, err := issueops.CloseIssueIfMatchInTx(ctx, tx, id, reason, actor, session, expectedRevision)
		return err
	})
}

// DeleteIssueIfMatch implements storage.ConditionalWriter — a guarded delete. Like
// UpdateIssueIfMatch it runs under withRetryTx for the 1213 replay, but commits
// the full delete-cascade table set (mirroring DeleteIssue). Wisps are
// dolt-ignored, so their delete skips DOLT_COMMIT.
func (s *DoltStore) DeleteIssueIfMatch(ctx context.Context, id string, expectedRevision *int64) error {
	isWisp := s.isActiveWisp(ctx, id)
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if err := s.checkConditionalWriteGate(ctx, tx); err != nil {
			return err
		}
		if err := issueops.DeleteIssueIfMatchInTx(ctx, tx, id, expectedRevision); err != nil {
			return err
		}
		if isWisp {
			return nil
		}
		for _, table := range []string{"issues", "dependencies", "labels", "comments", "events", "child_counters", "issue_snapshots", "compaction_snapshots"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			fmt.Sprintf("bd: delete %s", id), s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}
