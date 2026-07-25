//go:build cgo

package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestProxiedServerCleanDatabasesPurgeDropped(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "purge")

	createDatabase := func(t *testing.T, name string) {
		t.Helper()
		bdProxiedSQL(t, bd, p.dir, "CREATE DATABASE `"+name+"`")
	}

	dropDatabase := func(t *testing.T, name string) {
		t.Helper()
		bdProxiedSQL(t, bd, p.dir, "DROP DATABASE `"+name+"`")
	}

	databaseExists := func(t *testing.T, name string) bool {
		t.Helper()
		rows := bdProxiedSQLJSON(t, bd, p.dir,
			"SELECT COUNT(*) as count FROM information_schema.schemata WHERE schema_name = '"+name+"'")
		return len(rows) == 1 && sqlValueEquals(rows[0]["count"], 1)
	}

	undropSucceeds := func(t *testing.T, name string) bool {
		t.Helper()
		_, _, err := bdProxiedRunBuffers(t, bd, p.dir, "sql", fmt.Sprintf("CALL DOLT_UNDROP('%s')", name))
		return err == nil
	}

	cleanDatabasesRun := func(t *testing.T, args ...string) string {
		t.Helper()
		full := append([]string{"dolt", "clean-databases"}, args...)
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, full...)
		if err != nil {
			t.Fatalf("bd dolt clean-databases %s failed: %v\nstdout:\n%s\nstderr:\n%s",
				strings.Join(args, " "), err, stdout, stderr)
		}
		return stdout
	}

	const (
		dryDB      = "testdb_purge_dry"
		controlDB  = "testdb_purge_control"
		purgedDB   = "testdb_purge_target"
		residueDB  = "testdb_purge_residue"
		purgedText = "Purged all dropped databases"
	)

	t.Run("dry_run_ignores_purge", func(t *testing.T) {
		createDatabase(t, dryDB)
		stdout := cleanDatabasesRun(t, "--dry-run", "--purge-dropped")

		if !strings.Contains(stdout, "--purge-dropped ignored") {
			t.Errorf("expected dry-run to report --purge-dropped ignored, got:\n%s", stdout)
		}
		if strings.Contains(stdout, purgedText) {
			t.Errorf("dry-run must not purge, got:\n%s", stdout)
		}
		if !databaseExists(t, dryDB) {
			t.Errorf("dry-run must not drop %s", dryDB)
		}
		dropDatabase(t, dryDB)
	})

	t.Run("without_flag_keeps_undrop_recovery", func(t *testing.T) {
		createDatabase(t, controlDB)
		stdout := cleanDatabasesRun(t)

		if !strings.Contains(stdout, "Dropped: "+controlDB) {
			t.Fatalf("expected %s to be dropped, got:\n%s", controlDB, stdout)
		}
		if !strings.Contains(stdout, "remain recoverable") {
			t.Errorf("expected the DOLT_UNDROP recoverability trailer, got:\n%s", stdout)
		}
		if strings.Contains(stdout, purgedText) {
			t.Errorf("no --purge-dropped was passed, but output claims a purge:\n%s", stdout)
		}
		if !undropSucceeds(t, controlDB) {
			t.Errorf("DOLT_UNDROP(%q) failed without --purge-dropped; the drop should still be recoverable", controlDB)
		}
		dropDatabase(t, controlDB)
	})

	t.Run("purge_removes_undrop_recovery", func(t *testing.T) {
		createDatabase(t, purgedDB)
		stdout := cleanDatabasesRun(t, "--purge-dropped")

		if !strings.Contains(stdout, "Dropped: "+purgedDB) {
			t.Fatalf("expected %s to be dropped, got:\n%s", purgedDB, stdout)
		}
		if !strings.Contains(stdout, purgedText) {
			t.Fatalf("expected a purge confirmation, got:\n%s", stdout)
		}
		if undropSucceeds(t, purgedDB) {
			t.Errorf("DOLT_UNDROP(%q) succeeded after --purge-dropped; the database was not purged", purgedDB)
		}
		if undropSucceeds(t, controlDB) {
			t.Errorf("DOLT_UNDROP(%q) succeeded; the purge should be server-global and take prior residue with it", controlDB)
		}
	})

	t.Run("zero_stale_still_purges_residue", func(t *testing.T) {
		createDatabase(t, residueDB)
		dropDatabase(t, residueDB)

		stdout := cleanDatabasesRun(t, "--purge-dropped")

		if !strings.Contains(stdout, "No stale databases found") {
			t.Fatalf("expected no stale databases on this run, got:\n%s", stdout)
		}
		if !strings.Contains(stdout, purgedText) {
			t.Fatalf("expected --purge-dropped to purge residue even with nothing stale, got:\n%s", stdout)
		}
		if undropSucceeds(t, residueDB) {
			t.Errorf("DOLT_UNDROP(%q) succeeded; residue was not purged", residueDB)
		}
	})
}
