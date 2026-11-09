//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedUpdatedAfterSeesLabelEdits (GH#5442): --updated-after is the
// recency filter, and a live label edit is a change to the issue. An issue
// older than cutoff C that gets a label after C must show up in list, search
// and count with --updated-after C; before the fix, label edits never stamped
// updated_at and all three filters silently missed them.
func TestEmbeddedUpdatedAfterSeesLabelEdits(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ua5")

	// Three fixtures updated well in the past: two will receive label edits,
	// the third stays untouched as the control.
	aged := time.Now().UTC().AddDate(0, 0, -30).Truncate(time.Second)
	jsonlPath := filepath.Join(t.TempDir(), "aged.jsonl")
	writeJSONLFile(t, jsonlPath, []types.Issue{
		{ID: "ua5-label-add", Title: "ua5 label add probe", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: aged, UpdatedAt: aged},
		{ID: "ua5-patch-add", Title: "ua5 patch add probe", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: aged, UpdatedAt: aged},
		{ID: "ua5-untouched", Title: "ua5 untouched probe", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: aged, UpdatedAt: aged},
	})
	if out := bdImport(t, bd, dir, jsonlPath); !strings.Contains(out, "Imported 3 issues") {
		t.Fatalf("import: %s", out)
	}

	// The cutoff sits strictly between the fixture timestamps and the edits.
	cutoff := time.Now().UTC().AddDate(0, 0, -15).Format(time.RFC3339)

	// A live label command on the first, a labels-only update on the second.
	bdLabel(t, bd, dir, "add", "ua5-label-add", "probe")
	bdUpdate(t, bd, dir, "ua5-patch-add", "--add-label", "probe")

	// list --updated-after includes both edited issues and not the control.
	var listed []string
	for _, it := range bdListJSON(t, bd, dir, "--updated-after", cutoff) {
		listed = append(listed, it.ID)
	}
	if !containsAll(listed, "ua5-label-add", "ua5-patch-add") {
		t.Errorf("list --updated-after C = %v, want both label-edited issues", listed)
	}
	if containsAll(listed, "ua5-untouched") {
		t.Errorf("list --updated-after C = %v, the untouched control must stay excluded", listed)
	}

	// search --updated-after finds both by the shared title stem.
	var searched []string
	for _, r := range bdSearchJSON(t, bd, dir, "ua5", "--updated-after", cutoff) {
		if id, ok := r["id"].(string); ok {
			searched = append(searched, id)
		}
	}
	if !containsAll(searched, "ua5-label-add", "ua5-patch-add") {
		t.Errorf("search --updated-after C = %v, want both label-edited issues", searched)
	}
	if containsAll(searched, "ua5-untouched") {
		t.Errorf("search --updated-after C = %v, the untouched control must stay excluded", searched)
	}

	// count --updated-after counts exactly the two edited issues.
	m := bdCountJSON(t, bd, dir, "--updated-after", cutoff)
	count, ok := m["count"].(float64)
	if !ok {
		t.Fatalf("count --updated-after C JSON has no count field: %v", m)
	}
	if int(count) != 2 {
		t.Errorf("count --updated-after C = %d, want 2 (only the label-edited issues)", int(count))
	}
}

// containsAll reports whether got contains every want string.
func containsAll(got []string, want ...string) bool {
	set := make(map[string]bool, len(got))
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}
