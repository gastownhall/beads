package storage

import (
	"errors"
	"fmt"
	"strings"
)

// ErrCommitVerificationFailed marks a CommitVerificationError through any
// wrapping, so callers can match the failure class without importing the
// concrete type: a required completeness read failed after a full embedded
// commit, so the store cannot confirm whether tracked changes were left
// behind (GH#5433).
var ErrCommitVerificationFailed = errors.New("commit verification failed")

// CommitVerificationOutcome records DOLT_COMMIT's own answer when a
// required completeness read failed after it, so the error can say what is
// known and what is not — never more.
type CommitVerificationOutcome string

const (
	// CommitSucceeded is DOLT_COMMIT's success response: the commit may
	// have landed, but completeness could not be verified.
	CommitSucceeded CommitVerificationOutcome = "commit_succeeded"
	// CommitNothingToCommit is DOLT_COMMIT's tolerated nothing-to-commit
	// response: working-set cleanliness could not be verified, so the
	// completeness outcome is unknown.
	CommitNothingToCommit CommitVerificationOutcome = "nothing_to_commit"
)

// CommitVerificationError reports that a full embedded commit's required
// completeness verification could not run: the post-commit dolt_status or
// owned-policy read failed after the DOLT_COMMIT, so the store cannot say
// whether the stall (tracked changes skipped by staging) happened (GH#5433).
// It replaces a clean/success claim — it is an error — but it never claims
// the mutation failed and never prescribes replaying it: Outcome carries
// DOLT_COMMIT's own answer, and the message keeps what is verified apart
// from what is not.
type CommitVerificationError struct {
	// Outcome is DOLT_COMMIT's own response, recorded before the failed
	// read: CommitSucceeded means it reported success, CommitNothingToCommit
	// means it reported nothing to commit.
	Outcome CommitVerificationOutcome
	// Operation names the required status or policy read that failed.
	Operation string
	// Cause is the underlying read error.
	Cause error
}

func (e *CommitVerificationError) Error() string {
	var b strings.Builder
	b.WriteString("commit verification failed: ")
	switch e.Outcome {
	case CommitSucceeded:
		b.WriteString("DOLT_COMMIT reported success; the commit may have landed, but completeness could not be verified")
	default:
		b.WriteString("DOLT_COMMIT reported nothing to commit; working-set cleanliness could not be verified, so the completeness outcome is unknown")
	}
	fmt.Fprintf(&b, " (failed operation: %s: %v)", e.Operation, e.Cause)
	b.WriteString("; inspect the working set and the dolt_ignore configuration before retrying the commit; do not replay the writes")
	return b.String()
}

// Is matches the sentinel so errors.Is finds the failure class through any
// wrapper; the underlying cause matches through Unwrap.
func (e *CommitVerificationError) Is(target error) bool {
	return target == ErrCommitVerificationFailed
}

// Unwrap exposes the underlying read error: errors.Is(err, cause) holds for
// the failed operation through any wrapper.
func (e *CommitVerificationError) Unwrap() error {
	return e.Cause
}

// UncommittedChangesError reports a full embedded commit (DOLT_COMMIT '-Am')
// that left tracked changes behind in the working set (GH#5433). An enabled
// dolt_ignore pattern covering a tracked table makes Dolt skip staging that
// table while its working-set delta stays, so the commit answers success —
// committing the other dirty tables — or "nothing to commit", with data that
// will never reach HEAD and never be pushed.
//
// The fields are forensic, not an asserted cause: Tables are exactly what
// dolt_status still lists, and Patterns are the enabled ignore patterns that
// are not Beads' canonical clone-local set, which suggest where the skip
// comes from without proving who wrote them.
type UncommittedChangesError struct {
	// Tables are the dolt_status table_name values that remain changed after
	// the commit: the tracked tables whose changes are saved in the working
	// set but not in HEAD. A rename appears in Dolt's "<from> -> <to>"
	// spelling.
	Tables []string
	// Patterns are enabled dolt_ignore patterns outside Beads' canonical
	// clone-local set that plausibly cover the remaining tables. It is an
	// optional diagnostic: empty when no such pattern exists or when the
	// read that collects them failed.
	Patterns []string
	// Committed reports DOLT_COMMIT's own response. True means it reported
	// success and committed the changes outside the remaining tables — a
	// partial publication; false means it reported nothing to commit, so
	// nothing landed at all.
	Committed bool
}

func (e *UncommittedChangesError) Error() string {
	var b strings.Builder
	b.WriteString("commit incomplete: ")
	if e.Committed {
		b.WriteString("DOLT_COMMIT reported success, but tracked changes remain in ")
	} else {
		b.WriteString("tracked changes remain in ")
	}
	b.WriteString(strings.Join(e.Tables, ", "))
	b.WriteString("; these changes are not in HEAD and push will not send them")
	if len(e.Patterns) > 0 {
		b.WriteString(". Enabled dolt_ignore pattern(s) that may cover them: ")
		b.WriteString(strings.Join(e.Patterns, ", "))
		b.WriteString(" (suggested cause, not proof)")
	}
	b.WriteString(". The writes are saved in the working set: back up the store, inspect the dolt_ignore configuration, correct unwanted patterns, and retry the commit; do not replay the writes")
	return b.String()
}
