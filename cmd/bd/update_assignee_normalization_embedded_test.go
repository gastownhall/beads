//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdRunStdinEmbedded runs bd in dir with the given stdin and returns the
// combined output and exit code (a non-zero exit is an observation, not a
// harness failure).
func bdRunStdinEmbedded(t *testing.T, bd, dir, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("bd %s could not run: %v", strings.Join(args, " "), err)
	}
	return string(out), ee.ExitCode()
}

// hiddenEmptyAssignees are the spellings that must mean "unassigned" on the
// update paths: a bare "-" (the shell convention for an empty argument) and
// whitespace-only text. Everything else — including " - " and "--" — is a
// name and stays byte-for-byte (AC-5).
var hiddenEmptyAssignees = []string{"-", " ", "\t"}

// TestEmbeddedAssigneeHiddenEmptyClears pins bd-wkcfuz end-to-end on the
// embedded (direct) route: `bd update --assignee -` (and whitespace-only)
// must clear the assignee exactly like `--assignee ''`, across update,
// assign and batch, while the live-claim fence, the --if-assignee guard and
// every non-clear value keep their existing behavior.
func TestEmbeddedAssigneeHiddenEmptyClears(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "an")

	updateAssigneeJSON := func(t *testing.T, id string, args ...string) *types.Issue {
		t.Helper()
		out := bdUpdate(t, bd, dir, append([]string{"--json", id}, args...)...)
		var rows []*types.Issue
		if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
			t.Fatalf("parse update response for %s (%v): %s", id, err, out)
		}
		return rows[0]
	}

	// AC-1: `bd update <id> --assignee -` (and whitespace-only) clears like
	// `--assignee ''`: same stored value, same ready visibility, same
	// claimability by another actor.
	t.Run("update_hidden_empty_clears_like_explicit_empty", func(t *testing.T) {
		t.Parallel()
		for i, value := range hiddenEmptyAssignees {
			hidden := bdCreate(t, bd, dir, fmt.Sprintf("Hidden clear %d", i), "--assignee", "holder")
			baseline := bdCreate(t, bd, dir, fmt.Sprintf("Explicit clear %d", i), "--assignee", "holder")

			if got := updateAssigneeJSON(t, hidden.ID, "--assignee", value).Assignee; got != "" {
				t.Errorf("value %q: update stored assignee %q, want cleared (\"\")", value, got)
			}
			bdUpdate(t, bd, dir, baseline.ID, "--assignee", "")

			for name, id := range map[string]string{"hidden": hidden.ID, "explicit": baseline.ID} {
				if got := bdShow(t, bd, dir, id).Assignee; got != "" {
					t.Errorf("%s clear: persisted assignee = %q, want \"\"", name, got)
				}
				if !wakeReadyIDs(t, bd, dir)[id] {
					t.Errorf("%s clear: issue %s not in unassigned ready results", name, id)
				}
				bdUpdate(t, bd, dir, id, "--actor", "taker", "--claim")
				if got := bdShow(t, bd, dir, id).Assignee; got != "taker" {
					t.Errorf("%s clear: claim by another actor set assignee %q, want taker", name, got)
				}
			}
		}
	})

	// AC-2: `bd assign <id> -` (and whitespace-only) clears and prints the
	// existing Unassigned confirmation.
	t.Run("assign_hidden_empty_clears", func(t *testing.T) {
		t.Parallel()
		for i, value := range hiddenEmptyAssignees {
			issue := bdCreate(t, bd, dir, fmt.Sprintf("Assign clear %d", i), "--assignee", "holder")

			out := bdRunOK(t, bd, dir, "assign", issue.ID, value)
			if !strings.Contains(out, "Unassigned") {
				t.Errorf("value %q: assign output should confirm Unassigned, got:\n%s", value, out)
			}
			if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "" {
				t.Errorf("value %q: assign stored assignee %q, want cleared", value, got)
			}
		}
	})

	// AC-3: `bd batch` update with assignee=- (and the quoted whitespace
	// form) clears exactly like assignee= does. A bare trailing space or tab
	// cannot reach parseUpdateKVs through the batch tokenizer — it is
	// whitespace — so the quoted form is the CLI spelling of that half; the
	// unit test covers the raw tokens.
	t.Run("batch_hidden_empty_clears_like_empty_kv", func(t *testing.T) {
		t.Parallel()
		for i, token := range []string{`assignee=-`, `"assignee= "`} {
			hidden := bdCreate(t, bd, dir, fmt.Sprintf("Batch clear %d", i), "--assignee", "holder")
			baseline := bdCreate(t, bd, dir, fmt.Sprintf("Batch explicit %d", i), "--assignee", "holder")

			cases := []struct{ name, id, token string }{
				{"hidden", hidden.ID, token},
				{"explicit", baseline.ID, `assignee=`},
			}
			for _, c := range cases {
				out, code := bdRunStdinEmbedded(t, bd, dir, fmt.Sprintf("update %s %s\n", c.id, c.token), "batch")
				if code != 0 {
					t.Fatalf("%s %s: batch exited %d:\n%s", c.name, c.token, code, out)
				}
				if got := bdShow(t, bd, dir, c.id).Assignee; got != "" {
					t.Errorf("%s %s: batch stored assignee %q, want cleared", c.name, c.token, got)
				}
			}
		}
	})

	// AC-4: the bd-98s5c live-claim fence treats a hidden-empty clear exactly
	// like an explicit clear: refused without --force (claim preserved,
	// exit 1), applied with it.
	t.Run("live_claim_fence_on_hidden_empty_clear", func(t *testing.T) {
		t.Parallel()
		claimAs := func(t *testing.T, title string) *types.Issue {
			t.Helper()
			issue := bdCreate(t, bd, dir, title, "--type", "task")
			bdUpdate(t, bd, dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")
			return issue
		}

		t.Run("update_refused_then_force_clears", func(t *testing.T) {
			t.Parallel()
			issue := claimAs(t, "Fence update dash")
			out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--actor", "thief", "--assignee", "-")
			if code != 1 {
				t.Errorf("fence refusal exit code = %d, want 1:\n%s", code, out)
			}
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "holder" || got.Status != types.StatusInProgress {
				t.Errorf("refused hidden-empty clear clobbered the row: assignee=%q status=%s", got.Assignee, got.Status)
			}

			bdUpdate(t, bd, dir, issue.ID, "--actor", "thief", "--assignee", "-", "--force")
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
				t.Errorf("--force hidden-empty clear left assignee %q, want cleared", got.Assignee)
			}
		})

		t.Run("assign_refused_then_force_clears", func(t *testing.T) {
			t.Parallel()
			issue := claimAs(t, "Fence assign space")
			out, code := bdRunFailCode(t, bd, dir, "assign", issue.ID, " ", "--actor", "thief")
			if code != 1 {
				t.Errorf("assign fence exit code = %d, want 1:\n%s", code, out)
			}
			if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "holder" {
				t.Errorf("refused hidden-empty assign clobbered the row: assignee=%q", got)
			}

			bdRunOK(t, bd, dir, "assign", issue.ID, " ", "--actor", "thief", "--force")
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
				t.Errorf("--force hidden-empty assign left assignee %q, want cleared", got.Assignee)
			}
		})
	})

	// AC-5: everything outside the clear spellings is preserved verbatim,
	// the --if-assignee guard keeps its literal comparison, create keeps
	// storing raw values, and omitting the flag changes nothing.
	t.Run("non_clear_values_verbatim", func(t *testing.T) {
		t.Parallel()
		for _, value := range []string{"alice", " alice ", " - ", "--", "-x", "a-b"} {
			issue := bdCreate(t, bd, dir, "Verbatim update "+value)
			bdUpdate(t, bd, dir, issue.ID, "--assignee", value)
			if got := bdShow(t, bd, dir, issue.ID).Assignee; got != value {
				t.Errorf("update stored assignee %q, want verbatim %q", got, value)
			}
		}
		issue := bdCreate(t, bd, dir, "Verbatim assign")
		bdRunOK(t, bd, dir, "assign", issue.ID, "a-b")
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "a-b" {
			t.Errorf("assign stored %q, want verbatim a-b", got)
		}
		out, code := bdRunStdinEmbedded(t, bd, dir, fmt.Sprintf("update %s assignee=bob\n", issue.ID), "batch")
		if code != 0 {
			t.Fatalf("batch verbatim assignee exited %d:\n%s", code, out)
		}
		if got := bdShow(t, bd, dir, issue.ID).Assignee; got != "bob" {
			t.Errorf("batch stored %q, want verbatim bob", got)
		}
	})

	t.Run("if_assignee_guard_stays_literal", func(t *testing.T) {
		t.Parallel()
		// Seed a legacy literal "-" through the unchanged create path.
		legacy := bdCreate(t, bd, dir, "Legacy literal dash", "--assignee", "-")
		if got := bdShow(t, bd, dir, legacy.ID).Assignee; got != "-" {
			t.Fatalf("create should keep storing the raw dash, got %q", got)
		}
		// The guard compares literally: - matches the legacy row and permits
		// an explicit clear.
		bdUpdate(t, bd, dir, legacy.ID, "--assignee", "", "--if-assignee", "-")
		if got := bdShow(t, bd, dir, legacy.ID).Assignee; got != "" {
			t.Errorf("guard-matched explicit clear left assignee %q", got)
		}
		// The same guard on an unassigned row is a mismatch: exit 13, no
		// write.
		fresh := bdCreate(t, bd, dir, "Guard mismatch target")
		out, code := bdUpdateFailCode(t, bd, dir, fresh.ID, "--title", "Guarded", "--if-assignee", "-")
		if code != ExitGuardMismatch {
			t.Errorf("--if-assignee - on unassigned row exit code = %d, want %d:\n%s", code, ExitGuardMismatch, out)
		}
		if got := bdShow(t, bd, dir, fresh.ID).Title; got == "Guarded" {
			t.Errorf("guard-mismatched update wrote the title anyway")
		}
	})

	t.Run("omitted_and_claim_interactions", func(t *testing.T) {
		t.Parallel()
		// Omitting --assignee never clears.
		kept := bdCreate(t, bd, dir, "No assignee flag", "--assignee", "holder")
		bdUpdate(t, bd, dir, kept.ID, "--priority", "1")
		if got := bdShow(t, bd, dir, kept.ID).Assignee; got != "holder" {
			t.Errorf("update without --assignee changed assignee to %q", got)
		}
		// Repeated clearing is a successful no-op.
		bdUpdate(t, bd, dir, kept.ID, "--assignee", "")
		bdUpdate(t, bd, dir, kept.ID, "--assignee", "-")
		if got := bdShow(t, bd, dir, kept.ID).Assignee; got != "" {
			t.Errorf("repeated clear left assignee %q", got)
		}
		// --claim with a hidden-empty assignee behaves exactly like --claim
		// with an explicit empty one (parity, not a policy claim: the claim
		// postcondition permits a patch override, and the claim CAS itself
		// refuses an issue assigned to someone else, so these start
		// unassigned).
		hidden := bdCreate(t, bd, dir, "Claim parity hidden")
		baseline := bdCreate(t, bd, dir, "Claim parity explicit")
		bdUpdate(t, bd, dir, hidden.ID, "--actor", "bob", "--claim", "--assignee", "-")
		bdUpdate(t, bd, dir, baseline.ID, "--actor", "bob", "--claim", "--assignee", "")
		gotHidden := bdShow(t, bd, dir, hidden.ID).Assignee
		gotBaseline := bdShow(t, bd, dir, baseline.ID).Assignee
		if gotHidden != gotBaseline {
			t.Errorf("--claim --assignee -: assignee %q differs from explicit-empty baseline %q", gotHidden, gotBaseline)
		}
	})
}
