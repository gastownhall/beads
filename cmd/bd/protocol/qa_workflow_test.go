package protocol

import (
	"strings"
	"testing"
)

// TestProtocol_StoredBlockedAppearsInBlockedAndIsClaimable pins the QA
// "ghost blocked" workflow: a stored status=blocked issue must not vanish
// from both ready and blocked, and --claim must resume it when no blockers remain.
func TestProtocol_StoredBlockedAppearsInBlockedAndIsClaimable(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	held := w.create("--title", "Parked work")
	w.run("update", held, "--status", "blocked")

	readyOut := w.run("ready")
	if strings.Contains(readyOut, "No open issues") && !strings.Contains(readyOut, "blocked") {
		t.Fatalf("empty ready must not say 'No open issues' while stored-blocked leftovers exist:\n%s", readyOut)
	}

	blockedOut := w.run("blocked", "--json")
	found := false
	for _, issue := range parseJSONOutput(t, blockedOut) {
		if id, _ := issue["id"].(string); id == held {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("'bd blocked' should include stored-blocked %s\nOutput: %s", held, blockedOut)
	}

	w.run("update", held, "--claim")
	show := w.showJSON(held)
	if status, _ := show["status"].(string); status != "in_progress" {
		t.Fatalf("claim should resume stored-blocked %s, status = %q", held, status)
	}
}

// TestProtocol_ReadyClaimPrefersLeafOverEpic pins the QA claim-the-epic bug:
// `bd ready --claim` must take a leaf child, not the parent epic.
func TestProtocol_ReadyClaimPrefersLeafOverEpic(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	epic := w.create("--title", "Parent epic", "--type", "epic")
	child := w.create("--title", "Leaf work", "--type", "task", "--parent", epic)

	claimedItems := parseJSONOutput(t, w.run("ready", "--claim", "--json"))
	if len(claimedItems) == 0 {
		t.Fatal("ready --claim --json returned no issue")
	}
	id, _ := claimedItems[0]["id"].(string)
	if id == epic {
		t.Fatalf("ready --claim took the epic %s instead of the leaf %s", epic, child)
	}
	if id != child {
		t.Fatalf("ready --claim took %q, want leaf %s (epic %s)", id, child, epic)
	}
}

// TestProtocol_LintTutorialFeatureExitsZero pins the documented quickstart:
// `bd lint` is a report. Missing AC on a feature is a warning, not a failed
// tutorial, unless --strict is set.
func TestProtocol_LintTutorialFeatureExitsZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	w.create("--title", "Add dark mode", "--type", "feature", "--description", "Ship the toggle")

	out, err := w.tryRun("lint")
	if err != nil {
		t.Fatalf("bd lint should exit 0 on template warnings, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "Missing:") && !strings.Contains(out, "warning") {
		t.Fatalf("bd lint should still report warnings:\n%s", out)
	}

	out, code := w.runExpectError("lint", "--strict")
	if code == 0 {
		t.Fatalf("bd lint --strict should fail when warnings exist:\n%s", out)
	}
}
