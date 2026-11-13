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
}
