//go:build cgo

package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func bdProxiedSQL(t *testing.T, bd, dir string, args ...string) (string, string) {
	t.Helper()
	fullArgs := append([]string{"sql"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd sql %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout, stderr
}

func bdProxiedSQLJSON(t *testing.T, bd, dir string, query string) []map[string]interface{} {
	t.Helper()
	stdout, _ := bdProxiedSQL(t, bd, dir, "--json", query)
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("no JSON array found in sql output:\n%s", stdout)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(stdout[start:]), &rows); err != nil {
		t.Fatalf("failed to parse sql JSON output: %v\nraw: %s", err, stdout[start:])
	}
	return rows
}

func bdProxiedSQLFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"sql"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd sql %s to fail, but it succeeded:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func TestProxiedServerSQL(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "sql")

	one := bdProxiedCreate(t, bd, p.dir, "Test issue one", "--type", "task", "--priority", "1")
	two := bdProxiedCreate(t, bd, p.dir, "Test issue two", "--type", "bug", "--priority", "2")
	bdProxiedClose(t, bd, p.dir, two.ID)

	t.Run("select_count", func(t *testing.T) {
		rows := bdProxiedSQLJSON(t, bd, p.dir, "SELECT COUNT(*) as count FROM issues")
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		count, ok := rows[0]["count"]
		if !ok {
			t.Fatal("missing 'count' column in result")
		}
		if !sqlValueEquals(count, 2) {
			t.Errorf("expected count=2, got %v", count)
		}
	})

	t.Run("select_with_filter", func(t *testing.T) {
		rows := bdProxiedSQLJSON(t, bd, p.dir, `SELECT id, title FROM issues WHERE status = 'open'`)
		if len(rows) != 1 {
			t.Fatalf("expected 1 open issue, got %d", len(rows))
		}
		if rows[0]["title"] != "Test issue one" {
			t.Errorf("expected 'Test issue one', got %q", rows[0]["title"])
		}
		if rows[0]["id"] != one.ID {
			t.Errorf("expected id %q, got %q", one.ID, rows[0]["id"])
		}
	})

	t.Run("empty_result_json", func(t *testing.T) {
		rows := bdProxiedSQLJSON(t, bd, p.dir, `SELECT * FROM issues WHERE title = 'nonexistent'`)
		if len(rows) != 0 {
			t.Errorf("expected 0 rows, got %d", len(rows))
		}
	})

	t.Run("table_output", func(t *testing.T) {
		stdout, _ := bdProxiedSQL(t, bd, p.dir, "SELECT COUNT(*) as count FROM issues")
		if !strings.Contains(stdout, "count") {
			t.Errorf("expected table header 'count' in output: %s", stdout)
		}
		if !strings.Contains(stdout, "(1 rows)") {
			t.Errorf("expected '(1 rows)' in output: %s", stdout)
		}
	})

	t.Run("empty_table_output", func(t *testing.T) {
		stdout, _ := bdProxiedSQL(t, bd, p.dir, `SELECT * FROM issues WHERE title = 'nonexistent'`)
		if !strings.Contains(stdout, "(0 rows)") {
			t.Errorf("expected '(0 rows)' in output: %s", stdout)
		}
	})

	t.Run("csv_output", func(t *testing.T) {
		stdout, _ := bdProxiedSQL(t, bd, p.dir, "--csv", `SELECT id, title FROM issues WHERE status = 'open'`)
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(lines) != 2 {
			t.Fatalf("expected header + 1 data row, got %d lines:\n%s", len(lines), stdout)
		}
		if lines[0] != "id,title" {
			t.Errorf("expected CSV header 'id,title', got %q", lines[0])
		}
		if !strings.Contains(lines[1], one.ID) || !strings.Contains(lines[1], "Test issue one") {
			t.Errorf("expected CSV row with open issue, got %q", lines[1])
		}
	})

	t.Run("exec_update_reports_rows_affected", func(t *testing.T) {
		stdout, _ := bdProxiedSQL(t, bd, p.dir,
			"--json", "UPDATE issues SET priority = 3 WHERE id = '"+one.ID+"'")
		var res map[string]interface{}
		start := strings.Index(stdout, "{")
		if start < 0 {
			t.Fatalf("no JSON object in exec output:\n%s", stdout)
		}
		if err := json.Unmarshal([]byte(stdout[start:]), &res); err != nil {
			t.Fatalf("parse exec JSON: %v\nraw: %s", err, stdout[start:])
		}
		if !sqlValueEquals(res["rows_affected"], 1) {
			t.Errorf("expected rows_affected=1, got %v", res["rows_affected"])
		}

		rows := bdProxiedSQLJSON(t, bd, p.dir,
			"SELECT priority FROM issues WHERE id = '"+one.ID+"'")
		if len(rows) != 1 {
			t.Fatalf("expected 1 row after update, got %d", len(rows))
		}
		if !sqlValueEquals(rows[0]["priority"], 3) {
			t.Errorf("expected priority=3 after update, got %v", rows[0]["priority"])
		}
	})

	t.Run("exec_persists_across_connections", func(t *testing.T) {
		bdProxiedSQL(t, bd, p.dir,
			"UPDATE issues SET priority = 4 WHERE id = '"+two.ID+"'")

		db := openProxiedDB(t, p)
		var priority int
		if err := db.QueryRow(
			"SELECT priority FROM issues WHERE id = ?", two.ID).Scan(&priority); err != nil {
			t.Fatalf("query priority for %s: %v", two.ID, err)
		}
		if priority != 4 {
			t.Errorf("expected committed priority=4, got %d", priority)
		}
	})

	t.Run("invalid_sql_fails", func(t *testing.T) {
		out := bdProxiedSQLFail(t, bd, p.dir, "SELECT * FROM nonexistent_table")
		if !strings.Contains(strings.ToLower(out), "error") {
			t.Errorf("expected error for invalid SQL, got: %s", out)
		}
	})
}

func sqlValueEquals(v any, want float64) bool {
	switch x := v.(type) {
	case float64:
		return x == want
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return err == nil && f == want
	default:
		return false
	}
}
