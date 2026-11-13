//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdProxiedBatchOK runs `bd batch -f <script>` on the proxied-server route,
// expecting success.
func bdProxiedBatchOK(t *testing.T, bd, dir, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "batch.txt")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write batch script: %v", err)
	}
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, "batch", "-f", path)
	if err != nil {
		t.Fatalf("bd batch %q failed: %v\nstdout:\n%s\nstderr:\n%s", script, err, stdout, stderr)
	}
}

// TestProxiedServerAssigneeHiddenEmptyClears is the proxied-server half of
// bd-wkcfuz: the proxied update gather, the proxied assign handler, and the
// proxied batch route each read the assignee themselves, so each must fold a
// bare "-" and whitespace-only text into the `--assignee ""` clear, keep every
// other value verbatim, and leave the live-claim fence and --if-assignee
// literal.
func TestProxiedServerAssigneeHiddenEmptyClears(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	assigned := func(t *testing.T, p proxiedProject, title string) *types.Issue {
		t.Helper()
		return bdProxiedCreate(t, bd, p.dir, title, "--assignee", "holder")
	}
	claimAs := func(t *testing.T, p proxiedProject, holder string) *types.Issue {
		t.Helper()
		issue := bdProxiedCreate(t, bd, p.dir, "Held via proxy")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", holder, "--assignee", holder, "--status", "in_progress")
		return issue
	}

	t.Run("AC1_update_clears_like_explicit_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hpa")
		for _, value := range []string{"", "-", " ", "\t"} {
			issue := assigned(t, p, "Update clear via proxy")
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "dispatcher", "--assignee", value)
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "" {
				t.Errorf("update --assignee %q: assignee=%q, want cleared", value, got.Assignee)
				continue
			}
			found := false
			for _, r := range bdProxiedReadyJSON(t, bd, p, "--unassigned", "--limit", "0") {
				found = found || r.ID == issue.ID
			}
			if !found {
				t.Errorf("update --assignee %q: %s missing from ready --unassigned", value, issue.ID)
			}
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--claim", "--actor", "newcomer")
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "newcomer" || got.Status != types.StatusInProgress {
				t.Errorf("update --assignee %q then --claim by another actor: assignee=%q status=%s, want newcomer/in_progress", value, got.Assignee, got.Status)
			}
		}
	})

	t.Run("AC2_assign_positional_clears", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hpb")
		for _, value := range []string{"", "-", " "} {
			issue := assigned(t, p, "Assign clear via proxy")
			out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, value, "--actor", "dispatcher")
			if err != nil {
				t.Fatalf("assign %q failed: %v\n%s", value, err, out)
			}
			if !strings.Contains(string(out), "Unassigned") {
				t.Errorf("assign %q: want the Unassigned confirmation, got:\n%s", value, out)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "" {
				t.Errorf("assign %q: assignee=%q, want cleared", value, got.Assignee)
			}
		}
	})

	t.Run("AC3_batch_update_clears_like_assignee_eq", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hpc")
		for _, kv := range []string{"assignee=", "assignee=-", `assignee=" "`} {
			issue := assigned(t, p, "Batch clear via proxy")
			bdProxiedBatchOK(t, bd, p.dir, "update "+issue.ID+" "+kv+"\n")
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "" {
				t.Errorf("batch update %s: assignee=%q, want cleared", kv, got.Assignee)
			}
		}
	})

	t.Run("AC4_live_claim_fence_holds_then_force_clears", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "hpd")
		baseline := claimAs(t, p, "holder")
		bdProxiedUpdateOne(t, bd, p.dir, baseline.ID, "--actor", "thief", "--assignee", "", "--force")
		want := bdProxiedShow(t, bd, p.dir, baseline.ID)
		if want.Assignee != "" {
			t.Fatalf("explicit-empty --force baseline: assignee=%q, want cleared", want.Assignee)
		}

		for _, value := range []string{"-", " "} {
			issue := claimAs(t, p, "holder")
			if out, code := bdProxiedUpdateFailCode(t, bd, p.dir, issue.ID, "--actor", "thief", "--assignee", value); code != 1 || !strings.Contains(out, "holder") {
				t.Errorf("update --assignee %q over a live claim: exit=%d, want 1 naming the holder\n%s", value, code, out)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "holder" || got.Status != types.StatusInProgress {
				t.Errorf("refused update --assignee %q clobbered the claim: assignee=%q status=%s", value, got.Assignee, got.Status)
			}
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "thief", "--assignee", value, "--force")
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != want.Assignee || got.Status != want.Status {
				t.Errorf("update --assignee %q --force: assignee=%q status=%s, want %q/%s like --assignee ''", value, got.Assignee, got.Status, want.Assignee, want.Status)
			}

			issue = claimAs(t, p, "holder")
			if out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, value, "--actor", "thief"); err == nil || !strings.Contains(string(out), "holder") {
				t.Errorf("assign %q over a live claim: err=%v, want a refusal naming the holder\n%s", value, err, out)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "holder" {
				t.Errorf("refused assign %q clobbered the claim: assignee=%q", value, got.Assignee)
			}
			if out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, value, "--actor", "thief", "--force"); err != nil {
				t.Fatalf("assign %q --force failed: %v\n%s", value, err, out)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != want.Assignee || got.Status != want.Status {
				t.Errorf("assign %q --force: assignee=%q status=%s, want %q/%s like --assignee ''", value, got.Assignee, got.Status, want.Assignee, want.Status)
			}
		}
	})
}
