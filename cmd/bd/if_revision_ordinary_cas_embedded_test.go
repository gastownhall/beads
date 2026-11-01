//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// ifRevisionStaleExit is a literal on purpose: the contract with gascity's
// bdstore_conditional.go classifier is the number 13, not whatever name the
// constant for it (ExitGuardMismatch) happens to have.
const ifRevisionStaleExit = 13

// decodeIfRevisionBodyExact decodes the flat machine body a refused
// --if-revision write puts on the last line of stderr, keeping numbers as
// json.Number. Revisions are int64 values well past 2^53, so comparing them
// as float64 would accept a neighbouring revision; a json.Number also proves
// the field is a JSON number and not a string.
func decodeIfRevisionBodyExact(t *testing.T, line string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("decode the --if-revision refusal body: %v\nline: %s", err, line)
	}
	return body
}

// TestEmbeddedIfRevisionOrdinaryCAS pins the ordinary --if-revision
// compare-and-swap (#7203) end to end, as a gascity-style caller sees it:
//
//   - a stale decimal token on update, close, assign and delete (preview and
//     --force) exits 13 with a precondition_failed body on the last line of
//     stderr naming the id and both revisions as JSON integers, prints
//     nothing on stdout and writes nothing;
//   - a token that is not a decimal exits 1 with the "invalid --if-revision"
//     refusal, never 13, and writes nothing;
//   - the current token applies on update, close, assign and delete, and
//     each write that leaves the issue in place moves its revision on.
//
// A second meaning for the same flag (an opaque token in some other kind of
// workspace) must leave all of this unchanged in an ordinary one. This is
// the ordinary-workspace half of the fork's
// TestGraphPreviewIfRevisionOutsideLinkModeIsUpstreamCAS, ported onto this
// package's embedded helpers and extended from update/delete to close and
// assign.
func TestEmbeddedIfRevisionOrdinaryCAS(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ifrev")

	id := bdCreateSilent(t, bd, dir, "Stale guard", "--type", "task", "--priority", "2")
	stale := bdShowRevision(t, bd, dir, id)
	bdRunOK(t, bd, dir, "update", id, "--notes", "bump", "--json")
	current := bdShowRevision(t, bd, dir, id)
	if current == stale {
		t.Fatal("an unguarded update did not advance the revision")
	}
	staleToken := strconv.FormatInt(stale, 10)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"update", []string{"update", id, "--priority", "3"}},
		{"close", []string{"close", id}},
		{"assign", []string{"assign", id, "worker"}},
		{"delete preview", []string{"delete", id}},
		{"delete force", []string{"delete", id, "--force"}},
	} {
		args := slices.Concat(tc.args, []string{"--if-revision", staleToken, "--json"})
		stdout, stderr, exit := bdRunStreams(t, bd, dir, args...)
		if exit != ifRevisionStaleExit {
			t.Fatalf("stale --if-revision %s exited %d, want %d\nstdout:\n%s\nstderr:\n%s", tc.name, exit, ifRevisionStaleExit, stdout, stderr)
		}
		if stdout != "" {
			t.Errorf("stale --if-revision %s printed on stdout, want nothing:\n%s", tc.name, stdout)
		}
		body := decodeIfRevisionBodyExact(t, lastJSONLine(t, stderr))
		if body["code"] != ifRevisionCodePreconditionFailed || body["id"] != id {
			t.Errorf("stale --if-revision %s reported %v, want code %q for %s", tc.name, body, ifRevisionCodePreconditionFailed, id)
		}
		if got := body["expected_revision"]; got != json.Number(staleToken) {
			t.Errorf("stale --if-revision %s reported expected_revision %#v, want the JSON integer %s", tc.name, got, staleToken)
		}
		if got := body["current_revision"]; got != json.Number(strconv.FormatInt(current, 10)) {
			t.Errorf("stale --if-revision %s reported current_revision %#v, want the JSON integer %d", tc.name, got, current)
		}
		if msg, _ := body["error"].(string); !strings.Contains(msg, "precondition failed") {
			t.Errorf("stale --if-revision %s error text %q lacks \"precondition failed\"", tc.name, msg)
		}
		if got := bdShowRevision(t, bd, dir, id); got != current {
			t.Fatalf("stale --if-revision %s wrote: revision %d -> %d", tc.name, current, got)
		}
	}

	// A token that is not a decimal is a typo refused before any write, with
	// exit 1 and not the guard's 13, in both output modes.
	for _, base := range [][]string{
		{"update", id, "--priority", "3"},
		{"close", id},
		{"assign", id, "worker"},
		{"delete", id},
		{"delete", id, "--force"},
	} {
		for _, mode := range [][]string{nil, {"--json"}} {
			args := slices.Concat(base, []string{"--if-revision", "observed"}, mode)
			stdout, stderr, exit := bdRunStreams(t, bd, dir, args...)
			out := stdout + stderr
			// The text refusal quotes the token; the --json one carries the
			// same text JSON-escaped.
			refused := strings.Contains(out, `invalid --if-revision "observed"`) ||
				strings.Contains(out, `invalid --if-revision \"observed\"`)
			if exit != 1 || !refused {
				t.Errorf("bd %s exited %d, want exit 1 refusing the non-decimal token\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), exit, stdout, stderr)
			}
			if strings.Contains(out, ifRevisionCodePreconditionFailed) {
				t.Errorf("bd %s reported a stale revision for a token that is not one:\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), stdout, stderr)
			}
		}
	}
	if got := bdShowRevision(t, bd, dir, id); got != current {
		t.Fatalf("a refused non-decimal token wrote: revision %d -> %d", current, got)
	}

	// The current token applies, on each verb in turn.
	revision := current
	guarded := func(target string, args ...string) {
		t.Helper()
		bdRunOK(t, bd, dir, slices.Concat(args, []string{"--if-revision", strconv.FormatInt(revision, 10), "--json"})...)
		next := bdShowRevision(t, bd, dir, target)
		if next == revision {
			t.Fatalf("a matching --if-revision on bd %s did not advance the revision", strings.Join(args, " "))
		}
		revision = next
	}
	guarded(id, "update", id, "--priority", "3")
	if got := bdShow(t, bd, dir, id); got.Priority != 3 {
		t.Errorf("matching --if-revision update did not apply: priority = %d, want 3", got.Priority)
	}
	guarded(id, "close", id)
	if got := bdShow(t, bd, dir, id); got.Status != types.StatusClosed {
		t.Errorf("matching --if-revision close did not apply: status = %s, want closed", got.Status)
	}
	bdRunOK(t, bd, dir, "delete", id, "--force", "--if-revision", strconv.FormatInt(revision, 10), "--json")
	bdShowFail(t, bd, dir, id)

	// assign gets an issue of its own: closing an issue assigned to someone
	// other than the actor is refused without --force, whatever the guard.
	other := bdCreateSilent(t, bd, dir, "Guarded assign", "--type", "task")
	revision = bdShowRevision(t, bd, dir, other)
	guarded(other, "assign", other, "worker")
	if got := bdShow(t, bd, dir, other); got.Assignee != "worker" {
		t.Errorf("matching --if-revision assign did not apply: assignee = %q, want worker", got.Assignee)
	}
}
