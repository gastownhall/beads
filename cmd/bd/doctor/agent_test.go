package doctor

import (
	"strings"
	"testing"
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
// TestEnrichCloneLocalFKsDirectsRepairWithoutCurrentSquashAttribution:
// flatten and compact soft-reset main since bd-7bpkd's fix, so the agent
// explanation must not blame a flatten or compact that just ran for severed
// clone-local FKs. It keeps the historical cause (a hard reset dropped the
// FKs) and directs the explicit repair.
func TestEnrichCloneLocalFKsDirectsRepairWithoutCurrentSquashAttribution(t *testing.T) {
	enrichment := enrichCloneLocalFKs(DoctorCheck{
		Message: "1 severed FK(s) on clone-local tables: events.fk_events_issue (2 orphan(s))",
	})

	if !strings.Contains(enrichment.explanation, "bd doctor --fix") {
		t.Fatalf("expected explicit repair guidance, got: %s", enrichment.explanation)
	}
	if !strings.Contains(enrichment.explanation, "soft reset") {
		t.Fatalf("expected the explanation to say flatten/compact now soft-reset, got: %s", enrichment.explanation)
	}
	if strings.Contains(enrichment.explanation, "flatten/compact squash") {
		t.Fatalf("explanation attributes the severing to a current flatten/compact squash: %s", enrichment.explanation)
	}
	if len(enrichment.commands) == 0 || enrichment.commands[0] != "bd doctor --fix" {
		t.Fatalf("expected 'bd doctor --fix' first, got %#v", enrichment.commands)
	}
}

// TestCloneLocalFKDetailKeepsHistoryWithoutBlamingCurrentSquash pins the
// check's Detail line to the same rule: the historical cause kept, current
// flatten and compact not blamed.
func TestCloneLocalFKDetailKeepsHistoryWithoutBlamingCurrentSquash(t *testing.T) {
	detail := cloneLocalFKDetail(2)
	if !strings.Contains(detail, "hard reset") {
		t.Fatalf("expected the historical cause kept, got: %s", detail)
	}
	if !strings.Contains(detail, "soft-reset") {
		t.Fatalf("expected the detail to say flatten/compact soft-reset now, got: %s", detail)
	}
	if strings.Contains(detail, "(squash, merge abort)") {
		t.Fatalf("detail attributes the severing to a squash: %s", detail)
	}
}

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
