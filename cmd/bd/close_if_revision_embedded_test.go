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

// TestIfRevisionCloseReplaysMoleculeAutoClose pins mc-zndi7.75 item 1 on the
// embedded/direct --if-revision close route: runCloseDirectIfRevision bypasses
// `bd close`'s batch architecture entirely (A8, beads#4682 — BatchCloseItem
// carries no per-item ExpectedVersion), so it must re-drive molecule auto-close
// itself instead of silently dropping it.
//
// The setup mirrors close_embedded_test.go's
// "close_already_closed_replays_molecule_auto_close": strand a molecule root
// open by reopening ONLY the root after both steps are genuinely closed (the
// state a crash between the final step's close and its root auto-close would
// leave), then re-close the final step — this time guarded — and require the
// stranded-open root to heal.
func TestIfRevisionCloseReplaysMoleculeAutoClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rmg")

	root := bdCreate(t, bd, dir, "Guarded molecule root", "--type", "epic", "--labels", "template")
	step1 := bdCreate(t, bd, dir, "Guarded molecule step one", "--type", "task", "--parent", root.ID)
	step2 := bdCreate(t, bd, dir, "Guarded molecule step two", "--type", "task", "--parent", root.ID)

	bdClose(t, bd, dir, step1.ID, "--reason", "one")
	bdClose(t, bd, dir, step2.ID, "--reason", "two")
	if got := bdShow(t, bd, dir, root.ID); got.Status != types.StatusClosed {
		t.Fatalf("precondition: expected molecule root %s auto-closed after final step, got %s", root.ID, got.Status)
	}

	bdReopen(t, bd, dir, root.ID)
	if got := bdShow(t, bd, dir, root.ID); got.Status != types.StatusOpen {
		t.Fatalf("precondition: expected molecule root %s reopened, got %s", root.ID, got.Status)
	}

	// Re-close the already-closed final step, this time GUARDED. The idempotent
	// guarded re-close must replay molecule auto-close and heal the
	// stranded-open root exactly as the unguarded path does.
	rev := bdShowRevision(t, bd, dir, step2.ID)
	bdRunOK(t, bd, dir, "close", step2.ID, "--if-revision", revStr(rev), "--reason", "retry")

	if got := bdShow(t, bd, dir, root.ID); got.Status != types.StatusClosed {
		t.Errorf("expected stranded-open molecule root %s healed by a guarded re-close of the final step, got %s",
			root.ID, got.Status)
	}
}

// TestIfRevisionCloseWarnsOnForcedOpenChildren pins mc-zndi7.75 item 3: the
// "warning: closing X with N open child issue(s) still active" line
// (close.go:194-196) must still print on the guarded direct close route when
// --force waives the engine's open-children refusal, exactly as it does on
// the unguarded batch path.
func TestIfRevisionCloseWarnsOnForcedOpenChildren(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rmw")

	parent := bdCreate(t, bd, dir, "Guarded parent with open child", "--type", "task")
	_ = bdCreate(t, bd, dir, "Open child", "--type", "task", "--parent", parent.ID)

	rev := bdShowRevision(t, bd, dir, parent.ID)
	out := bdRunOK(t, bd, dir, "close", parent.ID, "--if-revision", revStr(rev), "--force")
	if !strings.Contains(out, "open child issue(s) still active") {
		t.Errorf("guarded forced close with an open child did not warn:\n%s", out)
	}

	if got := bdShow(t, bd, dir, parent.ID); got.Status != types.StatusClosed {
		t.Errorf("guarded forced close did not apply: status = %s, want closed", got.Status)
	}
}

// TestIfRevisionCloseRestoresPerIDFences pins beads#7206 on the direct route:
// bd close --if-revision must refuse another actor's bead, a pin, and an
// unsatisfied bead gate the same way the unguarded route does, while a stale
// token still exits 13 before those refusals.
func TestIfRevisionCloseRestoresPerIDFences(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cfr")
	store := openStore(t, beadsDir, "cfr")
	if err := store.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
		t.Fatalf("SetConfig types.custom: %v", err)
	}
	store.Close()

	t.Run("assignee_mismatch", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Held by someone else", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")
		rev := bdShowRevision(t, bd, dir, issue.ID)

		out, code := bdCloseFailCode(t, bd, dir, issue.ID, "--actor", "other", "--if-revision", revStr(rev))
		if code != 1 {
			t.Errorf("exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "assignee is \"holder\"") || !strings.Contains(out, "actor is \"other\"") {
			t.Errorf("expected the assignee fence, got:\n%s", out)
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status == types.StatusClosed {
			t.Errorf("assignee mismatch closed %s", issue.ID)
		}
		if got.Assignee != "holder" {
			t.Errorf("assignee = %q, want holder", got.Assignee)
		}
	})

	t.Run("status_pinned", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Status pin", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "pinned")
		rev := bdShowRevision(t, bd, dir, issue.ID)

		out, code := bdCloseFailCode(t, bd, dir, issue.ID, "--if-revision", revStr(rev))
		if code != 1 {
			t.Errorf("exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "cannot modify pinned issue "+issue.ID) {
			t.Errorf("expected the pin fence, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Status == types.StatusClosed {
			t.Errorf("status=pinned close applied")
		}
	})

	t.Run("boolean_pinned", func(t *testing.T) {
		plan := `{"nodes": [{"key": "p", "title": "Boolean pinned", "type": "task", "pinned": true}]}`
		planFile := filepath.Join(dir, "boolean-pinned-plan.json")
		if err := os.WriteFile(planFile, []byte(plan), 0o644); err != nil {
			t.Fatal(err)
		}
		out := bdRunOK(t, bd, dir, "create", "--graph", planFile, "--json")
		id := graphCreatedID(t, out, "p")
		if seeded := bdShow(t, bd, dir, id); !seeded.Pinned {
			t.Fatalf("precondition: pinned=true, got %+v", seeded)
		}
		rev := bdShowRevision(t, bd, dir, id)

		refused, code := bdCloseFailCode(t, bd, dir, id, "--if-revision", revStr(rev))
		if code != 1 {
			t.Errorf("exit code = %d, want 1\n%s", code, refused)
		}
		if !strings.Contains(refused, "cannot modify pinned issue "+id) {
			t.Errorf("expected the pin fence, got:\n%s", refused)
		}
		if got := bdShow(t, bd, dir, id); got.Status == types.StatusClosed {
			t.Errorf("boolean pin close applied")
		}
	})

	t.Run("unsatisfied_bead_gate", func(t *testing.T) {
		awaited := bdCreate(t, bd, dir, "Awaited open bead", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked by bead gate", "--type", "task")
		created := bdGate(t, bd, dir, "create", "--type=bead", "--blocks", blocked.ID, "--await-id", awaited.ID)
		gateID := parseCreatedGateID(t, created)
		rev := bdShowRevision(t, bd, dir, gateID)

		out, code := bdCloseFailCode(t, bd, dir, gateID, "--if-revision", revStr(rev))
		if code != 1 {
			t.Errorf("exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "cannot close "+gateID) || !strings.Contains(out, "gate condition not satisfied") {
			t.Errorf("expected the gate fence, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, gateID); got.Status == types.StatusClosed {
			t.Errorf("unsatisfied gate close applied")
		}
	})

	t.Run("matching_revision_closes", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Plain guarded close", "--type", "task")
		rev := bdShowRevision(t, bd, dir, issue.ID)
		bdClose(t, bd, dir, issue.ID, "--if-revision", revStr(rev), "--reason", "done")
		if got := bdShow(t, bd, dir, issue.ID); got.Status != types.StatusClosed {
			t.Errorf("status = %s, want closed", got.Status)
		}
	})

	t.Run("stale_revision_beats_assignee_fence", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Stale token vs holder", "--type", "task")
		rev0 := bdShowRevision(t, bd, dir, issue.ID)
		bdUpdate(t, bd, dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")

		out, code := bdCloseFailCode(t, bd, dir, issue.ID, "--actor", "other", "--if-revision", revStr(rev0))
		if code != ExitGuardMismatch {
			t.Errorf("exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("expected revision mismatch, got:\n%s", out)
		}
		if strings.Contains(out, "assignee is") {
			t.Errorf("stale token fell through to the assignee fence:\n%s", out)
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status == types.StatusClosed {
			t.Errorf("stale close applied")
		}
		if got.Assignee != "holder" {
			t.Errorf("assignee = %q, want holder", got.Assignee)
		}
	})

	t.Run("force_assignee_mismatch_closes", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Forced cross-actor close", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")
		rev := bdShowRevision(t, bd, dir, issue.ID)
		bdClose(t, bd, dir, issue.ID, "--actor", "other", "--force", "--if-revision", revStr(rev), "--reason", "override")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("status = %s, want closed", got.Status)
		}
	})
}

// bdCloseFailCode runs "bd close" expecting failure and returns the combined
// output plus the process exit code. Guard tests need the code, not only
// nonzero: 13 is a stale --if-revision, 1 is any other refusal.
func bdCloseFailCode(t *testing.T, bd, dir string, args ...string) (string, int) {
	t.Helper()
	fullArgs := append([]string{"close"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd close %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("bd close %s failed without an exit code: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), ee.ExitCode()
}

func graphCreatedID(t *testing.T, out, key string) string {
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
