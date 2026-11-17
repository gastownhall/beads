//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedUpdatedAfterSeesLabelEdits is GH#5442 end to end: a label edit
// made after the cutoff, through `bd label` or a labels-only `bd update`,
// counts as an update of the issue, so list, search and count with
// --updated-after include it. An aged issue nobody touched stays out.
func TestEmbeddedUpdatedAfterSeesLabelEdits(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt label tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "lua")

	aged := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	fixture := func(id string, labels ...string) types.Issue {
		return types.Issue{
			ID: id, Title: "aged label edit " + id, Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: aged, UpdatedAt: aged, Labels: labels,
		}
	}
	jsonlPath := filepath.Join(t.TempDir(), "aged.jsonl")
	writeJSONLFile(t, jsonlPath, []types.Issue{
		fixture("lua-add"),
		fixture("lua-rm", "old"),
		fixture("lua-upadd"),
		fixture("lua-uprm", "old"),
		fixture("lua-idle", "old"),
	})
	bdImport(t, bd, dir, jsonlPath)

	bdLabel(t, bd, dir, "add", "lua-add", "fresh")
	bdLabel(t, bd, dir, "remove", "lua-rm", "old")
	bdUpdate(t, bd, dir, "lua-upadd", "--add-label", "fresh")
	bdUpdate(t, bd, dir, "lua-uprm", "--remove-label", "old")

	want := []string{"lua-add", "lua-rm", "lua-upadd", "lua-uprm"}
	const cutoff = "2021-01-01"

	var listed []string
	for _, issue := range bdListJSON(t, bd, dir, "--updated-after", cutoff) {
		listed = append(listed, issue.ID)
	}
	slices.Sort(listed)
	if !slices.Equal(listed, want) {
		t.Errorf("list --updated-after %s = %v, want %v", cutoff, listed, want)
	}

	var found []string
	for _, hit := range bdSearchJSON(t, bd, dir, "aged label edit", "--updated-after", cutoff) {
		id, _ := hit["id"].(string)
		found = append(found, id)
	}
	slices.Sort(found)
	if !slices.Equal(found, want) {
		t.Errorf("search --updated-after %s = %v, want %v", cutoff, found, want)
	}

	if got, _ := bdCountJSON(t, bd, dir, "--updated-after", cutoff)["count"].(float64); int(got) != len(want) {
		t.Errorf("count --updated-after %s = %v, want %d", cutoff, got, len(want))
	}
}
