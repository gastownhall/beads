//go:build cgo

package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// End-to-end contract for the storage-class plumbing (bd-8rifr,
// Protocol v0.1 §C): create-time resolution (flag > per-type config >
// unset), the omitted-when-versioned marker rule (C2.4) down to the DB
// cell, and the ephemeral spelling routing to the wisp plane (C1.4).
func TestEmbeddedCreateStorageClass(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// storageClassCell reads the raw issues.storage_class cell so the test
	// pins the at-rest form (NULL vs literal), not just the JSON view.
	storageClassCell := func(t *testing.T, beadsDir, prefix, id string) sql.NullString {
		t.Helper()
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), prefix, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var got sql.NullString
		if err := db.QueryRowContext(t.Context(), "SELECT storage_class FROM issues WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("query storage_class: %v", err)
		}
		return got
	}

	t.Run("explicit_unversioned", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "su")
		issue := bdCreate(t, bd, dir, "Unversioned bead", "--storage-class", "unversioned")
		if issue.StorageClass != types.StorageClassUnversioned {
			t.Errorf("JSON storage_class: got %q, want unversioned", issue.StorageClass)
		}
		cell := storageClassCell(t, beadsDir, "su", issue.ID)
		if !cell.Valid || cell.String != "unversioned" {
			t.Errorf("DB cell: got %+v, want 'unversioned'", cell)
		}
	})

	t.Run("default_and_explicit_versioned_stay_null", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sv")
		plain := bdCreate(t, bd, dir, "Plain bead")
		if plain.StorageClass != "" {
			t.Errorf("plain create: storage_class should be unset, got %q", plain.StorageClass)
		}
		if cell := storageClassCell(t, beadsDir, "sv", plain.ID); cell.Valid {
			t.Errorf("plain create: DB cell should be NULL, got %q", cell.String)
		}
		// C2.4: the explicit versioned spelling normalizes to the same NULL.
		explicit := bdCreate(t, bd, dir, "Versioned bead", "--storage-class", "versioned")
		if cell := storageClassCell(t, beadsDir, "sv", explicit.ID); cell.Valid {
			t.Errorf("explicit versioned: DB cell should be NULL, got %q", cell.String)
		}
	})

	t.Run("per_type_config_default_and_override", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sc")
		if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "storage-class.task", "unversioned"); err != nil {
			t.Fatalf("config set: %v\n%s", err, out)
		}
		byDefault := bdCreate(t, bd, dir, "Defaulted bead", "-t", "task")
		if byDefault.StorageClass != types.StorageClassUnversioned {
			t.Errorf("config default: got %q, want unversioned", byDefault.StorageClass)
		}
		// Explicit per-record declaration wins over the per-type default (C1.3).
		forced := bdCreate(t, bd, dir, "Forced versioned", "-t", "task", "--storage-class", "versioned")
		if cell := storageClassCell(t, beadsDir, "sc", forced.ID); cell.Valid {
			t.Errorf("explicit versioned over config default: DB cell should be NULL, got %q", cell.String)
		}
		// Other types are untouched by the task default.
		bug := bdCreate(t, bd, dir, "A bug", "-t", "bug")
		if bug.StorageClass != "" {
			t.Errorf("other type: got %q, want unset", bug.StorageClass)
		}
	})

	t.Run("ephemeral_spelling_routes_to_wisp_plane", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "se")
		issue := bdCreate(t, bd, dir, "Ephemeral bead", "--storage-class", "ephemeral")
		if !issue.Ephemeral {
			t.Errorf("--storage-class ephemeral should set the ephemeral flag")
		}
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "se", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM wisps WHERE id = ?", issue.ID).Scan(&count); err != nil {
			t.Fatalf("query wisps: %v", err)
		}
		if count != 1 {
			t.Errorf("ephemeral bead should live in wisps, found %d rows", count)
		}
	})

	// An explicit durable class combined with a wisp-plane flag is a direct
	// command-line contradiction: the row would be ephemeral by construction, so
	// honoring the request is impossible. It must be rejected up front, not
	// silently collapsed into an effective-ephemeral record (Protocol v0.1 §C1.3).
	t.Run("explicit_versioned_with_ephemeral_is_rejected", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sf")
		out := bdCreateFail(t, bd, dir, "Conflicting bead", "--ephemeral", "--storage-class", "versioned")
		if !strings.Contains(out, "conflicts with --ephemeral/--no-history") {
			t.Errorf("expected wisp-plane conflict error, got:\n%s", out)
		}
	})

	t.Run("explicit_versioned_with_no_history_is_rejected", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sg")
		out := bdCreateFail(t, bd, dir, "Conflicting bead", "--no-history", "--storage-class", "versioned")
		if !strings.Contains(out, "conflicts with --ephemeral/--no-history") {
			t.Errorf("expected wisp-plane conflict error, got:\n%s", out)
		}
	})

	// A per-type config default is not an explicit contradiction the way the
	// flag pair above is: flag > config (Protocol v0.1 §C1.3), so a background
	// storage-class.task=unversioned must yield to an explicit wisp-plane flag
	// rather than reaching the storage-layer backstop and blocking the create.
	// bdCreate fails the test on a non-zero exit, so a returned issue is itself
	// proof the backstop did not reject the config-derived durable class.
	t.Run("config_default_unversioned_yields_to_ephemeral", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sy")
		if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "storage-class.task", "unversioned"); err != nil {
			t.Fatalf("config set: %v\n%s", err, out)
		}
		issue := bdCreate(t, bd, dir, "Ephemeral over config", "-t", "task", "--ephemeral")
		if !issue.Ephemeral {
			t.Errorf("--ephemeral over config default should set the ephemeral flag")
		}
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "sy", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM wisps WHERE id = ?", issue.ID).Scan(&count); err != nil {
			t.Fatalf("query wisps: %v", err)
		}
		if count != 1 {
			t.Errorf("ephemeral bead should live in wisps, found %d rows", count)
		}
	})

	t.Run("config_default_unversioned_yields_to_no_history", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sz")
		if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "storage-class.task", "unversioned"); err != nil {
			t.Fatalf("config set: %v\n%s", err, out)
		}
		// --no-history is a wisp-plane record (a non-GC-eligible wisps-table row),
		// so the config-derived durable class must yield rather than block: the
		// create succeeds, the JSON class is unset, and the durable 'unversioned'
		// never reaches the at-rest wisp row.
		issue := bdCreate(t, bd, dir, "No-history over config", "-t", "task", "--no-history")
		if issue.StorageClass != "" {
			t.Errorf("no-history over config default: storage_class should be unset, got %q", issue.StorageClass)
		}
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "sz", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var cell sql.NullString
		if err := db.QueryRowContext(t.Context(), "SELECT storage_class FROM wisps WHERE id = ?", issue.ID).Scan(&cell); err != nil {
			t.Fatalf("query wisps storage_class: %v", err)
		}
		if cell.Valid && cell.String == string(types.StorageClassUnversioned) {
			t.Errorf("config-derived unversioned leaked to the no-history wisp row: %q", cell.String)
		}
	})

	t.Run("config_set_validates_value", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sx")
		out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "storage-class.task", "permanent")
		if err == nil {
			t.Fatalf("config set with bad value should fail, got:\n%s", out)
		}
		if !strings.Contains(string(out), "versioned, unversioned, or ephemeral") {
			t.Errorf("error should enumerate valid values, got:\n%s", out)
		}
	})
}
