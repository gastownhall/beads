//go:build cgo

package main

import (
	"context"
	"strings"
	"testing"
)

func TestProxiedServerMultiStatementSQL(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "ms")

	// A multi-statement batch is treated as a write: it executes and commits
	// atomically, and reports "OK" with no rows-affected count.
	batch := "CREATE TABLE ms_t (id INT PRIMARY KEY); INSERT INTO ms_t VALUES (1); INSERT INTO ms_t VALUES (2)"
	out, err := bdProxiedRun(t, bd, p.dir, "sql", batch)
	if err != nil {
		t.Fatalf("bd sql multi-statement failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != "OK" {
		t.Fatalf("multi-statement output = %q, want %q", got, "OK")
	}

	// Both inserts must have committed.
	db := openProxiedDB(t, p)
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM ms_t").Scan(&n); err != nil {
		t.Fatalf("count ms_t: %v", err)
	}
	if n != 2 {
		t.Fatalf("ms_t row count = %d, want 2 (multi-statement writes not committed)", n)
	}

	// A single write still reports the affected-row count.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "INSERT INTO ms_t VALUES (3)")
	if err != nil {
		t.Fatalf("bd sql single write failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "OK, 1 rows affected" {
		t.Fatalf("single-write output = %q, want %q", got, "OK, 1 rows affected")
	}

	// A single read still renders a result table.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "SELECT COUNT(*) AS c FROM ms_t")
	if err != nil {
		t.Fatalf("bd sql single read failed: %v\n%s", err, out)
	}
	if s := string(out); !strings.Contains(s, "c") || !strings.Contains(s, "3") {
		t.Fatalf("single-read output missing rendered result:\n%s", s)
	}

	// Multi-statement with --json reports a status object, not rows_affected.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "--json",
		"INSERT INTO ms_t VALUES (4); INSERT INTO ms_t VALUES (5)")
	if err != nil {
		t.Fatalf("bd sql --json multi-statement failed: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "\"status\"") || !strings.Contains(s, "ok") {
		t.Fatalf("multi-statement --json output = %q, want a status:ok object", s)
	}
	if strings.Contains(s, "rows_affected") {
		t.Fatalf("multi-statement --json should not include rows_affected: %q", s)
	}
}

func TestProxiedServerSQLDatabaseFlag(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "df")

	// Stand up a second database with a table, alongside the project database.
	db := openProxiedDB(t, p)
	for _, q := range []string{
		"CREATE DATABASE IF NOT EXISTS df_other",
		"USE df_other; CREATE TABLE widgets (id INT PRIMARY KEY, name VARCHAR(32))",
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
	}

	// A single write routed to the other database with --database.
	out, err := bdProxiedRun(t, bd, p.dir, "sql", "--database", "df_other",
		"INSERT INTO widgets VALUES (1, 'gear')")
	if err != nil {
		t.Fatalf("bd sql --database write failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "OK, 1 rows affected" {
		t.Fatalf("--database write output = %q, want %q", got, "OK, 1 rows affected")
	}

	// The write must have committed in df_other, not the project database.
	var n int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM df_other.widgets WHERE name = 'gear'").Scan(&n); err != nil {
		t.Fatalf("count df_other.widgets: %v", err)
	}
	if n != 1 {
		t.Fatalf("df_other.widgets rows = %d, want 1 (--database write not committed there)", n)
	}

	// A read routed with --database against an unqualified table name resolves
	// in the switched database and renders results.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "--database", "df_other",
		"SELECT name FROM widgets WHERE id = 1")
	if err != nil {
		t.Fatalf("bd sql --database read failed: %v\n%s", err, out)
	}
	if s := string(out); !strings.Contains(s, "gear") {
		t.Fatalf("--database read output missing 'gear':\n%s", s)
	}

	// An invalid database identifier is rejected, not interpolated.
	out, err = bdProxiedRun(t, bd, p.dir, "sql", "--database", "bad;name",
		"SELECT 1")
	if err == nil {
		t.Fatalf("expected --database with invalid identifier to fail; got:\n%s", out)
	}
}
