package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// The is_blocked repair for gastownhall/beads#7037. Migration 0059's recursive
// blocked-propagation join dropped its d.type = 'parent-child' conjunct on
// engines before dolt 2.4.0 (go-mysql-server#3899, dolthub/dolt#11886), so
// stores that crossed it hold is_blocked = 1 on issues that are not blocked:
// they vanish from bd ready. 0059's bytes are frozen, so the repair is a
// marker pair, following the ignored/0031 shape: main 0075 records a durable
// obligation key in metadata, and ignored 0032 runs the maintained full
// recompute (issueops.RecomputeAllIsBlockedInTx) once every prerequisite —
// the whole main pass, the dependency id rekey, and every older ignored step
// — has completed on that clone.
//
// Journal decision: a documented migration-repair exemption. The hooks below
// run on the pinned migration session with journal activation absent, so
// neither the owed full repair nor the debt-free wisp-only pass emits a
// journal row or moves the seq: historical schema-repair passes are not
// operational mutations and do not create replay events for corrected
// derived flags. Consumers relying on the journal alone must refresh their
// readiness snapshot after this upgrade; the journal does not announce these
// corrections (see internal/storage/issueops/journal.go and the upgrade
// notes). This is a call-site exemption only — the recompute APIs keep their
// journaling behavior wherever a project's real transaction activates it.

// isBlockedRepairMigrationVersion is the main migration that records the
// obligation; isBlockedRepairIgnoredVersion is the clone-local marker whose
// hook performs it. The key name embeds the main version deliberately: the
// obligation is forever tied to the migration that minted it.
const (
	isBlockedRepairMigrationVersion = 75
	isBlockedRepairIgnoredVersion   = 32
)

// The obligation key and its one value. "full" is the only value this repair
// mints: the repair owed is always the full recompute.
const (
	isBlockedRepairOwedKey  = "is_blocked_repair_owed_0075"
	isBlockedRepairOwedFull = "full"
)

// recordIsBlockedRepairOwed0075 is the pre-migration repair for main 0075: it
// records the durable obligation when the store has any issue row and re-arms
// the ignored-series repair. Both writes land in 0075's normal per-step
// commit together with its cursor row, so a history that claims 0075 —
// including one a clone receives by pull — always carries the obligation. An
// empty initialization records no key: there is nothing to repair, and
// ignored 0032 runs its debt-free pass instead. The re-arm DELETE only fires
// when the clone-local cursor table exists and holds rows at or above 32; on
// a clone whose ignored cursor is still below 32 the repair is already
// pending and the DELETE is a no-op.
func recordIsBlockedRepairOwed0075(ctx context.Context, db DBConn) error {
	hasIssues, err := schemaTableExists(ctx, db, "issues")
	if err != nil {
		return fmt.Errorf("checking issues table: %w", err)
	}
	if hasIssues {
		var anyIssue bool
		if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM issues)").Scan(&anyIssue); err != nil {
			return fmt.Errorf("checking for issue rows: %w", err)
		}
		if anyIssue {
			if _, err := db.ExecContext(ctx,
				"INSERT INTO metadata (`key`, value) VALUES (?, ?) "+
					"ON DUPLICATE KEY UPDATE value = VALUES(value)",
				isBlockedRepairOwedKey, isBlockedRepairOwedFull); err != nil {
				return fmt.Errorf("recording %s: %w", isBlockedRepairOwedKey, err)
			}
		}
	}
	return rearmIgnoredIsBlockedRepair(ctx, db)
}

// repairMixedIsBlockedFlags0032 is the pre-migration repair for ignored 0032.
// A committed obligation at HEAD invokes the full recompute, clears the key,
// and publishes corrected issue flags together with this pass's
// dependency-graph changes (the rekey's dedup output, usually an empty diff)
// in one scoped repair commit — BEFORE this version's cursor row records, so
// an interrupted pass always either still owes the whole repair or has
// already published it durably. Only after that commit succeeds may the
// runner record 0032.
//
// The entry check re-verifies that issues and metadata carry no uncommitted
// rows: the pass-level typed guard in migrateUpAfterReconcile already refused
// pre-existing user edits to issues, dependencies and metadata before any
// repair write, and nothing else in a healthy pass dirties the two tables
// between that guard and here, so dirt at this point means something is
// wrong mid-pass and the repair must not fold it into its commit.
// dependencies is deliberately NOT checked: the pass's own rekey leaves its
// changes there for this commit to publish. The refusal is a plain error on
// purpose — it fires mid-pass, after main migrations have applied, and a
// lenient open intent must not be able to skip past it (same reasoning as the
// untyped ignored dirty-table guard in migrateUpAfterReconcile).
func repairMixedIsBlockedFlags0032(ctx context.Context, db DBConn) error {
	owed, err := isBlockedRepairOwedAtHead(ctx, db)
	if err != nil {
		return err
	}
	if !owed {
		// A debt-free clone owes nothing on the tracked plane: whichever
		// history it received already carries the repair. Its clone-local
		// wisp rows are repaired against the graph it holds by the thin
		// wisp-plane pass in this hook's debt-free branch (added with the
		// wisp-recompute seam in issueops).
		return nil
	}

	dirty, err := committableDirtyTables(ctx, db)
	if err != nil {
		return fmt.Errorf("reading the working set before the is_blocked repair: %w", err)
	}
	var unexpected []string
	for _, table := range []string{"issues", "metadata"} {
		if _, present := dirty[table]; present {
			unexpected = append(unexpected, table)
		}
	}
	if len(unexpected) > 0 {
		return fmt.Errorf(
			"is_blocked repair: %s hold(s) uncommitted rows the repair would fold into its commit; reconcile the working set (bd dolt commit) and reopen",
			strings.Join(unexpected, ", "))
	}

	fmt.Fprintf(stderr, "Repairing is_blocked flags over-set by migration 0059 (dolthub/dolt#11886)…\n")
	corrected, err := issueops.RecomputeAllIsBlockedInTx(ctx, db)
	if err != nil {
		return fmt.Errorf("recomputing is_blocked (gastownhall/beads#7037): %w", err)
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM metadata WHERE `key` = ?", isBlockedRepairOwedKey); err != nil {
		return fmt.Errorf("clearing %s: %w", isBlockedRepairOwedKey, err)
	}
	if err := stageAndCommitScopedTables(ctx, db,
		"schema: repair is_blocked flags over-set by migration 0059 (gastownhall/beads#7037)",
		"issues", "metadata", "dependencies"); err != nil {
		return fmt.Errorf("committing the is_blocked repair: %w", err)
	}
	// The count is fixpoint flips, not distinct rows and not proof of 0059
	// causation (a parent flip cascades through later passes); the upgrade
	// notes scope what an operator may read into it.
	fmt.Fprintf(stderr, "  corrected %d flag flip(s); journal consumers must refresh their readiness snapshot\n", corrected)
	return nil
}

// isBlockedRepairOwedAtHead reports whether a committed obligation key sits at
// HEAD. The key is read AS OF 'HEAD', never from the working set: a
// working-set-only key grants nothing (it may be an uncommitted edit, a
// half-applied step, or debris), while a committed one is durable history —
// the record of an already-consented upgrade that this clone received or
// started, finishing which is continuation of that consent (see
// migrate_consent.go). tableTrackedAtHead gates the probe so a store whose
// metadata table is not in committed history reads as owing nothing without
// issuing a failing AS OF statement.
func isBlockedRepairOwedAtHead(ctx context.Context, db DBConn) (bool, error) {
	tracked, err := tableTrackedAtHead(ctx, db, "", "metadata")
	if err != nil {
		return false, fmt.Errorf("checking metadata at HEAD: %w", err)
	}
	if !tracked {
		return false, nil
	}
	var value string
	err = db.QueryRowContext(ctx,
		"SELECT value FROM metadata AS OF 'HEAD' WHERE `key` = ?", isBlockedRepairOwedKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s at HEAD: %w", isBlockedRepairOwedKey, err)
	}
	return value == isBlockedRepairOwedFull, nil
}

// rearmIgnoredIsBlockedRepair deletes clone-local cursor rows with version >=
// 0032 so the ignored-series repair replays. The cursor is MAX(version), so
// deleting only row 32 would leave a higher cursor hiding the repair; the
// entire suffix from 32 through this binary's latest ignored migration
// replays, in order. Every cursor row below 32 — including earlier rekey
// markers — is preserved.
func rearmIgnoredIsBlockedRepair(ctx context.Context, db DBConn) error {
	exists, err := showTableExists(ctx, db, ignoredSource.cursorTable)
	if err != nil {
		return fmt.Errorf("probing %s: %w", ignoredSource.cursorTable, err)
	}
	if !exists {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM "+ignoredSource.cursorTable+" WHERE version >= ?", isBlockedRepairIgnoredVersion); err != nil {
		return fmt.Errorf("re-arming ignored migration %d: %w", isBlockedRepairIgnoredVersion, err)
	}
	return nil
}
