//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedMigrateConsent_BehindAndDirtyWorkingSetReconcileRecovers covers
// the #4566 deadlock the migration-consent gate would otherwise rebuild one
// check earlier. On a database that is both behind and dirty, `bd migrate
// schema` consents but is refused by the dirty-table guard, whose remedy is
// `bd dolt commit`; that commit opens the store without consent, and MigrateUp
// checks consent before the dirty guard. The working-set-reconcile open must
// warn and commit at the current schema, strict and read-only opens must keep
// refusing, and the consented migration must then converge.
func TestEmbeddedMigrateConsent_BehindAndDirtyWorkingSetReconcileRecovers(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Build the fixture under the package-wide consent (TestMain): create
	// and commit the database, leave `issues` dirty, and regress the cursor
	// to v51 (absolute, for the reasons dirty_tables_gate_test.go gives).
	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "testdb"); err != nil {
		store.Close()
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		store.Close()
		t.Fatalf("Commit (init): %v", err)
	}
	issue := &types.Issue{
		ID:        "testdb-1",
		Title:     "dirty working set issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		store.Close()
		t.Fatalf("CreateIssue: %v", err)
	}
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		store.Close()
		t.Fatalf("OpenSQL: %v", err)
	}
	const regressedVersion = 51
	if _, err := db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version > ?", regressedVersion); err != nil {
		t.Fatalf("regress schema_migrations: %v", err)
	}
	_ = cleanup()
	store.Close()

	// From here on no consent source is set: this is the `bd dolt commit`
	// an operator runs on the dirty guard's advice.
	schema.SetLocalMigrateConsent(false)
	schema.SetForceAllowRemoteMigrate(false)
	t.Cleanup(func() { schema.SetLocalMigrateConsent(true) })
	t.Setenv(schema.AllowMigrateEnv, "")
	t.Setenv(schema.AllowRemoteMigrateEnv, "")

	type opener func(context.Context, string, string, string) (*embeddeddolt.EmbeddedDoltStore, error)
	refuses := func(name string, open opener) {
		t.Helper()
		s, err := open(ctx, beadsDir, "testdb", "main")
		if err == nil {
			s.Close()
			t.Fatalf("%s = nil, want *schema.MigrateConsentError on a behind database without consent", name)
		}
		if !schema.IsMigrateConsentError(err) {
			t.Fatalf("%s error = %T (%v), want *schema.MigrateConsentError (consent is checked before the dirty guard)", name, err, err)
		}
	}
	before := refusalSnapshot(t, dataDir, "testdb")
	refuses("Open", embeddeddolt.Open)
	refuses("OpenForReadOnlyCommand", embeddeddolt.OpenForReadOnlyCommand)
	if after := refusalSnapshot(t, dataDir, "testdb"); after != before {
		t.Fatalf("a refused open wrote to the database:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	var reconcileStore *embeddeddolt.EmbeddedDoltStore
	var openErr error
	warning := captureStderr(t, func() {
		reconcileStore, openErr = embeddeddolt.OpenForWorkingSetReconcile(ctx, beadsDir, "testdb", "main")
	})
	if openErr != nil {
		t.Fatalf("OpenForWorkingSetReconcile = %v, want it to warn and continue past the consent refusal", openErr)
	}
	if want := "Working-set reconcile command: continuing on schema v51 without"; !strings.Contains(warning, want) {
		reconcileStore.Close()
		t.Fatalf("warning is missing %q; got:\n%s", want, warning)
	}
	if current, err := schemaVersion(ctx, dataDir, "testdb"); err != nil {
		reconcileStore.Close()
		t.Fatalf("read schema version: %v", err)
	} else if current != regressedVersion {
		reconcileStore.Close()
		t.Fatalf("schema version = %d, want %d (no consent, so no migration)", current, regressedVersion)
	}
	if err := reconcileStore.Commit(ctx, "checkpoint"); err != nil {
		reconcileStore.Close()
		t.Fatalf("Commit: %v", err)
	}
	reconcileStore.Close()

	// The commit consented to nothing.
	refuses("Open (after commit)", embeddeddolt.Open)

	// The consent `bd migrate schema` records now converges: the dirty guard
	// that refused it has nothing left to refuse.
	schema.SetLocalMigrateConsent(true)
	migrated, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open (consented, post-commit): %v", err)
	}
	defer migrated.Close()
	if current, err := schemaVersion(ctx, dataDir, "testdb"); err != nil {
		t.Fatalf("read schema version: %v", err)
	} else if current != schema.LatestVersion() {
		t.Fatalf("schema version after consented reopen = %d, want latest %d", current, schema.LatestVersion())
	}
}

// refusalSnapshot renders what a refused open must leave untouched: the
// working-set status, the dolt_ignore patterns MigrateUp seeds, and the
// schema cursor.
func refusalSnapshot(t *testing.T, dataDir, database string) string {
	t.Helper()
	ctx := t.Context()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	var b strings.Builder
	for _, q := range []string{
		"SELECT table_name, staged, status FROM dolt_status ORDER BY table_name, staged",
		"SELECT pattern, ignored FROM dolt_ignore ORDER BY pattern",
		"SELECT version FROM schema_migrations ORDER BY version",
	} {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("%s: columns: %v", q, err)
		}
		fmt.Fprintf(&b, "%s\n", q)
		for rows.Next() {
			vals := make([]any, len(cols))
			for i := range vals {
				vals[i] = new(any)
			}
			if err := rows.Scan(vals...); err != nil {
				rows.Close()
				t.Fatalf("%s: scan: %v", q, err)
			}
			for _, v := range vals {
				fmt.Fprintf(&b, " %v", *(v.(*any)))
			}
			b.WriteString("\n")
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("%s: close: %v", q, err)
		}
	}
	return b.String()
}
