//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedAssigneeCaseListings replays the listing half of GH#8628
// (bd-f76042): --assignee is free text that drifts in mixed case within a
// day, so the listings must treat "hierophant", "Hierophant" and
// "HIEROPHANT" as one principal across every plane that takes the filter —
// bd list, bd count, bd search and bd ready — while a genuinely different
// assignee (bob) never leaks into the set, and each issue's stored spelling
// is printed back unchanged.
func TestEmbeddedAssigneeCaseListings(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "as")
	lower := bdCreate(t, bd, dir, "Case fold lower spelling", "--type", "task", "--assignee", "hierophant", "--label", "gh8628-case")
	titled := bdCreate(t, bd, dir, "Case fold titled spelling", "--type", "task", "--assignee", "Hierophant", "--label", "gh8628-case")
	bobs := bdCreate(t, bd, dir, "Case fold stranger", "--type", "task", "--assignee", "bob", "--label", "gh8628-case")

	// The ready listing (open work) and the plain listing (seeded open) see
	// the same three issues; the label keeps unrelated state out.
	for _, spelling := range []string{"hierophant", "Hierophant", "HIEROPHANT"} {
		t.Run("list_"+spelling, func(t *testing.T) {
			t.Parallel()
			issues := bdListJSON(t, bd, dir, "--assignee", spelling, "--label", "gh8628-case")
			if len(issues) != 2 {
				t.Fatalf("bd list --assignee %s listed %d issues, want 2 (%s, %s)", spelling, len(issues), lower.ID, titled.ID)
			}
			got := map[string]bool{}
			for _, iss := range issues {
				if iss.Assignee != "hierophant" && iss.Assignee != "Hierophant" {
					t.Errorf("issue %s has assignee %q, want one of the hierophant spellings", iss.ID, iss.Assignee)
				}
				got[iss.ID] = true
			}
			if !got[lower.ID] || !got[titled.ID] {
				t.Errorf("bd list --assignee %s returned the wrong pair, want %s and %s", spelling, lower.ID, titled.ID)
			}
			if got[bobs.ID] {
				t.Errorf("bob's issue %s leaked into the --assignee %s listing", bobs.ID, spelling)
			}
		})

		t.Run("count_"+spelling, func(t *testing.T) {
			t.Parallel()
			m := bdCountJSON(t, bd, dir, "--assignee", spelling, "--label", "gh8628-case")
			if cnt := int(m["count"].(float64)); cnt != 2 {
				t.Errorf("bd count --assignee %s = %d, want 2", spelling, cnt)
			}
		})

		t.Run("search_"+spelling, func(t *testing.T) {
			t.Parallel()
			results := bdSearchJSON(t, bd, dir, "fold", "--assignee", spelling, "--label", "gh8628-case")
			if len(results) != 2 {
				t.Fatalf("bd search --assignee %s returned %d results, want 2", spelling, len(results))
			}
			// Each stored spelling is printed back exactly as it was given,
			// never rewritten to the asked casing.
			printed := map[string]bool{}
			for _, r := range results {
				if a, ok := r["assignee"].(string); ok {
					printed[a] = true
				}
			}
			if !printed["hierophant"] || !printed["Hierophant"] {
				t.Errorf("search printed assignees %v, want both stored spellings unchanged", printed)
			}
		})

		t.Run("ready_"+spelling, func(t *testing.T) {
			t.Parallel()
			ids := readyJSONIDs(t, bd, dir, "--assignee", spelling, "--label", "gh8628-case")
			if !ids[lower.ID] || !ids[titled.ID] || ids[bobs.ID] || len(ids) != 2 {
				t.Errorf("bd ready --assignee %s listed %v, want exactly %s and %s", spelling, ids, lower.ID, titled.ID)
			}
		})
	}
}
