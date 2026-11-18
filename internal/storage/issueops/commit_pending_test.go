package issueops

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestIsNothingToCommitErrorRejectsVerificationErrors: a commit-completeness
// finding is never Dolt's "nothing to commit" no-op, however it is wrapped
// and whatever the remaining tables or patterns are called (GH#5433). Every
// caller that tolerates the no-op would otherwise swallow it.
func TestIsNothingToCommitErrorRejectsVerificationErrors(t *testing.T) {
	residuals := map[string]error{
		"refused": &storage.UncommittedChangesError{Tables: []string{"issues"}},
		"partial": &storage.UncommittedChangesError{Tables: []string{"issues"}, Committed: true},
		"misleading names": &storage.UncommittedChangesError{
			Tables:         []string{"nothing to commit", "no changes to commit"},
			IgnorePatterns: []string{"nothing to commit%"},
		},
		"unverified no-op": &storage.CommitVerificationError{
			Outcome:   storage.CommitOutcomeNothingToCommit,
			Operation: "read dolt_status",
			Cause:     errors.New("nothing to commit"),
		},
		"unverified commit": &storage.CommitVerificationError{
			Outcome:   storage.CommitOutcomeSucceeded,
			Operation: "read dolt_status",
			Cause:     errors.New("no changes to commit"),
		},
		"sentinel": fmt.Errorf("%w: nothing to commit", storage.ErrCommitVerificationFailed),
	}
	for name, residual := range residuals {
		for wrapName, err := range map[string]error{
			"bare":    residual,
			"wrapped": fmt.Errorf("dolt commit: %w", residual),
			"joined":  errors.Join(errors.New("nothing to commit"), fmt.Errorf("x: %w", residual)),
		} {
			if IsNothingToCommitError(err) {
				t.Errorf("%s/%s: IsNothingToCommitError(%q) = true, want false", name, wrapName, err)
			}
		}
	}
	if !IsNothingToCommitError(fmt.Errorf("dolt commit: %w", errors.New("nothing to commit, working tree clean"))) {
		t.Error("IsNothingToCommitError no longer recognizes Dolt's own no-op response")
	}
}
