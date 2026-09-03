package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// fakeSwarmStorage is a minimal in-memory SwarmStorage. deps is keyed by the
// issue holding them (issue_id -> its outgoing dependency records).
type fakeSwarmStorage struct {
	issues map[string]*types.Issue
	deps   map[string][]*types.Dependency
}

func (f *fakeSwarmStorage) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return f.issues[id], nil
}

func (f *fakeSwarmStorage) GetDependents(_ context.Context, id string) ([]*types.Issue, error) {
	var out []*types.Issue
	for issueID, deps := range f.deps {
		for _, d := range deps {
			if d.DependsOnID == id {
				if issue, ok := f.issues[issueID]; ok {
					out = append(out, issue)
				}
			}
		}
	}
	return out, nil
}

func (f *fakeSwarmStorage) GetDependencyRecords(_ context.Context, id string) ([]*types.Dependency, error) {
	return f.deps[id], nil
}

func TestComputeReadyFrontsExcludesClosed(t *testing.T) {
	analysis := &SwarmAnalysis{
		Issues: map[string]*IssueNode{
			"open-a": {
				ID: "open-a", Title: "Open A", Status: string(types.StatusOpen),
				DependsOn: nil, DependedOnBy: []string{"open-b"}, Wave: -1,
			},
			"closed-c": {
				ID: "closed-c", Title: "Closed C", Status: string(types.StatusClosed),
				DependsOn: nil, DependedOnBy: []string{"open-b"}, Wave: -1,
			},
			"open-b": {
				ID: "open-b", Title: "Open B", Status: string(types.StatusOpen),
				// blocked by closed-c and open-a; closed should not hold it out of later wave
				DependsOn: []string{"open-a", "closed-c"}, DependedOnBy: nil, Wave: -1,
			},
		},
	}

	computeReadyFronts(analysis)

	// closed-c must not appear in any ready front
	for _, front := range analysis.ReadyFronts {
		for _, id := range front.Issues {
			if id == "closed-c" {
				t.Fatalf("closed issue listed in wave %d: %v", front.Wave, front.Issues)
			}
		}
	}
	if analysis.Issues["closed-c"].Wave != -1 {
		t.Fatalf("closed issue Wave = %d, want -1 (not assigned)", analysis.Issues["closed-c"].Wave)
	}

	// open-a is wave 0; open-b becomes ready after open-a (closed-c ignored)
	if analysis.Issues["open-a"].Wave != 0 {
		t.Fatalf("open-a Wave = %d, want 0", analysis.Issues["open-a"].Wave)
	}
	if analysis.Issues["open-b"].Wave != 1 {
		t.Fatalf("open-b Wave = %d, want 1 (only open-a is an open blocker)", analysis.Issues["open-b"].Wave)
	}
	if analysis.MaxParallelism != 1 {
		t.Fatalf("MaxParallelism = %d, want 1", analysis.MaxParallelism)
	}
	if analysis.EstimatedSessions != 2 {
		t.Fatalf("EstimatedSessions = %d, want 2 open issues", analysis.EstimatedSessions)
	}
}

func TestComputeReadyFrontsClosedLeafNotInWave0(t *testing.T) {
	analysis := &SwarmAnalysis{
		Issues: map[string]*IssueNode{
			"done": {ID: "done", Title: "Done", Status: string(types.StatusClosed), Wave: -1},
			"todo": {ID: "todo", Title: "Todo", Status: string(types.StatusOpen), Wave: -1},
		},
	}
	computeReadyFronts(analysis)
	if len(analysis.ReadyFronts) != 1 || len(analysis.ReadyFronts[0].Issues) != 1 || analysis.ReadyFronts[0].Issues[0] != "todo" {
		t.Fatalf("ready fronts = %+v, want single wave with only todo", analysis.ReadyFronts)
	}
}

// A closed<->open cycle must not be reported as a structural error, nor suppress
// ready fronts for unrelated open children (GH#4564). Pre-fix, the recorded cycle
// error made computeReadyFronts bail out and left ReadyFronts empty.
func TestAnalyzeEpicForSwarmClosedCycleDoesNotSuppressOpenFronts(t *testing.T) {
	ctx := context.Background()

	epic := &types.Issue{ID: "epic-1", Title: "Epic", Status: types.StatusOpen}
	closedA := &types.Issue{ID: "closed-a", Title: "Closed A", Status: types.StatusClosed}
	openB := &types.Issue{ID: "open-b", Title: "Open B", Status: types.StatusOpen}
	openC := &types.Issue{ID: "open-c", Title: "Unrelated Open C", Status: types.StatusOpen}

	store := &fakeSwarmStorage{
		issues: map[string]*types.Issue{
			epic.ID:    epic,
			closedA.ID: closedA,
			openB.ID:   openB,
			openC.ID:   openC,
		},
		deps: map[string][]*types.Dependency{
			// closed-a <-> open-b: mutual cycle, one side closed.
			"closed-a": {
				{IssueID: "closed-a", DependsOnID: epic.ID, Type: types.DepParentChild},
				{IssueID: "closed-a", DependsOnID: "open-b", Type: types.DepBlocks},
			},
			"open-b": {
				{IssueID: "open-b", DependsOnID: epic.ID, Type: types.DepParentChild},
				{IssueID: "open-b", DependsOnID: "closed-a", Type: types.DepBlocks},
			},
			// open-c: unrelated open child with no dependencies of its own.
			"open-c": {
				{IssueID: "open-c", DependsOnID: epic.ID, Type: types.DepParentChild},
			},
		},
	}

	analysis, err := analyzeEpicForSwarm(ctx, store, epic)
	if err != nil {
		t.Fatalf("analyzeEpicForSwarm() error = %v", err)
	}

	if len(analysis.Errors) != 0 {
		t.Fatalf("Errors = %v, want none (closed-a/open-b cycle should be excluded)", analysis.Errors)
	}
	if !analysis.Swarmable {
		t.Fatalf("Swarmable = false, want true")
	}
	if len(analysis.ReadyFronts) == 0 {
		t.Fatalf("ReadyFronts is empty, want at least wave 0 with open-b and open-c")
	}

	wave0 := analysis.ReadyFronts[0].Issues
	inWave0 := make(map[string]bool, len(wave0))
	for _, id := range wave0 {
		inWave0[id] = true
	}
	if !inWave0["open-b"] {
		t.Fatalf("wave 0 = %v, want open-b present (closed-a dependency satisfied)", wave0)
	}
	if !inWave0["open-c"] {
		t.Fatalf("wave 0 = %v, want unrelated open-c present", wave0)
	}
	if inWave0["closed-a"] {
		t.Fatalf("wave 0 = %v, closed-a must not appear in a ready front", wave0)
	}
}
