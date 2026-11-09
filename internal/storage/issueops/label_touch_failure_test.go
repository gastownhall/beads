package issueops

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// GH#5442's rollback contract: the updated_at stamp shares the label write's
// transaction, so a touch that fails must fail the whole mutation — the label
// row and the timestamp land together or not at all — and must fail BEFORE
// the derived event, journal and version writes, so a rejected mutation
// leaves none of them behind. sqlmock scripts the observable: the label
// write reports a real change, the touch UPDATE errors, and any statement
// the mutation attempted afterwards (an events INSERT, a journal row, a
// version mint) surfaces here as an unexpected call, failing the run.
func TestLabelTouchFailureAbortsTheMutation(t *testing.T) {
	t.Parallel()
	touchStmt := regexp.QuoteMeta("UPDATE issues SET updated_at = ? WHERE id = ?")
	touchErr := errors.New("stamp: engine unavailable")

	for _, tc := range []struct {
		name   string
		script func(t *testing.T) error
	}{
		{"add", func(t *testing.T) error {
			_, mock, tx := beginMockTx(t)
			mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)")).
				WithArgs("i1", "l1").
				WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec(touchStmt).
				WithArgs(sqlmock.AnyArg(), "i1").
				WillReturnError(touchErr)
			return addLabelInTx(context.Background(), tx, "labels", "events", "i1", "l1", "actor", true)
		}},
		{"remove", func(t *testing.T) error {
			_, mock, tx := beginMockTx(t)
			mock.ExpectExec(regexp.QuoteMeta("DELETE FROM labels WHERE issue_id = ? AND label = ?")).
				WithArgs("i1", "l1").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(touchStmt).
				WithArgs(sqlmock.AnyArg(), "i1").
				WillReturnError(touchErr)
			return removeLabelInTx(context.Background(), tx, "labels", "events", "i1", "l1", "actor", true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.script(t); !errors.Is(err, touchErr) {
				t.Fatalf("mutation error = %v, want the touch error %v", err, touchErr)
			}
		})
	}
}
