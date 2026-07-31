package issueops

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// cascadeFKPattern matches an ON DELETE CASCADE foreign key that points at the
// issues table, in either the bare CREATE TABLE form or the quoted ALTER TABLE
// form the guarded migrations use.
var cascadeFKPattern = regexp.MustCompile(
	`(?is)ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*)\s+ADD\s+CONSTRAINT.*?REFERENCES\s+issues\s*\(\s*id\s*\).*?ON\s+DELETE\s+CASCADE`)

// TestDeleteCascadeTablesCoversSchema derives the cascade set from the shipped
// migrations and asserts DeleteCascadeTables lists every table found. Hand-kept
// lists of cascade targets are exactly what drifted before: the dolt
// transaction marked one table, the embedded one marked five, and the real
// cascade reaches more than either. Adding a new ON DELETE CASCADE FK to issues
// without adding the table here should fail this test rather than silently
// leave rows out of the version commit.
func TestDeleteCascadeTablesCoversSchema(t *testing.T) {
	dir := filepath.Join("..", "schema", "migrations")
	found := map[string]bool{}

	if err := filepath.WalkDir(dir, walkSQL(t, found)); err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	if len(found) == 0 {
		t.Fatalf("no ON DELETE CASCADE references to issues found under %s; the test proves nothing", dir)
	}

	listed := DeleteCascadeTables(false)
	for table := range found {
		if !slices.Contains(listed, table) {
			t.Errorf("migrations declare ON DELETE CASCADE from issues to %q, but DeleteCascadeTables(false) omits it (has %v)",
				table, listed)
		}
	}

	if !slices.Contains(listed, "issues") {
		t.Errorf("DeleteCascadeTables(false) = %v, want it to include the deleted row's own table", listed)
	}
}

func walkSQL(t *testing.T, found map[string]bool) func(string, os.DirEntry, error) error {
	t.Helper()
	return func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".up.sql") {
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // G304: path comes from WalkDir over a repo-relative dir
		if readErr != nil {
			return readErr
		}
		text := string(body)
		for _, m := range cascadeFKPattern.FindAllStringSubmatch(text, -1) {
			found[m[1]] = true
		}
		// The bare CREATE TABLE form names the table in the statement header
		// rather than in the constraint, so it needs its own pass.
		for _, m := range createCascadePattern.FindAllStringSubmatch(text, -1) {
			found[m[1]] = true
		}
		return nil
	}
}

var createCascadePattern = regexp.MustCompile(
	`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + "`?" + `([A-Za-z_][A-Za-z0-9_]*)` + "`?" + `\s*\((?:[^;]*?)REFERENCES\s+issues\s*\(\s*id\s*\)\s*ON\s+DELETE\s+CASCADE`)

// TestDeleteCascadeTablesWispRouting pins that the wisp side stays on the
// wisp_* tables. DirtyTableTracker.MarkDirty drops those as dolt-ignored, so
// this is about not accidentally staging the issue-side tables for a wisp
// delete, which would sweep unrelated working-set changes into the commit.
func TestDeleteCascadeTablesWispRouting(t *testing.T) {
	for _, table := range DeleteCascadeTables(true) {
		if table != "wisps" && !strings.HasPrefix(table, "wisp_") {
			t.Errorf("DeleteCascadeTables(true) includes non-wisp table %q", table)
		}
	}
	if got := DeleteCascadeTables(true); !slices.Contains(got, "wisps") {
		t.Errorf("DeleteCascadeTables(true) = %v, want it to include wisps", got)
	}
}
