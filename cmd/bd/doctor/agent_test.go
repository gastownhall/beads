package doctor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestEnrichFreshClone_UsesBootstrapFirstGuidance(t *testing.T) {
	dc := DoctorCheck{Name: "Fresh Clone", Message: "database not found on configured server"}
	enrichment := enrichFreshClone(dc)

	if enrichment.severity != "blocking" {
		t.Fatalf("expected blocking severity, got %q", enrichment.severity)
	}
	if !strings.Contains(enrichment.explanation, "bd bootstrap") {
		t.Fatalf("expected bootstrap guidance, got: %s", enrichment.explanation)
	}
	if len(enrichment.commands) != 1 || enrichment.commands[0] != "bd bootstrap" {
		t.Fatalf("expected bootstrap-first commands, got %#v", enrichment.commands)
	}
	if strings.Contains(enrichment.explanation, "run bd init") {
		t.Fatalf("did not expect init-first guidance, got: %s", enrichment.explanation)
	}
}

func TestEnrichFreshClone_WithSyncRemoteMentionsBootstrapAndFallback(t *testing.T) {
	dc := DoctorCheck{Name: "Fresh Clone", Message: "sync.remote is configured but database not found"}
	enrichment := enrichFreshClone(dc)

	if !strings.Contains(enrichment.explanation, "bd bootstrap") {
		t.Fatalf("expected bootstrap guidance, got: %s", enrichment.explanation)
	}
	if len(enrichment.commands) != 2 || enrichment.commands[0] != "bd bootstrap" {
		t.Fatalf("expected bootstrap-first command list, got %#v", enrichment.commands)
	}
	if !strings.Contains(enrichment.commands[1], "sync.remote") {
		t.Fatalf("expected sync.remote fallback command, got %#v", enrichment.commands)
	}
}

// TestPruneEnrichmentsRecommendRealCommand pins the agent-facing pruning
// advice to a command that exists: these enrichments used to suggest
// 'bd cleanup', which is not a root-level command in this build (cleanup
// lives under 'bd admin cleanup'). 'bd prune --older-than 90d' previews by
// default, matching the "optional and destructive" framing.
func TestPruneEnrichmentsRecommendRealCommand(t *testing.T) {
	enrichments := map[string]agentEnrichment{
		"Large Database":      enrichLargeDatabase(DoctorCheck{Message: "6000 closed issues (threshold: 5000)"}),
		"Stale Closed Issues": enrichStaleClosedIssues(DoctorCheck{Message: "5000 stale closed issues"}),
	}
	for name, enrichment := range enrichments {
		if len(enrichment.commands) == 0 {
			t.Errorf("%s enrichment has no commands", name)
			continue
		}
		for _, cmd := range enrichment.commands {
			if strings.Contains(cmd, "bd cleanup") {
				t.Errorf("%s enrichment recommends nonexistent 'bd cleanup': %q", name, cmd)
			}
		}
		if !strings.HasPrefix(enrichment.commands[0], "bd prune") {
			t.Errorf("%s enrichment should recommend 'bd prune', got %#v", name, enrichment.commands)
		}
	}
}

// listedSearcher is an IssueSearcher whose search finds issues, or fails
// with err.
type listedSearcher struct {
	issues []*types.Issue
	err    error
}

func (s listedSearcher) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return s.issues, s.err
}

// TestEnrichBlockedStatusLeavesTheReleaseToReview pins bd doctor --agent's
// form of the Blocked Status advisory (bd-e5d8fd). The generic fallback
// reported it as degraded, expected the check to pass, and handed the agent
// the check's Fix text, status release included, as a command. A hold may
// be meant, so the agent gets an advisory whose commands inspect an issue or
// record a blocker, with the release named only for after review. A failed
// search lists nothing to review and stays degraded.
func TestEnrichBlockedStatusLeavesTheReleaseToReview(t *testing.T) {
	ctx := context.Background()

	listed := EnrichForAgent(CheckBlockedStatus(ctx, listedSearcher{issues: []*types.Issue{{ID: "bd-held", Status: types.StatusBlocked}}}), FixGate{})
	if listed.Name != BlockedStatusCheckName || listed.Severity != "advisory" {
		t.Errorf("listed hold: %s severity %q, want %s advisory", listed.Name, listed.Severity, BlockedStatusCheckName)
	}
	if want := []string{"bd show <id>", "bd dep add <id> <blocker-id>"}; !slices.Equal(listed.Commands, want) {
		t.Errorf("listed hold: commands %#v, want %#v: inspect or record a blocker, never release", listed.Commands, want)
	}
	if !strings.Contains(listed.Explanation, "Only after that review, and only when nothing holds the issue, release it with 'bd update <id> --status open'") {
		t.Errorf("listed hold: explanation %q, want the release named for after review only", listed.Explanation)
	}
	if want := "Each listed hold reviewed: released, given a recorded blocker, or kept on purpose"; listed.Expected != want {
		t.Errorf("listed hold: expected %q, want %q", listed.Expected, want)
	}
	if !strings.Contains(listed.Observed, "bd-held") {
		t.Errorf("listed hold: observed %q, want it to name bd-held", listed.Observed)
	}

	failed := EnrichForAgent(CheckBlockedStatus(ctx, listedSearcher{err: errors.New("server gone")}), FixGate{})
	if failed.Severity != "degraded" || len(failed.Commands) != 0 || !strings.Contains(failed.Explanation, "server gone") {
		t.Errorf("failed search: severity %q, commands %#v, explanation %q; want degraded, no commands, the error named", failed.Severity, failed.Commands, failed.Explanation)
	}
}
