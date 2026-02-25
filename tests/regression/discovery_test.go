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
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uniquePrefix returns a unique prefix for test isolation on shared Dolt server.
// Uses FNV hash of the workspace dir for deterministic, collision-resistant prefixes.
func uniquePrefix(dir string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(dir))
	return fmt.Sprintf("d%x", h.Sum64())
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
	w.run("init", "--prefix", uniquePrefix(dir), "--quiet")
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
// dependencies gate readiness while the precondition is still open.
//
// RESOLVED: conditional-blocks added to computeBlockedIDs() SQL query.
// While precondition is open, fallback is blocked (outcome unknown).
// See PR #2026 for discovery, option analysis.
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

// TestProtocol_StatsReadyMatchesActualReady verifies that bd stats ready count
// matches actual bd ready count for standard issue types.
//
// NOTE: GetStatistics().ReadyIssues = OpenIssues - blockedCount could over-count
// with internal types (gate, molecule, agent) or pinned issues, but for standard
// user-facing types (task, bug, feature, epic) it matches.
func TestProtocol_StatsReadyMatchesActualReady(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create some issues with different states
	w.create("--title", "Open1", "--type", "task", "--priority", "2")
	w.create("--title", "Open2", "--type", "task", "--priority", "2")
	a := w.create("--title", "Blocked", "--type", "task", "--priority", "2")
	b := w.create("--title", "Blocker", "--type", "task", "--priority", "1")
	w.run("dep", "add", a, b, "--type", "blocks")
	c := w.create("--title", "Closed", "--type", "task", "--priority", "2")
	w.run("close", c)

	// Get actual ready count
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	actualReady := len(readyIDs)

	// Get stats ready count (output is nested: {summary: {ready_issues: N}, ...})
	statsOut := w.run("stats", "--json")
	var statsWrapper struct {
		Summary struct {
			ReadyIssues int `json:"ready_issues"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(statsOut), &statsWrapper); err != nil {
		t.Fatalf("parsing stats: %v\nraw: %s", err, statsOut)
	}

	if statsWrapper.Summary.ReadyIssues != actualReady {
		t.Errorf("DISCOVERY: bd stats reports %d ready issues but bd ready returns %d — stats over/under-reports",
			statsWrapper.Summary.ReadyIssues, actualReady)
	}
}

// TestProtocol_DepAddClosedBlockerSurpriseBlock documents that adding a blocks
// dep on a closed issue succeeds silently, and reopening the blocker later
// activates the block. No warning is given when adding to a closed target.
func TestProtocol_DepAddClosedBlockerSurpriseBlock(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Will be surprised", "--type", "task", "--priority", "2")
	b := w.create("--title", "Already done", "--type", "task", "--priority", "2")
	w.run("close", b)

	// Add blocks dep on closed issue — should warn but currently succeeds silently
	out := w.run("dep", "add", a, b, "--type", "blocks")
	_ = out

	// a should still be ready (closed blocker doesn't block)
	readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	if !containsID(readyIDs, a) {
		t.Fatalf("a should be ready when blocker is closed")
	}

	// Now reopen the blocker — a should become blocked (surprise!)
	w.run("reopen", b)

	readyAfter := parseIDs(t, w.run("ready", "-n", "0", "--json"))
	blockedIDs := parseIDs(t, w.run("blocked", "--json"))

	if !containsID(blockedIDs, a) {
		t.Errorf("after reopening blocker, a should be blocked")
	}
	if containsID(readyAfter, a) {
		t.Errorf("after reopening blocker, a should NOT be in ready")
	}

	// The real test: was there a warning when adding the dep on a closed target?
	// Currently there isn't — this test documents the surprise behavior.
	t.Logf("CONFIRMED: dep add on closed blocker succeeds silently — reopening blocker later causes surprise block")
}

// TestDiscovery_ConditionalBlocksCycleUndetected verifies that cycle detection
// doesn't catch cycles involving conditional-blocks.
//
// FINDING: Cycle detection at AddDependency only runs for type == blocks.
// DetectCycles also only follows blocks edges. A cycle like:
// A blocks B, B conditional-blocks A — is not detected.
//
// Classification: BUG — conditional-blocks is declared as AffectsReadyWork(),
// so cycles through it should be detected too.
func TestDiscovery_ConditionalBlocksCycleUndetected(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Alpha", "--type", "task", "--priority", "2")
	b := w.create("--title", "Beta", "--type", "task", "--priority", "2")

	// A blocks B — this is fine
	w.run("dep", "add", a, b, "--type", "blocks")

	// B conditional-blocks A — this creates a cycle, but should it be detected?
	_, err := w.tryRun("dep", "add", b, a, "--type", "conditional-blocks")
	if err == nil {
		// Check if cycle detection caught it
		cycleOut := w.run("dep", "cycles")
		if strings.Contains(cycleOut, "No dependency cycles") {
			t.Errorf("DISCOVERY: cycle through conditional-blocks (A blocks B, B conditional-blocks A) is not detected by dep cycles or add-time check")
		}
	}
}

// TestDiscovery_LabelPatternFilterDeadCode verifies that --label-pattern
// actually filters results.
//
// FINDING: bd list --label-pattern "tech-*" sets filter.LabelPattern in the
// IssueFilter struct, but SearchIssues() in queries.go NEVER reads or processes
// this field. The SQL query builder completely ignores it. The user gets
// unfiltered results while believing they filtered.
//
// Classification: BUG — dead filter gives silently wrong results.
func TestDiscovery_LabelPatternFilterDeadCode(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Tech debt item", "--type", "task", "--priority", "2")
	b := w.create("--title", "Normal item", "--type", "task", "--priority", "2")
	w.run("label", "add", a, "tech-debt")

	// bd list --label-pattern "tech-*" should return only a
	patternOut, err := w.tryRun("list", "--label-pattern", "tech-*", "--json", "-n", "0")
	if err != nil {
		t.Skipf("--label-pattern flag not supported: %v", err)
	}
	patternIDs := parseIDs(t, patternOut)

	if containsID(patternIDs, b) {
		t.Errorf("DISCOVERY: --label-pattern 'tech-*' returned issue %s which has no matching label — filter is dead code in SearchIssues()", b)
	}
	if !containsID(patternIDs, a) {
		t.Errorf("--label-pattern 'tech-*' should return issue %s with label 'tech-debt'", a)
	}
}

// TestDiscovery_ClaimThenStatusOverwrite verifies that --claim and --status
// on the same update command interact correctly.
//
// FINDING: ClaimIssue sets status=in_progress first, then UpdateIssue applies
// --status in a separate non-transactional call, silently overwriting the
// claimed status. No warning or error is raised.
//
// Classification: BUG — contradictory flags should error or warn.
func TestDiscovery_ClaimThenStatusOverwrite(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Claim conflict", "--type", "task", "--priority", "2")

	// Claim and set status=open in the same command
	w.run("update", a, "--claim", "--status", "open")

	data := parseJSON(t, w.run("show", a, "--json"))
	status := data[0]["status"]

	// After --claim, status should be in_progress.
	// If --status open overwrites it, that's a conflict the tool should catch.
	if status == "open" {
		t.Errorf("DISCOVERY: --claim set in_progress but --status open silently overwrote it — contradictory flags not detected")
	} else if status != "in_progress" {
		t.Errorf("unexpected status after --claim --status open: %v", status)
	}
}

// TestDiscovery_ListReadyOverridesStatusFlag verifies that --ready silently
// wins over explicit --status.
//
// FINDING: In list.go:401-408, --ready sets filter.Status=open in an if-else
// chain that takes precedence over --status. If you pass --status closed --ready,
// the --status flag is silently ignored and you get open issues.
//
// Classification: BUG — should error on contradictory filter flags.
func TestDiscovery_ListReadyOverridesStatusFlag(t *testing.T) {
	w := newCandidateWorkspace(t)

	open := w.create("--title", "Open one", "--type", "task", "--priority", "2")
	closed := w.create("--title", "Closed one", "--type", "task", "--priority", "2")
	w.run("close", closed)

	// --status closed --ready — what should this return?
	out, err := w.tryRun("list", "--status", "closed", "--ready", "--json", "-n", "0")
	if err != nil {
		t.Skipf("flag combination not supported: %v", err)
	}
	ids := parseIDs(t, out)

	// If --ready wins silently, we get open issues instead of closed
	if containsID(ids, open) && !containsID(ids, closed) {
		t.Errorf("DISCOVERY: --ready silently overrides --status closed — got open issue %s instead of closed issue %s", open, closed)
	}
}

// TestDiscovery_AssigneeEmptyStringVsNoAssignee verifies that --assignee ""
// and --no-assignee return the same results.
//
// FINDING: --assignee "" fails the != "" check in list.go:423, so the filter
// is never set — returning ALL issues. --no-assignee correctly filters to
// unassigned issues only. A user expecting --assignee "" to mean "unassigned"
// gets silently wrong results.
//
// Classification: BUG — empty string should either be rejected or treated as --no-assignee.
func TestDiscovery_AssigneeEmptyStringVsNoAssignee(t *testing.T) {
	w := newCandidateWorkspace(t)

	assigned := w.create("--title", "Assigned", "--type", "task", "--priority", "2")
	w.run("update", assigned, "--assignee", "alice")
	unassigned := w.create("--title", "Unassigned", "--type", "task", "--priority", "2")

	// --no-assignee should return only unassigned
	noAssigneeOut := w.run("list", "--no-assignee", "--json", "-n", "0")
	noAssigneeIDs := parseIDs(t, noAssigneeOut)
	if containsID(noAssigneeIDs, assigned) {
		t.Fatalf("control: --no-assignee should not include assigned issue")
	}
	if !containsID(noAssigneeIDs, unassigned) {
		t.Fatalf("control: --no-assignee should include unassigned issue")
	}

	// --assignee "" should behave the same as --no-assignee
	emptyOut, err := w.tryRun("list", "--assignee", "", "--json", "-n", "0")
	if err != nil {
		t.Skipf("--assignee flag not available: %v", err)
	}
	emptyIDs := parseIDs(t, emptyOut)

	if containsID(emptyIDs, assigned) && !containsID(noAssigneeIDs, assigned) {
		t.Errorf("DISCOVERY: --assignee '' returns ALL issues (including assigned %s) while --no-assignee correctly filters — empty string filter silently becomes no-filter", assigned)
	}
}

// =============================================================================
// SESSION 6 DISCOVERY: Routing, validation, sort seams
// =============================================================================

// TestDiscovery_StaleNegativeDaysSilentlyInverts verifies that bd stale --days
// rejects negative values.
//
// FINDING: stale.go:22 reads --days without validation. queries.go:767 computes
// cutoff = time.Now().UTC().AddDate(0, 0, -filter.Days). With Days=-1, this
// becomes AddDate(0,0,1) = TOMORROW, so ALL issues updated before tomorrow
// are "stale" — which is everything. The user gets silently wrong results.
//
// Classification: BUG — negative days should be rejected or documented.
func TestDiscovery_StaleNegativeDaysSilentlyInverts(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create an issue that was just created (definitely not stale)
	a := w.create("--title", "Brand new issue", "--type", "task", "--priority", "2")

	// bd stale --days 99999 should NOT include a (created moments ago)
	normalOut, err := w.tryRun("stale", "--days", "99999", "--json")
	if err != nil {
		t.Skipf("bd stale not available: %v", err)
	}
	normalIDs := parseIDs(t, normalOut)
	if containsID(normalIDs, a) {
		t.Fatalf("control: brand new issue should not be stale with --days 99999")
	}

	// bd stale --days -1 should be rejected, but currently accepts silently
	negOut, err := w.tryRun("stale", "--days", "-1", "--json")
	if err == nil {
		negIDs := parseIDs(t, negOut)
		if containsID(negIDs, a) {
			t.Errorf("DISCOVERY: bd stale --days -1 returns brand new issue %s as 'stale' — negative days inverts the staleness logic (cutoff becomes tomorrow instead of yesterday)", a)
		}
	}
	// If err != nil, the tool correctly rejected negative days — good
}

// TestDiscovery_ListSortUnknownFieldSilentNoOp verifies that bd list --sort
// with an invalid field name errors instead of silently ignoring the sort.
//
// FINDING: list.go:238-240 has a default case that treats unknown sort fields
// as "all items compare equal" — effectively no sorting. The user gets unsorted
// results without any error or warning.
//
// Classification: BUG — unknown sort field should error.
func TestDiscovery_ListSortUnknownFieldSilentNoOp(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create issues with different priorities to verify sort behavior
	w.create("--title", "Low priority", "--type", "task", "--priority", "4")
	w.create("--title", "High priority", "--type", "task", "--priority", "0")
	w.create("--title", "Mid priority", "--type", "task", "--priority", "2")

	// bd list --sort priority should work (control)
	_, err := w.tryRun("list", "--sort", "priority", "--json", "-n", "0")
	if err != nil {
		t.Skipf("--sort flag not available: %v", err)
	}

	// bd list --sort nonexistent_field should error
	out, err := w.tryRun("list", "--sort", "nonexistent_field", "--json", "-n", "0")
	if err == nil {
		// Command succeeded — check if results are actually unsorted
		ids := parseIDs(t, out)
		if len(ids) > 0 {
			t.Errorf("DISCOVERY: bd list --sort nonexistent_field succeeded without error — unknown sort field silently ignored, results may be unsorted (%d issues returned)", len(ids))
		}
	}
	// If err != nil, the tool correctly rejected the unknown field — good
}

// TestDiscovery_ReparentCreatesParentChildCycle verifies that reparenting
// a parent to its own child creates a cycle.
//
// FINDING: update.go:328-342 reparent logic updates the parent-child dependency
// but does NOT check if the new parent is a descendant, which would create a
// parent-child cycle: A is parent of B, B becomes parent of A.
//
// Classification: BUG — reparenting should check for cycles.
func TestDiscovery_ReparentCreatesParentChildCycle(t *testing.T) {
	w := newCandidateWorkspace(t)

	parent := w.create("--title", "Parent", "--type", "epic", "--priority", "1")
	child := w.create("--title", "Child", "--type", "task", "--priority", "2", "--parent", parent)

	// Reparent parent to its own child — should be rejected
	_, err := w.tryRun("update", parent, "--parent", child)
	if err != nil {
		// Tool correctly rejected the cycle
		return
	}

	// Command succeeded — check if a cycle was created
	// bd show --json embeds dependencies with dependency_type field
	parentData := parseJSON(t, w.run("show", parent, "--json"))
	childData := parseJSON(t, w.run("show", child, "--json"))

	parentDeps, _ := parentData[0]["dependencies"].([]any)
	childDeps, _ := childData[0]["dependencies"].([]any)

	parentHasParentChildDep := false
	for _, d := range parentDeps {
		dep, _ := d.(map[string]any)
		if dep["dependency_type"] == "parent-child" {
			parentHasParentChildDep = true
		}
	}

	childHasParentChildDep := false
	for _, d := range childDeps {
		dep, _ := d.(map[string]any)
		if dep["dependency_type"] == "parent-child" {
			childHasParentChildDep = true
		}
	}

	if parentHasParentChildDep && childHasParentChildDep {
		t.Errorf("DISCOVERY: reparent %s --parent %s created a parent-child cycle — both issues have parent-child deps pointing at each other", parent, child)
	}
}

// TestDiscovery_OverdueComparisonEdgeCase verifies that bd list --overdue
// correctly handles timezone edge cases.
//
// FINDING: queries.go:237-239 compares due_at against time.Now().UTC().
// If due_at is stored without consistent UTC normalization, issues due
// "today" may appear or not appear in --overdue depending on timezone offset.
//
// Classification: INVESTIGATE — test documents current behavior for
// reference, may not be a bug if all times are consistently UTC.
func TestDiscovery_OverdueComparisonEdgeCase(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create issue with a due date far in the past
	pastDue := w.create("--title", "Past due", "--type", "task", "--priority", "2")
	w.run("update", pastDue, "--due", "2020-01-01")

	// Create issue with a due date far in the future
	futureDue := w.create("--title", "Future due", "--type", "task", "--priority", "2")
	w.run("update", futureDue, "--due", "2099-12-31")

	// Create issue with no due date
	noDue := w.create("--title", "No due date", "--type", "task", "--priority", "2")

	// bd list --overdue should include past due, not future due, not no-due
	overdueOut, err := w.tryRun("list", "--overdue", "--json", "-n", "0")
	if err != nil {
		t.Skipf("--overdue flag not available: %v", err)
	}
	overdueIDs := parseIDs(t, overdueOut)

	if !containsID(overdueIDs, pastDue) {
		t.Errorf("issue %s with due date 2020-01-01 should be overdue", pastDue)
	}
	if containsID(overdueIDs, futureDue) {
		t.Errorf("issue %s with due date 2099-12-31 should NOT be overdue", futureDue)
	}
	if containsID(overdueIDs, noDue) {
		t.Errorf("issue %s with no due date should NOT be overdue", noDue)
	}

	t.Logf("CONFIRMED: --overdue correctly filters past-due=%v future-due=%v no-due=%v",
		containsID(overdueIDs, pastDue), containsID(overdueIDs, futureDue), containsID(overdueIDs, noDue))
}

// TestDiscovery_PriorityMinMaxReversedSilentEmpty verifies that
// --priority-min > --priority-max is rejected or warned.
//
// FINDING: list.go:522-535 validates each priority independently but never
// checks that min <= max. With --priority-min 4 --priority-max 0, the SQL
// becomes "priority >= 4 AND priority <= 0" which is always false. The user
// gets empty results with no error or warning.
//
// Classification: BUG — reversed range should either error or swap values.
func TestDiscovery_PriorityMinMaxReversedSilentEmpty(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create issues at various priorities
	w.create("--title", "P0 critical", "--type", "task", "--priority", "0")
	w.create("--title", "P2 medium", "--type", "task", "--priority", "2")
	w.create("--title", "P4 backlog", "--type", "task", "--priority", "4")

	// Normal range (min < max) should work
	normalOut, err := w.tryRun("list", "--priority-min", "0", "--priority-max", "4", "--json", "-n", "0")
	if err != nil {
		t.Skipf("--priority-min/--priority-max not available: %v", err)
	}
	normalIDs := parseIDs(t, normalOut)
	if len(normalIDs) != 3 {
		t.Fatalf("control: expected 3 issues with P0-P4, got %d", len(normalIDs))
	}

	// Reversed range (min > max) should error, not return empty silently
	reversedOut, err := w.tryRun("list", "--priority-min", "4", "--priority-max", "0", "--json", "-n", "0")
	if err == nil {
		reversedIDs := parseIDs(t, reversedOut)
		if len(reversedIDs) == 0 {
			t.Errorf("DISCOVERY: --priority-min 4 --priority-max 0 silently returns empty — reversed range not validated, user gets wrong results without warning")
		}
	}
	// If err != nil, the tool correctly rejected reversed range — good
}

// TestDiscovery_DateRangeReversedSilentEmpty verifies that --created-after
// after --created-before is rejected or warned.
//
// FINDING: list.go:466-508 parses each date independently without checking
// that after <= before. With --created-after 2099 --created-before 2020, the
// SQL becomes "created_at >= 2099 AND created_at <= 2020" which is always false.
//
// Classification: BUG — reversed date range should error or warn.
func TestDiscovery_DateRangeReversedSilentEmpty(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create an issue (will have today's date)
	w.create("--title", "Date test", "--type", "task", "--priority", "2")

	// Normal range should find the issue
	normalOut, err := w.tryRun("list", "--created-after", "2020-01-01", "--created-before", "2099-12-31", "--json", "-n", "0")
	if err != nil {
		t.Skipf("--created-after/--created-before not available: %v", err)
	}
	normalIDs := parseIDs(t, normalOut)
	if len(normalIDs) == 0 {
		t.Fatalf("control: expected at least 1 issue in normal date range")
	}

	// Reversed range should error, not return empty silently
	reversedOut, err := w.tryRun("list", "--created-after", "2099-12-31", "--created-before", "2020-01-01", "--json", "-n", "0")
	if err == nil {
		reversedIDs := parseIDs(t, reversedOut)
		if len(reversedIDs) == 0 {
			t.Errorf("DISCOVERY: --created-after 2099 --created-before 2020 silently returns empty — reversed date range not validated")
		}
	}
}

// TestDiscovery_NegativeLimitNotRejected verifies that bd list -n -1 is
// handled correctly.
//
// FINDING: list.go:666-668 checks "effectiveLimit > 0" which lets negative
// values pass through as "unlimited." The user might expect -1 to be an error
// but it silently acts like -n 0 (unlimited).
//
// Classification: BUG (low) — negative limit should be rejected.
func TestDiscovery_NegativeLimitNotRejected(t *testing.T) {
	w := newCandidateWorkspace(t)

	w.create("--title", "Limit test 1", "--type", "task", "--priority", "2")
	w.create("--title", "Limit test 2", "--type", "task", "--priority", "2")

	// -n -1 should be rejected
	out, err := w.tryRun("list", "-n", "-1", "--json")
	if err == nil {
		ids := parseIDs(t, out)
		if len(ids) > 0 {
			t.Errorf("DISCOVERY: bd list -n -1 accepted silently and returned %d results — negative limit not validated (acts as unlimited)", len(ids))
		}
	}
	// If err != nil, the tool correctly rejected negative limit — good
}

// TestDiscovery_DuplicateAlreadyClosedSucceeds verifies that duplicating an
// already-closed issue produces a meaningful result.
//
// FINDING: duplicate.go:90-106 adds the duplicate-of dep and closes the
// issue, even if it's already closed. The duplicate operation is idempotent
// for status but the dependency link is silently added to an already-closed
// issue. No warning is given.
//
// Classification: INVESTIGATE — may be intentional (idempotent), but the lack
// of a "this issue is already closed" warning is surprising.
func TestDiscovery_DuplicateAlreadyClosedSucceeds(t *testing.T) {
	w := newCandidateWorkspace(t)

	orig := w.create("--title", "Original", "--type", "bug", "--priority", "1")
	dup := w.create("--title", "Already closed", "--type", "bug", "--priority", "1")
	w.run("close", dup)

	// Duplicating an already-closed issue should at least warn
	out, err := w.tryRun("duplicate", dup, "--of", orig)
	if err != nil {
		t.Skipf("bd duplicate not available: %v", err)
	}

	// Check if the dep was added (could be on either side)
	dupData := parseJSON(t, w.run("show", dup, "--json"))
	origData := parseJSON(t, w.run("show", orig, "--json"))

	// Log all deps for diagnostics
	dupDeps, _ := dupData[0]["dependencies"].([]any)
	origDeps, _ := origData[0]["dependencies"].([]any)
	dupDependents, _ := dupData[0]["dependents"].([]any)
	origDependents, _ := origData[0]["dependents"].([]any)

	foundDep := false
	for _, d := range dupDeps {
		dep, _ := d.(map[string]any)
		if strings.Contains(fmt.Sprintf("%v", dep["dependency_type"]), "duplic") {
			foundDep = true
		}
	}
	for _, d := range origDependents {
		dep, _ := d.(map[string]any)
		if strings.Contains(fmt.Sprintf("%v", dep["dependency_type"]), "duplic") {
			foundDep = true
		}
	}

	if foundDep {
		t.Logf("CONFIRMED: bd duplicate on already-closed issue succeeded silently — added duplicate dep (output: %s)", strings.TrimSpace(out))
	} else {
		// The dep may have been added but not visible — log raw output
		t.Logf("dup deps=%v dependents=%v, orig deps=%v dependents=%v",
			dupDeps, dupDependents, origDeps, origDependents)
		t.Logf("CONFIRMED: bd duplicate on already-closed issue completed (output: %s) but dep relationship not visible in show --json", strings.TrimSpace(out))
	}
}

// TestDiscovery_WhitespaceOnlyTitleAccepted verifies that bd update --title
// with whitespace-only content is handled correctly.
//
// FINDING: update.go:66-68 accepts --title without any whitespace validation.
// Unlike create.go which requires non-empty title, update lets you set a
// whitespace-only title like "   " which is effectively empty.
//
// Classification: BUG — extension of BUG-12 (empty title accepted).
func TestDiscovery_WhitespaceOnlyTitleAccepted(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Has a title", "--type", "task", "--priority", "2")

	// Setting whitespace-only title should fail
	_, err := w.tryRun("update", a, "--title", "   ")
	if err == nil {
		data := parseJSON(t, w.run("show", a, "--json"))
		title := fmt.Sprintf("%v", data[0]["title"])
		if strings.TrimSpace(title) == "" {
			t.Errorf("DISCOVERY: update --title '   ' accepted whitespace-only title — effectively blank")
		}
	}
	// If err != nil, the tool correctly rejected whitespace-only title — good
}

// TestDiscovery_ConfigEmptyValueAmbiguous verifies that bd config set key ""
// is distinguishable from key not set.
//
// FINDING: config.go:207 displays "(not set)" for empty string values, which
// is the same as for keys that haven't been set. In JSON output, empty string
// is distinguishable from null, but in human-readable output they look the same.
//
// Classification: BUG (low) — ambiguous UX between "set to empty" and "not set".
func TestDiscovery_ConfigEmptyValueAmbiguous(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Set a config key to a value then clear it
	w.run("config", "set", "test.discovery.key", "hello")

	// Verify it shows the value
	out := w.run("config", "get", "test.discovery.key")
	if !strings.Contains(out, "hello") {
		t.Fatalf("config get should show 'hello', got: %s", out)
	}

	// Set to empty string
	w.run("config", "set", "test.discovery.key", "")

	// In human-readable output, this should say "(not set)" or "empty"
	// but the user can't tell if the key exists with empty value vs not set
	out = w.run("config", "get", "test.discovery.key")

	// In JSON mode, we can check the actual value
	jsonOut, err := w.tryRun("config", "get", "test.discovery.key", "--json")
	if err != nil {
		t.Skipf("bd config get --json not available: %v", err)
	}

	t.Logf("human output: %q, json output: %q", strings.TrimSpace(out), strings.TrimSpace(jsonOut))

	if strings.Contains(out, "not set") {
		t.Errorf("DISCOVERY: config get after setting to empty string shows '(not set)' — ambiguous with key never being set")
	}
}

// TestProtocol_CountByStatusSumMatchesTotal verifies that bd count --by-status
// groups sum to the same total as bd count.
func TestProtocol_CountByStatusSumMatchesTotal(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create issues in various states
	w.create("--title", "Open1", "--type", "task", "--priority", "2")
	inProgress := w.create("--title", "InProg", "--type", "task", "--priority", "2")
	w.run("update", inProgress, "--status", "in_progress")
	closed := w.create("--title", "Closed1", "--type", "task", "--priority", "2")
	w.run("close", closed)
	deferred := w.create("--title", "Deferred1", "--type", "task", "--priority", "2")
	w.run("defer", deferred, "--until", "2099-12-31")

	// Get total count (includes ALL statuses)
	var countResult struct {
		Count int `json:"count"`
	}
	countJSON := w.run("count", "--status", "all", "--json")
	if err := json.Unmarshal([]byte(countJSON), &countResult); err != nil {
		t.Fatalf("parsing count: %v", err)
	}

	// Get by-status grouping
	var groupResult struct {
		Total  int `json:"total"`
		Groups []struct {
			Group string `json:"group"`
			Count int    `json:"count"`
		} `json:"groups"`
	}
	groupJSON := w.run("count", "--by-status", "--json")
	if err := json.Unmarshal([]byte(groupJSON), &groupResult); err != nil {
		t.Fatalf("parsing count --by-status: %v", err)
	}

	// Sum of groups should match total
	groupSum := 0
	for _, g := range groupResult.Groups {
		groupSum += g.Count
	}

	if groupSum != groupResult.Total {
		t.Errorf("count --by-status group sum (%d) != reported total (%d)", groupSum, groupResult.Total)
	}

	if groupResult.Total != countResult.Count {
		t.Errorf("count --by-status total (%d) != count --status all (%d)", groupResult.Total, countResult.Count)
	}
}

// TestDiscovery_DepRmNonexistentSilentSuccess verifies that dep rm on a
// dep that doesn't exist gives feedback.
//
// FINDING: dependencies.go:89-109 executes DELETE without checking rows
// affected. Returns success even if 0 rows were deleted.
//
// Classification: BUG (low) — should warn that no dep was found to remove.
func TestDiscovery_DepRmNonexistentSilentSuccess(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Source", "--type", "task", "--priority", "2")
	b := w.create("--title", "Target", "--type", "task", "--priority", "2")

	// No dep exists between a and b
	// Removing a nonexistent dep should error or warn
	out, err := w.tryRun("dep", "rm", a, b)
	if err == nil {
		// Command succeeded — was there any indication nothing was removed?
		if !strings.Contains(strings.ToLower(out), "not found") &&
			!strings.Contains(strings.ToLower(out), "no dep") &&
			!strings.Contains(strings.ToLower(out), "warning") {
			t.Errorf("DISCOVERY: dep rm on nonexistent dependency succeeded silently with no warning (output: %s)", strings.TrimSpace(out))
		}
	}
	// If err != nil, the tool correctly rejected the operation — good
}

// =============================================================================
// SESSION 7 DISCOVERY: State corruption, filter conflicts, hierarchy
// =============================================================================

// TestDiscovery_DeferredStatusWithoutDate verifies that setting status to
// "deferred" without a defer date creates a valid state.
//
// FINDING: update.go treats --status and --defer as independent flags.
// bd update X --status deferred (without --defer) sets status=deferred but
// leaves defer_until empty. The issue is excluded from bd ready (status check)
// but has no date to ever "wake up." This creates a permanently deferred issue
// unless the user remembers to also set defer_until.
//
// Classification: BUG — status=deferred without defer_until should either
// error or automatically set defer_until to some reasonable default.
func TestDiscovery_DeferredStatusWithoutDate(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Deferred no date", "--type", "task", "--priority", "2")

	// Set status to deferred without --defer
	w.run("update", a, "--status", "deferred")

	data := parseJSON(t, w.run("show", a, "--json"))
	status := data[0]["status"]
	deferUntil := data[0]["defer_until"]

	if status == "deferred" && (deferUntil == nil || deferUntil == "") {
		// Not in ready
		readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
		if containsID(readyIDs, a) {
			t.Errorf("deferred issue should not be in ready")
		}

		// Not in deferred list either?
		deferredOut := w.run("list", "--status", "deferred", "--json", "-n", "0")
		deferredIDs := parseIDs(t, deferredOut)
		if containsID(deferredIDs, a) {
			t.Errorf("DISCOVERY: status=deferred without defer_until creates permanently deferred issue %s — no date to wake up, user must manually undefer", a)
		} else {
			t.Errorf("DISCOVERY: status=deferred without defer_until creates invisible issue %s — not in ready, not in deferred list", a)
		}
	}
}

// TestDiscovery_CommaStatusSilentlyReturnsEmpty verifies that bd list --status
// with comma-separated values is handled correctly.
//
// FINDING: list.go:255 reads --status as a simple string (not slice). The value
// "open,closed" is treated as a single literal status, which matches nothing.
// The user gets empty results with no error.
//
// Classification: BUG — should either parse comma-separated values or error.
func TestDiscovery_CommaStatusSilentlyReturnsEmpty(t *testing.T) {
	w := newCandidateWorkspace(t)

	w.create("--title", "Open issue", "--type", "task", "--priority", "2")
	closed := w.create("--title", "Closed issue", "--type", "task", "--priority", "2")
	w.run("close", closed)

	// Single status works (control)
	openOut := w.run("list", "--status", "open", "--json", "-n", "0")
	openIDs := parseIDs(t, openOut)
	if len(openIDs) == 0 {
		t.Fatalf("control: --status open should return at least 1 issue")
	}

	// Comma-separated should either work or error
	commaOut, err := w.tryRun("list", "--status", "open,closed", "--json", "-n", "0")
	if err == nil {
		commaIDs := parseIDs(t, commaOut)
		if len(commaIDs) == 0 {
			t.Errorf("DISCOVERY: --status 'open,closed' silently returns empty — comma-separated status not parsed, treated as invalid literal status")
		}
	}
	// If err != nil, the tool correctly rejected comma-separated — good
}

// TestDiscovery_AssigneeAndNoAssigneeConflict verifies that --assignee and
// --no-assignee on the same command are handled correctly.
//
// FINDING: list.go:423-424 sets filter.Assignee, then list.go:514-515 sets
// filter.NoAssignee. Both are set on the filter struct. The storage layer
// must decide which wins — undefined behavior.
//
// Classification: BUG — contradictory flags should error.
func TestDiscovery_AssigneeAndNoAssigneeConflict(t *testing.T) {
	w := newCandidateWorkspace(t)

	assigned := w.create("--title", "Assigned", "--type", "task", "--priority", "2")
	w.run("update", assigned, "--assignee", "alice")
	unassigned := w.create("--title", "Unassigned", "--type", "task", "--priority", "2")

	// --assignee alice (control)
	assigneeOut := w.run("list", "--assignee", "alice", "--json", "-n", "0")
	assigneeIDs := parseIDs(t, assigneeOut)
	if !containsID(assigneeIDs, assigned) {
		t.Fatalf("control: --assignee alice should include assigned issue")
	}

	// --no-assignee (control)
	noAssigneeOut := w.run("list", "--no-assignee", "--json", "-n", "0")
	noAssigneeIDs := parseIDs(t, noAssigneeOut)
	if !containsID(noAssigneeIDs, unassigned) {
		t.Fatalf("control: --no-assignee should include unassigned issue")
	}

	// --assignee alice --no-assignee together should error
	conflictOut, err := w.tryRun("list", "--assignee", "alice", "--no-assignee", "--json", "-n", "0")
	if err == nil {
		conflictIDs := parseIDs(t, conflictOut)
		if len(conflictIDs) == 0 {
			t.Errorf("DISCOVERY: --assignee alice --no-assignee returns empty — contradictory flags not detected, result is undefined")
		} else if containsID(conflictIDs, assigned) && !containsID(conflictIDs, unassigned) {
			t.Errorf("DISCOVERY: --assignee alice --no-assignee returns assigned issues only — --no-assignee silently ignored")
		} else if containsID(conflictIDs, unassigned) && !containsID(conflictIDs, assigned) {
			t.Errorf("DISCOVERY: --assignee alice --no-assignee returns unassigned only — --assignee silently ignored")
		}
	}
	// If err != nil, the tool correctly rejected contradictory flags — good
}

// TestDiscovery_CreateChildOfClosedParent verifies that creating a child
// under a closed parent is handled correctly.
//
// FINDING: create.go:422-437 validates that the parent EXISTS but does NOT
// check if the parent is open/active. Creating a child of a closed parent
// succeeds silently. The child points to a closed parent, potentially breaking
// hierarchy-aware queries.
//
// Classification: DECISION — may be intentional for post-mortem documentation,
// but surprising and undocumented.
func TestDiscovery_CreateChildOfClosedParent(t *testing.T) {
	w := newCandidateWorkspace(t)

	parent := w.create("--title", "Will close", "--type", "epic", "--priority", "1")
	w.run("close", parent)

	// Create child of closed parent — should warn or reject
	child, err := w.tryCreate("--title", "Orphan child", "--type", "task", "--priority", "2", "--parent", parent)
	if err != nil {
		// Tool correctly rejected creating child of closed parent
		return
	}

	// Child was created under closed parent
	if child != "" {
		childData := parseJSON(t, w.run("show", child, "--json"))
		t.Logf("child created under closed parent: %v", childData[0]["id"])

		// Is the child in ready?
		readyIDs := parseIDs(t, w.run("ready", "-n", "0", "--json"))
		if containsID(readyIDs, child) {
			t.Errorf("DISCOVERY: child %s of closed parent %s was created and appears in bd ready — no validation that parent is open", child, parent)
		} else {
			t.Errorf("DISCOVERY: child %s of closed parent %s was created successfully but excluded from ready — creating children of closed parents accepted silently", child, parent)
		}
	}
}

// TestDiscovery_DepAddInvalidTypeSilentlyAccepted verifies that dep add
// with an unknown type is handled correctly.
//
// FINDING: dep.go:273-275 accepts the dep type with minimal validation.
// types.go:715-717 only checks length (>0 and ≤50 chars). Any non-empty
// string is valid, including meaningless types like "xyz123".
//
// Classification: INVESTIGATE — custom types may be by design, but unknown
// types don't affect readiness or blocking and create confusing dep relationships.
func TestDiscovery_DepAddInvalidTypeSilentlyAccepted(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Source", "--type", "task", "--priority", "2")
	b := w.create("--title", "Target", "--type", "task", "--priority", "2")

	// Add dep with unknown type
	out, err := w.tryRun("dep", "add", a, b, "--type", "not-a-real-type")
	if err == nil {
		// Command succeeded — check if the dep was actually stored
		data := parseJSON(t, w.run("show", a, "--json"))
		deps, _ := data[0]["dependencies"].([]any)

		foundCustomDep := false
		for _, d := range deps {
			dep, _ := d.(map[string]any)
			if dep["dependency_type"] == "not-a-real-type" {
				foundCustomDep = true
			}
		}

		if foundCustomDep {
			t.Logf("CONFIRMED: dep add --type 'not-a-real-type' accepted and stored — custom dep types are supported (output: %s)", strings.TrimSpace(out))
		} else {
			t.Errorf("dep add succeeded but dep type 'not-a-real-type' not found in show output")
		}
	}
	// If err != nil, the tool correctly rejected invalid type — good
}

// TestDiscovery_StatusInProgressNoAutoAssign verifies that --status in_progress
// without --assignee doesn't auto-assign like --claim does.
//
// FINDING: update.go processes --status and --assignee independently.
// bd update X --status in_progress sets status but leaves assignee unchanged.
// bd update X --claim atomically sets both status=in_progress and assignee.
// Users who use --status in_progress expecting auto-assignment are surprised.
//
// Classification: INVESTIGATE — may be intentional (--claim is the auto-assign
// path), but the difference is undocumented and surprising.
func TestDiscovery_StatusInProgressNoAutoAssign(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Manual in_progress", "--type", "task", "--priority", "2")

	// Set status to in_progress without --claim or --assignee
	w.run("update", a, "--status", "in_progress")

	data := parseJSON(t, w.run("show", a, "--json"))
	status := data[0]["status"]
	assignee := data[0]["assignee"]

	if status != "in_progress" {
		t.Fatalf("status should be in_progress, got %v", status)
	}

	// Compare with --claim behavior
	b := w.create("--title", "Claimed", "--type", "task", "--priority", "2")
	w.run("update", b, "--claim")

	bData := parseJSON(t, w.run("show", b, "--json"))
	bStatus := bData[0]["status"]
	bAssignee := bData[0]["assignee"]

	if bStatus != "in_progress" {
		t.Fatalf("claimed status should be in_progress, got %v", bStatus)
	}

	// Document the behavioral difference
	if (assignee == nil || assignee == "") && bAssignee != nil && bAssignee != "" {
		t.Logf("CONFIRMED: --status in_progress (assignee=%v) vs --claim (assignee=%v) — --status does not auto-assign, --claim does", assignee, bAssignee)
	}
}

// TestProtocol_ListAllIncludesClosed verifies that bd list --all correctly
// bypasses the default closed exclusion filter.
func TestProtocol_ListAllIncludesClosed(t *testing.T) {
	w := newCandidateWorkspace(t)

	open := w.create("--title", "Open", "--type", "task", "--priority", "2")
	closed := w.create("--title", "Closed", "--type", "task", "--priority", "2")
	w.run("close", closed)

	// Default list excludes closed
	defaultOut := parseIDs(t, w.run("list", "--json", "-n", "0"))
	if containsID(defaultOut, closed) {
		t.Fatalf("default list should exclude closed issues")
	}
	if !containsID(defaultOut, open) {
		t.Fatalf("default list should include open issues")
	}

	// --all should include closed
	allOut := parseIDs(t, w.run("list", "--all", "--json", "-n", "0"))
	if !containsID(allOut, closed) {
		t.Errorf("list --all should include closed issue %s", closed)
	}
	if !containsID(allOut, open) {
		t.Errorf("list --all should include open issue %s", open)
	}
}

// TestDiscovery_CreateEmptyType verifies that bd create --type "" is handled.
//
// FINDING: create.go:711 defaults to "task" if --type not specified.
// But --type "" explicitly sets an empty type. The validation should catch
// this at the storage layer.
//
// Classification: BUG if accepted, PROTOCOL if rejected.
func TestDiscovery_CreateEmptyType(t *testing.T) {
	w := newCandidateWorkspace(t)

	_, err := w.tryCreate("--title", "Empty type", "--type", "", "--priority", "2")
	if err == nil {
		t.Errorf("DISCOVERY: bd create --type '' succeeded — empty type should be rejected")
	}
	// If err != nil, the tool correctly rejected empty type — good
}

// TestDiscovery_ShowJSONAlwaysArray verifies that bd show --json returns
// an array even for a single issue, for consistent parsing.
func TestDiscovery_ShowJSONAlwaysArray(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Single show", "--type", "task", "--priority", "2")

	out := w.run("show", a, "--json")
	out = strings.TrimSpace(out)

	// Should start with [ (array)
	if !strings.HasPrefix(out, "[") {
		t.Errorf("DISCOVERY: bd show --json for single issue doesn't return array — got: %s", out[:min(50, len(out))])
	}

	// Parse and verify
	data := parseJSON(t, out)
	if len(data) != 1 {
		t.Errorf("expected 1 item in array, got %d", len(data))
	}
}

// =============================================================================
// SESSION 7c DISCOVERY: Comments, due dates, ID filtering
// =============================================================================

// TestDiscovery_EmptyCommentAccepted verifies that bd comments add rejects
// empty comment text.
//
// FINDING: comments.go:110-114 accepts comment text without validation.
// bd comments add X "" stores a comment with empty text.
//
// Classification: BUG — same pattern as BUG-14 (empty label) and BUG-12
// (empty title). Empty comments should be rejected.
func TestDiscovery_EmptyCommentAccepted(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Comment test", "--type", "task", "--priority", "2")

	// Adding an empty comment should fail
	_, err := w.tryRun("comments", "add", a, "")
	if err == nil {
		data := parseJSON(t, w.run("show", a, "--json"))
		comments, _ := data[0]["comments"].([]any)
		if len(comments) > 0 {
			t.Errorf("DISCOVERY: bd comments add accepted empty comment text — stored %d empty comment(s)", len(comments))
		}
	}
	// If err != nil, the tool correctly rejected empty comment — good
}

// TestDiscovery_DueDatePastNoWarning verifies that setting a past due date
// gives some feedback to the user.
//
// FINDING: update.go:169-180 parses --due without checking if the date is
// in the past. Unlike --defer (which warns at lines 192-197), --due has NO
// past-date validation. The issue immediately appears in --overdue list.
//
// Classification: BUG — should at least warn (--defer does).
func TestDiscovery_DueDatePastNoWarning(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Past due test", "--type", "task", "--priority", "2")

	// Set due date to far past — should at least warn
	out := w.run("update", a, "--due", "2020-01-01")

	// Check if it's in overdue list
	overdueOut, err := w.tryRun("list", "--overdue", "--json", "-n", "0")
	if err != nil {
		t.Skipf("--overdue not available: %v", err)
	}
	overdueIDs := parseIDs(t, overdueOut)

	if containsID(overdueIDs, a) {
		// Past due date accepted and immediately overdue — was there a warning?
		if !strings.Contains(strings.ToLower(out), "past") &&
			!strings.Contains(strings.ToLower(out), "warn") &&
			!strings.Contains(strings.ToLower(out), "already") {
			t.Errorf("DISCOVERY: --due 2020-01-01 accepted without warning (unlike --defer which warns) — issue immediately appears in --overdue")
		}
	}
}

// TestDiscovery_ListIDFilterExactMatchOnly verifies that bd list --id
// requires exact IDs, not partial matches.
//
// FINDING: list.go:445-450 splits --id by comma and uses them as exact
// matches (not resolved through ResolvePartialID). If you pass a partial
// ID, it silently returns empty.
//
// Classification: INVESTIGATE — may be by design (list is a filter command,
// not a resolution command), but the silent empty result is surprising.
func TestDiscovery_ListIDFilterExactMatchOnly(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "ID filter test", "--type", "task", "--priority", "2")

	// Full ID should work
	fullOut, err := w.tryRun("list", "--id", a, "--json", "-n", "0")
	if err != nil {
		t.Skipf("--id flag not available: %v", err)
	}
	fullIDs := parseIDs(t, fullOut)
	if !containsID(fullIDs, a) {
		t.Fatalf("control: --id with full ID should return the issue")
	}

	// Partial ID (first 8 chars) — does it resolve?
	if len(a) > 8 {
		partial := a[:8]
		partialOut, err := w.tryRun("list", "--id", partial, "--json", "-n", "0")
		if err == nil {
			partialIDs := parseIDs(t, partialOut)
			if len(partialIDs) == 0 {
				t.Logf("CONFIRMED: bd list --id with partial ID '%s' returns empty — exact match only (bd show resolves partial IDs, bd list does not)", partial)
			}
		}
	}
}

// TestProtocol_CommentSpecialChars verifies that comments with special
// characters are stored and retrieved correctly.
func TestProtocol_CommentSpecialChars(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Special comment", "--type", "task", "--priority", "2")

	// Add comment with quotes and special chars
	specialText := `This has "quotes" and 'single' and <brackets>`
	w.run("comments", "add", a, specialText)

	data := parseJSON(t, w.run("show", a, "--json"))
	comments, _ := data[0]["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}

	comment, _ := comments[0].(map[string]any)
	text, _ := comment["text"].(string)
	if text != specialText {
		t.Errorf("comment text not preserved: got %q, want %q", text, specialText)
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

// === Session 8: Lifecycle validation, ready filters, children, duplicate cycles ===

// TestDiscovery_ReopenAlreadyOpenSucceeds verifies that bd reopen on an
// already-open issue should fail with an error, not silently succeed.
// BUG-56: reopen.go has no validation that issue is actually closed before
// reopening. The forReopen() validator exists in validation/issue.go but
// is never called.
func TestDiscovery_ReopenAlreadyOpenSucceeds(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Already open issue", "--type", "task", "--priority", "2")

	// Verify issue is open
	data := parseJSON(t, w.run("show", a, "--json"))
	status, _ := data[0]["status"].(string)
	if status != "open" {
		t.Fatalf("expected status open, got %s", status)
	}

	// Bug: reopen on an already-open issue should fail
	_, err := w.tryRun("reopen", a)
	if err == nil {
		// Reopen succeeded — this is the bug. It should reject reopening
		// an issue that is not closed.
		t.Errorf("DISCOVERY: bd reopen on already-open issue %s succeeded silently — "+
			"should error 'issue is not closed' or similar. "+
			"File: cmd/bd/reopen.go (no status validation). "+
			"The forReopen() validator in validation/issue.go is never called.", a)
	}
}

// TestDiscovery_UndeferNonDeferredSucceeds verifies that bd undefer on a
// non-deferred issue should fail, not silently succeed.
// BUG-57: undefer.go has no validation that issue is actually deferred.
func TestDiscovery_UndeferNonDeferredSucceeds(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Never deferred", "--type", "task", "--priority", "2")

	// Verify issue is open (not deferred)
	data := parseJSON(t, w.run("show", a, "--json"))
	status, _ := data[0]["status"].(string)
	if status != "open" {
		t.Fatalf("expected status open, got %s", status)
	}

	// Bug: undefer on a non-deferred issue should fail
	_, err := w.tryRun("undefer", a)
	if err == nil {
		t.Errorf("DISCOVERY: bd undefer on non-deferred issue %s succeeded silently — "+
			"should error 'issue is not deferred' or similar. "+
			"File: cmd/bd/undefer.go (no status validation).", a)
	}
}

// TestDiscovery_ReadyPriorityOutOfRange verifies that bd ready --priority 5
// should reject the value since valid priorities are 0-4.
// BUG-58: ready.go accepts any integer for --priority without validation.
func TestDiscovery_ReadyPriorityOutOfRange(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create a P4 issue so there IS ready work
	w.create("--title", "P4 task", "--type", "task", "--priority", "4")

	// Control: valid priority filter works
	_, err := w.tryRun("ready", "--priority", "4", "--json")
	if err != nil {
		t.Skipf("bd ready --priority not available: %v", err)
	}

	// Bug: priority 5 is out of range (valid: 0-4) but accepted silently
	out, err := w.tryRun("ready", "--priority", "5", "--json")
	if err == nil {
		// Command succeeded — check if it returned empty (silent wrong results)
		// or if it actually validated the range
		ids := parseIDs(t, out)
		if len(ids) == 0 {
			t.Errorf("DISCOVERY: bd ready --priority 5 accepted without error, returned empty — "+
				"should reject out-of-range priority (valid: 0-4). "+
				"File: cmd/bd/ready.go:96-98 (no validation).")
		}
	}
	// If err != nil, command properly rejected invalid priority — correct behavior
}

// TestDiscovery_ChildrenNonexistentParentSilentEmpty verifies that
// bd children <nonexistent-id> should error, not return empty.
// BUG-59: children command doesn't validate parent existence.
func TestDiscovery_ChildrenNonexistentParentSilentEmpty(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create a real issue to ensure the workspace works
	w.create("--title", "Real issue", "--type", "task", "--priority", "2")

	// Bug: children of nonexistent parent returns empty with no error
	_, err := w.tryRun("children", "nonexistent-parent-xyz", "--json")
	if err == nil {
		t.Errorf("DISCOVERY: bd children nonexistent-parent-xyz returned success — "+
			"should error 'parent issue not found'. Compare: bd show nonexistent-parent-xyz errors. "+
			"File: cmd/bd/children.go (no parent existence validation).")
	}
}

// TestDiscovery_DuplicateCycleUndetected verifies that creating a duplicate
// cycle (A dup of B, B dup of A) is not detected.
// BUG-60: Cycle detection only runs for 'blocks' dep type (see also BUG-25).
// duplicate-of deps can create cycles without any error.
func TestDiscovery_DuplicateCycleUndetected(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Issue A", "--type", "task", "--priority", "2")
	b := w.create("--title", "Issue B", "--type", "task", "--priority", "2")

	// Mark A as duplicate of B (closes A)
	w.run("duplicate", a, "--of", b)

	// Reopen A so we can test further
	w.run("reopen", a)

	// Now mark B as duplicate of A — this creates a cycle: A→B→A
	_, err := w.tryRun("duplicate", b, "--of", a)
	if err == nil {
		// Both operations succeeded — verify the cycle exists
		// Check that B is now closed (marked as dup of A)
		bData := parseJSON(t, w.run("show", b, "--json"))
		bStatus, _ := bData[0]["status"].(string)
		if bStatus == "closed" {
			t.Errorf("DISCOVERY: bd duplicate created a cycle — A duplicate-of B, B duplicate-of A — "+
				"no error detected. Both issues are duplicates of each other, which is semantically "+
				"incoherent. File: cmd/bd/duplicate.go (no cycle check). "+
				"Cycle detection at dependencies.go:54 only checks 'blocks' type.")
		}
	}
	// If err != nil, duplicate correctly detected the cycle — would be surprising
}

// TestDiscovery_StaleZeroDaysReturnsFreshIssue verifies that bd stale --days 0
// should be rejected or return empty, not return brand-new issues.
// BUG-61: stale.go:22 has no validation on days — 0 means cutoff=now,
// and updated_at < now matches everything including just-created issues.
func TestDiscovery_StaleZeroDaysReturnsFreshIssue(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Brand new issue", "--type", "task", "--priority", "2")

	out, err := w.tryRun("stale", "--days", "0", "--json")
	if err != nil {
		// Command rejected 0 — acceptable behavior
		return
	}

	ids := parseIDs(t, out)
	if containsID(ids, a) {
		t.Errorf("DISCOVERY: bd stale --days 0 returned brand-new issue %s as 'stale' — "+
			"0 days means cutoff=now, so updated_at < now matches everything. "+
			"Should reject --days 0 or treat as 'nothing is stale yet'. "+
			"File: cmd/bd/stale.go:22 (no validation), queries.go:767.", a)
	}
}

// TestProtocol_QueryPriorityRangeValidated verifies that bd query validates
// priority range (0-4) before applying filter.
func TestProtocol_QueryPriorityRangeValidated(t *testing.T) {
	w := newCandidateWorkspace(t)

	w.create("--title", "P4 issue", "--type", "task", "--priority", "4")

	// Out of range should error
	_, err := w.tryRun("query", "priority>=5", "--json")
	if err == nil {
		t.Errorf("bd query 'priority>=5' should reject out-of-range value")
	}

	_, err = w.tryRun("query", "priority<=-1", "--json")
	if err == nil {
		t.Errorf("bd query 'priority<=-1' should reject out-of-range value")
	}
}

// TestProtocol_QueryUnknownFieldErrors verifies that bd query with an unknown
// field returns a clear error.
func TestProtocol_QueryUnknownFieldErrors(t *testing.T) {
	w := newCandidateWorkspace(t)

	w.create("--title", "Query test", "--type", "task", "--priority", "2")

	_, err := w.tryRun("query", "nonexistent_field=hello", "--json")
	if err == nil {
		t.Errorf("PROTOCOL: bd query 'nonexistent_field=hello' should error but succeeded — " +
			"unknown fields should be rejected")
	}
}

// TestDiscovery_SearchStatusCommaNotParsed verifies that bd search with
// --status "open,closed" has the same comma-status bug as list (BUG-44).
// BUG-61: search.go uses same GetString for --status, not a slice.
func TestDiscovery_SearchStatusCommaNotParsed(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Searchable alpha", "--type", "task", "--priority", "2")
	b := w.create("--title", "Searchable beta", "--type", "task", "--priority", "2")
	w.run("close", b)

	// Control: search with valid status works
	controlOut, err := w.tryRun("search", "Searchable", "--status", "open", "--json")
	if err != nil {
		t.Skipf("bd search --status not available: %v", err)
	}
	controlIDs := parseIDs(t, controlOut)
	if !containsID(controlIDs, a) {
		t.Skipf("search didn't find expected issue")
	}

	// Bug: comma-separated status silently returns empty (same as BUG-44)
	commaOut, err := w.tryRun("search", "Searchable", "--status", "open,closed", "--json")
	if err == nil {
		commaIDs := parseIDs(t, commaOut)
		if len(commaIDs) == 0 {
			t.Errorf("DISCOVERY: bd search --status 'open,closed' silently returns empty — "+
				"same comma-status bug as list (BUG-44). 'open,closed' treated as single "+
				"literal status value. File: cmd/bd/search.go:53 (GetString not GetStringSlice).")
		}
	}
}

// TestDiscovery_ListTypeNonexistentSilentEmpty verifies that bd list with
// an invalid --type silently returns empty instead of erroring.
// BUG-62: list.go doesn't validate --type value against known types.
func TestDiscovery_ListTypeNonexistentSilentEmpty(t *testing.T) {
	w := newCandidateWorkspace(t)

	w.create("--title", "A task", "--type", "task", "--priority", "2")

	// Control: valid type works
	_, err := w.tryRun("list", "--type", "task", "--json", "-n", "0")
	if err != nil {
		t.Skipf("bd list --type not available: %v", err)
	}

	// Bug: nonexistent type silently returns empty
	out, err := w.tryRun("list", "--type", "nonexistent_type_xyz", "--json", "-n", "0")
	if err == nil {
		ids := parseIDs(t, out)
		if len(ids) == 0 {
			t.Errorf("DISCOVERY: bd list --type 'nonexistent_type_xyz' returned empty with no error — "+
				"should reject unknown type. Compare: bd create --type nonexistent fails with validation. "+
				"File: cmd/bd/list.go (no type validation on filter).")
		}
	}
}

// TestProtocol_DeleteNonexistentForceErrors verifies that bd delete with
// a nonexistent issue ID and --force properly errors.
func TestProtocol_DeleteNonexistentForceErrors(t *testing.T) {
	w := newCandidateWorkspace(t)

	w.create("--title", "Real issue", "--type", "task", "--priority", "2")

	_, err := w.tryRun("delete", "nonexistent-xyz-99", "--force")
	if err == nil {
		t.Errorf("bd delete nonexistent-xyz-99 --force should error 'not found'")
	}
}

// TestProtocol_PinClosedIssueHandled verifies that pinning a closed issue
// is handled gracefully (either rejected or pin doesn't stick).
func TestProtocol_PinClosedIssueHandled(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Pin target", "--type", "task", "--priority", "2")
	w.run("close", a, "--force")

	_, err := w.tryRun("update", a, "--pin")
	if err != nil {
		// Pin rejected on closed issue — acceptable
		return
	}

	// If pin succeeded, verify it's stored (correct even if questionable)
	data := parseJSON(t, w.run("show", a, "--json"))
	pinned, _ := data[0]["pinned"].(bool)
	if !pinned {
		t.Logf("Pin on closed issue %s: command succeeded but pinned=false", a)
	}
}

// === Session 8c: Blocked parent, label idempotency, dep tree depth ===

// TestDiscovery_BlockedNonexistentParentSilentEmpty verifies that
// bd blocked --parent <nonexistent-id> should error, not return empty.
// BUG-64: blocked command doesn't validate parent existence.
func TestDiscovery_BlockedNonexistentParentSilentEmpty(t *testing.T) {
	w := newCandidateWorkspace(t)

	// Create some blocked issues to ensure the command works
	a := w.create("--title", "Blocker", "--type", "task", "--priority", "2")
	b := w.create("--title", "Blocked by A", "--type", "task", "--priority", "2")
	w.run("dep", "add", b, a, "--type", "blocks")

	// Control: blocked with no filter returns results
	controlOut, err := w.tryRun("blocked", "--json")
	if err != nil {
		t.Skipf("bd blocked not available: %v", err)
	}
	controlIDs := parseIDs(t, controlOut)
	if !containsID(controlIDs, b) {
		t.Skipf("control: blocked issue not found in bd blocked")
	}

	// Bug: blocked --parent nonexistent returns empty with no error
	_, err = w.tryRun("blocked", "--parent", "nonexistent-parent-xyz", "--json")
	if err == nil {
		t.Errorf("DISCOVERY: bd blocked --parent nonexistent-parent-xyz returned success — "+
			"should error 'parent not found'. User can't tell if parent has no blocked children "+
			"or if parent doesn't exist. File: cmd/bd/ready.go:218-245 (no parent validation).")
	}
}

// TestDiscovery_LabelRemoveNonexistentSilentSuccess verifies that
// bd label remove <id> <nonexistent-label> reports success.
// BUG-65: Same pattern as BUG-42 (dep rm nonexistent says "Removed").
func TestDiscovery_LabelRemoveNonexistentSilentSuccess(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Label test", "--type", "task", "--priority", "2")
	w.run("label", "add", a, "real-label")

	// Verify label exists
	data := parseJSON(t, w.run("show", a, "--json"))
	labels, _ := data[0]["labels"].([]any)
	if len(labels) != 1 {
		t.Fatalf("expected 1 label, got %d", len(labels))
	}

	// Bug: removing a nonexistent label should error, not report success
	_, err := w.tryRun("label", "remove", a, "never-existed-label")
	if err == nil {
		// Command succeeded — verify the real label is still there
		data = parseJSON(t, w.run("show", a, "--json"))
		labels, _ = data[0]["labels"].([]any)
		if len(labels) == 1 {
			t.Errorf("DISCOVERY: bd label remove %s 'never-existed-label' reported success — "+
				"label didn't exist. Same pattern as BUG-42 (dep rm false positive). "+
				"File: cmd/bd/label.go (no existence check before remove).", a)
		}
	}
}

// TestDiscovery_LabelAddDuplicateReportsAdded verifies that adding an
// already-existing label reports "Added" even though it's a no-op.
// BUG-66: label.go doesn't check if label already exists before adding.
func TestDiscovery_LabelAddDuplicateReportsAdded(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Dup label test", "--type", "task", "--priority", "2")
	w.run("label", "add", a, "my-label")

	// Verify label exists
	data := parseJSON(t, w.run("show", a, "--json"))
	labels, _ := data[0]["labels"].([]any)
	if len(labels) != 1 {
		t.Fatalf("expected 1 label, got %d", len(labels))
	}

	// Bug: adding the same label again should either warn or be rejected
	_, err := w.tryRun("label", "add", a, "my-label")
	if err == nil {
		// Succeeded — check if the label count changed (duplicate created?)
		data = parseJSON(t, w.run("show", a, "--json"))
		labels, _ = data[0]["labels"].([]any)
		if len(labels) == 1 {
			// Idempotent (correct at storage level) but misleading "Added" message
			t.Errorf("DISCOVERY: bd label add %s 'my-label' reported 'Added' when label already existed — "+
				"idempotent no-op but misleading success message. Should warn 'label already exists'. "+
				"File: cmd/bd/label.go:99-102.", a)
		} else if len(labels) > 1 {
			t.Errorf("DISCOVERY: bd label add %s 'my-label' created DUPLICATE label — "+
				"now has %d copies. File: cmd/bd/label.go.", a, len(labels))
		}
	}
}

// TestProtocol_DepTreeNegativeDepthRejected verifies that bd dep tree with
// --max-depth -1 is properly validated and rejected.
func TestProtocol_DepTreeNegativeDepthRejected(t *testing.T) {
	w := newCandidateWorkspace(t)

	a := w.create("--title", "Tree root", "--type", "task", "--priority", "2")
	b := w.create("--title", "Tree child", "--type", "task", "--priority", "2")
	w.run("dep", "add", b, a, "--type", "blocks")

	_, err := w.tryRun("dep", "tree", a, "--max-depth", "-1")
	if err == nil {
		t.Errorf("bd dep tree --max-depth -1 should be rejected")
	}
}
