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
