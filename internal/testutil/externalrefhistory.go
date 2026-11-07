package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"
)

// PreviousExternalRefsStore is the pair of history reads the external_ref
// parity scenario compares: the per-issue lookup and its batch form.
type PreviousExternalRefsStore interface {
	PreviousExternalRef(ctx context.Context, issueID string, asOf time.Time) (string, bool, error)
	PreviousExternalRefs(ctx context.Context, issueIDs []string, asOf time.Time) (map[string]string, error)
}

// externalRefHistoryDay is the scenario's day 0. Commit dates are explicit
// days after it, so every cutoff below names a known point in the history
// regardless of when the test runs.
var externalRefHistoryDay = time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)

func externalRefHistoryAt(days float64) time.Time {
	return externalRefHistoryDay.Add(time.Duration(days * float64(24*time.Hour)))
}

// ExternalRefHistoryScenario is a committed issues history whose
// external_ref values change in every way the batch history read has to
// agree with the per-issue read on: relinks, NULL and empty refs, a delete
// and recreate, an issue created late, two commits sharing the latest date
// with different refs, a side branch dated between the local line's
// commits and merged after every cutoff, and a side commit dated before its
// own parent and merged by fast-forward.
type ExternalRefHistoryScenario struct {
	// IDs are the scenario's issue ids, keyed by role.
	IDs map[string]string
	// Cutoffs are the asOf values the scenario's assertions read at.
	Cutoffs []time.Time
	// DiscriminatorCutoff falls between the side branch's commit and the
	// next local-line commit: the latest local-line snapshot at or before
	// it (DiscriminatorSnapshot) still holds the local ref, while the
	// merged history's latest qualifying row is the side branch's.
	DiscriminatorCutoff   time.Time
	DiscriminatorSnapshot string
	// LateUnrelatedCommit is the unrelated-table commit dated just before
	// LateCutoff and after the side branch's commit.
	LateUnrelatedCommit string
	LateCutoff          time.Time
	// TieCutoff is the date two local-line commits share; at it, the tied
	// issue's latest qualifying rows disagree.
	TieCutoff time.Time
	// OutOfOrderBefore, OutOfOrderChild and OutOfOrderPast straddle the
	// out-of-order issue's two rows: before both, at the side commit dated a
	// day before its parent, and past the parent.
	OutOfOrderBefore time.Time
	OutOfOrderChild  time.Time
	OutOfOrderPast   time.Time
}

// BuildExternalRefHistoryScenario commits the scenario on conn's current
// branch (and a side branch it creates and merges), leaving conn on that
// branch with a clean working set. prefix keeps its ids apart from anything
// else the database holds.
func BuildExternalRefHistoryScenario(t *testing.T, ctx context.Context, conn *sql.Conn, prefix string) *ExternalRefHistoryScenario {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		rows, err := conn.QueryContext(ctx, query, args...)
		if err != nil {
			t.Fatalf("external_ref history scenario: %s: %v", query, err)
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("external_ref history scenario: %s: %v", query, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("external_ref history scenario: %s: %v", query, err)
		}
	}
	scalar := func(query string, args ...any) string {
		t.Helper()
		var value string
		if err := conn.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
			t.Fatalf("external_ref history scenario: %s: %v", query, err)
		}
		return value
	}
	commit := func(days float64, message string) string {
		t.Helper()
		exec("CALL DOLT_COMMIT('-Am', ?, '--allow-empty', '--date', ?)", message, externalRefHistoryAt(days).Format(time.RFC3339))
		return scalar("SELECT HASHOF('HEAD')")
	}
	insert := func(id string, ref any) {
		t.Helper()
		exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, external_ref) VALUES (?, 'history', '', '', '', '', 'open', 2, 'task', ?)", id, ref)
	}
	setRef := func(id string, ref any) {
		t.Helper()
		exec("UPDATE issues SET external_ref = ? WHERE id = ?", ref, id)
	}

	ids := map[string]string{}
	for _, role := range []string{"merged", "steady", "relinked", "nullref", "emptyref", "unlinked", "deleted", "tied", "late", "outoforder"} {
		ids[role] = prefix + "-" + role
	}
	ref := func(s string) string { return "https://tracker.test/" + prefix + "/" + s }

	home := scalar("SELECT active_branch()")
	side := home + "-side"

	insert(ids["merged"], ref("R0"))
	insert(ids["steady"], ref("steady"))
	insert(ids["relinked"], ref("old"))
	insert(ids["nullref"], nil)
	insert(ids["emptyref"], "")
	insert(ids["unlinked"], ref("unlink-me"))
	insert(ids["deleted"], ref("deleted-1"))
	insert(ids["tied"], ref("T1"))
	commit(1, "history: seed")
	exec("CALL DOLT_BRANCH(?, 'HEAD')", side)

	exec("DELETE FROM issues WHERE id = ?", ids["deleted"])
	commit(2, "history: delete")
	setRef(ids["merged"], ref("RA"))
	setRef(ids["unlinked"], nil)
	commit(3, "history: local relink of merged")
	setRef(ids["relinked"], ref("new"))
	discriminatorSnapshot := commit(4, "history: relink")
	insert(ids["deleted"], ref("deleted-2"))
	commit(6, "history: recreate")
	setRef(ids["tied"], ref("T2"))
	commit(7, "history: tie one")
	setRef(ids["tied"], ref("T3"))
	commit(7, "history: tie two")
	exec("REPLACE INTO config (`key`, value) VALUES (?, 'x')", prefix+".history-marker")
	lateUnrelated := commit(8, "history: unrelated table")
	insert(ids["late"], ref("late"))
	commit(11, "history: late create")

	// The side branch's commit is dated between the local line's commits
	// at day 4 and day 6, so its rows sit out of walk order once merged.
	exec("CALL DOLT_CHECKOUT(?)", side)
	setRef(ids["merged"], ref("RB"))
	commit(5, "history: side relink of merged")
	exec("CALL DOLT_CHECKOUT(?)", home)

	// Both lines changed merged's ref, so the merge conflicts; keep ours.
	exec("SET @@dolt_allow_commit_conflicts = 1")
	exec("SET @@dolt_force_transaction_commit = 1")
	exec("CALL DOLT_MERGE(?)", side)
	if n := scalar("SELECT CAST(COUNT(*) AS CHAR) FROM dolt_conflicts"); n != "0" {
		exec("CALL DOLT_CONFLICTS_RESOLVE('--ours', 'issues')")
	}
	commit(12, "history: merge side")
	exec("SET @@dolt_allow_commit_conflicts = 0")
	exec("SET @@dolt_force_transaction_commit = 0")
	exec("CALL DOLT_BRANCH('-D', ?)", side)

	// The out-of-order issue's history is two rows: its parent commit at day
	// 15 and a side commit dated day 14 on top of it, merged by fast-forward,
	// so HEAD's ancestry holds a row dated before its graph position.
	outOfOrderSide := home + "-side2"
	insert(ids["outoforder"], ref("RN-base"))
	commit(15, "history: out-of-order parent")
	exec("CALL DOLT_BRANCH(?, 'HEAD')", outOfOrderSide)
	exec("CALL DOLT_CHECKOUT(?)", outOfOrderSide)
	setRef(ids["outoforder"], ref("RN"))
	commit(14, "history: side commit dated before its parent")
	exec("CALL DOLT_CHECKOUT(?)", home)
	exec("CALL DOLT_MERGE(?)", outOfOrderSide)
	exec("CALL DOLT_BRANCH('-D', ?)", outOfOrderSide)

	// The scenario must actually hold the shapes its cutoffs name, or a
	// parity check against it would pass without exercising them.
	for _, snapshot := range []string{discriminatorSnapshot, lateUnrelated} {
		if got := scalar("SELECT COALESCE(external_ref, '') FROM issues AS OF '"+snapshot+"' WHERE id = ?", ids["merged"]); got != ref("RA") {
			t.Fatalf("external_ref history scenario: merged as of local-line commit %s = %q, want %q", snapshot, got, ref("RA"))
		}
	}
	if n := scalar("SELECT CAST(COUNT(*) AS CHAR) FROM (SELECT id, commit_date FROM dolt_history_issues) h WHERE h.id = ? AND h.commit_date = ?",
		ids["tied"], externalRefHistoryAt(7)); n != "2" {
		t.Fatalf("external_ref history scenario: %s has %s history rows dated day 7, want 2 (a latest-date tie)", ids["tied"], n)
	}
	if got := scalar("SELECT COALESCE(external_ref, '') FROM issues WHERE id = ?", ids["outoforder"]); got != ref("RN") {
		t.Fatalf("external_ref history scenario: %s at HEAD = %q, want %q (the fast-forward merge of the side commit)", ids["outoforder"], got, ref("RN"))
	}

	cutoffs := []time.Time{
		externalRefHistoryAt(0),
		externalRefHistoryAt(1),
		externalRefHistoryAt(2.5),
		externalRefHistoryAt(3),
		externalRefHistoryAt(5),
		externalRefHistoryAt(5.5),
		externalRefHistoryAt(7),
		externalRefHistoryAt(10),
		externalRefHistoryAt(11),
		externalRefHistoryAt(13),
		externalRefHistoryAt(14),
		externalRefHistoryAt(16),
	}
	return &ExternalRefHistoryScenario{
		IDs:                   ids,
		Cutoffs:               cutoffs,
		DiscriminatorCutoff:   externalRefHistoryAt(5.5),
		DiscriminatorSnapshot: discriminatorSnapshot,
		LateUnrelatedCommit:   lateUnrelated,
		LateCutoff:            externalRefHistoryAt(10),
		TieCutoff:             externalRefHistoryAt(7),
		OutOfOrderBefore:      externalRefHistoryAt(13),
		OutOfOrderChild:       externalRefHistoryAt(14),
		OutOfOrderPast:        externalRefHistoryAt(16),
	}
}

// Ref returns the scenario's full external_ref for a short name, so a test
// can state an expected value.
func (s *ExternalRefHistoryScenario) Ref(prefix, name string) string {
	return "https://tracker.test/" + prefix + "/" + name
}

// RequestIDs is every scenario id plus one that never existed, a duplicate
// and an empty id, in a fixed order.
func (s *ExternalRefHistoryScenario) RequestIDs(prefix string) []string {
	ids := make([]string, 0, len(s.IDs)+3)
	for _, id := range s.IDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return append(ids, prefix+"-never", ids[0], "")
}

// AssertPreviousExternalRefsParity reads every cutoff of the scenario through
// both methods of store and fails on any id whose presence or value differs.
func AssertPreviousExternalRefsParity(t *testing.T, ctx context.Context, store PreviousExternalRefsStore, scenario *ExternalRefHistoryScenario, prefix string) {
	t.Helper()
	request := scenario.RequestIDs(prefix)
	for _, cutoff := range scenario.Cutoffs {
		batch, err := store.PreviousExternalRefs(ctx, request, cutoff)
		if err != nil {
			t.Fatalf("PreviousExternalRefs(%s): %v", cutoff, err)
		}
		want := map[string]string{}
		for _, id := range request {
			if id == "" {
				continue
			}
			ref, found, err := store.PreviousExternalRef(ctx, id, cutoff)
			if err != nil {
				t.Fatalf("PreviousExternalRef(%s, %s): %v", id, cutoff, err)
			}
			if found {
				want[id] = ref
			}
		}
		if fmt.Sprint(batch) != fmt.Sprint(want) {
			t.Errorf("as of %s: PreviousExternalRefs = %v, per-issue PreviousExternalRef = %v", cutoff.Format(time.RFC3339), batch, want)
		}
	}
}

// AssertExternalRefHistoryDiscriminator pins the late-merge case on store.
// At DiscriminatorCutoff the side branch's row (dated day 5, merged day 12)
// is the latest qualifying one, so both reads must return it even though the
// local line's latest snapshot at that cutoff (day 4) still holds RA: a read
// of that snapshot instead of the history would answer RA.
//
// At LateCutoff, past the day-8 unrelated-table commit, every local-line
// commit carries the merged issue's row (dolt_history_issues has one row per
// commit per id, changed or not), so the latest qualifying row is day 8's RA,
// the same value the day-8 snapshot holds. BuildExternalRefHistoryScenario
// has already checked both snapshots hold RA. This corrects the approved
// design's discriminator, which expected RB at the day-10 cutoff: the day-8
// commit's RA row makes RA the right answer there, so the late-merge case
// is checked at day 5.5, where RB is the latest qualifying row.
func AssertExternalRefHistoryDiscriminator(t *testing.T, ctx context.Context, store PreviousExternalRefsStore, scenario *ExternalRefHistoryScenario, prefix string) {
	t.Helper()
	id := scenario.IDs["merged"]
	for _, check := range []struct {
		name    string
		cutoff  time.Time
		wantRef string
	}{
		{"side commit merged after the cutoff", scenario.DiscriminatorCutoff, "RB"},
		{"unrelated commit after the side commit", scenario.LateCutoff, "RA"},
	} {
		single, found, err := store.PreviousExternalRef(ctx, id, check.cutoff)
		if err != nil || !found || single != scenario.Ref(prefix, check.wantRef) {
			t.Fatalf("%s: PreviousExternalRef = (%q, %t, %v), want (%q, true, nil)", check.name, single, found, err, scenario.Ref(prefix, check.wantRef))
		}
		batch, err := store.PreviousExternalRefs(ctx, []string{id}, check.cutoff)
		if err != nil {
			t.Fatalf("%s: PreviousExternalRefs: %v", check.name, err)
		}
		if got, ok := batch[id]; !ok || got != single {
			t.Errorf("%s: PreviousExternalRefs[%s] = (%q, %t), want the per-issue answer %q", check.name, id, got, ok, single)
		}
	}
}

// AssertExternalRefHistoryOutOfOrderMerge pins the out-of-order merge on
// store: before both of the issue's rows nothing qualifies, at the side
// commit's date (a day before its parent) its RN row is the latest
// qualifying one, and past the parent's date the parent's RN-base row wins
// back, though HEAD holds RN. Both reads must agree at every step.
func AssertExternalRefHistoryOutOfOrderMerge(t *testing.T, ctx context.Context, store PreviousExternalRefsStore, scenario *ExternalRefHistoryScenario, prefix string) {
	t.Helper()
	id := scenario.IDs["outoforder"]
	for _, check := range []struct {
		name    string
		cutoff  time.Time
		wantRef string // "" means not found
	}{
		{"before both rows", scenario.OutOfOrderBefore, ""},
		{"at the side commit dated before its parent", scenario.OutOfOrderChild, "RN"},
		{"past the parent", scenario.OutOfOrderPast, "RN-base"},
	} {
		want := ""
		if check.wantRef != "" {
			want = scenario.Ref(prefix, check.wantRef)
		}
		single, found, err := store.PreviousExternalRef(ctx, id, check.cutoff)
		if err != nil || found != (want != "") || single != want {
			t.Fatalf("%s: PreviousExternalRef = (%q, %t, %v), want (%q, %t, nil)", check.name, single, found, err, want, want != "")
		}
		batch, err := store.PreviousExternalRefs(ctx, []string{id}, check.cutoff)
		if err != nil {
			t.Fatalf("%s: PreviousExternalRefs: %v", check.name, err)
		}
		if got, ok := batch[id]; ok != found || got != single {
			t.Errorf("%s: PreviousExternalRefs[%s] = (%q, %t), want the per-issue answer (%q, %t)", check.name, id, got, ok, single, found)
		}
	}
}
