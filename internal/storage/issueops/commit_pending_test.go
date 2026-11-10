package issueops

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestIsNothingToCommitErrorRejectsResidualChanges pins the classifier's
// typed pre-check (GH#5433 AC-4): a commit that verifiably left tracked
// changes behind must survive no-op classification under every caller's
// policy, so the residual finding is rejected by type before the string
// match — including wrappers, and including a table or pattern name that
// reads "nothing to commit", which the string match alone would happily
// misread as a healthy no-op.
func TestIsNothingToCommitErrorRejectsResidualChanges(t *testing.T) {
	plain := &storage.UncommittedChangesError{
		Tables:    []string{"issues"},
		Patterns:  []string{"issues"},
		Committed: false,
	}
	// The misleading name: a table (or pattern) whose own text contains the
	// phrase the string match keys on.
	misleading := &storage.UncommittedChangesError{
		Tables:    []string{"nothing to commit"},
		Patterns:  []string{"nothing to commit"},
		Committed: true,
	}
	for name, err := range map[string]error{
		"plain":           plain,
		"misleading name": misleading,
		"wrapped":         fmt.Errorf("commit pending before push: %w", plain),
		"double wrapped":  fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", misleading)),
		"errors.Is chain": fmt.Errorf("shutdown flush: %w", misleading),
	} {
		if IsNothingToCommitError(err) {
			t.Errorf("IsNothingToCommitError(%s residual) = true, want false: the typed finding must survive no-op classification", name)
		}
	}
	// The healthy Dolt response keeps its classification, and the typed
	// rejection does not narrow anything for plain errors that merely
	// mention a commit.
	for name, err := range map[string]error{
		"dolt phrase":      errors.New("Error 1105 (HY000): nothing to commit, working tree clean"),
		"wrapped dolt":     fmt.Errorf("dolt commit: %w", errors.New("nothing to commit")),
		"unrelated commit": errors.New("failed to run commit"),
		"nil":              nil,
	} {
		want := name == "dolt phrase" || name == "wrapped dolt"
		if got := IsNothingToCommitError(err); got != want {
			t.Errorf("IsNothingToCommitError(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestIsNothingToCommitErrorRejectsVerificationErrors pins the classifier's
// typed pre-check for the GH#5433 AC-5 verification failure: a commit whose
// required completeness read failed is never a healthy no-op. The
// nothing_to_commit outcome's message quotes Dolt's own "nothing to commit"
// response, so the string match alone would misclassify it — the typed
// rejection is the only honest classification, through any wrapper.
func TestIsNothingToCommitErrorRejectsVerificationErrors(t *testing.T) {
	readErr := errors.New("dolt_status read failed")
	for name, err := range map[string]error{
		"commit_succeeded": &storage.CommitVerificationError{
			Outcome:   storage.CommitSucceeded,
			Operation: "read dolt_status after the commit",
			Cause:     readErr,
		},
		"nothing_to_commit": &storage.CommitVerificationError{
			Outcome:   storage.CommitNothingToCommit,
			Operation: "load the Beads-owned dolt_ignore policy",
			Cause:     readErr,
		},
		"wrapped":       fmt.Errorf("commit pending before push: %w", &storage.CommitVerificationError{Outcome: storage.CommitNothingToCommit, Cause: readErr}),
		"sentinel only": fmt.Errorf("guard: %w", storage.ErrCommitVerificationFailed),
	} {
		if IsNothingToCommitError(err) {
			t.Errorf("IsNothingToCommitError(%s) = true, want false: an unverified commit outcome is not a healthy no-op", name)
		}
		if name != "sentinel only" {
			var typed *storage.CommitVerificationError
			if !errors.As(err, &typed) {
				t.Errorf("errors.As(%s) = false, want the typed payload through the wrapper", name)
			}
			if !errors.Is(err, readErr) {
				t.Errorf("errors.Is(%s, cause) = false, want the underlying read error through Unwrap", name)
			}
		}
		if !errors.Is(err, storage.ErrCommitVerificationFailed) {
			t.Errorf("errors.Is(%s, ErrCommitVerificationFailed) = false, want the sentinel through any wrapper", name)
		}
	}
}
