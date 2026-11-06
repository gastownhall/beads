package storagecontract

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// ExternalRefHistoryBatchFixture supplies the store-specific hooks the
// external-ref history batch contract needs. Every hook must act on the SAME
// database the WithTx hook reads through.
type ExternalRefHistoryBatchFixture struct {
	// Prefix namespaces the ids and branch names each case seeds so several
	// suites can share one database.
	Prefix string
	// WithTx runs fn inside one read transaction on the store's checked-out
	// branch. The batch body under test (issueops.PreviousExternalRefsInTx)
	// and its single-id oracle run inside this one transaction, the way the
	// dolt and embeddeddolt wrappers hand them a history-read transaction.
	WithTx func(ctx context.Context, fn func(tx *sql.Tx) error) error
	// ExecMain runs one statement on the store's checked-out branch: working
	// set writes, dated commits, and branch and merge steps. Dated commits go
	// through CALL DOLT_COMMIT('--date', ?, '-Am', ?) with an RFC3339 time.
	ExecMain func(ctx context.Context, query string, args ...any) error
	// QueryRowMain scans exactly one column of one row from the checked-out
	// branch — the AS OF preconditions and the tie non-vacuity counts.
	QueryRowMain func(ctx context.Context, dest any, query string, args ...any) error
	// ExecSide runs one statement on a session whose checked-out branch is
	// branch. The side-branch cases need writes that never touch the main
	// branch's working set.
	ExecSide func(ctx context.Context, branch, query string, args ...any) error
	// CreateIssue seeds one durable issue through the store API, so the
	// issues row carries every column the real write path sets.
	CreateIssue func(ctx context.Context, id, title string) error
	// CurrentCommit returns HEAD of the checked-out branch.
	CurrentCommit func(ctx context.Context) (string, error)
	// MergeSide merges branch into the checked-out branch as a merge commit
	// dated at, taking the side branch's rows of the issues table on
	// conflict ('--theirs'). DOLT_MERGE refuses to leave conflicts behind
	// under autocommit, so the sequence runs in one transaction with
	// dolt_allow_commit_conflicts set — the pattern the store's own
	// pull/merge settlement uses (store.go settleMergeInTx).
	MergeSide func(ctx context.Context, branch string, at time.Time) error
}

// RunExternalRefHistoryBatchContract runs every external-ref history batch
// case in order against one fixture.
//
// The cases share a database and run sequentially: the merge cases advance
// HEAD and add side-branch ancestry that the later cases build on. Each case
// names the ids and branches it seeds.
//
// What the cases pin is PARITY — the batch answer equals the single-id
// answer, including presence — plus pinned absolute expectations at spot
// cutoffs. The single-id helper (issueops.PreviousExternalRefInTx) is the
// shipped semantics for "what was external_ref as of T"; the batch exists to
// answer the same question for many ids in one query (GH#6605), so any
// divergence is the defect, whatever value the tie or the merge leaves.
func RunExternalRefHistoryBatchContract(t *testing.T, ctx context.Context, fx ExternalRefHistoryBatchFixture) {
	t.Helper()
	t.Run("BatchMatchesSingleAcrossCutoffs", func(t *testing.T) {
		RunExternalRefHistoryBatchMatchesSingleAcrossCutoffs(t, ctx, fx)
	})
	t.Run("IgnoresDuplicateAndEmptyIDs", func(t *testing.T) {
		RunExternalRefHistoryBatchIgnoresDuplicateAndEmptyIDs(t, ctx, fx)
	})
	t.Run("EqualDateTieMatchesSingleIssueLookup", func(t *testing.T) {
		RunExternalRefHistoryBatchEqualDateTieMatchesSingleIssueLookup(t, ctx, fx)
	})
	t.Run("MergedSideBranchHistoryMatchesSingleIssueLookup", func(t *testing.T) {
		RunExternalRefHistoryBatchMergedSideBranchHistoryMatchesSingleIssueLookup(t, ctx, fx)
	})
	t.Run("MergedSideBranchNewerThanCutoffStaysExcluded", func(t *testing.T) {
		RunExternalRefHistoryBatchMergedSideBranchNewerThanCutoffStaysExcluded(t, ctx, fx)
	})
}

// batchCutoff dates every seeded commit and cutoff in the contract on one
// fixed timeline (March 2025, noon UTC) so cases cannot interact through
// today's clock. The timeline is deliberately in the PAST: the server-mode
// store version-commits each CreateIssue at the current date (dolt
// issues.go, createIssue → doltAddAndCommit), and those create-commits —
// which carry a NULL external_ref row for the new id — must postdate every
// cutoff in this contract so they can never leak into a sweep. Equal
// timestamps are deliberate only in the tie case.
func batchCutoff(day int) time.Time {
	return time.Date(2025, time.March, day, 12, 0, 0, 0, time.UTC)
}

// RunExternalRefHistoryBatchMatchesSingleAcrossCutoffs walks one sweep of
// cutoffs over ids covering every history shape the pull path asks about:
// relinked, NULL and empty refs, deleted, deleted-then-recreated, created
// after the cutoff, and never created. At each cutoff, inside one
// transaction, every id's single-id answer must equal the batch answer, and
// spot checks pin the absolute values so parity cannot pass by both paths
// being wrong together.
func RunExternalRefHistoryBatchMatchesSingleAcrossCutoffs(t *testing.T, ctx context.Context, fx ExternalRefHistoryBatchFixture) {
	t.Helper()
	plain := fx.Prefix + "-plain"
	nullish := fx.Prefix + "-nullish"
	dead := fx.Prefix + "-dead"
	reborn := fx.Prefix + "-reborn"
	late := fx.Prefix + "-late"
	never := fx.Prefix + "-never"
	ids := []string{plain, nullish, dead, reborn, late, never}

	mustCreate := func(id string) {
		t.Helper()
		if err := fx.CreateIssue(ctx, id, "Batch contract "+id); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	setRef := func(id string, ref *string) {
		t.Helper()
		var refArg any
		if ref != nil {
			refArg = *ref
		}
		if err := fx.ExecMain(ctx, "UPDATE issues SET external_ref = ? WHERE id = ?", refArg, id); err != nil {
			t.Fatalf("set external_ref of %s to %v: %v", id, refArg, err)
		}
	}
	commitAt := func(day int, msg string) {
		t.Helper()
		batchMustCommitAt(t, ctx, fx, msg, batchCutoff(day))
	}
	del := func(id string) {
		t.Helper()
		if err := fx.ExecMain(ctx, "DELETE FROM issues WHERE id = ?", id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
	}

	mustCreate(plain)
	setRef(plain, strPtr("R0"))
	commitAt(1, "seed plain R0")
	mustCreate(nullish)
	setRef(nullish, strPtr("RA"))
	commitAt(1, "seed nullish RA")
	mustCreate(dead)
	setRef(dead, strPtr("RA"))
	commitAt(1, "seed dead RA")
	mustCreate(reborn)
	setRef(reborn, strPtr("R0"))
	commitAt(1, "seed reborn R0")

	setRef(plain, strPtr("RA"))
	commitAt(4, "relink plain RA")
	setRef(nullish, nil)
	commitAt(5, "nullish ref to NULL")
	setRef(nullish, strPtr(""))
	commitAt(6, "nullish ref to empty string")
	del(dead)
	commitAt(6, "delete dead")
	del(reborn)
	commitAt(6, "delete reborn")
	mustCreate(reborn)
	setRef(reborn, strPtr("RB"))
	commitAt(8, "recreate reborn RB")
	mustCreate(late)
	setRef(late, strPtr("RL"))
	commitAt(8, "seed late RL")

	for _, cutoff := range []int{0, 2, 3, 5, 7, 9, 20} {
		asOf := batchCutoff(cutoff)
		var got map[string]string
		singles := map[string]singleAnswer{}
		err := fx.WithTx(ctx, func(tx *sql.Tx) error {
			for _, id := range ids {
				ref, found, err := issueops.PreviousExternalRefInTx(ctx, tx, id, asOf)
				if err != nil {
					return fmt.Errorf("single-id lookup for %s at %s: %w", id, asOf, err)
				}
				singles[id] = singleAnswer{ref: ref, found: found}
			}
			var err error
			got, err = issueops.PreviousExternalRefsInTx(ctx, tx, ids, asOf)
			return err
		})
		if err != nil {
			t.Fatalf("cutoff day %d: %v", cutoff, err)
		}
		for _, id := range ids {
			want := singles[id]
			// An empty string in the map is a FOUND NULL/empty ref, so map
			// presence is what carries the single-id call's found.
			batchRef, present := got[id]
			if present != want.found {
				t.Errorf("cutoff day %d: batch presence of %s = %v, single-id found = %v", cutoff, id, present, want.found)
			}
			if present && batchRef != want.ref {
				t.Errorf("cutoff day %d: batch ref of %s = %q, single-id ref = %q", cutoff, id, batchRef, want.ref)
			}
		}
		// Pinned spot checks: absolute values, independent of the oracle.
		pinned := []struct {
			id    string
			day   int
			ref   string
			found bool
		}{
			{plain, 0, "", false},
			{plain, 2, "R0", true},
			{plain, 5, "RA", true},
			{nullish, 7, "", true},
			{dead, 5, "RA", true},
			{dead, 7, "RA", true},
			{reborn, 2, "R0", true},
			{reborn, 7, "R0", true},
			{reborn, 9, "RB", true},
			{late, 2, "", false},
			{late, 9, "RL", true},
		}
		for _, pin := range pinned {
			if pin.day != cutoff {
				continue
			}
			ref, present := got[pin.id]
			if present != pin.found || (pin.found && ref != pin.ref) {
				t.Errorf("cutoff day %d: pinned %s = (%q, %v), want (%q, %v)", cutoff, pin.id, ref, present, pin.ref, pin.found)
			}
		}
	}
}

// RunExternalRefHistoryBatchIgnoresDuplicateAndEmptyIDs pins the request
// normalization: duplicate ids collapse, empty ids drop, nil and all-empty
// requests answer an empty map without error, and a real id mixed in with
// duplicates still resolves exactly once in the answer.
func RunExternalRefHistoryBatchIgnoresDuplicateAndEmptyIDs(t *testing.T, ctx context.Context, fx ExternalRefHistoryBatchFixture) {
	t.Helper()
	plain := fx.Prefix + "-plain"
	never := fx.Prefix + "-never"
	err := fx.WithTx(ctx, func(tx *sql.Tx) error {
		got, err := issueops.PreviousExternalRefsInTx(ctx, tx, nil, batchCutoff(9))
		if err != nil {
			return fmt.Errorf("nil request: %w", err)
		}
		if len(got) != 0 {
			t.Errorf("nil request = %v, want an empty map", got)
		}
		got, err = issueops.PreviousExternalRefsInTx(ctx, tx, []string{"", ""}, batchCutoff(9))
		if err != nil {
			return fmt.Errorf("all-empty request: %w", err)
		}
		if len(got) != 0 {
			t.Errorf("all-empty request = %v, want an empty map", got)
		}
		got, err = issueops.PreviousExternalRefsInTx(ctx, tx, []string{plain, plain, "", never, plain}, batchCutoff(9))
		if err != nil {
			return fmt.Errorf("mixed request: %w", err)
		}
		if _, dup := got[plain]; !dup {
			t.Errorf("mixed request lost %s", plain)
		}
		if _, empty := got[""]; empty {
			t.Error(`mixed request returned an entry for the empty id ""`)
		}
		if _, present := got[never]; present {
			t.Errorf("mixed request returned %s, which has no history", never)
		}
		if len(got) != 1 {
			t.Errorf("mixed request = %v, want exactly %s", got, plain)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
}

// RunExternalRefHistoryBatchEqualDateTieMatchesSingleIssueLookup pins the
// equal-date tie: dolt_history_issues holds one row per commit per live
// issue, and separate commits can share a commit_date, so an id's greatest
// qualifying date can carry two different refs. The date-only ordering does
// not define a winner, so the batch must answer exactly what the single-id
// lookup answers rather than whichever tied row the scan met first. The case
// first proves the tie is real — at least two history rows at the id's max
// date — so it cannot pass vacuously.
func RunExternalRefHistoryBatchEqualDateTieMatchesSingleIssueLookup(t *testing.T, ctx context.Context, fx ExternalRefHistoryBatchFixture) {
	t.Helper()
	id := fx.Prefix + "-tie"
	if err := fx.CreateIssue(ctx, id, "Batch contract tie"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", id, err)
	}
	if err := fx.ExecMain(ctx, "UPDATE issues SET external_ref = ? WHERE id = ?", "T0", id); err != nil {
		t.Fatalf("set ref T0: %v", err)
	}
	batchMustCommitAt(t, ctx, fx, "tie T0", batchCutoff(2))
	if err := fx.ExecMain(ctx, "UPDATE issues SET external_ref = ? WHERE id = ?", "TA", id); err != nil {
		t.Fatalf("set ref TA: %v", err)
	}
	batchMustCommitAt(t, ctx, fx, "tie TA", batchCutoff(3))
	if err := fx.ExecMain(ctx, "UPDATE issues SET external_ref = ? WHERE id = ?", "TB", id); err != nil {
		t.Fatalf("set ref TB: %v", err)
	}
	batchMustCommitAt(t, ctx, fx, "tie TB", batchCutoff(3))

	// Non-vacuity: the tie is two or more history rows at one date, not one
	// row the batch happens to re-resolve.
	var rowsAtTiedDate int
	if err := fx.QueryRowMain(ctx, &rowsAtTiedDate, "SELECT COUNT(*) FROM dolt_history_issues WHERE id = ? AND commit_date = ?", id, batchCutoff(3)); err != nil {
		t.Fatalf("count history rows at the tied date: %v", err)
	}
	if rowsAtTiedDate < 2 {
		t.Fatalf("history holds %d rows for %s at %s, want at least 2: the tie this case exists for did not materialize", rowsAtTiedDate, id, batchCutoff(3))
	}

	err := fx.WithTx(ctx, func(tx *sql.Tx) error {
		single, found, err := issueops.PreviousExternalRefInTx(ctx, tx, id, batchCutoff(4))
		if err != nil {
			return fmt.Errorf("single-id lookup: %w", err)
		}
		if !found {
			t.Fatal("single-id lookup did not find the tied id")
		}
		if single != "TA" && single != "TB" {
			t.Errorf("single-id lookup at the tie = %q, want TA or TB (T0 predates the tie)", single)
		}
		got, err := issueops.PreviousExternalRefsInTx(ctx, tx, []string{id}, batchCutoff(4))
		if err != nil {
			return fmt.Errorf("batch lookup: %w", err)
		}
		batchRef, present := got[id]
		if !present {
			t.Fatalf("batch lookup omitted %s, single-id found %q", id, single)
		}
		if batchRef != single {
			t.Errorf("batch lookup at the tie = %q, single-id lookup = %q", batchRef, single)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
}

// RunExternalRefHistoryBatchMergedSideBranchHistoryMatchesSingleIssueLookup
// is the merge discriminator. A side branch changes the issue's ref with a
// commit dated BETWEEN the snapshot commit and the cutoff, and is merged
// after the cutoff; after the merge, that commit's history row is visible in
// HEAD's ancestry with a qualifying date, so a batch that resolved ids by
// snapshot-walking (the value at the latest-dated ANCESTOR commit) would
// answer the snapshot value RA while the date-ordered single-id lookup
// answers the merged value RB. The batch must agree with the single-id
// lookup.
//
// Dating the side commit at day 9 — between snapshot commit C (day 8) and
// cutoff (day 10) — is a deliberate re-dating of the approved design, which
// placed it at day 5. Measured on dolt 2.6.0 (see bd-84vbqq): dolt_history_issues
// emits one row per commit per LIVE issue, including commits that do not
// touch the issue, so with a day-5 side commit the rows at days 8 (RA) and
// later dominate and BOTH the snapshot walk and the date ordering answer RA
// — the case could not fail. Day 9 makes the two readings disagree, which is
// the only placement that discriminates. The precondition asserts below pin
// both readings so a dolt history-semantics change fails the case loudly
// instead of silently passing.
func RunExternalRefHistoryBatchMergedSideBranchHistoryMatchesSingleIssueLookup(t *testing.T, ctx context.Context, fx ExternalRefHistoryBatchFixture) {
	t.Helper()
	id := fx.Prefix + "-merged"
	side := fx.Prefix + "-side"
	if err := fx.CreateIssue(ctx, id, "Batch contract merge"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", id, err)
	}
	if err := fx.ExecMain(ctx, "UPDATE issues SET external_ref = ? WHERE id = ?", "R0", id); err != nil {
		t.Fatalf("set ref R0: %v", err)
	}
	batchMustCommitAt(t, ctx, fx, "merged R0", batchCutoff(1))
	if err := fx.ExecMain(ctx, "CALL DOLT_BRANCH(?)", side); err != nil {
		t.Fatalf("DOLT_BRANCH(%s): %v", side, err)
	}
	if err := fx.ExecMain(ctx, "UPDATE issues SET external_ref = ? WHERE id = ?", "RA", id); err != nil {
		t.Fatalf("set ref RA: %v", err)
	}
	batchMustCommitAt(t, ctx, fx, "merged RA", batchCutoff(3))
	// An unrelated same-table write, committed at a dated point: any commit
	// emits a history row for the live issue, which is exactly the property
	// the re-dating below leans on. The create itself may already be
	// version-committed at the current date (server leg), so the dated
	// commit carries a bystander title bump — a write to a row the case
	// never asks about.
	bystander := fx.Prefix + "-bystander"
	if err := fx.CreateIssue(ctx, bystander, "Batch contract bystander"); err != nil {
		t.Fatalf("CreateIssue(bystander): %v", err)
	}
	if err := fx.ExecMain(ctx, "UPDATE issues SET title = ? WHERE id = ?", "Batch contract bystander bumped", bystander); err != nil {
		t.Fatalf("bump bystander title: %v", err)
	}
	batchMustCommitAt(t, ctx, fx, "unrelated bystander", batchCutoff(8))
	snapshotCommit, err := fx.CurrentCommit(ctx)
	if err != nil {
		t.Fatalf("CurrentCommit: %v", err)
	}

	if err := fx.ExecSide(ctx, side, "UPDATE issues SET external_ref = ? WHERE id = ?", "RB", id); err != nil {
		t.Fatalf("side UPDATE: %v", err)
	}
	if err := fx.ExecSide(ctx, side, "CALL DOLT_COMMIT('--date', ?, '-Am', ?)", batchCutoff(9).Format(time.RFC3339), "side sets RB"); err != nil {
		t.Fatalf("side commit: %v", err)
	}

	if err := fx.MergeSide(ctx, side, batchCutoff(12)); err != nil {
		t.Fatalf("merge %s: %v", side, err)
	}

	// Precondition 1: the snapshot at commit C, read by hash, still shows
	// RA — the merge that landed RB must not time-travel the past.
	var asOfSnapshot string
	if err := fx.QueryRowMain(ctx, &asOfSnapshot, fmt.Sprintf("SELECT external_ref FROM issues AS OF '%s' WHERE id = ?", snapshotCommit), id); err != nil {
		t.Fatalf("read issues AS OF the snapshot commit: %v", err)
	}
	if asOfSnapshot != "RA" {
		t.Fatalf("issues AS OF %s shows external_ref %q, want RA: the merge rewrote history, so this case cannot discriminate", snapshotCommit, asOfSnapshot)
	}

	// Precondition 2 and the assertion, in one transaction.
	err = fx.WithTx(ctx, func(tx *sql.Tx) error {
		single, found, err := issueops.PreviousExternalRefInTx(ctx, tx, id, batchCutoff(10))
		if err != nil {
			return fmt.Errorf("single-id lookup: %w", err)
		}
		if !found || single != "RB" {
			return fmt.Errorf("precondition: single-id lookup at cutoff = (%q, %v), want (RB, true): the date ordering no longer sees the day-9 side commit, so this case cannot discriminate", single, found)
		}
		got, err := issueops.PreviousExternalRefsInTx(ctx, tx, []string{id}, batchCutoff(10))
		if err != nil {
			return fmt.Errorf("batch lookup: %w", err)
		}
		batchRef, present := got[id]
		if !present || batchRef != "RB" {
			t.Errorf("batch lookup = (%q, %v), want (RB, true) — the snapshot-walk answer RA means the batch is not date-ordered like the single-id lookup", batchRef, present)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
}

// RunExternalRefHistoryBatchMergedSideBranchNewerThanCutoffStaysExcluded is
// the out-of-order-date half of the merge coverage: a side branch commit
// dated EARLIER than its own parent (day 11 under a day-12 parent), merged
// by fast-forward, puts a row into HEAD's ancestry whose commit_date
// predates its graph position. For this id the whole history is two rows —
// day 12 (RN-base) and day 11 (RN) — so the cutoff sweep crosses both sides
// of the date flip: below day 11 nothing qualifies, at day 11 the
// out-of-order row wins, and past day 12 the parent's row wins back. The
// batch must track the single-id lookup at every step.
func RunExternalRefHistoryBatchMergedSideBranchNewerThanCutoffStaysExcluded(t *testing.T, ctx context.Context, fx ExternalRefHistoryBatchFixture) {
	t.Helper()
	id := fx.Prefix + "-merged2"
	side := fx.Prefix + "-side2"
	if err := fx.CreateIssue(ctx, id, "Batch contract merge 2"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", id, err)
	}
	if err := fx.ExecMain(ctx, "UPDATE issues SET external_ref = ? WHERE id = ?", "RN-base", id); err != nil {
		t.Fatalf("set ref RN-base: %v", err)
	}
	batchMustCommitAt(t, ctx, fx, "merged2 base", batchCutoff(12))
	if err := fx.ExecMain(ctx, "CALL DOLT_BRANCH(?)", side); err != nil {
		t.Fatalf("DOLT_BRANCH(%s): %v", side, err)
	}
	if err := fx.ExecSide(ctx, side, "UPDATE issues SET external_ref = ? WHERE id = ?", "RN", id); err != nil {
		t.Fatalf("side UPDATE: %v", err)
	}
	if err := fx.ExecSide(ctx, side, "CALL DOLT_COMMIT('--date', ?, '-Am', ?)", batchCutoff(11).Format(time.RFC3339), "side sets RN before its parent's date"); err != nil {
		t.Fatalf("side commit: %v", err)
	}
	if err := fx.ExecMain(ctx, "CALL DOLT_MERGE(?)", side); err != nil {
		t.Fatalf("DOLT_MERGE(%s): %v", side, err)
	}

	pinned := map[int]string{
		10: "",        // no qualifying rows yet
		11: "RN",      // the out-of-order row is the latest qualifying date
		13: "RN-base", // the parent's row wins back past day 12
	}
	err := fx.WithTx(ctx, func(tx *sql.Tx) error {
		for cutoffDay, wantRef := range pinned {
			asOf := batchCutoff(cutoffDay)
			single, found, err := issueops.PreviousExternalRefInTx(ctx, tx, id, asOf)
			if err != nil {
				return fmt.Errorf("single-id lookup at day %d: %w", cutoffDay, err)
			}
			got, err := issueops.PreviousExternalRefsInTx(ctx, tx, []string{id}, asOf)
			if err != nil {
				return fmt.Errorf("batch lookup at day %d: %w", cutoffDay, err)
			}
			batchRef, present := got[id]
			if present != found || (found && batchRef != single) {
				t.Errorf("day %d: batch = (%q, %v), single-id = (%q, %v)", cutoffDay, batchRef, present, single, found)
			}
			if found != (wantRef != "") || (found && single != wantRef) {
				t.Errorf("day %d: single-id = (%q, %v), want (%q, %v)", cutoffDay, single, found, wantRef, wantRef != "")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}
}

// batchMustCommitAt makes one dated Dolt commit of the whole working set.
func batchMustCommitAt(t *testing.T, ctx context.Context, fx ExternalRefHistoryBatchFixture, msg string, at time.Time) {
	t.Helper()
	if err := fx.ExecMain(ctx, "CALL DOLT_COMMIT('--date', ?, '-Am', ?)", at.Format(time.RFC3339), msg); err != nil {
		t.Fatalf("dated commit %q at %s: %v", msg, at, err)
	}
}

type singleAnswer struct {
	ref   string
	found bool
}

func strPtr(s string) *string { return &s }
