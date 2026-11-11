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

// TestCloneLocalFKGuidanceDirectsExplicitRepair: bd flatten and bd compact
// no longer hard-reset main (they soft-reset since bd-c5s3mv), so the
// severed clone-local FK guidance must not name them as a current cause. It
// directs the explicit repair and says the repair deletes orphaned rows.
func TestCloneLocalFKGuidanceDirectsExplicitRepair(t *testing.T) {
	dc := DoctorCheck{
		Name:    "Clone-Local FKs",
		Message: "1 severed FK(s) on clone-local tables: wisp_labels.fk_wisp_labels_issue (2 orphan(s))",
		Detail:  severedCloneLocalFKDetail(2),
	}
	enrichment := enrichCloneLocalFKs(dc)

	if len(enrichment.commands) != 1 || enrichment.commands[0] != "bd doctor --fix" {
		t.Fatalf("commands = %#v, want the explicit repair 'bd doctor --fix'", enrichment.commands)
	}
	if !strings.Contains(enrichment.explanation, "bd doctor --fix") {
		t.Errorf("explanation does not direct 'bd doctor --fix': %s", enrichment.explanation)
	}
	if !strings.Contains(enrichment.explanation, "deletes") || !strings.Contains(enrichment.explanation, "orphan") {
		t.Errorf("explanation does not say the repair deletes orphaned rows: %s", enrichment.explanation)
	}
	// Any sentence naming flatten or compact must be about older releases.
	for name, text := range map[string]string{"explanation": enrichment.explanation, "observed": enrichment.observed} {
		for _, sentence := range strings.Split(text, ". ") {
			lower := strings.ToLower(sentence)
			if (strings.Contains(lower, "flatten") || strings.Contains(lower, "compact") || strings.Contains(lower, "squash")) &&
				!strings.Contains(lower, "older") {
				t.Errorf("%s attributes a current flatten/compact to a hard reset: %q", name, sentence)
			}
		}
	}
}
