package dolt

import (
	"database/sql"
	"os"
	"testing"
)

func TestDoltStoredGeneratedColumnStaleAfterFKCascade(t *testing.T) {
	if os.Getenv("BEADS_RUN_DOLT_UPSTREAM_REPRO") == "" {
		t.Skip("set BEADS_RUN_DOLT_UPSTREAM_REPRO=1 to reproduce the Dolt stored generated-column FK cascade bug")
	}

	// MySQL rejects ON UPDATE CASCADE on a base column referenced by a stored
	// generated column. Dolt currently accepts the schema and executes the FK
	// cascade, so this test documents the resulting consistency bug: the base
	// column updates to the new value, but the stored generated column remains
	// at the old value.
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := store.db.ExecContext(ctx, `
		CREATE TABLE repro_generated_parent (
			id VARCHAR(255) PRIMARY KEY
		)
	`); err != nil {
		t.Fatalf("create parent table: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TABLE repro_generated_child (
			source_id VARCHAR(255) NOT NULL,
			target_issue_id VARCHAR(255) NULL,
			target_wisp_id VARCHAR(255) NULL,
			target_id VARCHAR(255) AS (COALESCE(target_issue_id, target_wisp_id)) STORED,
			PRIMARY KEY (source_id, target_id),
			CONSTRAINT fk_repro_generated_target FOREIGN KEY (target_issue_id)
				REFERENCES repro_generated_parent(id) ON UPDATE CASCADE
		)
	`); err != nil {
		t.Fatalf("create child table: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO repro_generated_parent (id) VALUES ('old-target')
	`); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO repro_generated_child (source_id, target_issue_id)
		VALUES ('source', 'old-target')
	`); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `
		UPDATE repro_generated_parent SET id = 'new-target' WHERE id = 'old-target'
	`); err != nil {
		t.Fatalf("rename parent via FK cascade: %v", err)
	}

	var splitTarget, generatedTarget sql.NullString
	if err := store.db.QueryRowContext(ctx, `
		SELECT target_issue_id, target_id
		FROM repro_generated_child
		WHERE source_id = 'source'
	`).Scan(&splitTarget, &generatedTarget); err != nil {
		t.Fatalf("query child: %v", err)
	}

	if !splitTarget.Valid || splitTarget.String != "new-target" {
		t.Fatalf("target_issue_id = %v after FK cascade, want new-target", splitTarget)
	}
	if !generatedTarget.Valid || generatedTarget.String != "new-target" {
		t.Fatalf("target_id = %v after FK cascade, want new-target", generatedTarget)
	}
}
