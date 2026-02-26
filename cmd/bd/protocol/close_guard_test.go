package protocol

import "testing"

// TestProtocol_CyclePreventionBlocks asserts that adding a dependency that
// would create a cycle is rejected with an error.
//
// Invariant: bd dep add C A --type blocks MUST fail when A→B→C chain exists.
func TestProtocol_CyclePreventionBlocks(t *testing.T) {
	w := newWorkspace(t)
	a := w.create("--title", "Chain-A", "--type", "task")
	b := w.create("--title", "Chain-B", "--type", "task")
	c := w.create("--title", "Chain-C", "--type", "task")

	w.run("dep", "add", b, a) // B depends on A
	w.run("dep", "add", c, b) // C depends on B

	// Adding C→A would create A→B→C→A cycle — must be rejected
	_, err := w.tryRun("dep", "add", a, c, "--type", "blocks")
	if err == nil {
		t.Errorf("dep add A→C should fail: would create cycle A→B→C→A")
	}
}

// TestProtocol_SelfDependencyPrevention asserts that an issue cannot depend
// on itself.
//
// Invariant: bd dep add A A MUST fail.
func TestProtocol_SelfDependencyPrevention(t *testing.T) {
	w := newWorkspace(t)
	a := w.create("--title", "Self-dep test", "--type", "task")

	_, err := w.tryRun("dep", "add", a, a, "--type", "blocks")
	if err == nil {
		t.Errorf("dep add %s %s should fail: self-dependency not allowed", a, a)
	}
}

// TestProtocol_CloseGuardRespectsDepType asserts that close guard only blocks
// for blocking dep types (blocks), not non-blocking types (caused-by).
//
// Invariant: bd close A succeeds when A has only caused-by deps on open issues.
func TestProtocol_CloseGuardRespectsDepType(t *testing.T) {
	w := newWorkspace(t)
	a := w.create("--title", "Issue A", "--type", "task")
	b := w.create("--title", "Issue B (open)", "--type", "task")

	// caused-by is non-blocking
	w.run("dep", "add", a, b, "--type", "caused-by")

	// Close should succeed despite open caused-by dep
	_, err := w.tryRun("close", a)
	if err != nil {
		t.Errorf("close A should succeed: caused-by is non-blocking, but got: %v", err)
	}
}

// TestProtocol_CloseGuardBlocksForBlocksDep asserts that close guard prevents
// actually closing an issue that has open blocking deps.
//
// Note: BUG-10 means the command may exit 0 even when close guard fires,
// so we check actual status instead of exit code.
func TestProtocol_CloseGuardBlocksForBlocksDep(t *testing.T) {
	w := newWorkspace(t)
	a := w.create("--title", "Blocked issue", "--type", "task")
	b := w.create("--title", "Blocker (open)", "--type", "task")

	w.run("dep", "add", a, b) // A depends on B (B blocks A)

	// Try to close A (which is blocked by open B)
	w.tryRun("close", a) //nolint:errcheck // exit code may be 0 per BUG-10

	// Regardless of exit code, A should NOT actually be closed
	shown := w.showJSON(a)
	status, _ := shown["status"].(string)
	if status == "closed" {
		t.Errorf("close guard should prevent closing blocked issue %s, but status = closed", a)
	}
}

// TestProtocol_CloseForceOverridesGuard asserts that --force bypasses close guard.
//
// Invariant: bd close A --force MUST succeed even with open blockers.
func TestProtocol_CloseForceOverridesGuard(t *testing.T) {
	w := newWorkspace(t)
	a := w.create("--title", "Blocked issue", "--type", "task")
	b := w.create("--title", "Blocker", "--type", "task")

	w.run("dep", "add", a, b)

	// Close with --force should succeed
	_, err := w.tryRun("close", a, "--force")
	if err != nil {
		t.Errorf("close A --force should succeed despite open blocker, got: %v", err)
	}

	// Verify closed
	shown := w.showJSON(a)
	status, _ := shown["status"].(string)
	if status != "closed" {
		t.Errorf("status = %q after close --force, want closed", status)
	}
}

// TestProtocol_SupersedeClosesAndCreatesDep asserts that bd supersede A --with B
// closes A and creates a supersedes dependency.
//
// Invariant: superseded issue is closed and has dependency on replacement.
func TestProtocol_SupersedeClosesAndCreatesDep(t *testing.T) {
	w := newWorkspace(t)
	a := w.create("--title", "Old approach", "--type", "task", "--priority", "2")
	b := w.create("--title", "New approach", "--type", "task", "--priority", "2")

	w.run("supersede", a, "--with", b)

	// A should be closed
	shown := w.showJSON(a)
	status, _ := shown["status"].(string)
	if status != "closed" {
		t.Errorf("superseded issue status = %q, want closed", status)
	}

	// A should have a supersedes dependency on B
	deps := getObjectSlice(shown, "dependencies")
	found := false
	for _, dep := range deps {
		depID, _ := dep["id"].(string)
		depType, _ := dep["dependency_type"].(string)
		if depID == b && depType == "supersedes" {
			found = true
		}
	}
	if !found {
		t.Errorf("superseded issue %s should have supersedes dep on %s, got deps: %v", a, b, deps)
	}
}
