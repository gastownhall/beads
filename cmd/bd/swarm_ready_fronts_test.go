package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

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
			"done":  {ID: "done", Title: "Done", Status: string(types.StatusClosed), Wave: -1},
			"todo":  {ID: "todo", Title: "Todo", Status: string(types.StatusOpen), Wave: -1},
		},
	}
	computeReadyFronts(analysis)
	if len(analysis.ReadyFronts) != 1 || len(analysis.ReadyFronts[0].Issues) != 1 || analysis.ReadyFronts[0].Issues[0] != "todo" {
		t.Fatalf("ready fronts = %+v, want single wave with only todo", analysis.ReadyFronts)
	}
}
