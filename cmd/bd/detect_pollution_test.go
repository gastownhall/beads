package main

import (
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func issue(id, title, desc string, status types.Status, typ types.IssueType) *types.Issue {
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
		issue("bd-1", "test issue fixture", "", types.StatusClosed, types.TypeTask),
		issue("bd-2", "test issue fixture", "", types.StatusOpen, types.TypeEpic),
		issue("bd-3", "test issue fixture", "", types.StatusOpen, types.TypeTask),
	}
	got := detectTestPollution(issues)
	if len(got) != 1 || got[0].issue.ID != "bd-3" {
		t.Fatalf("got %+v, want only open non-epic bd-3", got)
	}
}

func TestDetectTestPollution_PrefixAloneNotEnough(t *testing.T) {
	// Real infra bug titles: prefix + substantive description → not pollution.
	issues := []*types.Issue{
		issue("dcr-lzse", "test-driver.sh isolation broken — constants.sh clobbers env",
			"Long description of a real test-harness bug with repro and expected behavior.",
			types.StatusOpen, types.TypeBug),
		issue("dcr-9f3", "Test-quality architecture",
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
		issue("tmp-1", "test-foo bare fixture", "", types.StatusOpen, types.TypeTask),
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
		iss := issue(
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
