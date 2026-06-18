package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// explicitDoltCommit performs an intentional, user-initiated commit — the
// shared logic behind 'bd dolt commit' and 'bd vc commit'.
//
// Explicit commits are flush points and include the config table, the same
// rationale CommitPending documents for GH#2455. The old path called
// Commit(), which excludes config and returns nil early on a config-only
// working set — both commands then printed success while committing
// nothing, and the next 'bd dolt pull' failed on the still-dirty config
// table (GH#4078).
//
// Returns whether a commit was actually created so callers can report
// truthfully ("Committed." vs "Nothing to commit").
func explicitDoltCommit(ctx context.Context, st storage.DoltStorage, message string) (bool, error) {
	// No custom message: CommitPending already does exactly this job —
	// config-inclusive (CommitWithConfig), truthful committed bool, and a
	// descriptive summary message built from the working set.
	if strings.TrimSpace(message) == "" {
		return st.CommitPending(ctx, getActor())
	}

	// Custom message: check for committable changes first so a clean
	// working set reports "nothing to commit" instead of a fake success.
	// Same optional-capability assertion as workingSetHasUnflaggedWrites.
	if checker, ok := storage.UnwrapStore(st).(interface {
		HasPendingChanges(ctx context.Context) (bool, error)
	}); ok {
		pending, err := checker.HasPendingChanges(ctx)
		if err != nil {
			return false, fmt.Errorf("checking pending changes: %w", err)
		}
		if !pending {
			return false, nil
		}
	}

	if err := st.CommitWithConfig(ctx, message); err != nil {
		// Embedded CommitWithConfig surfaces dolt's raw "nothing to
		// commit" error (the server store swallows it) — either way it is
		// the truthful not-committed outcome, not a failure.
		if isDoltNothingToCommit(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
