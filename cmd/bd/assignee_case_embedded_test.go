//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedAssigneeCaseInsensitiveCLI replays GH#8628 end to end: letter
// case is not part of an identity, so an issue assigned "hierophant" belongs
// to actor "Hierophant" at every ownership gate, without --force. A different
// name stays a stranger: the fold covers letter case and nothing else.
func TestEmbeddedAssigneeCaseInsensitiveCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ac")

	// AC-1: the issue's script. bd close by the same actor under another
	// letter case exits 0; a different name is still refused.
	t.Run("close_by_case_variant_actor", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "x", "--assignee", "hierophant")

		out := bdCloseFail(t, bd, dir, issue.ID, "--actor", "hierophant2")
		if !strings.Contains(out, `assignee is "hierophant"`) || !strings.Contains(out, "--force") {
			t.Errorf("close by hierophant2 should be the not-assignee refusal, got:\n%s", out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Status == types.StatusClosed {
			t.Fatalf("refused close by hierophant2 closed the issue")
		}

		bdClose(t, bd, dir, issue.ID, "--actor", "Hierophant")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("status = %s, want closed", got.Status)
		}
		if got.Assignee != "hierophant" {
			t.Errorf("assignee = %q, want the stored spelling hierophant kept", got.Assignee)
		}
	})

	// AC-2: on an issue in_progress and held by "hierophant", actor
	// "Hierophant" re-claims (an idempotent success) and then unclaims,
	// both without --force.
	t.Run("claim_and_unclaim_by_case_variant_actor", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "held", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--actor", "hierophant", "--claim")
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "hierophant" || got.Status != types.StatusInProgress {
			t.Fatalf("setup claim: assignee=%q status=%s, want hierophant/in_progress", got.Assignee, got.Status)
		}

		bdUpdate(t, bd, dir, issue.ID, "--actor", "Hierophant", "--claim")
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "hierophant" || got.Status != types.StatusInProgress {
			t.Errorf("re-claim by Hierophant: assignee=%q status=%s, want the idempotent hierophant/in_progress", got.Assignee, got.Status)
		}

		bdRunOK(t, bd, dir, "unclaim", issue.ID, "--actor", "Hierophant")
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" || got.Status != types.StatusOpen {
			t.Errorf("unclaim by Hierophant: assignee=%q status=%s, want released (empty/open)", got.Assignee, got.Status)
		}
	})

	// AC-3: on an issue in_progress and held by "hierophant", actor
	// "Hierophant" re-spells the assignee without --force and the stored
	// spelling becomes "Hierophant"; a stranger moving the claim to a third
	// name is still refused.
	t.Run("reassign_by_case_variant_actor", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "held", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--actor", "hierophant", "--assignee", "hierophant", "--status", "in_progress")

		out, code := bdUpdateFailCode(t, bd, dir, issue.ID, "--actor", "bob", "--assignee", "carol")
		if code != 1 || !strings.Contains(out, "cannot reassign") || !strings.Contains(out, "held by") {
			t.Errorf("bob --assignee carol: exit %d, want 1 with the cannot reassign … held by refusal, got:\n%s", code, out)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "hierophant" {
			t.Fatalf("refused reassign clobbered the row: assignee=%q, want hierophant", got.Assignee)
		}

		bdUpdate(t, bd, dir, issue.ID, "--actor", "Hierophant", "--assignee", "Hierophant")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "Hierophant" || got.Status != types.StatusInProgress {
			t.Errorf("re-spell by Hierophant: assignee=%q status=%s, want Hierophant/in_progress", got.Assignee, got.Status)
		}
	})

	// AC-4: --assignee on list, count, search and ready ignores letter case:
	// every spelling of the filter returns both hierophant issues, never
	// bob's, and prints each stored spelling unchanged.
	t.Run("listings_fold_assignee_case", func(t *testing.T) {
		ldir, _, _ := bdInit(t, bd, "--prefix", "lc")
		lower := bdCreate(t, bd, ldir, "casefold lower", "--assignee", "hierophant")
		upper := bdCreate(t, bd, ldir, "casefold upper", "--assignee", "Hierophant")
		bdCreate(t, bd, ldir, "casefold other", "--assignee", "bob")
		want := map[string]string{lower.ID: "hierophant", upper.ID: "Hierophant"}

		check := func(t *testing.T, what string, rows []map[string]interface{}) {
			t.Helper()
			got := map[string]string{}
			for _, row := range rows {
				id, _ := row["id"].(string)
				assignee, _ := row["assignee"].(string)
				got[id] = assignee
			}
			if len(got) != len(want) {
				t.Errorf("%s returned %v, want exactly %v", what, got, want)
			}
			for id, spelling := range want {
				if got[id] != spelling {
					t.Errorf("%s: %s assignee = %q, want the stored spelling %q (all: %v)", what, id, got[id], spelling, got)
				}
			}
		}
		readyRows := func(t *testing.T, asked string) []map[string]interface{} {
			t.Helper()
			cmd := exec.Command(bd, "ready", "--json", "--assignee", asked)
			cmd.Dir = ldir
			cmd.Env = bdEnv(ldir)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			if err != nil {
				t.Fatalf("bd ready --assignee %s: %v\nstdout:\n%s\nstderr:\n%s", asked, err, stdout.String(), stderr.String())
			}
			out := strings.TrimSpace(stdout.String())
			var rows []map[string]interface{}
			if err := json.Unmarshal([]byte(out[strings.Index(out, "["):]), &rows); err != nil {
				t.Fatalf("parse ready JSON: %v\n%s", err, out)
			}
			return rows
		}

		for _, asked := range []string{"hierophant", "Hierophant", "HIEROPHANT"} {
			t.Run(asked, func(t *testing.T) {
				var listed []map[string]interface{}
				for _, l := range bdListJSON(t, bd, ldir, "--assignee", asked) {
					listed = append(listed, map[string]interface{}{"id": l.ID, "assignee": l.Assignee})
				}
				check(t, "bd list --assignee "+asked, listed)
				check(t, "bd search --assignee "+asked, bdSearchJSON(t, bd, ldir, "casefold", "--assignee", asked))
				check(t, "bd ready --assignee "+asked, readyRows(t, asked))
				if got := bdCountJSON(t, bd, ldir, "--assignee", asked)["count"]; got != float64(2) {
					t.Errorf("bd count --assignee %s = %v, want 2", asked, got)
				}
			})
		}
	})
}
