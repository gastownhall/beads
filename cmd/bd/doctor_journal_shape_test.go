//go:build cgo

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/types"
)

// findDoctorCheck returns the check named name from checks, or nil.
func findDoctorCheck(checks []doctorCheck, name string) *doctorCheck {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

// TestDoctorJournalShape is T2.12 (BEADS-JOURNAL-PLAN.md §4.2f, PR A2):
// "bd doctor" raises an "Events Journal Shape" finding for a bd_events_journal
// missing an optional column, and `--fix` (fix.EventsJournalShape) clears it
// by applying the converging ignored-plane migration (ignored/0028). A
// counter behind bd_events_journal's own MAX(seq) is also raised, but is
// deliberately NOT claimed as --fix-able: ignored/0028's own header states it
// "never reads or writes bd_events_seq or a single journal row", and when no
// column is also missing there is no sentinel contradiction to float the
// ignored cursor's floor and force the replay that would otherwise touch it
// (ignored/0022's own idempotent high-water-mark raise, re-run only as part
// of that replay).
//
// Scope: this test covers only what A1/A2 actually built and what exists in
// this schema today. The plan's design note (f) also lists "outbox depth and
// the age of its oldest row" and "a NULL epoch"; those reference
// bd_events_outbox and bd_events_seq.epoch, introduced by PRs A5/A6 (§5, a
// separate, later plan item) — neither object exists yet, so there is
// nothing for this test (or the check itself) to exercise for them.
//
// The two sub-findings are tested from independently damaged stores (not one
// store carrying both faults at once): dropping comment_json ALSO floors the
// ignored cursor via its sentinel and forces a 22..28 replay on the next
// writable open, and that replay's pass through ignored/0022 re-raises
// bd_events_seq to GREATEST(current, MAX(seq)) as an unrelated side effect —
// entangling the two findings' fixability would make the counter case look
// fixable only because a *different* finding's repair happened to touch it.
//
// Kills: the check not wired into doctor (runDiagnostics never calling
// doctor.CheckEventsJournalShape) — confirmed via the clean-store sub-test
// below, which goes through runDiagnostics itself rather than calling the
// check function directly, and manually by temporarily removing doctor.go's
// wiring call and observing it fail with "check not found" before restoring
// it; the check function itself going unimplemented (a compile failure);
// and --fix not actually converging the column shape (the post-fix re-probe
// would still see missing comment_json).
func TestDoctorJournalShape(t *testing.T) {
	t.Run("wired into runDiagnostics on a healthy store", func(t *testing.T) {
		tmpDir, store := setupValidateTestDB(t, "jnlok")
		ctx := context.Background()
		store.SetEventsJournalEnabled(true)
		if err := store.EventsJournalActivationError(); err != nil {
			t.Fatalf("activation: %v", err)
		}
		issue := &types.Issue{Title: "t2.12 wiring seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "jnlok"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		store.Close()

		result := runDiagnostics(tmpDir)
		check := findDoctorCheck(result.Checks, "Events Journal Shape")
		if check == nil {
			t.Fatal("Events Journal Shape check not found in runDiagnostics' output")
		}
		if check.Status != statusOK {
			t.Errorf("status = %q, want %q (message: %s, detail: %s)", check.Status, statusOK, check.Message, check.Detail)
		}
	})

	t.Run("missing optional column is raised and fixed", func(t *testing.T) {
		tmpDir, store := setupValidateTestDB(t, "jnlcol")
		ctx := context.Background()
		store.SetEventsJournalEnabled(true)
		if err := store.EventsJournalActivationError(); err != nil {
			t.Fatalf("activation: %v", err)
		}
		issue := &types.Issue{Title: "t2.12 column seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "jnlcol"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		// Damage exactly as the plan's evidence base found it (gas-city-inc:
		// comment_json absent outright).
		if _, err := store.DB().ExecContext(ctx, "ALTER TABLE bd_events_journal DROP COLUMN comment_json"); err != nil {
			t.Fatalf("drop comment_json: %v", err)
		}
		store.Close()

		check := doctor.CheckEventsJournalShape(tmpDir)
		if check.Status != statusWarning {
			t.Fatalf("after damage: status = %q, want %q (message: %s)", check.Status, statusWarning, check.Message)
		}
		if !strings.Contains(check.Detail, "comment_json") {
			t.Errorf("after damage: Detail = %q, want it to mention comment_json", check.Detail)
		}
		if strings.Contains(check.Detail, "bd_events_seq counter") {
			t.Errorf("after damage: Detail = %q, unexpectedly mentions the counter (this store never touched it)", check.Detail)
		}
		if check.Fix == "" || !strings.Contains(check.Fix, "bd doctor --fix") {
			t.Errorf("after damage: Fix = %q, want guidance to run 'bd doctor --fix'", check.Fix)
		}

		if err := fix.EventsJournalShape(tmpDir); err != nil {
			t.Fatalf("fix.EventsJournalShape: %v", err)
		}

		check = doctor.CheckEventsJournalShape(tmpDir)
		if check.Status != statusOK {
			t.Fatalf("after fix: status = %q, want %q (message: %s, detail: %s)", check.Status, statusOK, check.Message, check.Detail)
		}
	})

	t.Run("counter behind MAX(seq) is raised and not claimed fixable", func(t *testing.T) {
		tmpDir, store := setupValidateTestDB(t, "jnlseq")
		ctx := context.Background()
		store.SetEventsJournalEnabled(true)
		if err := store.EventsJournalActivationError(); err != nil {
			t.Fatalf("activation: %v", err)
		}
		issue := &types.Issue{Title: "t2.12 counter seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "jnlseq"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		// Damage ONLY the counter (no column touched, so no sentinel
		// contradiction floors the ignored cursor and nothing replays
		// ignored/0022 on the next writable open).
		if _, err := store.DB().ExecContext(ctx, "UPDATE bd_events_seq SET next_seq = 0 WHERE id = 0"); err != nil {
			t.Fatalf("reseed next_seq behind MAX(seq): %v", err)
		}
		store.Close()

		check := doctor.CheckEventsJournalShape(tmpDir)
		if check.Status != statusWarning {
			t.Fatalf("after damage: status = %q, want %q (message: %s)", check.Status, statusWarning, check.Message)
		}
		if !strings.Contains(check.Detail, "bd_events_seq counter") {
			t.Errorf("after damage: Detail = %q, want it to mention the counter behind MAX(seq)", check.Detail)
		}
		if strings.Contains(check.Detail, "comment_json") {
			t.Errorf("after damage: Detail = %q, unexpectedly mentions comment_json (this store never touched it)", check.Detail)
		}
		if !strings.Contains(check.Detail, "not repaired by 'bd doctor --fix'") {
			t.Errorf("after damage: Detail = %q, want it to disclose this finding is not --fix-able", check.Detail)
		}
		if strings.Contains(check.Fix, "bd doctor --fix") {
			t.Errorf("after damage: Fix = %q, must not claim 'bd doctor --fix' resolves a counter-behind finding", check.Fix)
		}

		if err := fix.EventsJournalShape(tmpDir); err != nil {
			t.Fatalf("fix.EventsJournalShape: %v", err)
		}

		check = doctor.CheckEventsJournalShape(tmpDir)
		if check.Status != statusWarning {
			t.Fatalf("after fix: status = %q, want %q (no pending migration exists to repair a counter-only fault, so --fix is a no-op here): message=%s detail=%s",
				check.Status, statusWarning, check.Message, check.Detail)
		}
		if !strings.Contains(check.Detail, "bd_events_seq counter") {
			t.Errorf("after fix: Detail = %q, want the still-unrepaired counter finding to remain", check.Detail)
		}
	})
}
