//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// gastownhall/beads#5269: the migration runner records a version mark once the
// migration body returns no error, without checking that the body's DDL took
// effect. A reporter's clone ended up with marks v50..v53 while 0050's guarded
// (PREPARE/EXECUTE) `ALTER TABLE dependencies ALTER COLUMN id DROP DEFAULT`
// never applied, and because the mark is present no later `bd migrate` heals it.
//
// silentPreparedALTERConn stands in for that environment: it delivers 0050's
// body with the guarded ALTER replaced by a no-op, so the call still succeeds
// (the shape of dolthub/dolt#11345, a PREPARE'd ALTER that reports success and
// does nothing).
type silentPreparedALTERConn struct {
	schema.DBConn
}

func (c silentPreparedALTERConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "Migration 0050:") {
		query = strings.Replace(query, "'ALTER TABLE dependencies ALTER COLUMN id DROP DEFAULT'", "'SELECT 1'", 1)
	}
	return c.DBConn.ExecContext(ctx, query, args...)
}

func dependenciesIDDefault(t *testing.T, ctx context.Context, conn *sql.Conn) sql.NullString {
	t.Helper()
	var def sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id'`).Scan(&def); err != nil {
		t.Fatalf("read dependencies.id default: %v", err)
	}
	return def
}

// seedDependenciesIDDefaultAt49 leaves a committed, pre-0050 shape on a
// database migrated to v49: dependencies.id carrying 0043-era DEFAULT
// (UUID()), the exact state the reporter's clone was in.
func seedDependenciesIDDefaultAt49(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	for _, q := range []string{
		"ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())",
		"CALL DOLT_COMMIT('-Am', 'test: pre-#4259 0043 shape')",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if def := dependenciesIDDefault(t, ctx, conn); !def.Valid {
		t.Fatalf("precondition: dependencies.id has no DEFAULT at v49; want 0043's DEFAULT (uuid())")
	}
}

// TestEmbeddedMigrateDoesNotRecordVersionWhoseDDLDidNotApply_5269 asserts the
// behaviour the report expects: when 0050's DDL does not take effect, the pass
// either fails loudly or does not record version 50. It must never leave the
// database claiming v50+ while dependencies.id still carries 0043's
// DEFAULT (uuid()).
func TestEmbeddedMigrateDoesNotRecordVersionWhoseDDLDidNotApply_5269(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 49)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	// Today's 0043 drops the random default itself; the reporter's clone ran
	// the earlier 0043 that left it for 0050 to drop. Restore that shape.
	seedDependenciesIDDefaultAt49(t, ctx, conn)

	_, migrateErr := schema.MigrateUp(ctx, silentPreparedALTERConn{DBConn: conn})

	var has50 int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 50").Scan(&has50); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	def := dependenciesIDDefault(t, ctx, conn)
	maxVersion := currentMainVersion(t, ctx, conn)
	if migrateErr == nil && has50 == 1 && def.Valid {
		t.Fatalf("MigrateUp succeeded and recorded version 50 (cursor now v%d) while dependencies.id still has DEFAULT %q: "+
			"the mark claims 0050 applied but its DDL never took effect, and re-running migrate cannot heal it", maxVersion, def.String)
	}

	// Re-running migrate is the user's only lever; it must not stay silent either.
	_, rerunErr := schema.MigrateUp(ctx, conn)
	if def := dependenciesIDDefault(t, ctx, conn); rerunErr == nil && def.Valid {
		t.Fatalf("re-run MigrateUp succeeded but dependencies.id still has DEFAULT %q while schema_migrations claims v%d", def.String, currentMainVersion(t, ctx, conn))
	}
}

// TestEmbeddedMigrateCorrectionCommitsWithTheMark_5269 proves where the direct
// correction lands: in the same per-step commit as the version mark, not in
// the working set. The pass is aborted right after 0050's step commit; at that
// point the seed's only change since the baseline is the correction, so a
// clean working set plus a missing default proves the 0050 step commit (HEAD)
// carries the corrected DDL together with the cursor row.
func TestEmbeddedMigrateCorrectionCommitsWithTheMark_5269(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 49)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedDependenciesIDDefaultAt49(t, ctx, conn)

	restore := schema.SetMigrateStepFaultHookForTest(func(_ context.Context, _ schema.DBConn, version int) error {
		if version == 50 {
			return fmt.Errorf("injected fault: stop right after migration 50's step commit")
		}
		return nil
	})
	defer restore()

	_, err := schema.MigrateUp(ctx, silentPreparedALTERConn{DBConn: conn})
	if err == nil || !strings.Contains(err.Error(), "injected fault") {
		t.Fatalf("MigrateUp err = %v, want the injected fault after 0050", err)
	}

	if v := currentMainVersion(t, ctx, conn); v != 50 {
		t.Fatalf("version after the aborted pass = %d, want 50 (the mark was recorded)", v)
	}
	if def := dependenciesIDDefault(t, ctx, conn); def.Valid {
		t.Fatalf("dependencies.id still has DEFAULT %q after the pass", def.String)
	}
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("working set dirty after 0050's step commit (%v): the correction must ride the step commit, not the working set", dirty)
	}
	var headMsg string
	if err := conn.QueryRowContext(ctx, "SELECT message FROM dolt_log ORDER BY date DESC LIMIT 1").Scan(&headMsg); err != nil {
		t.Fatalf("read HEAD commit message: %v", err)
	}
	if !strings.Contains(headMsg, "schema: apply migration 0050") {
		t.Fatalf("HEAD message = %q, want the 0050 step commit", headMsg)
	}
}

// failingCorrection scenario: refusingCorrectionConn forwards to the wrapped
// conn but fails the direct, identifier-quoted DROP DEFAULT (the backticked
// form only the post-check issues; the frozen bodies quote nothing).
type refusingCorrectionConn struct {
	schema.DBConn
}

func (c refusingCorrectionConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "`dependencies`") && strings.Contains(query, "DROP DEFAULT") {
		return nil, fmt.Errorf("injected: correction refused")
	}
	return c.DBConn.ExecContext(ctx, query, args...)
}

// TestEmbeddedMigrateUncorrectableDefaultFailsLoudlyWithoutTheMark_5269: when
// neither the guarded body nor the direct correction can drop the default, the
// pass fails loudly, records no mark, and stays failed on retry — the database
// never silently claims the version (GH#5269).
func TestEmbeddedMigrateUncorrectableDefaultFailsLoudlyWithoutTheMark_5269(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 49)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedDependenciesIDDefaultAt49(t, ctx, conn)

	engine := silentPreparedALTERConn{DBConn: conn}
	_, migrateErr := schema.MigrateUp(ctx, refusingCorrectionConn{DBConn: engine})
	if migrateErr == nil {
		t.Fatalf("MigrateUp succeeded although dependencies.id's default could be dropped neither by the body nor by the correction")
	}
	var perr *schema.PostConditionError
	if !errors.As(migrateErr, &perr) {
		t.Fatalf("MigrateUp err = %T %v, want a *schema.PostConditionError", migrateErr, migrateErr)
	}
	if !strings.Contains(migrateErr.Error(), "dependencies.id") || !strings.Contains(migrateErr.Error(), "(uuid())") {
		t.Fatalf("err = %v, want it to name dependencies.id and the surviving default", migrateErr)
	}

	markCount := func() int {
		var has50 int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 50").Scan(&has50); err != nil {
			t.Fatalf("read schema_migrations: %v", err)
		}
		return has50
	}
	if has50 := markCount(); has50 != 0 {
		t.Fatalf("version 50 recorded (%d rows) although its post-condition failed; every later pass would skip the body", has50)
	}
	if v := currentMainVersion(t, ctx, conn); v != 49 {
		t.Fatalf("version after the failed pass = %d, want 49", v)
	}
	if def := dependenciesIDDefault(t, ctx, conn); !def.Valid {
		t.Fatalf("dependencies.id lost its default although the correction was refused")
	}

	// Re-running is the user's only lever; it must not go silent either.
	if _, err := schema.MigrateUp(ctx, refusingCorrectionConn{DBConn: engine}); err == nil {
		t.Fatalf("re-run MigrateUp succeeded while dependencies.id still carries its default")
	}
	if has50 := markCount(); has50 != 0 {
		t.Fatalf("re-run recorded version 50 (%d rows) while the default survived", has50)
	}
}
