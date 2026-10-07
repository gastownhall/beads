//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdProxiedShowRevision is bdShowRevision's proxied-server twin: it runs
// "bd show <id> --json" against the shared proxied server and returns the
// issue's current revision as an int64, decoded the same way --if-revision
// expects a caller to supply it (types.ParseRevisionToken on the wire
// "revision" string). See bdShowRevision's doc for why this parses the raw
// JSON rather than unmarshaling into types.Issue (whose RowVersion is
// json:"-").
func bdProxiedShowRevision(t *testing.T, bd, dir, id string) int64 {
	t.Helper()
	s := bdProxiedShowRaw(t, bd, dir, id, "--json")
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

func proxiedRevStr(rev int64) string { return strconv.FormatInt(rev, 10) }

// TestProxiedIfRevisionOutranksReassignFence pins mc-zndi7.74 on the
// proxied-server route, the topology where this race actually happens: every
// shared-dolt-server clone writes through it. See
// TestIfRevisionOutranksReassignFenceCLI's doc for the full race shape —
// this is the same scenario, proved against the real shared Dolt server
// instead of embedded Dolt.
func TestProxiedIfRevisionOutranksReassignFence(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("update_minus_a", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ivp")
		issue := bdProxiedCreate(t, bd, p.dir, "Guarded proxied update vs fence")
		rev0 := bdProxiedShowRevision(t, bd, p.dir, issue.ID)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")

		out, code := bdProxiedUpdateFailCode(t, bd, p.dir, issue.ID, "--actor", "thief", "--assignee", "thief", "--if-revision", proxiedRevStr(rev0))
		if code != ExitGuardMismatch {
			t.Errorf("lost race exit code = %d, want %d (precondition_failed, not the live-claim refusal)\n%s", code, ExitGuardMismatch, out)
		}
		if !strings.Contains(out, "revision mismatch") {
			t.Errorf("expected the --if-revision mismatch copy, got:\n%s", out)
		}
		if strings.Contains(out, "holder") {
			t.Errorf("lost race must not fall through to the live-claim refusal naming the holder, got:\n%s", out)
		}
		if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "holder" {
			t.Errorf("lost race must not have applied: assignee=%q, want holder", got.Assignee)
		}
	})

	t.Run("assign", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ivq")
		issue := bdProxiedCreate(t, bd, p.dir, "Guarded proxied assign vs fence")
		rev0 := bdProxiedShowRevision(t, bd, p.dir, issue.ID)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")

		out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, "thief", "--actor", "thief", "--if-revision", proxiedRevStr(rev0))
		if err == nil {
			t.Fatalf("lost race should have failed, got:\n%s", out)
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("lost race failed without an exit code: %v\n%s", err, out)
		}
		if ee.ExitCode() != ExitGuardMismatch {
			t.Errorf("lost race exit code = %d, want %d (precondition_failed, not the live-claim refusal)\n%s", ee.ExitCode(), ExitGuardMismatch, out)
		}
		if !strings.Contains(string(out), "revision mismatch") {
			t.Errorf("expected the --if-revision mismatch copy, got:\n%s", out)
		}
		if strings.Contains(string(out), "holder") {
			t.Errorf("lost race must not fall through to the live-claim refusal naming the holder, got:\n%s", out)
		}
		if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "holder" {
			t.Errorf("lost race must not have applied: assignee=%q, want holder", got.Assignee)
		}
	})
}
