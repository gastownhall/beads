package dolt

import (
	"context"
	"database/sql"
	"errors"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// ErrReadOnly is returned when an EXPLICIT sweep is asked of a store opened
// read-only. The lazy sweep behind a ready read stays silent in that mode —
// a listing must not fail because the client cannot write — but a caller that
// asked for a sweep by name is owed the reason it did not happen.
var ErrReadOnly = errors.New("dolt: store is read-only")

// RunScheduledSweeps runs the two lazy time-based sweeps — defer wake and due
// trigger — on demand, and REPORTS what fired.
//
// It is the same pass a ready read triggers (runScheduledSweeps below is now
// this function under the read paths' advisory contract), with the two
// differences an explicit caller needs: the result names the beads that fired
// rather than discarding them, and a failure is returned rather than reduced
// to a warning. `bd due sweep` stands on it, and so does the external clock
// that owns the due rail — a timer that cannot tell a failed sweep from an
// empty one is not a clock, it is a silence.
func (s *DoltStore) RunScheduledSweeps(ctx context.Context) (issueops.ScheduledSweepResult, error) {
	var swept issueops.ScheduledSweepResult
	if s.readOnly {
		return swept, ErrReadOnly
	}
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		return s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (issueops.ChangedTables, string, error) {
			// Reset per attempt: runIssueOperationTxWithMessage may retry the
			// body, and a partial result from an abandoned attempt must not
			// survive into the one that commits.
			swept = issueops.ScheduledSweepResult{}
			var err error
			swept, err = issueops.RunScheduledSweepsInTx(ctx, tx)
			if err != nil {
				return nil, "", err
			}
			if swept.IssueRows() == 0 {
				// Wisp-only sweeps persist with the SQL commit but mint no
				// version commit: wisp tables are dolt_ignored.
				return nil, "", nil
			}
			tables := issueops.ChangedTables{}
			tables.Add("issues", "events")
			return tables, swept.CommitMessage(), nil
		})
	})
	if err != nil {
		return issueops.ScheduledSweepResult{}, err
	}
	return swept, nil
}
