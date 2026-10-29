package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestHoldBackRetainedTypesKeepsDecayOffRetainedBeads pins `bd gc`'s decay
// partition: a closed bead of a type types.retained names is held back and
// counted; an unset setting holds nothing back.
func TestHoldBackRetainedTypesKeepsDecayOffRetainedBeads(t *testing.T) {
	op := &types.Issue{ID: "bd-op", IssueType: types.IssueType("op")}
	task := &types.Issue{ID: "bd-task", IssueType: types.TypeTask}

	kept, held := holdBackRetainedTypes([]*types.Issue{op, task}, `["op"]`)
	if held != 1 || len(kept) != 1 || kept[0] != task {
		t.Errorf("holdBackRetainedTypes(op retained) = %v, %d; want [bd-task], 1", kept, held)
	}
	kept, held = holdBackRetainedTypes([]*types.Issue{op, task}, "")
	if held != 0 || len(kept) != 2 {
		t.Errorf("holdBackRetainedTypes(unset) = %v, %d; want both kept, 0", kept, held)
	}
	if line := retainedTypesKeptLine(3, "closed issue(s)"); !strings.Contains(line, "Kept 3 closed issue(s)") || !strings.Contains(line, "--retain-ids") {
		t.Errorf("retainedTypesKeptLine(3) = %q, want the count, the noun and the flag that deletes them", line)
	}
}

// TestRetentionStatsAppearOnlyWhenSomethingWasKept pins the --json shape of
// prune and purge: retention_required is attached when the sweep held a
// retained bead back and absent otherwise, so a workspace without the setting
// sees its old shape.
func TestRetentionStatsAppearOnlyWhenSomethingWasKept(t *testing.T) {
	stats := map[string]interface{}{}
	addRetentionStats(stats, issueops.SweepResult{})
	if _, ok := stats["retention_required"]; ok {
		t.Errorf("retention_required attached for a sweep that kept nothing: %v", stats)
	}
	result := issueops.SweepResult{Skipped: issueops.SweepSkips{RetentionRequired: 2}}
	addRetentionStats(stats, result)
	if stats["retention_required"] != 2 {
		t.Errorf("retention_required = %v, want 2", stats["retention_required"])
	}
	if note := retentionNote(result); !strings.Contains(note, "2 closed bead(s) of a retained type") || !strings.Contains(note, "--retain-ids") {
		t.Errorf("retentionNote = %q, want the count and the flag that deletes them", note)
	}
}

// TestSelectCleanupCandidatesHoldsBackRetainedTypes pins `bd admin cleanup`'s
// selection: pinned beads and beads of a type types.retained names are held
// back and counted, the rest are its ids. Cleanup deletes through the Deleter
// role, which writes no tombstone, so a retained bead is never its to take.
func TestSelectCleanupCandidatesHoldsBackRetainedTypes(t *testing.T) {
	op := &types.Issue{ID: "bd-op", IssueType: types.IssueType("op")}
	task := &types.Issue{ID: "bd-task", IssueType: types.TypeTask}
	pinned := &types.Issue{ID: "bd-pinned", IssueType: types.TypeTask, Pinned: true}
	closed := []*types.Issue{op, task, pinned}

	got, err := selectCleanupCandidates(closed, `["op"]`, false)
	if err != nil {
		t.Fatalf("selectCleanupCandidates: %v", err)
	}
	if strings.Join(got.IDs, ",") != "bd-task" || got.Pinned != 1 || got.RetentionRequired != 1 {
		t.Errorf("selectCleanupCandidates(op retained) = %+v; want ids [bd-task], 1 pinned, 1 retained", got)
	}

	got, err = selectCleanupCandidates(closed, "", false)
	if err != nil {
		t.Fatalf("selectCleanupCandidates(unset): %v", err)
	}
	if strings.Join(got.IDs, ",") != "bd-op,bd-task" || got.RetentionRequired != 0 {
		t.Errorf("selectCleanupCandidates(unset) = %+v; want ids [bd-op bd-task] and nothing retained", got)
	}
}

// TestSelectCleanupCandidatesRefusesCascadeWhileTypesAreRetained pins that
// `bd admin cleanup --cascade` is refused while types.retained names anything:
// the cascade expands to every dependent server-side, past the selection the
// guard filtered, so it could delete a retained bead without a tombstone. With
// nothing retained --cascade behaves as before.
func TestSelectCleanupCandidatesRefusesCascadeWhileTypesAreRetained(t *testing.T) {
	task := &types.Issue{ID: "bd-task", IssueType: types.TypeTask}
	_, err := selectCleanupCandidates([]*types.Issue{task}, "op", true)
	if err == nil || !strings.Contains(err.Error(), "--cascade") || !strings.Contains(err.Error(), issueops.SettingKeyTypesRetained) {
		t.Errorf("selectCleanupCandidates(cascade, op retained) error = %v; want a refusal naming --cascade and %s", err, issueops.SettingKeyTypesRetained)
	}
	if _, err := selectCleanupCandidates([]*types.Issue{task}, "", true); err != nil {
		t.Errorf("selectCleanupCandidates(cascade, unset) = %v; want no refusal", err)
	}
}

// TestRetentionReportAppearsOnlyWhenSomethingWasKept pins the --json member
// the bulk deletes outside the Sweeper role add: retention_required when they
// held a retained bead back, and no member otherwise, so a workspace without
// the setting sees its old shape.
func TestRetentionReportAppearsOnlyWhenSomethingWasKept(t *testing.T) {
	if report := retentionReport(0); report != nil {
		t.Errorf("retentionReport(0) = %v, want nil", report)
	}
	if report := retentionReport(2); report["retention_required"] != 2 || len(report) != 1 {
		t.Errorf("retentionReport(2) = %v, want only retention_required 2", report)
	}
}
