//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdShowRevision runs "bd show <id> --json" and returns the issue's current
// revision as an int64, decoded the same way --if-revision expects a caller
// to supply it (types.ParseRevisionToken on the wire "revision" string).
//
// "bd show --json" always wraps its result in a JSON array (cmd/bd/show.go's
// allDetails accumulator), even for a single id, so this parses the first
// JSON object the same trailing-content-tolerant way parseIssueJSON does
// (json.Unmarshal first, falling back to a json.Decoder that safely ignores
// the trailing "]" by reading exactly one JSON value) rather than
// json.Unmarshal-ing the whole array into a struct.
func bdShowRevision(t *testing.T, bd, dir, id string) int64 {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\nstdout:\n%s\nstderr:\n%s", id, err, stdout.String(), stderr.String())
	}
	s := stdout.String()
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object found in bd show %s --json output:\n%s", id, s)
	}
	var details struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(s[start:]), &details); err != nil {
		dec := json.NewDecoder(strings.NewReader(s[start:]))
		if decErr := dec.Decode(&details); decErr != nil {
			t.Fatalf("failed to parse revision from bd show %s --json: %v\nraw: %s", id, decErr, s[start:])
		}
	}
	rev, perr := types.ParseRevisionToken(details.Revision)
	if perr != nil {
		t.Fatalf("bd show %s --json returned an unparseable revision %q: %v", id, details.Revision, perr)
	}
	return rev
}

func revStr(rev int64) string { return strconv.FormatInt(rev, 10) }

// TestIfRevisionAdvertisedOnFourVerbs pins T4.1: --if-revision is a real,
// documented flag on all four verbs A8 adds it to, not an undocumented side
// channel a caller has to discover by reading source.
func TestIfRevisionAdvertisedOnFourVerbs(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "h4")

	for _, verb := range []string{"update", "close", "assign", "delete"} {
		t.Run(verb, func(t *testing.T) {
			out := bdRunOK(t, bd, dir, verb, "--help")
			if !strings.Contains(out, "--if-revision") {
				t.Errorf("bd %s --help does not advertise --if-revision:\n%s", verb, out)
			}
		})
	}
}

// TestIfRevisionMatchAndMismatch pins T4.2 across all four verbs: a stale
// --if-revision token exits ExitGuardMismatch (13) and writes nothing at all
// (not the field, not the revision itself); the same token, once it is the
// issue's CURRENT revision, applies the write and the revision moves again.
func TestIfRevisionMatchAndMismatch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mm")

	t.Run("update", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded update", "--type", "task", "--priority", "2")
		rev0 := bdShowRevision(t, bd, dir, issue.ID)
		// Bump the revision via an unguarded edit so rev0 is now stale.
		bdUpdate(t, bd, dir, issue.ID, "--notes", "bump")
		rev1 := bdShowRevision(t, bd, dir, issue.ID)
		if rev1 == rev0 {
			t.Fatalf("unguarded update did not change the revision")
		}

		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-revision", revStr(rev0), "--priority", "3")
		if code != ExitGuardMismatch {
			t.Errorf("stale --if-revision exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("mismatch error should say \"revision mismatch\", got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Priority != 2 {
			t.Errorf("stale --if-revision update still applied: priority = %d, want 2", got.Priority)
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev1 {
			t.Errorf("stale --if-revision update still advanced the revision: %d, want unchanged %d", got, rev1)
		}

		bdUpdate(t, bd, dir, issue.ID, "--if-revision", revStr(rev1), "--priority", "3")
		if got := bdShow(t, bd, dir, issue.ID); got.Priority != 3 {
			t.Errorf("matching --if-revision update did not apply: priority = %d, want 3", got.Priority)
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got == rev1 {
			t.Errorf("matching --if-revision update did not advance the revision")
		}
	})

	t.Run("close", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded close", "--type", "task")
		rev0 := bdShowRevision(t, bd, dir, issue.ID)
		bdUpdate(t, bd, dir, issue.ID, "--notes", "bump")
		rev1 := bdShowRevision(t, bd, dir, issue.ID)

		out, code := bdRunFailCode(t, bd, dir, "close", issue.ID, "--if-revision", revStr(rev0))
		if code != ExitGuardMismatch {
			t.Errorf("stale --if-revision close exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("mismatch error should say \"revision mismatch\", got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Status == types.StatusClosed {
			t.Errorf("stale --if-revision close still closed the issue")
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev1 {
			t.Errorf("stale --if-revision close still advanced the revision: %d, want unchanged %d", got, rev1)
		}

		bdRunOK(t, bd, dir, "close", issue.ID, "--if-revision", revStr(rev1))
		if got := bdShow(t, bd, dir, issue.ID); got.Status != types.StatusClosed {
			t.Errorf("matching --if-revision close did not apply: status = %s, want closed", got.Status)
		}
	})

	t.Run("assign", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded assign", "--type", "task")
		rev0 := bdShowRevision(t, bd, dir, issue.ID)
		bdUpdate(t, bd, dir, issue.ID, "--notes", "bump")
		rev1 := bdShowRevision(t, bd, dir, issue.ID)

		out, code := bdRunFailCode(t, bd, dir, "assign", issue.ID, "worker", "--if-revision", revStr(rev0))
		if code != ExitGuardMismatch {
			t.Errorf("stale --if-revision assign exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("mismatch error should say \"revision mismatch\", got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
			t.Errorf("stale --if-revision assign still applied: assignee = %q, want unassigned", got.Assignee)
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev1 {
			t.Errorf("stale --if-revision assign still advanced the revision: %d, want unchanged %d", got, rev1)
		}

		bdRunOK(t, bd, dir, "assign", issue.ID, "worker", "--if-revision", revStr(rev1))
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "worker" {
			t.Errorf("matching --if-revision assign did not apply: assignee = %q, want worker", got.Assignee)
		}
	})

	t.Run("delete", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Guarded delete", "--type", "task")
		rev0 := bdShowRevision(t, bd, dir, issue.ID)
		bdUpdate(t, bd, dir, issue.ID, "--notes", "bump")
		rev1 := bdShowRevision(t, bd, dir, issue.ID)

		out, code := bdRunFailCode(t, bd, dir, "delete", issue.ID, "--if-revision", revStr(rev0), "--force")
		if code != ExitGuardMismatch {
			t.Errorf("stale --if-revision delete exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("mismatch error should say \"revision mismatch\", got:\n%s", out)
		}
		// The row must still exist: `bd show` must still succeed.
		bdShow(t, bd, dir, issue.ID)

		bdRunOK(t, bd, dir, "delete", issue.ID, "--if-revision", revStr(rev1), "--force")
		bdShowFail(t, bd, dir, issue.ID)
	})
}

// TestIfRevisionDeletePreflightGoneIsPreconditionFailed pins mc-zndi7.81: `bd
// delete`'s single-id path resolves the row with resolveAndGetIssueForMutation
// BEFORE calling deleter.Delete() and the per-id lock fence #7244 added. On a
// real Dolt sql-server, a same-token --if-revision racer that loses that fence
// finds the row already gone right there
// (TestSharedServerDeleteIfRevisionSingleWinner/same_token, which this test
// cannot reach without a container) and, pre-fix, exited 1 with an
// unclassified "not found" instead of the ExitGuardMismatch (13)
// precondition_failed every other --if-revision loser gets. This
// reproduces the SAME code path deterministically, without a race or a real
// Dolt server: delete the row out from under a --if-revision token first (an
// ordinary, unguarded delete lands the row-gone precondition exactly as a
// winning racer's delete would), then present that now-stale token. The
// pre-flight existence check must fail exactly the same way a mid-guard
// version mismatch does.
func TestIfRevisionDeletePreflightGoneIsPreconditionFailed(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pg")

	issue := bdCreate(t, bd, dir, "Preflight gone", "--type", "task")
	rev0 := bdShowRevision(t, bd, dir, issue.ID)

	// Stand in for the winning racer: an ordinary delete removes the row out
	// from under rev0 without ever consulting it.
	bdDelete(t, bd, dir, issue.ID, "--force")
	bdShowFail(t, bd, dir, issue.ID)

	// The loser's --if-revision now names a row that is not merely stale but
	// entirely gone. Must classify exactly like a mid-guard version mismatch:
	// ExitGuardMismatch (13), "precondition failed" -- never the bare,
	// unclassified "not found" exit 1 the direct-store pre-flight check would
	// otherwise return on its own.
	out, code := bdRunFailCode(t, bd, dir, "delete", issue.ID, "--if-revision", revStr(rev0), "--force")
	if code != ExitGuardMismatch {
		t.Errorf("preflight-gone --if-revision delete exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
	}
	if !strings.Contains(out, "precondition failed") {
		t.Errorf("preflight-gone delete should say \"precondition failed\", got:\n%s", out)
	}
	if strings.Contains(out, "not found") {
		t.Errorf("preflight-gone delete leaked the raw, unclassified \"not found\" error instead of the guard envelope:\n%s", out)
	}
}

// TestIfRevisionCascadeDelete pins mc-zndi7.76 (gap 4): a single named id with
// --cascade takes the SAME deleteBatch path a multi-id delete does
// (cmd/bd/delete.go:105, "len(issueIDs) > 1 || cascade"), which is the only
// way a single-id --if-revision request ever reaches deleteBatch's own
// ExpectedVersion wiring (requireSingleIfRevisionID refuses a guard beside
// more than one explicit id, so a literal multi-id request can never get
// here). A stale guard must refuse the WHOLE cascade — the dependent survives
// right alongside the named parent — on both the real run and the unconfirmed
// --dry-run preview, which reports the same conditional-write envelope rather
// than falling through to the generic preview-with-error path.
func TestIfRevisionCascadeDelete(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cd")

	parent := bdCreate(t, bd, dir, "Cascade parent", "--type", "task")
	child := bdCreate(t, bd, dir, "Cascade child", "--type", "task")
	bdDepAdd(t, bd, dir, child.ID, parent.ID)

	rev0 := bdShowRevision(t, bd, dir, parent.ID)
	bdUpdate(t, bd, dir, parent.ID, "--notes", "bump")
	rev1 := bdShowRevision(t, bd, dir, parent.ID)

	// A stale guard refuses the preview too: the dedicated conditional-write
	// envelope, not the generic "here is what cascade would delete" preview.
	out, code := bdRunFailCode(t, bd, dir, "delete", parent.ID, "--cascade", "--dry-run", "--if-revision", revStr(rev0))
	if code != ExitGuardMismatch {
		t.Errorf("stale --if-revision cascade dry-run exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
	}
	if !strings.Contains(out, "revision mismatch") {
		t.Errorf("cascade dry-run mismatch error should say \"revision mismatch\", got:\n%s", out)
	}
	// MO: a stale guard reports the dedicated conditional-write envelope
	// instead of falling through to the generic "here is what cascade would
	// delete" preview render -- the two are mutually exclusive outcomes for
	// the same failed dry-run, not a report-then-preview sequence.
	if strings.Contains(out, "DELETE PREVIEW") {
		t.Errorf("stale --if-revision dry-run rendered the generic deletion preview instead of the guard-mismatch envelope:\n%s", out)
	}
	bdShow(t, bd, dir, parent.ID)
	bdShow(t, bd, dir, child.ID)

	// A stale guard refuses the real cascade entirely -- neither the named
	// parent nor the dependent cascade pulled in alongside it is deleted.
	out, code = bdRunFailCode(t, bd, dir, "delete", parent.ID, "--cascade", "--force", "--if-revision", revStr(rev0))
	if code != ExitGuardMismatch {
		t.Errorf("stale --if-revision cascade delete exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
	}
	if !strings.Contains(out, "revision mismatch") {
		t.Errorf("cascade delete mismatch error should say \"revision mismatch\", got:\n%s", out)
	}
	bdShow(t, bd, dir, parent.ID)
	bdShow(t, bd, dir, child.ID)

	// The matching token applies the cascade: both rows are gone.
	bdRunOK(t, bd, dir, "delete", parent.ID, "--cascade", "--force", "--if-revision", revStr(rev1))
	bdShowFail(t, bd, dir, parent.ID)
	bdShowFail(t, bd, dir, child.ID)
}

// TestIfRevisionComposesWithIfAssigneeIfStatus pins T4.5: on `bd update`,
// --if-revision composes with --if-assignee/--if-status as a conjunction —
// every guard present must hold, not just one of them, regardless of which
// guard happens to be the stale one.
func TestIfRevisionComposesWithIfAssigneeIfStatus(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cp")

	issue := bdCreate(t, bd, dir, "Compose guards", "--type", "task")
	bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice")
	rev := bdShowRevision(t, bd, dir, issue.ID)

	t.Run("revision_and_assignee_match_but_status_does_not_still_refuses", func(t *testing.T) {
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID,
			"--if-revision", revStr(rev), "--if-assignee", "alice", "--if-status", "in_progress",
			"--priority", "3")
		if code != ExitGuardMismatch {
			t.Errorf("exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Priority == 3 {
			t.Errorf("composed guard with one stale member still applied the write")
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev {
			t.Errorf("composed guard refusal advanced the revision: %d, want unchanged %d", got, rev)
		}
	})

	t.Run("assignee_and_status_match_but_revision_does_not_still_refuses", func(t *testing.T) {
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID,
			"--if-revision", revStr(rev+1_000_000), "--if-assignee", "alice", "--if-status", "open",
			"--priority", "3")
		if code != ExitGuardMismatch {
			t.Errorf("exit code = %d, want %d\n%s", code, ExitGuardMismatch, out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Priority == 3 {
			t.Errorf("composed guard with a stale revision still applied the write")
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev {
			t.Errorf("composed guard refusal advanced the revision: %d, want unchanged %d", got, rev)
		}
	})

	t.Run("all_three_guards_hold_applies", func(t *testing.T) {
		bdUpdate(t, bd, dir, issue.ID,
			"--if-revision", revStr(rev), "--if-assignee", "alice", "--if-status", "open",
			"--priority", "3")
		if got := bdShow(t, bd, dir, issue.ID); got.Priority != 3 {
			t.Errorf("fully-matching composed guard did not apply: priority = %d, want 3", got.Priority)
		}
	})
}

// TestIfRevisionRejectsLabelAndParentEdits pins T4.6: `bd update --if-revision`
// (alone or composed with --if-assignee/--if-status) refuses when the ONLY
// edit riding with it is a label or parent change, because those edits do not
// run inside the guarded issues-row write. Refused before any write, exit 1
// (a flag-validation refusal, never 13 — 13 is reserved for an actual stale
// guard).
func TestIfRevisionRejectsLabelAndParentEdits(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "lp")

	issue := bdCreate(t, bd, dir, "Label/parent guard", "--type", "task")
	rev := bdShowRevision(t, bd, dir, issue.ID)

	t.Run("label_only_edit_rejected", func(t *testing.T) {
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-revision", revStr(rev), "--add-label", "x")
		if code != 1 {
			t.Errorf("exit code = %d, want 1 (flag validation, not a guard mismatch)\n%s", code, out)
		}
		if !strings.Contains(out, "field update") {
			t.Errorf("expected the field-update requirement in the error, got:\n%s", out)
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev {
			t.Errorf("rejected label-only edit still wrote: revision = %d, want unchanged %d", got, rev)
		}
	})

	t.Run("parent_only_edit_rejected", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Would-be parent", "--type", "task")
		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--if-revision", revStr(rev), "--parent", parent.ID)
		if code != 1 {
			t.Errorf("exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "field update") {
			t.Errorf("expected the field-update requirement in the error, got:\n%s", out)
		}
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev {
			t.Errorf("rejected parent-only edit still wrote: revision = %d, want unchanged %d", got, rev)
		}
	})
}

// TestIfRevisionMultiIDRejected pins T4.8 on the three verbs that accept more
// than one id (assign is cobra.ExactArgs(2): id + assignee, so it can never
// carry a second id). A single --if-revision token names one row's version;
// refused before any write rather than applying it to only the first id a
// batch happens to resolve.
func TestIfRevisionMultiIDRejected(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mi")

	t.Run("update", func(t *testing.T) {
		a := bdCreate(t, bd, dir, "Multi A", "--type", "task")
		b := bdCreate(t, bd, dir, "Multi B", "--type", "task")
		revA, revB := bdShowRevision(t, bd, dir, a.ID), bdShowRevision(t, bd, dir, b.ID)

		out, code := bdUpdateFailCode(t, bd, dir, a.ID, b.ID, "--if-revision", revStr(revA), "--priority", "3")
		if code != 1 {
			t.Errorf("multi-id --if-revision update exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "one issue only") {
			t.Errorf("expected the one-id rule named in the error, got:\n%s", out)
		}
		if got := bdShowRevision(t, bd, dir, a.ID); got != revA {
			t.Errorf("rejected multi-id update still wrote to %s", a.ID)
		}
		if got := bdShowRevision(t, bd, dir, b.ID); got != revB {
			t.Errorf("rejected multi-id update still wrote to %s", b.ID)
		}
	})

	t.Run("close", func(t *testing.T) {
		a := bdCreate(t, bd, dir, "Multi close A", "--type", "task")
		b := bdCreate(t, bd, dir, "Multi close B", "--type", "task")
		rev := bdShowRevision(t, bd, dir, a.ID)

		out, code := bdRunFailCode(t, bd, dir, "close", a.ID, b.ID, "--if-revision", revStr(rev))
		if code != 1 {
			t.Errorf("multi-id --if-revision close exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "one issue only") {
			t.Errorf("expected the one-id rule named in the error, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, a.ID); got.Status == types.StatusClosed {
			t.Errorf("rejected multi-id close still closed %s", a.ID)
		}
		if got := bdShow(t, bd, dir, b.ID); got.Status == types.StatusClosed {
			t.Errorf("rejected multi-id close still closed %s", b.ID)
		}
	})

	t.Run("delete", func(t *testing.T) {
		a := bdCreate(t, bd, dir, "Multi delete A", "--type", "task")
		b := bdCreate(t, bd, dir, "Multi delete B", "--type", "task")
		rev := bdShowRevision(t, bd, dir, a.ID)

		out, code := bdRunFailCode(t, bd, dir, "delete", a.ID, b.ID, "--if-revision", revStr(rev), "--force")
		if code != 1 {
			t.Errorf("multi-id --if-revision delete exit code = %d, want 1\n%s", code, out)
		}
		if !strings.Contains(out, "one issue only") {
			t.Errorf("expected the one-id rule named in the error, got:\n%s", out)
		}
		bdShow(t, bd, dir, a.ID)
		bdShow(t, bd, dir, b.ID)
	})
}

// TestRevisionRemintMatrix pins T4.10: the revision is a token over the
// issues-row write, not over "anything happened to this id". A field update,
// a metadata edit, a close and a reopen must each mint a fresh revision (so a
// caller who read one of those as "no-op" cannot keep replaying a now-stale
// --if-revision); a label edit, a dependency edit and the derived is_blocked
// flag must NOT, because none of those touch the issues-row write the token
// guards.
func TestRevisionRemintMatrix(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rm")

	t.Run("field_update_changes_revision", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Field update", "--type", "task")
		rev := bdShowRevision(t, bd, dir, issue.ID)
		bdUpdate(t, bd, dir, issue.ID, "--priority", "1")
		if got := bdShowRevision(t, bd, dir, issue.ID); got == rev {
			t.Errorf("field update did not change the revision")
		}
	})

	t.Run("metadata_edit_changes_revision", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Metadata edit", "--type", "task")
		rev := bdShowRevision(t, bd, dir, issue.ID)
		bdUpdate(t, bd, dir, issue.ID, "--set-metadata", "k=v")
		if got := bdShowRevision(t, bd, dir, issue.ID); got == rev {
			t.Errorf("metadata edit did not change the revision")
		}
	})

	t.Run("close_changes_revision", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Close remint", "--type", "task")
		rev := bdShowRevision(t, bd, dir, issue.ID)
		bdClose(t, bd, dir, issue.ID)
		if got := bdShowRevision(t, bd, dir, issue.ID); got == rev {
			t.Errorf("close did not change the revision")
		}
	})

	t.Run("reopen_changes_revision", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reopen remint", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		rev := bdShowRevision(t, bd, dir, issue.ID)
		bdReopen(t, bd, dir, issue.ID)
		if got := bdShowRevision(t, bd, dir, issue.ID); got == rev {
			t.Errorf("reopen did not change the revision")
		}
	})

	t.Run("label_edit_does_not_change_revision", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Label no-remint", "--type", "task")
		rev := bdShowRevision(t, bd, dir, issue.ID)
		bdLabel(t, bd, dir, "add", issue.ID, "x")
		if got := bdShowRevision(t, bd, dir, issue.ID); got != rev {
			t.Errorf("label edit changed the revision: %d, want unchanged %d", got, rev)
		}
	})

	t.Run("dependency_and_is_blocked_do_not_change_revision", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked", "--type", "task")
		rev := bdShowRevision(t, bd, dir, blocked.ID)

		bdDep(t, bd, dir, "add", blocked.ID, blocker.ID)
		if got := bdShowRevision(t, bd, dir, blocked.ID); got != rev {
			t.Errorf("adding a dependency changed the dependent's revision: %d, want unchanged %d", got, rev)
		}
		// "bd blocked" is the derived is_blocked view (blocked_by/blocked_by_count
		// are computed over the dependency graph): confirms the dependency
		// actually produced the blocking relationship this subtest is about,
		// independent of whichever column or query answers it.
		if entries := bdBlockedJSON(t, bd, dir); !blockedJSONContains(entries, blocked.ID) {
			t.Fatalf("expected %s to appear in \"bd blocked\" once it depends on an open issue: %v", blocked.ID, entries)
		}
		if gotRev := bdShowRevision(t, bd, dir, blocked.ID); gotRev != rev {
			t.Errorf("becoming blocked changed the revision: %d, want unchanged %d", gotRev, rev)
		}

		bdClose(t, bd, dir, blocker.ID)
		if entries := bdBlockedJSON(t, bd, dir); blockedJSONContains(entries, blocked.ID) {
			t.Fatalf("expected %s to drop out of \"bd blocked\" once its dependency closed: %v", blocked.ID, entries)
		}
		if gotRev := bdShowRevision(t, bd, dir, blocked.ID); gotRev != rev {
			t.Errorf("becoming unblocked changed the dependent's revision: %d, want unchanged %d", gotRev, rev)
		}
	})
}

// blockedJSONContains reports whether id appears among bdBlockedJSON's
// entries (each a marshaled types.BlockedIssue, so "id" is Issue.ID's own
// json tag, not BlockedIssue's).
func blockedJSONContains(entries []map[string]interface{}, id string) bool {
	for _, e := range entries {
		if got, _ := e["id"].(string); got == id {
			return true
		}
	}
	return false
}
