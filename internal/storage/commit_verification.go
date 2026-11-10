package storage

import (
	"errors"
	"fmt"
	"strings"
)

// UncommittedChangesError reports that a full-working-set commit
// (DOLT_COMMIT('-Am')) left tracked changes behind: Dolt answered with success
// or with "nothing to commit", yet dolt_status still lists tables that are not
// Beads-owned clone-local state. Those changes are saved in the working set
// but are not in HEAD, so a push will not send them (GH#5433).
//
// It is a verification finding, not a write failure: the mutations that
// dirtied the tables have landed, and replaying them would double-apply.
type UncommittedChangesError struct {
	// Tables are the dolt_status table names still dirty after the commit,
	// sorted and de-duplicated. A rename row keeps Dolt's "old -> new" form.
	Tables []string
	// IgnorePatterns are enabled dolt_ignore patterns outside Beads'
	// canonical clone-local set that may cover the tables above. They
	// suggest a cause without proving it or who added them, and are empty
	// when there are none or the optional read failed.
	IgnorePatterns []string
	// Committed reports that DOLT_COMMIT did create a commit (a partial
	// commit): some changes reached HEAD, the tables above did not.
	Committed bool
}

func (e *UncommittedChangesError) Error() string {
	tables := strings.Join(e.Tables, ", ")
	var b strings.Builder
	if e.Committed {
		fmt.Fprintf(&b, "commit incomplete: a commit was created, but tracked changes remain in %s", tables)
	} else {
		fmt.Fprintf(&b, "commit incomplete: tracked changes remain in %s", tables)
	}
	b.WriteString("; these changes are not in HEAD and push will not send them")
	if len(e.IgnorePatterns) > 0 {
		fmt.Fprintf(&b, " (enabled noncanonical dolt_ignore patterns that may cover them, a suggested cause, not proof: %s)", strings.Join(e.IgnorePatterns, ", "))
	}
	b.WriteString("; the writes may still be saved in the working set: back up the store, inspect and correct any unwanted configuration, then retry the commit; do not replay the mutations")
	return b.String()
}

// ErrCommitVerificationFailed marks a commit whose completeness could not be
// checked: a required read after DOLT_COMMIT failed, so the store cannot
// claim the working set is clean. Match it with errors.Is; the payload is a
// *CommitVerificationError.
var ErrCommitVerificationFailed = errors.New("commit verification failed")

// CommitOutcome records what DOLT_COMMIT itself reported before the
// verification read failed.
type CommitOutcome string

const (
	// CommitOutcomeSucceeded: DOLT_COMMIT reported a new commit.
	CommitOutcomeSucceeded CommitOutcome = "commit_succeeded"
	// CommitOutcomeNothingToCommit: DOLT_COMMIT reported nothing to commit.
	CommitOutcomeNothingToCommit CommitOutcome = "nothing_to_commit"
)

// CommitVerificationError is the payload of ErrCommitVerificationFailed. It
// never claims the mutation failed: the SQL transaction carrying DOLT_COMMIT
// was published, and only the completeness check after it is unknown.
type CommitVerificationError struct {
	// Outcome is DOLT_COMMIT's own response.
	Outcome CommitOutcome
	// Operation names the required read that failed.
	Operation string
	// Cause is the read's error.
	Cause error
}

func (e *CommitVerificationError) Error() string {
	var head string
	switch e.Outcome {
	case CommitOutcomeSucceeded:
		head = "DOLT_COMMIT reported success; the commit may have landed, but completeness could not be verified"
	default:
		head = "DOLT_COMMIT reported nothing to commit; working-set cleanliness could not be verified, so the completeness outcome is unknown"
	}
	return fmt.Sprintf("%s: %s (%s: %v); inspect the working set and history before retrying the commit; do not replay the mutations",
		ErrCommitVerificationFailed, head, e.Operation, e.Cause)
}

// Is matches ErrCommitVerificationFailed.
func (e *CommitVerificationError) Is(target error) bool {
	return target == ErrCommitVerificationFailed
}

// Unwrap exposes the failed read's error.
func (e *CommitVerificationError) Unwrap() error {
	return e.Cause
}
