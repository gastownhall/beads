package issueops

import (
	"strings"
	"testing"
)

// TestUpsertRevisionIsTwoSided locks the ON DUPLICATE KEY UPDATE side of the
// issue upsert. The INSERT/VALUES side is covered by the source-scan guard
// (TestAllIssueRowWritesBumpRevision); this asserts the upsert also bumps
// revision when it UPDATES an existing row, so a `bd import` onto an existing
// bead cannot change content while leaving revision stale — the Q1 lost-update
// the red-team flagged (B3).
func TestUpsertRevisionIsTwoSided(t *testing.T) {
	found := false
	for _, c := range issueUpsertColumns {
		if c == "revision" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issueUpsertColumns must contain revision so the upsert bumps it on UPDATE: %v", issueUpsertColumns)
	}

	// updated_at must stay last: the rejectStale IF compares VALUES(updated_at)
	// against the still-stored updated_at, so it must be reassigned after every
	// other column (including revision) has been decided.
	if last := issueUpsertColumns[len(issueUpsertColumns)-1]; last != "updated_at" {
		t.Fatalf("updated_at must be the last upsert column, got %q last in %v", last, issueUpsertColumns)
	}

	if plain := issueUpsertAssignments("issues", false); !strings.Contains(plain, "revision = VALUES(revision)") {
		t.Errorf("plain upsert must set revision = VALUES(revision), got:\n%s", plain)
	}
	if stale := issueUpsertAssignments("issues", true); !strings.Contains(stale,
		"revision = IF(VALUES(updated_at) > issues.updated_at, VALUES(revision), issues.revision)") {
		t.Errorf("rejectStale upsert must guard revision by updated_at, got:\n%s", stale)
	}
}
