//go:build regression

// discovery_test.go contains tests discovered during manual regression testing
// on 2026-02-22. These tests exercise the candidate binary ONLY (not differential)
// since bd export was removed from main (BUG-1 in DISCOVERY.md).
//
// TestMain starts an isolated Dolt server on a dynamic port (via BEADS_DOLT_PORT).
// Each test uses a unique prefix to avoid cross-contamination (BUG-6).
package regression

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uniquePrefix returns a random prefix for test isolation on shared Dolt server.
func uniquePrefix(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("t%d", rand.Intn(99999))
}

// newCandidateWorkspace creates a workspace using only the candidate binary with a unique prefix.
func newCandidateWorkspace(t *testing.T) *workspace {
	t.Helper()
	dir := t.TempDir()
	w := &workspace{dir: dir, bdPath: candidateBin, t: t}
	w.git("init")
	w.git("config", "user.name", "regression-test")
	w.git("config", "user.email", "test@regression.test")

	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w.git("add", ".")
	w.git("commit", "-m", "initial")
	w.run("init", "--prefix", uniquePrefix(t), "--quiet")
	return w
}

// parseJSON parses JSON array output from bd commands.
func parseJSON(t *testing.T, data string) []map[string]any {
	t.Helper()
	var result []map[string]any
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatalf("parsing JSON: %v\ndata: %s", err, data)
	}
	return result
}

// parseIDs extracts "id" fields from JSON array output.
func parseIDs(t *testing.T, data string) []string {
	t.Helper()
	items := parseJSON(t, data)
	var ids []string
	for _, item := range items {
		if id, ok := item["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// containsID checks if an ID is in a list of IDs.
func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// =============================================================================
// BUG REPRODUCTION TESTS
// =============================================================================

// TestBug2_DepTreeShowsNoChildren reproduces GH#1954: dep tree only shows root.
// Root cause: buildDependencyTree() never sets TreeNode.ParentID.
func TestBug2_DepTreeShowsNoChildren(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Top", "--type", "epic", "--priority", "1")
	b := w.create("--title", "Left", "--type", "task", "--priority", "2")
	c := w.create("--title", "Right", "--type", "task", "--priority", "2")
	d := w.create("--title", "Bottom", "--type", "task", "--priority", "3")

	w.run("dep", "add", a, b, "--type", "blocks")
	w.run("dep", "add", a, c, "--type", "blocks")
	w.run("dep", "add", b, d, "--type", "blocks")
	w.run("dep", "add", c, d, "--type", "blocks")

	out := w.run("dep", "tree", a)

	// The tree should contain all 4 issue IDs
	for _, id := range []string{a, b, c, d} {
		if !strings.Contains(out, id) {
			t.Errorf("dep tree output missing %s:\n%s", id, out)
		}
	}
}

// TestBug3_DepTreeReadyAnnotation checks that blocked root shows [BLOCKED] not [READY].
func TestBug3_DepTreeReadyAnnotation(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Blocked root", "--type", "task", "--priority", "2")
	b := w.create("--title", "Blocker", "--type", "task", "--priority", "1")
	w.run("dep", "add", a, b, "--type", "blocks")

	out := w.run("dep", "tree", a)

	if strings.Contains(out, "[READY]") {
		t.Errorf("blocked root should not show [READY]:\n%s", out)
	}
}

// TestBug4_ListStatusBlocked checks that list --status blocked returns blocked issues.
func TestBug4_ListStatusBlocked(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Blocked issue", "--type", "task", "--priority", "2")
	b := w.create("--title", "Blocker issue", "--type", "task", "--priority", "1")
	w.run("dep", "add", a, b, "--type", "blocks")

	// bd blocked should find a
	blockedOut := w.run("blocked", "--json")
	blockedIDs := parseIDs(t, blockedOut)
	if !containsID(blockedIDs, a) {
		t.Errorf("bd blocked should include %s, got: %v", a, blockedIDs)
	}

	// bd list --status blocked should also find a
	listOut := w.run("list", "--status", "blocked", "--json", "-n", "0")
	listIDs := parseIDs(t, listOut)
	if !containsID(listIDs, a) {
		t.Errorf("bd list --status blocked should include %s, got: %v", a, listIDs)
	}
}

// TestBug7_DepAddOverwritesType checks that dep add doesn't silently overwrite dep type.
func TestBug7_DepAddOverwritesType(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Source", "--type", "task", "--priority", "2")
	b := w.create("--title", "Target", "--type", "task", "--priority", "2")

	w.run("dep", "add", a, b, "--type", "blocks")

	// After adding blocks, a should be blocked
	blockedOut := w.run("blocked", "--json")
	blockedIDs := parseIDs(t, blockedOut)
	if !containsID(blockedIDs, a) {
		t.Fatalf("after adding blocks dep, %s should be blocked", a)
	}

	// Now add caused-by on the SAME pair — should either fail or preserve blocks
	w.run("dep", "add", a, b, "--type", "caused-by")

	// a should STILL be blocked (blocks dep should be preserved)
	blockedOut2 := w.run("blocked", "--json")
	blockedIDs2 := parseIDs(t, blockedOut2)
	if !containsID(blockedIDs2, a) {
		t.Errorf("after adding caused-by, %s should still be blocked (blocks dep lost!)", a)
	}
}

// TestBug8_ReparentDualParent checks that reparented child only shows under new parent.
func TestBug8_ReparentDualParent(t *testing.T) {
	w := newCandidateWorkspace(t)

	p1 := w.create("--title", "Parent1", "--type", "epic", "--priority", "1")
	p2 := w.create("--title", "Parent2", "--type", "epic", "--priority", "1")
	ch := w.create("--title", "Child", "--type", "task", "--priority", "2", "--parent", p1)

	// Reparent to p2
	w.run("update", ch, "--parent", p2)

	// Child should only appear under p2
	p1Children := parseIDs(t, w.run("children", p1, "--json"))
	p2Children := parseIDs(t, w.run("children", p2, "--json"))

	if containsID(p1Children, ch) {
		t.Errorf("after reparent, old parent %s should not list child %s", p1, ch)
	}
	if !containsID(p2Children, ch) {
		t.Errorf("after reparent, new parent %s should list child %s", p2, ch)
	}
}

// TestBug9_ListReadyIncludesBlocked checks list --ready vs bd ready parity.
func TestBug9_ListReadyIncludesBlocked(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Blocked", "--type", "task", "--priority", "2")
	b := w.create("--title", "Blocker", "--type", "task", "--priority", "1")
	c := w.create("--title", "Free", "--type", "task", "--priority", "3")
	w.run("dep", "add", a, b, "--type", "blocks")

	listReady := parseIDs(t, w.run("list", "--ready", "-n", "0", "--json"))
	bdReady := parseIDs(t, w.run("ready", "-n", "0", "--json"))

	// a (blocked) should NOT be in bd ready
	if containsID(bdReady, a) {
		t.Errorf("bd ready should not include blocked %s", a)
	}

	// b and c should be in both
	if !containsID(bdReady, b) {
		t.Errorf("bd ready should include unblocked %s", b)
	}
	if !containsID(bdReady, c) {
		t.Errorf("bd ready should include free %s", c)
	}

	// Ideally list --ready should match bd ready
	if containsID(listReady, a) && !containsID(bdReady, a) {
		t.Logf("KNOWN: list --ready includes blocked %s but bd ready does not", a)
	}
}

// =============================================================================
// PROTOCOL INVARIANT TESTS (working correctly, good to formalize)
// =============================================================================

// TestProtocol_CloseGuardRespectDepTypes verifies close guard only applies to blocks.
func TestProtocol_CloseGuardRespectDepTypes(t *testing.T) {
	w := newCandidateWorkspace(t)

	for _, depType := range []string{"caused-by", "validates", "tracks"} {
		t.Run(depType, func(t *testing.T) {
			a := w.create("--title", "Source "+depType, "--type", "task", "--priority", "2")
			b := w.create("--title", "Target "+depType, "--type", "task", "--priority", "2")
			w.run("dep", "add", a, b, "--type", depType)

			// Non-blocking deps should allow close
			w.run("close", a)
			out := parseJSON(t, w.run("show", a, "--json"))
			if out[0]["status"] != "closed" {
				t.Errorf("close should succeed with %s dep, got status=%v", depType, out[0]["status"])
			}
		})
	}

	// blocks should prevent close
	t.Run("blocks", func(t *testing.T) {
		a := w.create("--title", "Blocked source", "--type", "task", "--priority", "2")
		b := w.create("--title", "Blocker target", "--type", "task", "--priority", "2")
		w.run("dep", "add", a, b, "--type", "blocks")

		out, _ := w.tryRun("close", a)
		if !strings.Contains(out, "blocked by open issues") {
			t.Errorf("close of blocked issue should be rejected, got: %s", out)
		}

		// Verify still open
		showOut := parseJSON(t, w.run("show", a, "--json"))
		if showOut[0]["status"] != "open" {
			t.Errorf("blocked issue should still be open, got: %v", showOut[0]["status"])
		}
	})
}

// TestProtocol_EpicLifecycle verifies epic doesn't auto-close when all children close.
func TestProtocol_EpicLifecycle(t *testing.T) {
	w := newCandidateWorkspace(t)

	epic := w.create("--title", "Epic", "--type", "epic", "--priority", "1")
	c1 := w.create("--title", "Child1", "--type", "task", "--priority", "2", "--parent", epic)
	c2 := w.create("--title", "Child2", "--type", "task", "--priority", "2", "--parent", epic)

	// Close all children
	w.run("close", c1)
	w.run("close", c2)

	// Epic should still be open
	epicData := parseJSON(t, w.run("show", epic, "--json"))
	if epicData[0]["status"] != "open" {
		t.Errorf("epic should remain open after all children closed, got: %v", epicData[0]["status"])
	}

	// Epic should be in ready list
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, epic) {
		t.Errorf("epic with all children closed should be in ready list")
	}
}

// TestProtocol_DeleteCleansUpDeps verifies delete removes dependency links.
func TestProtocol_DeleteCleansUpDeps(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Dependent", "--type", "task", "--priority", "2")
	b := w.create("--title", "Will delete", "--type", "task", "--priority", "2")
	w.run("dep", "add", a, b, "--type", "blocks")

	// Verify a is blocked
	blockedIDs := parseIDs(t, w.run("blocked", "--json"))
	if !containsID(blockedIDs, a) {
		t.Fatalf("a should be blocked before delete")
	}

	// Delete b
	w.run("delete", b, "--force")

	// a should be ready now
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, a) {
		t.Errorf("after deleting blocker, %s should be ready", a)
	}
}

// TestProtocol_ReopenPreservesDeps verifies close/reopen preserves dependencies.
func TestProtocol_ReopenPreservesDeps(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Will reopen", "--type", "task", "--priority", "2")
	b := w.create("--title", "Dep target", "--type", "task", "--priority", "2")
	w.run("dep", "add", a, b, "--type", "caused-by")
	w.run("label", "add", a, "important")
	w.run("comments", "add", a, "Test comment")

	// Close and reopen
	w.run("close", a)
	w.run("reopen", a)

	// Verify data preserved
	data := parseJSON(t, w.run("show", a, "--json"))
	issue := data[0]

	if issue["status"] != "open" {
		t.Errorf("reopened issue should be open, got: %v", issue["status"])
	}

	deps, _ := issue["dependencies"].([]any)
	if len(deps) == 0 {
		t.Errorf("dependencies should be preserved after reopen")
	}

	labels, _ := issue["labels"].([]any)
	if len(labels) == 0 {
		t.Errorf("labels should be preserved after reopen")
	}

	comments, _ := issue["comments"].([]any)
	if len(comments) == 0 {
		t.Errorf("comments should be preserved after reopen")
	}
}

// TestProtocol_TransitiveBlockingChain verifies cascade unblocking.
func TestProtocol_TransitiveBlockingChain(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "A head", "--type", "task", "--priority", "1")
	b := w.create("--title", "B mid", "--type", "task", "--priority", "2")
	c := w.create("--title", "C mid", "--type", "task", "--priority", "3")
	d := w.create("--title", "D leaf", "--type", "task", "--priority", "4")

	w.run("dep", "add", a, b, "--type", "blocks")
	w.run("dep", "add", b, c, "--type", "blocks")
	w.run("dep", "add", c, d, "--type", "blocks")

	// Only D should be ready
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, d) {
		t.Errorf("D (leaf) should be ready")
	}
	for _, id := range []string{a, b, c} {
		if containsID(readyIDs, id) {
			t.Errorf("%s should NOT be ready (blocked)", id)
		}
	}

	// Close D → C becomes ready
	w.run("close", d)
	readyIDs = parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, c) {
		t.Errorf("after closing D, C should be ready")
	}
	if containsID(readyIDs, b) {
		t.Errorf("B should still be blocked")
	}

	// Close C → B becomes ready
	w.run("close", c)
	readyIDs = parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, b) {
		t.Errorf("after closing C, B should be ready")
	}

	// Close B → A becomes ready
	w.run("close", b)
	readyIDs = parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, a) {
		t.Errorf("after closing B, A should be ready")
	}
}

// TestProtocol_CircularDepPrevention verifies cycle detection.
func TestProtocol_CircularDepPrevention(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "X", "--type", "task", "--priority", "2")
	b := w.create("--title", "Y", "--type", "task", "--priority", "2")
	c := w.create("--title", "Z", "--type", "task", "--priority", "2")

	w.run("dep", "add", a, b, "--type", "blocks")
	w.run("dep", "add", b, c, "--type", "blocks")

	// Attempt to create cycle
	out, err := w.tryRun("dep", "add", c, a, "--type", "blocks")
	if err == nil {
		t.Errorf("creating cycle should fail, but got success: %s", out)
	}
	if !strings.Contains(out, "cycle") {
		t.Errorf("error should mention cycle, got: %s", out)
	}

	// Verify no cycle exists
	cycleOut := w.run("dep", "cycles")
	if !strings.Contains(cycleOut, "No dependency cycles") {
		t.Errorf("dep cycles should find none, got: %s", cycleOut)
	}
}

// TestProtocol_CloseForceOverridesGuard verifies --force bypasses close guard.
// NOTE: Close guard prints to stderr but returns exit 0 (BUG-10),
// so we check output text instead of error code.
func TestProtocol_CloseForceOverridesGuard(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Blocked", "--type", "task", "--priority", "2")
	b := w.create("--title", "Blocker", "--type", "task", "--priority", "1")
	w.run("dep", "add", a, b, "--type", "blocks")

	// Normal close should be rejected (prints to stderr, but BUG-10: exit code is 0)
	out := w.run("close", a)
	if !strings.Contains(out, "blocked by open issues") && !strings.Contains(out, "cannot close") {
		t.Fatalf("close without --force should mention blocking, got: %s", out)
	}

	// Issue should still be open
	data := parseJSON(t, w.run("show", a, "--json"))
	if data[0]["status"] != "open" {
		t.Fatalf("blocked issue should remain open after close guard, got: %v", data[0]["status"])
	}

	// Force close should succeed
	w.run("close", a, "--force")
	data = parseJSON(t, w.run("show", a, "--json"))
	if data[0]["status"] != "closed" {
		t.Errorf("force close should succeed, got status=%v", data[0]["status"])
	}
}

// TestBug10_CloseGuardExitCode verifies close guard returns non-zero exit for blocked issues.
// Currently FAILS: close guard prints to stderr but returns exit 0.
func TestBug10_CloseGuardExitCode(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Blocked", "--type", "task", "--priority", "2")
	b := w.create("--title", "Blocker", "--type", "task", "--priority", "1")
	w.run("dep", "add", a, b, "--type", "blocks")

	// Close of blocked issue should return non-zero exit code
	_, err := w.tryRun("close", a)
	if err == nil {
		t.Errorf("BUG-10: close guard should return non-zero exit code for blocked issue, but got exit 0")
	}
}

// TestBug10_ClaimExitCode verifies update --claim returns non-zero exit for already-claimed issues.
// Currently FAILS: claim error prints to stderr but returns exit 0.
func TestBug10_ClaimExitCode(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Claimable", "--type", "task", "--priority", "2")
	w.run("update", a, "--claim")

	// Second claim should return non-zero exit code
	_, err := w.tryRun("update", a, "--claim")
	if err == nil {
		t.Errorf("BUG-10: second claim should return non-zero exit code, but got exit 0")
	}
}

// TestProtocol_DeferExcludesFromReady verifies defer/undefer semantics.
func TestProtocol_DeferExcludesFromReady(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Deferred", "--type", "task", "--priority", "2")

	w.run("defer", a, "--until", "2099-12-31")

	// Should not be in ready
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if containsID(readyIDs, a) {
		t.Errorf("deferred issue should not be in ready list")
	}

	// Undefer
	w.run("undefer", a)

	// Should be in ready
	readyIDs = parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, a) {
		t.Errorf("undeferred issue should be in ready list")
	}
}

// TestProtocol_ClaimSemantics verifies atomic claim behavior.
// NOTE: Second claim error prints to stderr but returns exit 0 (BUG-10).
func TestProtocol_ClaimSemantics(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Claimable", "--type", "task", "--priority", "2")

	w.run("update", a, "--claim")

	data := parseJSON(t, w.run("show", a, "--json"))
	if data[0]["status"] != "in_progress" {
		t.Errorf("claimed issue should be in_progress, got: %v", data[0]["status"])
	}

	// Second claim should fail (BUG-10: returns exit 0, so check stderr text)
	out := w.run("update", a, "--claim")
	if !strings.Contains(out, "already claimed") {
		t.Errorf("second claim should report 'already claimed', got: %s", out)
	}
}

// TestProtocol_NotesAppendVsOverwrite verifies notes semantics.
func TestProtocol_NotesAppendVsOverwrite(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Notes test", "--type", "task", "--priority", "2")

	w.run("update", a, "--notes", "Original")
	data := parseJSON(t, w.run("show", a, "--json"))
	if data[0]["notes"] != "Original" {
		t.Errorf("notes should be 'Original', got: %v", data[0]["notes"])
	}

	w.run("update", a, "--notes", "Replaced")
	data = parseJSON(t, w.run("show", a, "--json"))
	if data[0]["notes"] != "Replaced" {
		t.Errorf("notes should be 'Replaced', got: %v", data[0]["notes"])
	}

	w.run("update", a, "--append-notes", "Extra")
	data = parseJSON(t, w.run("show", a, "--json"))
	expected := "Replaced\nExtra"
	if data[0]["notes"] != expected {
		t.Errorf("notes should be %q, got: %v", expected, data[0]["notes"])
	}
}

// TestProtocol_SupersedeCreatesDepAndCloses verifies supersede behavior.
func TestProtocol_SupersedeCreatesDepAndCloses(t *testing.T) {
	w := newCandidateWorkspace(t)

	old := w.create("--title", "Old approach", "--type", "feature", "--priority", "2")
	new := w.create("--title", "New approach", "--type", "feature", "--priority", "2")

	w.run("supersede", old, "--with", new)

	data := parseJSON(t, w.run("show", old, "--json"))
	if data[0]["status"] != "closed" {
		t.Errorf("superseded issue should be closed, got: %v", data[0]["status"])
	}

	// Should have supersedes dependency
	deps, ok := data[0]["dependencies"].([]any)
	if !ok || len(deps) == 0 {
		t.Fatalf("superseded issue should have dependencies")
	}
	depMap := deps[0].(map[string]any)
	if depMap["dependency_type"] != "supersedes" {
		t.Errorf("dep type should be 'supersedes', got: %v", depMap["dependency_type"])
	}
}

// TestProtocol_DuplicateClosesWithDep verifies duplicate behavior.
func TestProtocol_DuplicateClosesWithDep(t *testing.T) {
	w := newCandidateWorkspace(t)

	orig := w.create("--title", "Original", "--type", "bug", "--priority", "1")
	dup := w.create("--title", "Duplicate", "--type", "bug", "--priority", "1")

	w.run("duplicate", dup, "--of", orig)

	data := parseJSON(t, w.run("show", dup, "--json"))
	if data[0]["status"] != "closed" {
		t.Errorf("duplicate issue should be closed, got: %v", data[0]["status"])
	}
}

// TestProtocol_CountByGrouping verifies count --by-* accuracy.
func TestProtocol_CountByGrouping(t *testing.T) {
	w := newCandidateWorkspace(t)

	w.create("--title", "Bug1", "--type", "bug", "--priority", "1")
	w.create("--title", "Bug2", "--type", "bug", "--priority", "2")
	w.create("--title", "Task1", "--type", "task", "--priority", "2")
	id := w.create("--title", "Feature1", "--type", "feature", "--priority", "3")
	w.run("close", id)

	// count --by-type
	out := w.run("count", "--by-type", "--json")
	var typeResult struct {
		Total  int `json:"total"`
		Groups []struct {
			Group string `json:"group"`
			Count int    `json:"count"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(out), &typeResult); err != nil {
		t.Fatalf("parsing count --by-type: %v", err)
	}

	if typeResult.Total != 4 {
		t.Errorf("total should be 4, got %d", typeResult.Total)
	}

	// Verify bug count
	for _, g := range typeResult.Groups {
		if g.Group == "bug" && g.Count != 2 {
			t.Errorf("bug count should be 2, got %d", g.Count)
		}
	}
}

// TestProtocol_SpecialCharsInFields verifies special characters are preserved.
func TestProtocol_SpecialCharsInFields(t *testing.T) {
	w := newCandidateWorkspace(t)

	title := `Test "quotes" & <brackets> 'single'`
	a := w.create("--title", title, "--type", "task", "--priority", "2")

	data := parseJSON(t, w.run("show", a, "--json"))
	if data[0]["title"] != title {
		t.Errorf("title not preserved: got %v, want %v", data[0]["title"], title)
	}
}

// TestProtocol_SQLInjectionSafe verifies parameterized queries.
func TestProtocol_SQLInjectionSafe(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create an issue so we know the DB isn't empty
	w.create("--title", "Normal issue", "--type", "task", "--priority", "2")

	// Try SQL injection via search
	w.run("search", "'; DROP TABLE issues; --")

	// Verify database is intact
	out := w.run("count", "--json")
	var countResult struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &countResult); err != nil {
		t.Fatalf("parsing count after SQL injection attempt: %v", err)
	}
	if countResult.Count == 0 {
		t.Error("database appears empty after SQL injection attempt!")
	}
}

// =============================================================================
// NEWLY DISCOVERED BUGS (session 2)
// =============================================================================

// TestBug11_UpdateAcceptsInvalidStatus verifies status validation on update.
// Currently FAILS: update --status accepts arbitrary strings like "invalid".
func TestBug11_UpdateAcceptsInvalidStatus(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Status test", "--type", "task", "--priority", "2")

	// Setting an invalid status should fail
	_, err := w.tryRun("update", a, "--status", "bogus")
	if err == nil {
		// Check what status was actually set
		data := parseJSON(t, w.run("show", a, "--json"))
		if data[0]["status"] == "bogus" {
			t.Errorf("BUG-11: update --status accepted invalid value 'bogus'; should reject with error")
		}
	}
}

// TestBug12_UpdateAcceptsEmptyTitle verifies title validation on update.
// Currently FAILS: update --title "" succeeds and stores empty title.
func TestBug12_UpdateAcceptsEmptyTitle(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Has a title", "--type", "task", "--priority", "2")

	// Setting empty title should fail (create rejects it, update should too)
	_, err := w.tryRun("update", a, "--title", "")
	if err == nil {
		data := parseJSON(t, w.run("show", a, "--json"))
		if data[0]["title"] == "" {
			t.Errorf("BUG-12: update --title accepted empty string; should reject like create does")
		}
	}
}

// TestBug13_ReopenDeferredLimbo verifies reopen of closed+deferred issue.
// Currently FAILS: reopened issue has status "open" but defer_until still set.
// The issue is excluded from ready (good) but also excluded from list --status deferred.
func TestBug13_ReopenDeferredLimbo(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Defer then close", "--type", "task", "--priority", "2")
	w.run("defer", a, "--until", "2099-12-31")
	w.run("close", a)
	w.run("reopen", a)

	data := parseJSON(t, w.run("show", a, "--json"))
	status := data[0]["status"]

	// After reopening a previously-deferred issue, either:
	// 1. Status should be "deferred" (preserving the defer), OR
	// 2. defer_until should be cleared (truly reopening)
	// Currently: status="open" but defer_until still set = limbo
	if status == "open" {
		deferUntil, hasDeferUntil := data[0]["defer_until"]
		if hasDeferUntil && deferUntil != nil && deferUntil != "" {
			// Check it's not in ready
			readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
			if !containsID(readyIDs, a) {
				// Not in ready (correct), but also check deferred list
				deferredOut := w.run("list", "--status", "deferred", "--json", "-n", "0")
				deferredIDs := parseIDs(t, deferredOut)
				if !containsID(deferredIDs, a) {
					t.Errorf("BUG-13: reopened+deferred issue in limbo: status=%v, defer_until=%v, not in ready or deferred list", status, deferUntil)
				}
			}
		}
	}
}

// TestBug14_EmptyLabelAccepted verifies empty label validation.
// Currently FAILS: label add accepts empty string as a label.
func TestBug14_EmptyLabelAccepted(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Label test", "--type", "task", "--priority", "2")

	// Adding an empty label should fail
	_, err := w.tryRun("label", "add", a, "")
	if err == nil {
		data := parseJSON(t, w.run("show", a, "--json"))
		labels, ok := data[0]["labels"].([]any)
		if ok {
			for _, l := range labels {
				if l == "" {
					t.Errorf("BUG-14: empty string label was accepted and stored")
				}
			}
		}
	}
}

// =============================================================================
// ADDITIONAL PROTOCOL INVARIANT TESTS
// =============================================================================

// TestProtocol_CreateWithDepsBlocksIssue verifies --deps creates blocking dependency.
func TestProtocol_CreateWithDepsBlocksIssue(t *testing.T) {
	w := newCandidateWorkspace(t)

	blocker := w.create("--title", "Blocker", "--type", "task", "--priority", "1")
	blocked := w.create("--title", "Blocked", "--type", "task", "--priority", "2", "--deps", blocker)

	blockedIDs := parseIDs(t, w.run("blocked", "--json"))
	if !containsID(blockedIDs, blocked) {
		t.Errorf("issue created with --deps should be blocked, got: %v", blockedIDs)
	}

	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, blocker) {
		t.Errorf("blocker should be in ready list")
	}
	if containsID(readyIDs, blocked) {
		t.Errorf("blocked issue should NOT be in ready list")
	}
}

// TestProtocol_DepRemoveUnblocks verifies that removing a blocking dep unblocks.
func TestProtocol_DepRemoveUnblocks(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Source", "--type", "task", "--priority", "2")
	b := w.create("--title", "Blocker", "--type", "task", "--priority", "2")
	w.run("dep", "add", a, b, "--type", "blocks")

	// a should be blocked
	blockedIDs := parseIDs(t, w.run("blocked", "--json"))
	if !containsID(blockedIDs, a) {
		t.Fatalf("a should be blocked")
	}

	// Remove the dep
	w.run("dep", "rm", a, b)

	// a should be ready
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, a) {
		t.Errorf("after dep rm, a should be in ready list")
	}
}

// TestProtocol_SelfDepPrevented verifies self-dependency is rejected.
func TestProtocol_SelfDepPrevented(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Self ref", "--type", "task", "--priority", "2")
	_, err := w.tryRun("dep", "add", a, a, "--type", "blocks")
	if err == nil {
		t.Errorf("self-dependency should be rejected")
	}
}

// TestProtocol_StatusTransitionRoundTrip verifies full status lifecycle.
func TestProtocol_StatusTransitionRoundTrip(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Status lifecycle", "--type", "task", "--priority", "2")

	// open → in_progress
	w.run("update", a, "--status", "in_progress")
	data := parseJSON(t, w.run("show", a, "--json"))
	if data[0]["status"] != "in_progress" {
		t.Errorf("expected in_progress, got: %v", data[0]["status"])
	}

	// in_progress → open
	w.run("update", a, "--status", "open")
	data = parseJSON(t, w.run("show", a, "--json"))
	if data[0]["status"] != "open" {
		t.Errorf("expected open, got: %v", data[0]["status"])
	}

	// open → closed
	w.run("close", a)
	data = parseJSON(t, w.run("show", a, "--json"))
	if data[0]["status"] != "closed" {
		t.Errorf("expected closed, got: %v", data[0]["status"])
	}

	// closed → open (reopen)
	w.run("reopen", a)
	data = parseJSON(t, w.run("show", a, "--json"))
	if data[0]["status"] != "open" {
		t.Errorf("expected open after reopen, got: %v", data[0]["status"])
	}
}

// TestProtocol_TypeChangeRoundTrip verifies issue type can be changed.
func TestProtocol_TypeChangeRoundTrip(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Type change", "--type", "task", "--priority", "2")

	w.run("update", a, "--type", "bug")
	data := parseJSON(t, w.run("show", a, "--json"))
	if data[0]["issue_type"] != "bug" {
		t.Errorf("expected bug, got: %v", data[0]["issue_type"])
	}

	w.run("update", a, "--type", "epic")
	data = parseJSON(t, w.run("show", a, "--json"))
	if data[0]["issue_type"] != "epic" {
		t.Errorf("expected epic, got: %v", data[0]["issue_type"])
	}
}

// TestProtocol_DueDateRoundTrip verifies due date can be set and cleared.
func TestProtocol_DueDateRoundTrip(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Due date test", "--type", "task", "--priority", "2")

	w.run("update", a, "--due", "2099-06-15")
	data := parseJSON(t, w.run("show", a, "--json"))
	dueAt, ok := data[0]["due_at"].(string)
	if !ok || !strings.Contains(dueAt, "2099-06-15") {
		t.Errorf("due_at should contain 2099-06-15, got: %v", data[0]["due_at"])
	}
}

// TestProtocol_LabelAddRemoveRoundTrip verifies label add/remove.
func TestProtocol_LabelAddRemoveRoundTrip(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Label test", "--type", "task", "--priority", "2")

	w.run("label", "add", a, "bug-fix")
	w.run("label", "add", a, "urgent")

	data := parseJSON(t, w.run("show", a, "--json"))
	labels, _ := data[0]["labels"].([]any)
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}

	w.run("label", "remove", a, "bug-fix")
	data = parseJSON(t, w.run("show", a, "--json"))
	labels, _ = data[0]["labels"].([]any)
	if len(labels) != 1 {
		t.Errorf("expected 1 label after remove, got %d", len(labels))
	}

	// Verify correct label remains
	if len(labels) > 0 && labels[0] != "urgent" {
		t.Errorf("remaining label should be 'urgent', got: %v", labels[0])
	}
}

// =============================================================================
// CANDIDATE-ONLY DISCOVERY: SILENT LOSS SEAMS (session 3)
// =============================================================================

// TestDiscovery_ExternalBlockerIgnoredByReady verifies that issues with external
// blocking dependencies are correctly excluded from bd ready.
//
// FINDING: computeBlockedIDs() in queries.go only marks issues blocked if BOTH
// issue AND blocker are in the local activeIDs map. External blockers (e.g.
// "external:project:capability") are never in activeIDs, so issues blocked by
// external deps silently appear in bd ready as if unblocked.
//
// Classification: DECISION — maintainer must decide whether external blockers
// should gate readiness or remain advisory-only.
func TestDiscovery_ExternalBlockerIgnoredByReady(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Externally blocked", "--type", "task", "--priority", "2")
	b := w.create("--title", "Locally blocked", "--type", "task", "--priority", "2")
	blocker := w.create("--title", "Local blocker", "--type", "task", "--priority", "1")

	// Add an external blocker to a
	w.run("dep", "add", a, "external:otherproject:some-capability", "--type", "blocks")

	// Add a local blocker to b (control group)
	w.run("dep", "add", b, blocker, "--type", "blocks")

	// b should NOT be in ready (locally blocked — control)
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if containsID(readyIDs, b) {
		t.Fatalf("control: locally blocked issue %s should NOT be in ready", b)
	}

	// a should NOT be in ready (externally blocked)
	if containsID(readyIDs, a) {
		t.Errorf("DISCOVERY: externally blocked issue %s appears in bd ready — external blockers are silently ignored by computeBlockedIDs()", a)
	}

	// Also verify close guard: closing an externally-blocked issue should warn
	out := w.run("close", a)
	showOut := parseJSON(t, w.run("show", a, "--json"))
	if showOut[0]["status"] == "closed" {
		t.Errorf("DISCOVERY: close guard did not prevent closing externally-blocked issue %s; close output: %s", a, out)
	}
}

// TestDiscovery_ConditionalBlocksNotEvaluated verifies that conditional-blocks
// dependencies are evaluated in readiness computation.
//
// FINDING: types.AffectsReadyWork() returns true for conditional-blocks, but
// computeBlockedIDs() SQL only queries WHERE type IN ('blocks', 'waits-for').
// conditional-blocks is never evaluated, so issues that should be conditionally
// blocked appear as ready.
//
// Classification: DECISION — conditional-blocks semantics are complex (B runs
// only if A fails). The maintainer must decide if they should gate readiness
// while A is still open.
func TestDiscovery_ConditionalBlocksNotEvaluated(t *testing.T) {
	w := newCandidateWorkspace(t)

	precondition := w.create("--title", "Precondition (might fail)", "--type", "task", "--priority", "1")
	fallback := w.create("--title", "Fallback (runs if precondition fails)", "--type", "task", "--priority", "2")

	// fallback is conditionally-blocked by precondition
	w.run("dep", "add", fallback, precondition, "--type", "conditional-blocks")

	// While precondition is still open, fallback should NOT be ready
	// (it can't run yet — we don't know if precondition will fail)
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if containsID(readyIDs, fallback) {
		t.Errorf("DISCOVERY: conditionally-blocked issue %s appears in bd ready while precondition %s is still open — conditional-blocks not evaluated by computeBlockedIDs()", fallback, precondition)
	}

	// precondition should be ready (it has no blockers)
	if !containsID(readyIDs, precondition) {
		t.Errorf("precondition %s should be in ready list", precondition)
	}

	// Also verify: bd blocked should include fallback
	blockedIDs := parseIDs(t, w.run("blocked", "--json"))
	if !containsID(blockedIDs, fallback) {
		t.Errorf("DISCOVERY: conditionally-blocked issue %s not in bd blocked output", fallback)
	}
}

// NOTE: Wisp dep type overwrite (same root cause as BUG-7 but in
// wisp_dependencies table) confirmed via code review but not tested here
// because ephemeral issue dep routing requires different test setup.
// See PR #1999 (BUG-7 fix) for the shared root cause.

// TestDiscovery_CountVsListDefaultFilter verifies that bd count and bd list
// agree on default filtering behavior.
//
// FINDING: bd count (no flags) counts ALL issues including closed.
// bd list (no flags) excludes closed issues by default.
// This means bd count and bd list -n 0 --json | jq length give different numbers.
//
// Root cause: count.go doesn't apply ExcludeStatus for closed issues by default,
// while list.go:410 does: filter.ExcludeStatus = []types.Status{types.StatusClosed}
//
// Classification: BUG — commands that report the same metric should agree on defaults.
func TestDiscovery_CountVsListDefaultFilter(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create 3 open and 2 closed issues
	w.create("--title", "Open1", "--type", "task", "--priority", "2")
	w.create("--title", "Open2", "--type", "task", "--priority", "2")
	w.create("--title", "Open3", "--type", "task", "--priority", "2")
	c1 := w.create("--title", "WillClose1", "--type", "task", "--priority", "2")
	c2 := w.create("--title", "WillClose2", "--type", "task", "--priority", "2")
	w.run("close", c1)
	w.run("close", c2)

	// bd list (no flags) should exclude closed → 3 issues
	listOut := parseJSON(t, w.run("list", "--json", "-n", "0"))
	listCount := len(listOut)

	// bd count (no flags) should match list count
	var countResult struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(w.run("count", "--json")), &countResult); err != nil {
		t.Fatalf("parsing count: %v", err)
	}

	if countResult.Count != listCount {
		t.Errorf("DISCOVERY: bd count (%d) disagrees with bd list (%d) on default filtering — count includes closed issues, list excludes them",
			countResult.Count, listCount)
	}
}

// TestDiscovery_WaitsForBlocksReadiness verifies that waits-for deps
// actually block readiness as declared by AffectsReadyWork().
//
// FINDING: AffectsReadyWork() returns true for waits-for, and computeBlockedIDs()
// includes waits-for in its SQL query. However, waits-for gating has additional
// metadata semantics (gate_type: all-children vs any-children) that may cause
// the basic case (no gate metadata) to behave differently.
//
// Classification: INVESTIGATE — need to understand if bare waits-for (no gate
// metadata) is expected to block or only gates with proper metadata.
func TestDiscovery_WaitsForBlocksReadiness(t *testing.T) {
	w := newCandidateWorkspace(t)

	spawner := w.create("--title", "Spawner", "--type", "task", "--priority", "1")
	waiter := w.create("--title", "Waiter", "--type", "task", "--priority", "2")

	// waiter waits-for spawner (bare dep, no gate metadata)
	w.run("dep", "add", waiter, spawner, "--type", "waits-for")

	// Per AffectsReadyWork(), waiter should be blocked while spawner is open
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if containsID(readyIDs, waiter) {
		t.Errorf("DISCOVERY: waits-for dep doesn't block readiness — waiter %s appears in bd ready while spawner %s is open (AffectsReadyWork() says it should block)", waiter, spawner)
	}

	blockedIDs := parseIDs(t, w.run("blocked", "--json"))
	if !containsID(blockedIDs, waiter) {
		t.Errorf("DISCOVERY: waiter %s with waits-for dep not in bd blocked", waiter)
	}
}

// TestDiscovery_ParentBlockedChildrenConsistency verifies that children of a
// blocked parent are consistently reported across bd ready and bd blocked.
//
// FINDING: computeBlockedIDs() correctly propagates blocking to children of
// blocked parents for bd ready (children excluded). But bd blocked does NOT
// list these transitively-blocked children. This creates an inconsistency:
// the child is invisible — not in ready, not in blocked, just in list.
//
// Classification: BUG — child is excluded from ready but not shown in blocked,
// making it hard for users to understand why an issue isn't showing in ready.
func TestDiscovery_ParentBlockedChildrenConsistency(t *testing.T) {
	w := newCandidateWorkspace(t)

	parent := w.create("--title", "Parent", "--type", "epic", "--priority", "1")
	blocker := w.create("--title", "Blocker", "--type", "task", "--priority", "1")
	child := w.create("--title", "Child", "--type", "task", "--priority", "2", "--parent", parent)

	// Block the parent
	w.run("dep", "add", parent, blocker, "--type", "blocks")

	// Parent should be blocked
	blockedIDs := parseIDs(t, w.run("blocked", "--json"))
	if !containsID(blockedIDs, parent) {
		t.Fatalf("parent %s should be blocked", parent)
	}

	// Child correctly NOT in ready (parent is blocked → child transitively blocked)
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if containsID(readyIDs, child) {
		t.Errorf("child %s of blocked parent %s should NOT be in bd ready", child, parent)
	}

	// Child should also appear in blocked list for consistency
	if !containsID(blockedIDs, child) {
		t.Errorf("DISCOVERY: child %s of blocked parent %s is excluded from bd ready but NOT listed in bd blocked — user has no way to discover why it's missing from ready", child, parent)
	}
}

// TestDiscovery_UpdateStatusClosedBypassesCloseGuard verifies that
// bd update --status closed bypasses the close guard that bd close enforces.
//
// FINDING: bd close checks for open blockers and rejects the close.
// bd update --status closed does NOT check for blockers — it sets status
// directly, bypassing the close guard, gate checks, and close hooks.
// It also leaves close_reason empty (losing audit trail).
//
// Classification: BUG — update should either reject --status closed for
// blocked issues or route through the same validation as bd close.
func TestDiscovery_UpdateStatusClosedBypassesCloseGuard(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Blocked issue", "--type", "task", "--priority", "2")
	b := w.create("--title", "Blocker", "--type", "task", "--priority", "1")
	w.run("dep", "add", a, b, "--type", "blocks")

	// bd close should reject (close guard)
	out := w.run("close", a)
	showOut := parseJSON(t, w.run("show", a, "--json"))
	if showOut[0]["status"] == "closed" {
		t.Fatalf("control failed: bd close should not close blocked issue")
	}
	_ = out

	// bd update --status closed should ALSO reject for blocked issues
	w.run("update", a, "--status", "closed")
	showOut2 := parseJSON(t, w.run("show", a, "--json"))
	if showOut2[0]["status"] == "closed" {
		t.Errorf("DISCOVERY: bd update --status closed bypassed close guard — blocked issue %s was closed without --force", a)
	}
}

// TestDiscovery_ReopenSupersededSemanticCorruption verifies that reopening a
// superseded issue creates a semantically incoherent state.
//
// FINDING: bd supersede A --with B creates a supersedes dep and closes A.
// bd reopen A sets status to open but does NOT remove the supersedes dep.
// Result: A is "open but superseded by B" — a contradictory state.
// The supersedes dep is non-blocking, so A appears in bd ready.
//
// Classification: DECISION — should reopen remove supersedes/duplicates deps,
// or should reopen be rejected for superseded/duplicated issues?
func TestDiscovery_ReopenSupersededSemanticCorruption(t *testing.T) {
	w := newCandidateWorkspace(t)

	old := w.create("--title", "Old approach", "--type", "feature", "--priority", "2")
	new := w.create("--title", "New approach", "--type", "feature", "--priority", "2")

	w.run("supersede", old, "--with", new)

	// Verify old is closed with supersedes dep
	data := parseJSON(t, w.run("show", old, "--json"))
	if data[0]["status"] != "closed" {
		t.Fatalf("superseded issue should be closed")
	}

	// Reopen the superseded issue
	w.run("reopen", old)

	// After reopen, the supersedes dep should be removed (or reopen should be rejected)
	data = parseJSON(t, w.run("show", old, "--json"))
	if data[0]["status"] != "open" {
		t.Fatalf("reopened issue should be open, got %v", data[0]["status"])
	}

	deps, _ := data[0]["dependencies"].([]any)
	hasSupersedes := false
	for _, d := range deps {
		dep, _ := d.(map[string]any)
		if dep["dependency_type"] == "supersedes" {
			hasSupersedes = true
		}
	}
	if hasSupersedes {
		t.Errorf("DISCOVERY: reopened issue %s still has supersedes dep — semantically incoherent state (open but superseded)", old)
	}
}

// TestDiscovery_DeferPastDateInvisible verifies that deferring with a past date
// doesn't create an invisible issue.
//
// FINDING: bd defer X --until 2020-01-01 sets status=deferred and defer_until
// to a past date. Nothing transitions status back to open when defer_until
// passes. The issue is not in bd ready (status is deferred, not open) and not
// in any other actionable view. It becomes invisible.
//
// Note: bd update --defer warns about past dates but bd defer does NOT.
//
// Classification: BUG — either reject past dates or auto-transition to open.
func TestDiscovery_DeferPastDateInvisible(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Past deferred", "--type", "task", "--priority", "2")

	// Defer with a past date
	w.run("defer", a, "--until", "2020-01-01")

	// Issue should still be actionable since defer date has passed
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, a) {
		// Check if it's in deferred list
		deferredOut := w.run("list", "--status", "deferred", "--json", "-n", "0")
		deferredIDs := parseIDs(t, deferredOut)
		if containsID(deferredIDs, a) {
			t.Errorf("DISCOVERY: issue %s deferred with past date (2020-01-01) is in deferred list but not in ready — defer_until passed but status never transitions back to open", a)
		} else {
			t.Errorf("DISCOVERY: issue %s deferred with past date (2020-01-01) is not in ready AND not in deferred list — invisible to user", a)
		}
	}
}

// TestDiscovery_SearchIncludesClosedByDefault verifies that bd search returns
// closed issues while bd list does not, creating a consistency gap.
//
// FINDING: bd search "term" returns ALL matching issues (including closed).
// bd list excludes closed by default. A user who searches for an issue by
// keyword and then tries to find it with list may not see it.
//
// Classification: INVESTIGATE — may be intentional (search is exhaustive)
// but the difference is undocumented and surprising.
func TestDiscovery_SearchIncludesClosedByDefault(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Searchable unique token xyzzy", "--type", "task", "--priority", "2")
	w.run("close", a)

	// bd search should find it
	searchOut := w.run("search", "xyzzy", "--json")
	searchIDs := parseIDs(t, searchOut)

	// bd list should NOT find it (excludes closed by default)
	listOut := w.run("list", "--json", "-n", "0")
	listIDs := parseIDs(t, listOut)

	if containsID(searchIDs, a) && !containsID(listIDs, a) {
		// This is the expected finding — search includes closed, list doesn't
		t.Logf("CONFIRMED: bd search includes closed issue %s, bd list excludes it (intentional but undocumented)", a)
	}

	if !containsID(searchIDs, a) {
		t.Errorf("bd search should find closed issue %s by keyword", a)
	}
}

// TestProtocol_CommentAddAndPreserve verifies comments persist through operations.
func TestProtocol_CommentAddAndPreserve(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Comment test", "--type", "task", "--priority", "2")
	w.run("comments", "add", a, "First comment")
	w.run("comments", "add", a, "Second comment")

	data := parseJSON(t, w.run("show", a, "--json"))
	comments, _ := data[0]["comments"].([]any)
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}

	// Close and reopen — comments should be preserved
	w.run("close", a)
	w.run("reopen", a)

	data = parseJSON(t, w.run("show", a, "--json"))
	comments, _ = data[0]["comments"].([]any)
	if len(comments) != 2 {
		t.Errorf("comments should be preserved after close/reopen, got %d", len(comments))
	}
}
