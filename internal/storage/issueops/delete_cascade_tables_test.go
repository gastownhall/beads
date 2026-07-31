package issueops

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// cascadeFKPattern matches an ON DELETE CASCADE foreign key that points at
// refTable, in the quoted ALTER TABLE form the guarded migrations use.
func cascadeFKPattern(refTable string) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(
		`(?is)ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*)\s+ADD\s+CONSTRAINT.*?REFERENCES\s+%s\s*\(\s*id\s*\).*?ON\s+DELETE\s+CASCADE`,
		refTable))
}

// createCascadePattern matches the bare CREATE TABLE form, which names the
// table in the statement header rather than in the constraint.
func createCascadePattern(refTable string) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(
		`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?`+"`?"+`([A-Za-z_][A-Za-z0-9_]*)`+"`?"+`\s*\((?:[^;]*?)REFERENCES\s+%s\s*\(\s*id\s*\)\s*ON\s+DELETE\s+CASCADE`,
		refTable))
}

// migrationCascadeTargets derives, from the shipped migrations, every table
// with an ON DELETE CASCADE FK pointing at refTable(id).
func migrationCascadeTargets(t *testing.T, refTable string) map[string]bool {
	t.Helper()
	dir := filepath.Join("..", "schema", "migrations")
	found := map[string]bool{}
	alterPat := cascadeFKPattern(refTable)
	createPat := createCascadePattern(refTable)

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
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
		for _, m := range alterPat.FindAllStringSubmatch(text, -1) {
			found[m[1]] = true
		}
		for _, m := range createPat.FindAllStringSubmatch(text, -1) {
			found[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return found
}

// TestDeleteCascadeTablesCoversSchema derives the cascade set from the shipped
// migrations and asserts DeleteCascadeTables lists every table found. Hand-kept
// lists of cascade targets are exactly what drifted before: the dolt
// transaction marked one table, the embedded one marked five, and the real
// cascade reaches more than either. Adding a new ON DELETE CASCADE FK to issues
// without adding the table here should fail this test rather than silently
// leave rows out of the version commit.
func TestDeleteCascadeTablesCoversSchema(t *testing.T) {
	found := migrationCascadeTargets(t, "issues")
	if len(found) == 0 {
		t.Fatal("no ON DELETE CASCADE references to issues found; the test proves nothing")
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

// TestDeleteCascadeTablesWispCoversSchema is the same derivation for the wisp
// plane: every FK-cascade target of wisps(id) must be listed. This is what
// would have caught the original list missing wisp_comments and
// wisp_child_counters.
func TestDeleteCascadeTablesWispCoversSchema(t *testing.T) {
	found := migrationCascadeTargets(t, "wisps")
	if len(found) == 0 {
		t.Fatal("no ON DELETE CASCADE references to wisps found; the test proves nothing")
	}

	listed := DeleteCascadeTables(true)
	for table := range found {
		if !slices.Contains(listed, table) {
			t.Errorf("migrations declare ON DELETE CASCADE from wisps to %q, but DeleteCascadeTables(true) omits it (has %v)",
				table, listed)
		}
	}

	if !slices.Contains(listed, "wisps") {
		t.Errorf("DeleteCascadeTables(true) = %v, want it to include the deleted row's own table", listed)
	}
}

// TestDeleteCascadeTablesWispRouting pins the wisp set's boundary in both
// directions. The only issue-plane table allowed is dependencies — a wisp
// delete explicitly removes the sync-plane edges pointing at the wisp
// (DeleteWispFromDependenciesInTx; no FK exists from dependencies to wisps),
// and that deletion MUST be staged or it is left out of the version commit and
// resurrected by the next hard reset. FK derivation cannot see an explicit
// DELETE, hence this hand-pinned assertion. No other issue-plane table may
// appear: staging one would sweep unrelated working-set changes into the
// commit.
func TestDeleteCascadeTablesWispRouting(t *testing.T) {
	got := DeleteCascadeTables(true)
	for _, table := range got {
		if table == "dependencies" {
			continue
		}
		if table != "wisps" && !strings.HasPrefix(table, "wisp_") {
			t.Errorf("DeleteCascadeTables(true) includes unexpected issue-plane table %q", table)
		}
	}
	if !slices.Contains(got, "dependencies") {
		t.Errorf("DeleteCascadeTables(true) = %v, want it to include dependencies: deleteIssueRowInTx explicitly deletes the wisp's sync-plane edges, and unstaged deletions are lost from the version commit", got)
	}
}
