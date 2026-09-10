package dolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// RecordProvenanceEvent validates and appends a provenance event, then commits
// the provenance_events table. The id is always derived deterministically from
// the event's idempotency basis, so a duplicate insert is ignored: inserted is
// false when the id already existed. Append-only — there is no update or delete
// path.
func (s *DoltStore) RecordProvenanceEvent(ctx context.Context, ev types.ProvenanceEvent) (id string, inserted bool, err error) {
	err = s.withRetryTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		id, inserted, txErr = issueops.RecordProvenanceEventInTx(ctx, tx, ev)
		return txErr
	})
	if err != nil {
		return "", false, err
	}
	if inserted {
		if err := s.doltAddAndCommit(ctx, []string{"provenance_events"},
			fmt.Sprintf("bd: provenance %s %s", ev.Kind, ev.IssueID)); err != nil {
			return "", false, err
		}
	}
	return id, inserted, nil
}

// GetProvenanceEvents returns the provenance events for an issue, ordered by
// occurred_at (nulls last) then id. A non-empty kindFilter restricts to one kind.
func (s *DoltStore) GetProvenanceEvents(ctx context.Context, issueID, kindFilter string) ([]types.ProvenanceEvent, error) {
	var result []types.ProvenanceEvent
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetProvenanceEventsInTx(ctx, tx, issueID, kindFilter)
		return err
	})
	return result, err
}

// GetProvenanceByRef returns every provenance event bound to the opaque ref,
// ordered by occurred_at (nulls last) then id.
func (s *DoltStore) GetProvenanceByRef(ctx context.Context, ref string) ([]types.ProvenanceEvent, error) {
	var result []types.ProvenanceEvent
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetProvenanceByRefInTx(ctx, tx, ref)
		return err
	})
	return result, err
}
