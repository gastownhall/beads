package versioncontrolops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// This file holds additive driver primitives for a fast-forward "smart
// migrate" gate: checking whether local HEAD is a strict ancestor of a
// cached ref, checking the working set is clean (ignoring wisp tables), and
// performing the actual fast-forward-only adopt. Nothing in this package or
// elsewhere calls these yet — they are wired into the smart migrate gate in
// a later change.

// LocalIsStrictAncestorOf reports whether local HEAD is a STRICT ancestor of
// ref in the Dolt commit graph: local has zero commits that ref lacks
// (ahead == 0) and at least one commit that local lacks (behind >= 1). A
// local HEAD equal to ref (ahead == 0, behind == 0) is NOT a strict
// ancestor, and returns false.
//
// ref must already be present locally (e.g. a cached remote-tracking ref
// such as "origin/main" after a fetch); this performs no fetch of its own.
func LocalIsStrictAncestorOf(ctx context.Context, db DBConn, ref string) (bool, error) {
	if err := issueops.ValidateRef(ref); err != nil {
		return false, fmt.Errorf("invalid ref: %w", err)
	}

	// Dolt's AS OF requires a literal ref, not a bind parameter; ref was
	// validated above via the shared allowlist regex, mirroring the same
	// ahead/behind pattern used by EmbeddedDoltStore.SyncStatus
	// (internal/storage/embeddeddolt/federation.go).
	//nolint:gosec // G201: ref validated by ValidateRef above — AS OF requires a literal
	query := fmt.Sprintf(`
		SELECT
			(SELECT COUNT(*) FROM dolt_log WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log AS OF '%s')) AS ahead,
			(SELECT COUNT(*) FROM dolt_log AS OF '%s' WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log)) AS behind
	`, ref, ref)

	var ahead, behind int
	if err := db.QueryRowContext(ctx, query).Scan(&ahead, &behind); err != nil {
		return false, fmt.Errorf("compare local HEAD to %s: %w", ref, err)
	}

	return ahead == 0 && behind >= 1, nil
}

// WorkingSetClean reports whether the Dolt working set has no uncommitted
// changes, EXCLUDING dolt-ignored wisp tables ("wisps" and "wisp_*"), which
// cannot be staged/committed and so should never block a clean-working-set
// gate. This mirrors the exclusion in DirtyTableTracker.MarkDirty
// (commit.go), but — unlike the unexported workingSetClean in
// mergesettle.go, which does not exclude wisps and swallows its query
// error — this reports the error to the caller instead of treating it as
// dirty.
func WorkingSetClean(ctx context.Context, db DBConn) (bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		return false, fmt.Errorf("query dolt_status: %w", err)
	}
	defer rows.Close()

	clean := true
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return false, fmt.Errorf("scan dolt_status: %w", err)
		}
		if table == "wisps" || strings.HasPrefix(table, "wisp_") {
			continue
		}
		clean = false
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate dolt_status: %w", err)
	}

	return clean, nil
}

// FastForwardAdopt fast-forwards the current branch to ref via
// CALL DOLT_MERGE('--ff-only', ref). ref must already be cached locally
// (e.g. a remote-tracking ref updated by a prior fetch); this performs no
// fetch of its own and fails if the merge would not be a pure fast-forward.
func FastForwardAdopt(ctx context.Context, db DBConn, ref string) error {
	if err := issueops.ValidateRef(ref); err != nil {
		return fmt.Errorf("invalid ref: %w", err)
	}

	if _, err := db.ExecContext(ctx, "CALL DOLT_MERGE('--ff-only', ?)", ref); err != nil {
		return fmt.Errorf("fast-forward to %s: %w", ref, err)
	}
	return nil
}
