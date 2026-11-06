package schema

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// The is_blocked repair for gastownhall/beads#7037. Frozen migration 0059 and
// its ignored twin 0015 recompute is_blocked with a recursive join that
// dolthub/dolt#11886 mis-evaluates on engines before go-mysql-server#3899, so
// stores that crossed them there hold flags their graph does not imply. Main
// 0075 and ignored 0032 are inert markers; the hooks below do the work.
//
// The owed-repair key is the hand-off between them. Main 0075 records it in
// the step that records 0075 itself, so it is committed before any flag
// changes. Ignored 0032 trusts only the copy committed at HEAD: a working-set
// key is not yet part of any history a peer can see, and the commit that
// carries the repaired flags must also be the one that removes it.
//
// Journal decision: a documented migration-repair exemption at this call
// site. Both hooks run on the pinned migration session with journal
// activation absent, so neither the owed full repair nor the key-free
// wisp-only pass writes a journal row or moves the seq: historical
// schema-repair passes are not operational mutations and create no replay
// event for the derived flags they correct. Consumers that read readiness
// only from the journal must refresh their snapshot after this upgrade (see
// internal/storage/issueops/journal.go and the upgrade notes). The recompute
// APIs keep their journaling wherever a real transaction activates it; do
// not activate it here without a transactional design of its own.

// isBlockedRepairOwedKey is the metadata key main 0075 records when the store
// holds issues whose flags a full recompute must check.
const isBlockedRepairOwedKey = "is_blocked_repair_owed_0075"

// isBlockedRepairIgnoredVersion is the ignored marker that performs the repair.
const isBlockedRepairIgnoredVersion = 32

// isBlockedRepairMainVersion is the main marker that records the owed key.
const isBlockedRepairMainVersion = 75

// isBlockedRepairInputTables are the tracked tables the repair reads and
// commits: the flags, the graph they derive from, and the owed key.
var isBlockedRepairInputTables = []string{"issues", "dependencies", "metadata"}

// refuseDirtyIsBlockedRepairInputs refuses a pass that may perform the owed
// repair (main 0075 pending, or its key committed at HEAD) while a repair
// input carries uncommitted changes the repair commit would otherwise sweep
// in. It runs before any write of the pass, and its error is the typed one
// the main pending-migration guard uses, with the same `bd dolt commit`
// recovery. The raw-text guard already covers issues and metadata through
// 0075's header; dependencies is only read by the recompute, so only this
// guard sees it. No probe runs unless an input table is dirty.
func refuseDirtyIsBlockedRepairInputs(ctx context.Context, db DBConn, mainVersionBefore int, dirtyBefore map[string]dirtyTableState) error {
	var dirty []string
	for _, table := range isBlockedRepairInputTables {
		if _, ok := dirtyBefore[table]; ok {
			dirty = append(dirty, table)
		}
	}
	if len(dirty) == 0 {
		return nil
	}
	owed := mainVersionBefore < isBlockedRepairMainVersion
	if !owed {
		var err error
		if owed, err = isBlockedRepairOwedAtHead(ctx, db); err != nil {
			return err
		}
	}
	if !owed {
		return nil
	}
	return &DirtyTablesError{Tables: dirty}
}

// isBlockedRepairCommitMessage labels the commit carrying the repaired flags.
const isBlockedRepairCommitMessage = "schema: repair is_blocked flags left by 0059 (gastownhall/beads#7037)"

// recordIsBlockedRepairOwed is the pre-0075 repair. It records the owed key
// when the store holds issues and re-arms ignored 0032 on this clone, so the
// ignored pass later in the same MigrateUp performs the repair.
func recordIsBlockedRepairOwed(ctx context.Context, db DBConn) error {
	var hasIssues int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM issues LIMIT 1) AS any_issue").Scan(&hasIssues); err != nil {
		return fmt.Errorf("checking for issues to repair: %w", err)
	}
	if hasIssues > 0 {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO metadata (`key`, value) VALUES (?, 'full') ON DUPLICATE KEY UPDATE value = 'full'",
			isBlockedRepairOwedKey); err != nil {
			return fmt.Errorf("recording owed is_blocked repair: %w", err)
		}
	}
	return rearmIgnoredIsBlockedRepair(ctx, db)
}

// rearmIgnoredIsBlockedRepair deletes this clone's ignored cursor rows from
// 0032 up, so the ignored pass runs 0032 (and replays every later ignored
// step) again. Rows below 0032 are kept. A clone whose ignored cursor table
// does not exist yet runs the whole ignored series anyway.
func rearmIgnoredIsBlockedRepair(ctx context.Context, db DBConn) error {
	exists, err := schemaTableExists(ctx, db, ignoredSource.cursorTable)
	if err != nil {
		return fmt.Errorf("checking %s: %w", ignoredSource.cursorTable, err)
	}
	if !exists {
		return nil
	}
	//nolint:gosec // G202: the cursor table name is a package constant.
	if _, err := db.ExecContext(ctx,
		"DELETE FROM "+ignoredSource.cursorTable+" WHERE version >= ?", isBlockedRepairIgnoredVersion); err != nil {
		return fmt.Errorf("re-arming ignored migration %04d: %w", isBlockedRepairIgnoredVersion, err)
	}
	return nil
}

// isBlockedRepairOwedAtHead reports whether the owed key is committed at HEAD.
// It lists HEAD before selecting from it so the probe cannot fail on a store
// whose HEAD predates the metadata table (be-bv7x, see tableTrackedAtHead).
func isBlockedRepairOwedAtHead(ctx context.Context, db DBConn) (bool, error) {
	tracked, err := tableTrackedAtHead(ctx, db, "", "metadata")
	if err != nil || !tracked {
		return false, err
	}
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM metadata AS OF 'HEAD' WHERE `key` = ?", isBlockedRepairOwedKey).Scan(&n); err != nil {
		return false, fmt.Errorf("reading owed is_blocked repair at HEAD: %w", err)
	}
	return n > 0, nil
}

// repairIsBlockedFlags is the pre-ignored-0032 repair. With the owed key
// committed it recomputes both planes, removes the key, and commits issues,
// dependencies and metadata together, returning only after that commit succeeded.
// Without the key the tracked half is already repaired (or was never owed),
// so it recomputes only this clone's wisps, which are never committed.
func repairIsBlockedFlags(ctx context.Context, db DBConn) error {
	owed, err := isBlockedRepairOwedAtHead(ctx, db)
	if err != nil {
		return err
	}
	if !owed {
		corrected, err := issueops.RecomputeAllWispIsBlockedInTx(ctx, db)
		if err != nil {
			return fmt.Errorf("recomputing wisp is_blocked: %w", err)
		}
		if corrected > 0 {
			fmt.Fprintf(stderr, "Recomputed is_blocked for local wisps (gastownhall/beads#7037): flipped %d flag(s)\n", corrected)
		}
		return nil
	}
	fmt.Fprintln(stderr, "Recomputing is_blocked for every issue and wisp (gastownhall/beads#7037)…")
	corrected, err := issueops.RecomputeAllIsBlockedInTx(ctx, db)
	if err != nil {
		return fmt.Errorf("recomputing is_blocked: %w", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM metadata WHERE `key` = ?", isBlockedRepairOwedKey); err != nil {
		return fmt.Errorf("clearing owed is_blocked repair: %w", err)
	}
	// dependencies rides along: the same pass's rekey may have rewritten or
	// collapsed edges, and the flags above were derived from that graph.
	if err := commitScopedTablesChange(ctx, db, isBlockedRepairCommitMessage, "issues", "dependencies", "metadata"); err != nil {
		return fmt.Errorf("committing is_blocked repair: %w", err)
	}
	fmt.Fprintf(stderr, "  flipped %d is_blocked flag(s)\n", corrected)
	return nil
}
