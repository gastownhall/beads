package dolt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestDoltNew_ReadOnlyOpenReadsWithIgnoredSeriesBehind reproduces
// gastownhall/beads#7038 (bd-i1zmul): a clone that pulled the migrated main
// series on an old binary sits at the latest main version but an older
// clone-local (ignored) version, and a read-only server-mode open (bd list,
// ready, show, count) then fails every issue read on leases.granted_node
// (ignored/0016) with a raw Error 1105 until a read-write command applies the
// clone-local series.
//
// The triage repro expected the read itself to succeed — a read-only open
// that applies the clone-local series. The approved design rejects that
// shape: a read-only open performs no migration (the GH#804 contract,
// bd-6dnrw.32's read-only-skips-migrate invariant), so the fix is a typed
// refusal at open naming the missing column and the per-database remedy
// (AC-1), while the version-bump upgrade hook — the authorized maintenance
// path — is repaired to reach a server-mode database (AC-2). This test pins
// the corrected contract: the diagnostic open still reaches the database and
// its reads still fail on the raw column (the bug, unchanged policy), while
// an open that asks for a readable schema refuses with the typed error.
func TestDoltNew_ReadOnlyOpenReadsWithIgnoredSeriesBehind(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)
	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*testTimeout)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	// Main series at latest, clone-local series behind ignored/0016: the
	// state `bd dolt pull` on a pre-0016 binary leaves.
	for _, stmt := range []string{
		"ALTER TABLE leases DROP COLUMN granted_node",
		"DELETE FROM ignored_schema_migrations WHERE version = 16",
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// The bug itself, on the open that does not ask: the open succeeds and
	// the hydrated read fails on the raw unknown-column error (#7038's
	// report). CountIssues is not asserted: its query does not select the
	// lease overlay, so it survives a missing lease column by design.
	ro, err := New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
		ReadOnly:       true,
	})
	if err != nil {
		t.Fatalf("New (read-only diagnostic open) = %v, want success", err)
	}
	_, searchErr := ro.SearchIssues(ctx, "", types.IssueFilter{})
	ro.Close()
	if searchErr == nil || !strings.Contains(searchErr.Error(), "granted_node") {
		t.Errorf("SearchIssues on the pre-0016 clone-local shape = %v, want the raw granted_node failure the report showed", searchErr)
	}

	// The fix: an open that asks for a readable schema refuses with the
	// typed clone-local error instead of permitting that raw read.
	_, err = New(ctx, &Config{
		Path:                  tmpDir,
		CommitterName:         "test",
		CommitterEmail:        "test@example.com",
		Database:              dbName,
		ReadOnly:              true,
		RequireReadableSchema: true,
	})
	var behind *schema.CloneLocalBehindError
	if !errors.As(err, &behind) {
		t.Fatalf("New (read-only, clone-local series behind) = %T (%v), want *schema.CloneLocalBehindError", err, err)
	}
	if len(behind.MissingColumns) != 1 || behind.MissingColumns[0] != "granted_node" {
		t.Errorf("MissingColumns = %v, want [granted_node]", behind.MissingColumns)
	}
	if msg := err.Error(); !strings.Contains(msg, "bd migrate schema") {
		t.Errorf("refusal %q does not name the per-database remedy", msg)
	}
}
