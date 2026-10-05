//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedIfRevisionCloseReplaysMoleculeAutoClose is
// TestIfRevisionCloseReplaysMoleculeAutoClose's proxied-server twin, pinning
// mc-zndi7.75 item 1 on the route `bd serve` actually runs:
// runCloseProxiedIfRevision bypasses the batch entirely (A8, beads#4682), so
// it must re-drive molecule auto-close itself via its own post-close unit of
// work, exactly as closeProxiedRunPostClose does for the unguarded batch
// route.
func TestProxiedIfRevisionCloseReplaysMoleculeAutoClose(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ivc")

	root := bdProxiedCreate(t, bd, p.dir, "Guarded proxied molecule root", "-t", "epic", "--labels", "template")
	step1 := bdProxiedCreate(t, bd, p.dir, "Guarded proxied molecule step one", "--parent", root.ID)
	step2 := bdProxiedCreate(t, bd, p.dir, "Guarded proxied molecule step two", "--parent", root.ID)

	bdProxiedClose(t, bd, p.dir, step1.ID, "--reason", "one")
	bdProxiedClose(t, bd, p.dir, step2.ID, "--reason", "two")
	db := openProxiedDB(t, p)
	if got := readStatus(t, db, root.ID); got != types.StatusClosed {
		t.Fatalf("precondition: expected molecule root %s auto-closed after final step, got %q", root.ID, got)
	}

	bdProxiedReopen(t, bd, p.dir, root.ID)
	if got := readStatus(t, db, root.ID); got != types.StatusOpen {
		t.Fatalf("precondition: expected molecule root %s reopened, got %q", root.ID, got)
	}

	// Re-close the already-closed final step, this time GUARDED. The
	// idempotent guarded re-close must replay molecule auto-close and heal
	// the stranded-open root exactly as the unguarded path does.
	rev := bdProxiedShowRevision(t, bd, p.dir, step2.ID)
	bdProxiedClose(t, bd, p.dir, step2.ID, "--if-revision", proxiedRevStr(rev), "--reason", "retry")

	if got := readStatus(t, db, root.ID); got != types.StatusClosed {
		t.Errorf("expected stranded-open molecule root %s healed by a guarded proxied re-close of the final step, got %q",
			root.ID, got)
	}
}

// TestProxiedIfRevisionCloseWarnsOnForcedOpenChildren is
// TestIfRevisionCloseWarnsOnForcedOpenChildren's proxied-server twin, pinning
// mc-zndi7.75 item 3 on the proxied route.
func TestProxiedIfRevisionCloseWarnsOnForcedOpenChildren(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ivw")

	parent := bdProxiedCreate(t, bd, p.dir, "Guarded proxied parent with open child")
	_ = bdProxiedCreate(t, bd, p.dir, "Open proxied child", "--parent", parent.ID)

	rev := bdProxiedShowRevision(t, bd, p.dir, parent.ID)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "close", parent.ID, "--if-revision", proxiedRevStr(rev), "--force")
	if err != nil {
		t.Fatalf("bd close --force --if-revision failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "open child issue(s) still active") {
		t.Errorf("guarded proxied forced close with an open child did not warn:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	db := openProxiedDB(t, p)
	if got := readStatus(t, db, parent.ID); got != types.StatusClosed {
		t.Errorf("guarded proxied forced close did not apply: status = %q, want closed", got)
	}
}

// TestProxiedIfRevisionCloseRestoresPerIDFences is the proxied twin of
// TestIfRevisionCloseRestoresPerIDFences (beads#7206).
func TestProxiedIfRevisionCloseRestoresPerIDFences(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("assignee_mismatch", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ifa")
		issue := bdProxiedCreate(t, bd, p.dir, "Held by someone else")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")
		rev := bdProxiedShowRevision(t, bd, p.dir, issue.ID)

		out, code := bdProxiedCloseFailCode(t, bd, p.dir, issue.ID, "--actor", "other", "--if-revision", proxiedRevStr(rev))
		if code != 1 {
			t.Errorf("exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "assignee is \"holder\"") || !strings.Contains(out, "actor is \"other\"") {
			t.Errorf("expected the assignee fence, got:\n%s", out)
		}
		got := bdProxiedShow(t, bd, p.dir, issue.ID)
		if got.Status == types.StatusClosed {
			t.Errorf("assignee mismatch closed %s", issue.ID)
		}
		if got.Assignee != "holder" {
			t.Errorf("assignee = %q, want holder", got.Assignee)
		}
	})

	t.Run("status_pinned", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ifs")
		issue := bdProxiedCreate(t, bd, p.dir, "Status pin")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--status", "pinned")
		rev := bdProxiedShowRevision(t, bd, p.dir, issue.ID)

		out, code := bdProxiedCloseFailCode(t, bd, p.dir, issue.ID, "--if-revision", proxiedRevStr(rev))
		if code != 1 {
			t.Errorf("exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "cannot modify pinned issue "+issue.ID) {
			t.Errorf("expected the pin fence, got:\n%s", out)
		}
		if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Status == types.StatusClosed {
			t.Errorf("status=pinned close applied")
		}
	})

	t.Run("boolean_pinned", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ifb")
		plan := `{"nodes": [{"key": "p", "title": "Boolean pinned", "type": "task", "pinned": true}]}`
		planFile := filepath.Join(p.dir, "boolean-pinned-plan.json")
		if err := os.WriteFile(planFile, []byte(plan), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := bdProxiedRun(t, bd, p.dir, "create", "--graph", planFile, "--json")
		if err != nil {
			t.Fatalf("bd create --graph failed: %v\n%s", err, out)
		}
		id := proxiedGraphCreatedID(t, string(out), "p")
		if seeded := bdProxiedShow(t, bd, p.dir, id); !seeded.Pinned {
			t.Fatalf("precondition: pinned=true, got %+v", seeded)
		}
		rev := bdProxiedShowRevision(t, bd, p.dir, id)

		refused, code := bdProxiedCloseFailCode(t, bd, p.dir, id, "--if-revision", proxiedRevStr(rev))
		if code != 1 {
			t.Errorf("exit code = %d, want 1\n%s", code, refused)
		}
		if !strings.Contains(refused, "cannot modify pinned issue "+id) {
			t.Errorf("expected the pin fence, got:\n%s", refused)
		}
		if got := bdProxiedShow(t, bd, p.dir, id); got.Status == types.StatusClosed {
			t.Errorf("boolean pin close applied")
		}
	})

	t.Run("unsatisfied_bead_gate", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ifg")
		awaited := bdProxiedCreate(t, bd, p.dir, "Awaited open bead")
		blocked := bdProxiedCreate(t, bd, p.dir, "Blocked by bead gate")
		created, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "create", "--type=bead", "--blocks", blocked.ID, "--await-id", awaited.ID)
		if err != nil {
			t.Fatalf("bead gate create failed: %v\nstderr:\n%s", err, stderr)
		}
		gateID := parseCreatedGateID(t, created)
		rev := bdProxiedShowRevision(t, bd, p.dir, gateID)

		out, code := bdProxiedCloseFailCode(t, bd, p.dir, gateID, "--if-revision", proxiedRevStr(rev))
		if code != 1 {
			t.Errorf("exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "cannot close "+gateID) || !strings.Contains(out, "gate condition not satisfied") {
			t.Errorf("expected the gate fence, got:\n%s", out)
		}
		if got := bdProxiedShow(t, bd, p.dir, gateID); got.Status == types.StatusClosed {
			t.Errorf("unsatisfied gate close applied")
		}
	})

	t.Run("matching_revision_closes", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ifo")
		issue := bdProxiedCreate(t, bd, p.dir, "Plain guarded close")
		rev := bdProxiedShowRevision(t, bd, p.dir, issue.ID)
		bdProxiedClose(t, bd, p.dir, issue.ID, "--if-revision", proxiedRevStr(rev), "--reason", "done")
		if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Status != types.StatusClosed {
			t.Errorf("status = %s, want closed", got.Status)
		}
	})

	t.Run("stale_revision_beats_assignee_fence", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ifz")
		issue := bdProxiedCreate(t, bd, p.dir, "Stale token vs holder")
		rev0 := bdProxiedShowRevision(t, bd, p.dir, issue.ID)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")

		out, code := bdProxiedCloseFailCode(t, bd, p.dir, issue.ID, "--actor", "other", "--if-revision", proxiedRevStr(rev0))
		if code != ExitGuardMismatch {
			t.Errorf("exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("expected revision mismatch, got:\n%s", out)
		}
		if strings.Contains(out, "assignee is") {
			t.Errorf("stale token fell through to the assignee fence:\n%s", out)
		}
		got := bdProxiedShow(t, bd, p.dir, issue.ID)
		if got.Status == types.StatusClosed {
			t.Errorf("stale close applied")
		}
		if got.Assignee != "holder" {
			t.Errorf("assignee = %q, want holder", got.Assignee)
		}
	})

	t.Run("force_assignee_mismatch_closes", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "iff")
		issue := bdProxiedCreate(t, bd, p.dir, "Forced cross-actor close")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")
		rev := bdProxiedShowRevision(t, bd, p.dir, issue.ID)
		bdProxiedClose(t, bd, p.dir, issue.ID, "--actor", "other", "--force", "--if-revision", proxiedRevStr(rev), "--reason", "override")
		if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Status != types.StatusClosed {
			t.Errorf("status = %s, want closed", got.Status)
		}
	})
}

func bdProxiedCloseFailCode(t *testing.T, bd, dir string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"close"}, args...)
	out, err := bdProxiedRun(t, bd, dir, full...)
	if err == nil {
		t.Fatalf("expected bd close %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("bd close %s failed without an exit code: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), ee.ExitCode()
}

func proxiedGraphCreatedID(t *testing.T, out, key string) string {
	t.Helper()
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON in graph create output:\n%s", out)
	}
	var created GraphApplyResult
	if err := json.Unmarshal([]byte(out[start:]), &created); err != nil {
		t.Fatalf("parse graph result: %v\n%s", err, out[start:])
	}
	id := created.IDs[key]
	if id == "" {
		t.Fatalf("expected an ID for key %s, got %#v", key, created.IDs)
	}
	return id
}
