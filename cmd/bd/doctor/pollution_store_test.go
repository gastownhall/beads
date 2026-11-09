//go:build cgo

package doctor

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestPollutionStoreParity runs the full doctor gate and the detailed
// classifier against one real store: they must report the same IDs and
// count, and the evidence lookups must see dependency rows and comments in
// both the permanent and the wisp tables.
func TestPollutionStoreParity(t *testing.T) {
	store := newTestDoltStore(t, "pol")
	ctx := context.Background()

	create := func(issue *types.Issue) *types.Issue {
		t.Helper()
		if issue.Status == "" {
			issue.Status = types.StatusOpen
		}
		if issue.IssueType == "" {
			issue.IssueType = types.TypeTask
		}
		issue.Priority = 2
		if issue.Status == types.StatusClosed {
			now := time.Now()
			issue.ClosedAt = &now
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue %q: %v", issue.Title, err)
		}
		return issue
	}
	link := func(from, to *types.Issue) {
		t.Helper()
		dep := &types.Dependency{IssueID: from.ID, DependsOnID: to.ID, Type: types.DepRelated, CreatedAt: time.Now(), CreatedBy: "test"}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", from.ID, to.ID, err)
		}
	}
	comment := func(issue *types.Issue) {
		t.Helper()
		if _, err := store.AddIssueComment(ctx, issue.ID, "test", "seen on CI"); err != nil {
			t.Fatalf("AddComment %s: %v", issue.ID, err)
		}
	}

	want := []string{
		create(&types.Issue{Title: "Test-quality"}).ID,
		create(&types.Issue{Title: "TEST ISSUE seven", Description: "short"}).ID,
		create(&types.Issue{Title: "a sample issue"}).ID,
		create(&types.Issue{Title: "tmp-wisp", NoHistory: true}).ID,
	}
	create(&types.Issue{ID: "test-ordinary", Title: "Ordinary work"})
	create(&types.Issue{Title: "test-closed", Status: types.StatusClosed})
	create(&types.Issue{Title: "debug-epic", IssueType: types.TypeEpic})
	create(&types.Issue{Title: "test-driver.sh clobbers env", Description: "Sourcing constants.sh overwrites exported paths."})
	real := create(&types.Issue{Title: "Real work", Description: "Something substantive enough."})
	wispReal := create(&types.Issue{Title: "Wisp work", Description: "Something substantive enough.", NoHistory: true})

	outgoing := create(&types.Issue{Title: "test-outgoing"})
	link(outgoing, real)
	incoming := create(&types.Issue{Title: "test-incoming"})
	link(real, incoming)
	wispInbound := create(&types.Issue{Title: "test-wisp-inbound"})
	link(wispReal, wispInbound)
	wispOutgoing := create(&types.Issue{Title: "dummy-wisp-outgoing", NoHistory: true})
	link(wispOutgoing, real)
	commented := create(&types.Issue{Title: "temp-commented"})
	comment(commented)
	wispCommented := create(&types.Issue{Title: "benchmark-wisp-commented", NoHistory: true})
	comment(wispCommented)

	all, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	detailed, err := DetectTestPollution(ctx, store, all)
	if err != nil {
		t.Fatalf("DetectTestPollution: %v", err)
	}
	gate, err := FindTestPollution(ctx, store)
	if err != nil {
		t.Fatalf("FindTestPollution: %v", err)
	}

	detailedIDs, gateIDs := matchIDs(detailed), matchIDs(gate)
	slices.Sort(detailedIDs)
	slices.Sort(gateIDs)
	slices.Sort(want)
	if !slices.Equal(detailedIDs, want) || !slices.Equal(gateIDs, want) {
		t.Fatalf("detailed = %v, gate = %v, want both %v", detailedIDs, gateIDs, want)
	}

	check := checkTestPollutionWithStore(ctx, store)
	if check.Status != StatusWarning || check.Message != "4 potential test issue(s) detected" {
		t.Fatalf("gate check = %q %q, want a warning counting 4", check.Status, check.Message)
	}
}
