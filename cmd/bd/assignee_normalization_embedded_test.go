//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedAssigneeInputNormalizationCLI pins the sys-iagxd9 contract: the
// hidden-empty spellings of an assignee — a bare '-' and whitespace-only
// values — must take the same clear-to-NULL path that --assignee= and
// --assignee '' already take, on both ingest surfaces: `bd update --assignee`
// and `bd assign <id> <who>`. Storing those literals verbatim gave
// "unassigned" three on-disk spellings and every emptiness check (-z, //"",
// == null) classified a dead-lane dash as ASSIGNED, so a released claim
// stayed hooked with no alert. Real names — including spaced, parenthesized
// and dash-containing ones — must pass through verbatim.
func TestEmbeddedAssigneeInputNormalizationCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "an")

	t.Run("update_clear_forms_store_null", func(t *testing.T) {
		for _, form := range [][]string{
			{"--assignee="},
			{"--assignee", ""},
			{"--assignee", "-"},
			{"--assignee", " "},
			{"--assignee", "  - "},
		} {
			issue := bdCreate(t, bd, dir, "Clear form", "--type", "task", "--assignee", "someone")
			bdUpdate(t, bd, dir, append([]string{issue.ID}, form...)...)
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
				t.Errorf("update %v on assigned open issue: assignee=%q, want cleared (NULL)", form, got.Assignee)
			}
		}
	})

	t.Run("assign_positional_clear_forms_store_null", func(t *testing.T) {
		for _, clear := range []string{"", "-", " "} {
			issue := bdCreate(t, bd, dir, "Assign clear", "--type", "task", "--assignee", "someone")
			bdRunOK(t, bd, dir, "assign", issue.ID, clear)
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != "" {
				t.Errorf("assign %q: assignee=%q, want cleared (NULL)", clear, got.Assignee)
			}
		}
	})

	t.Run("real_names_pass_through_verbatim", func(t *testing.T) {
		for _, name := range []string{"Bob", "Bob (Bot)", "a-b", "Dana-Dash (Robot)"} {
			issue := bdCreate(t, bd, dir, "Hold name", "--type", "task")
			bdUpdate(t, bd, dir, issue.ID, "--assignee", name)
			if got := bdShow(t, bd, dir, issue.ID); got.Assignee != name {
				t.Errorf("update --assignee %q: assignee=%q, want verbatim", name, got.Assignee)
			}
		}
	})
}
