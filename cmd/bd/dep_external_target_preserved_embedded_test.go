//go:build cgo

package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedDepAddUnheldTargetsStayExternal pins what GH#8299 must not
// change: classifying a target by what the database holds is only for
// targets it holds. A foreign-prefix target held nowhere (the multi-rig "add
// now, route later" shape) and an explicit external: reference are still
// stored in depends_on_external, the unresolved foreign target still warns,
// and `bd dep remove` still deletes both edges.
func TestEmbeddedDepAddUnheldTargetsStayExternal(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "b")
	src := bdCreate(t, bd, dir, "Source", "--type", "task")
	const foreign = "zz-8299"
	const external = "external:proj:cap8299"

	_, stderr := bdRunSplit(t, bd, dir, "dep", "add", src.ID, foreign)
	if !strings.Contains(stderr, foreign+" was not found") || !strings.Contains(stderr, "stored unverified") {
		t.Errorf("dep add %s %s: stderr = %q, want the unresolved-target warning", src.ID, foreign, stderr)
	}
	_, stderr = bdRunSplit(t, bd, dir, "dep", "add", src.ID, external)
	if strings.Contains(stderr, "was not found") {
		t.Errorf("dep add %s %s: stderr = %q, want no unresolved-target warning", src.ID, external, stderr)
	}

	for _, target := range []string{foreign, external} {
		issueCol, wispCol, externalCol, found := embeddedDepTargetColumns(t, beadsDir, src.ID, target)
		if !found {
			t.Fatalf("no dependencies row %s -> %s", src.ID, target)
		}
		if issueCol.Valid || wispCol.Valid || externalCol.String != target {
			t.Errorf("%s -> %s: issue=%v wisp=%v external=%v, want the target only in depends_on_external",
				src.ID, target, issueCol, wispCol, externalCol)
		}
	}

	for _, target := range []string{foreign, external} {
		bdDep(t, bd, dir, "remove", src.ID, target)
		if _, _, _, found := embeddedDepTargetColumns(t, beadsDir, src.ID, target); found {
			t.Errorf("dep remove %s %s left the row behind", src.ID, target)
		}
	}
}

// embeddedDepTargetColumns reads the typed target columns of the
// dependencies row source -> target from the embedded store.
func embeddedDepTargetColumns(t *testing.T, beadsDir, source, target string) (issueCol, wispCol, externalCol sql.NullString, found bool) {
	t.Helper()
	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	err = db.QueryRowContext(t.Context(),
		"SELECT depends_on_issue_id, depends_on_wisp_id, depends_on_external FROM dependencies"+
			" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		source, target).Scan(&issueCol, &wispCol, &externalCol)
	switch {
	case err == sql.ErrNoRows:
		return issueCol, wispCol, externalCol, false
	case err != nil:
		t.Fatalf("read dependencies row %s -> %s: %v", source, target, err)
	}
	return issueCol, wispCol, externalCol, true
}
