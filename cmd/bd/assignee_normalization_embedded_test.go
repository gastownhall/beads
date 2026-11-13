//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdBatchOK runs `bd batch` with the given script on stdin, expecting success.
func bdBatchOK(t *testing.T, bd, dir, script string) string {
	t.Helper()
	cmd := exec.Command(bd, "batch")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd batch %q failed: %v\n%s", script, err, out)
	}
	return string(out)
}

// TestEmbeddedAssigneeHiddenEmptyClears pins bd-wkcfuz (PR #8203): the
// hidden-empty spellings of an assignee — a bare "-" and whitespace-only text
// — take the same clear path `--assignee ""` already takes on every CLI
// update ingest (`bd update --assignee`, `bd assign <id> <who>`, and the
// `bd batch` update KV). Storing those literals verbatim gave "unassigned"
// three on-disk spellings, and every emptiness check (claim, ready
// --unassigned, the transfer fence) classified them as ASSIGNED, so a bead
// believed back in the pool stayed hooked to a dead lane. Every other value,
// a spaced dash included, passes through verbatim, and --if-assignee keeps
// its literal match.
func TestEmbeddedAssigneeHiddenEmptyClears(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ahe")

	assigned := func(t *testing.T, title string) *types.Issue {
		t.Helper()
		return bdCreate(t, bd, dir, title, "--type", "task", "--assignee", "holder")
	}

	t.Run("AC1_update_clears_like_explicit_empty", func(t *testing.T) {
		for _, form := range [][]string{
			{"--assignee="},
			{"--assignee", ""},
			{"--assignee", "-"},
			{"--assignee", " "},
			{"--assignee", "\t"},
			{"-a", "-"},
		} {
			issue := assigned(t, "Update clear")
			bdUpdate(t, bd, dir, append([]string{issue.ID, "--actor", "dispatcher"}, form...)...)
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
				t.Errorf("update %q on an assigned open issue: assignee=%q, want cleared", form, got.Assignee)
				continue
			}
			if !readyJSONIDs(t, bd, dir, "--unassigned", "--limit", "0")[issue.ID] {
				t.Errorf("update %q: %s missing from ready --unassigned", form, issue.ID)
			}
			bdUpdate(t, bd, dir, issue.ID, "--claim", "--actor", "newcomer")
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "newcomer" || got.Status != types.StatusInProgress {
				t.Errorf("update %q then --claim by another actor: assignee=%q status=%s, want newcomer/in_progress", form, got.Assignee, got.Status)
			}
		}
	})

	t.Run("AC2_assign_positional_clears", func(t *testing.T) {
		for _, value := range []string{"", "-", " ", "\t"} {
			issue := assigned(t, "Assign clear")
			out := bdRunOK(t, bd, dir, "assign", issue.ID, value, "--actor", "dispatcher")
			if !strings.Contains(out, "Unassigned") {
				t.Errorf("assign %q: want the Unassigned confirmation, got:\n%s", value, out)
			}
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
				t.Errorf("assign %q: assignee=%q, want cleared", value, got.Assignee)
			}
		}
	})

	t.Run("AC3_batch_update_clears_like_assignee_eq", func(t *testing.T) {
		for _, kv := range []string{"assignee=", "assignee=-", `assignee="-"`, `assignee=" "`} {
			issue := assigned(t, "Batch clear")
			bdBatchOK(t, bd, dir, "update "+issue.ID+" "+kv+"\n")
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
				t.Errorf("batch update %s: assignee=%q, want cleared", kv, got.Assignee)
			}
		}
	})
}
