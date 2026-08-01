package main

import (
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func mkPollutionIssue(id, title, desc string, status types.Status, typ types.IssueType) *types.Issue {
	return &types.Issue{
		ID:          id,
		Title:       title,
		Description: desc,
		Status:      status,
		IssueType:   typ,
		CreatedAt:   time.Now(),
	}
}

func TestDetectTestPollution_SkipsClosedAndEpics(t *testing.T) {
	issues := []*types.Issue{
		mkPollutionIssue("bd-1", "test issue fixture", "", types.StatusClosed, types.TypeTask),
		mkPollutionIssue("bd-2", "test issue fixture", "", types.StatusOpen, types.TypeEpic),
		mkPollutionIssue("bd-3", "test issue fixture", "", types.StatusOpen, types.TypeTask),
	}
	got := detectTestPollution(issues)
	if len(got) != 1 || got[0].issue.ID != "bd-3" {
		t.Fatalf("got %+v, want only open non-epic bd-3", got)
	}
}

func TestDetectTestPollution_PrefixAloneNotEnough(t *testing.T) {
	// Real infra bug titles: prefix + substantive description → not pollution.
	issues := []*types.Issue{
		mkPollutionIssue("dcr-lzse", "test-driver.sh isolation broken — constants.sh clobbers env",
			"Long description of a real test-harness bug with repro and expected behavior.",
			types.StatusOpen, types.TypeBug),
		mkPollutionIssue("dcr-9f3", "Test-quality architecture",
			"Epic-scoped plan for improving component test suite quality across the monorepo.",
			types.StatusOpen, types.TypeEpic),
	}
	got := detectTestPollution(issues)
	if len(got) != 0 {
		t.Fatalf("prefix-only real work flagged: %+v", got)
	}
}

func TestDetectTestPollution_RequiresCorroboration(t *testing.T) {
	// Prefix + empty description → pollution.
	issues := []*types.Issue{
		mkPollutionIssue("tmp-1", "test-foo bare fixture", "", types.StatusOpen, types.TypeTask),
	}
	got := detectTestPollution(issues)
	if len(got) != 1 {
		t.Fatalf("expected 1 pollution hit, got %d", len(got))
	}
	if got[0].score < 0.7 {
		t.Fatalf("score = %v, want >= 0.7", got[0].score)
	}
}

func TestDetectTestPollution_BulkCreateNotEvidence(t *testing.T) {
	// Many issues same minute with substantive titles/descriptions must not flag.
	var issues []*types.Issue
	now := time.Now()
	for i := 0; i < 12; i++ {
		iss := mkPollutionIssue(
			"imp-"+strconv.Itoa(i),
			"test-quality report item",
			"Imported engineering task from report batch with full body text here.",
			types.StatusOpen,
			types.TypeTask,
		)
		iss.CreatedAt = now
		issues = append(issues, iss)
	}
	got := detectTestPollution(issues)
	if len(got) != 0 {
		t.Fatalf("bulk import of real work flagged as pollution: %d hits", len(got))
	}
}

func TestDetectTestPollution_ShortDescriptionDeadZoneClosed(t *testing.T) {
	// Pins the GH#5137 dead-zone fix: prefix (0.4) + prefix-gated short-desc (0.3)
	// now clears the 0.7 threshold, where the old flat 0.2 weight fell short at 0.6.
	issues := []*types.Issue{
		mkPollutionIssue("bd-1hao", "test-foo", "x", types.StatusOpen, types.TypeTask),
	}
	got := detectTestPollution(issues)
	if len(got) != 1 {
		t.Fatalf("expected dead-zone fixture to be flagged, got %d hits", len(got))
	}
	if got[0].score < 0.7 {
		t.Fatalf("score = %v, want >= 0.7 (0.4 prefix + 0.3 short description)", got[0].score)
	}
}

func TestDetectTestPollution_BarePrefixNormalDescriptionStillNotFlagged(t *testing.T) {
	// A bare test prefix with a normal-length description must stay unflagged
	// (score 0.4, no corroboration) despite the prefix-gated short-desc bump.
	issues := []*types.Issue{
		mkPollutionIssue("bd-9f3", "test-migration cleanup",
			"Removes the legacy migration path once the new one is verified stable.",
			types.StatusOpen, types.TypeTask),
	}
	got := detectTestPollution(issues)
	if len(got) != 0 {
		t.Fatalf("bare prefix + normal-length description flagged as pollution: %+v", got)
	}
}

func TestDetectTestPollution_SequentialIDShortDescriptionNoPrefixNotFlagged(t *testing.T) {
	// Guards against a flat (non-prefix-gated) bump: counter-mode/--id IDs
	// (NextCounterIDTx, cmd/bd/create.go) match sequentialPattern on real
	// issues too, so the bump must require a test-prefixed title, not just this ID shape.
	issues := []*types.Issue{
		mkPollutionIssue("bd-42", "Fix login redirect bug", "See logs", types.StatusOpen, types.TypeBug),
	}
	got := detectTestPollution(issues)
	if len(got) != 0 {
		t.Fatalf("ordinary issue with sequential ID + short description flagged as pollution: %+v", got)
	}
}

func TestDetectTestPollution_PrefixSequentialIDShortDescriptionScoresHigh(t *testing.T) {
	// Pins maphew's review arithmetic for a test-prefixed fixture with a sequential ID and thin description.
	issues := []*types.Issue{
		mkPollutionIssue("test-42", "test-42 fixture", "wip", types.StatusOpen, types.TypeTask),
	}
	got := detectTestPollution(issues)
	if len(got) != 1 {
		t.Fatalf("expected fixture to be flagged, got %d hits", len(got))
	}
	if got[0].score < 0.9 {
		t.Fatalf("score = %v, want >= 0.9 (high confidence)", got[0].score)
	}
}

func TestDetectTestPollution_PrefixHashIDThinDescriptionAtBoundary(t *testing.T) {
	// Prefixed title (0.4) + hash id + thin description (0.3) = 0.70 (GH#5137):
	// flagged for review, but below the 0.9 band clean mode acts on.
	issues := []*types.Issue{
		mkPollutionIssue("dcr-a3f9", "test-driver.sh isolation broken — constants.sh clobbers env",
			"Broke isolation", types.StatusOpen, types.TypeBug),
	}
	got := detectTestPollution(issues)
	if len(got) != 1 {
		t.Fatalf("expected boundary fixture to be flagged for review, got %d hits", len(got))
	}
	if got[0].score < 0.7 || got[0].score >= 0.9 {
		t.Fatalf("score = %v, want exactly 0.70 (flagged, but below --clean's 0.9 high-confidence gate)", got[0].score)
	}
}
