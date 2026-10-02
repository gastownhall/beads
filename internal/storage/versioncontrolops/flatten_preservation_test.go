package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestFlattenRefusesUnsafeBase(t *testing.T) {
	for _, name := range []string{"branch", "ambiguous", "nonempty"} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			branch, reason := "main", "one ancestry root"
			if name == "branch" {
				branch, reason = "other", "active branch main"
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT active_branch()")).
				WillReturnRows(sqlmock.NewRows([]string{"branch"}).AddRow(branch))
			if name != "branch" {
				mock.ExpectQuery("SELECT COUNT.*FROM dolt_log").
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
				roots := sqlmock.NewRows([]string{"root"}).AddRow("root")
				if name == "ambiguous" {
					roots.AddRow("other root")
				}
				mock.ExpectQuery(regexp.QuoteMeta(flattenRootQuery)).WillReturnRows(roots)
				if name == "nonempty" {
					reason = "is not empty"
					mock.ExpectQuery("SHOW TABLES AS OF").WithArgs("root").
						WillReturnRows(sqlmock.NewRows([]string{"table"}).AddRow("config"))
				}
			}
			if err := Flatten(context.Background(), db); err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("Flatten error = %v; want %q before any mutation", err, reason)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFlattenFailureRetainsCheckpoint(t *testing.T) {
	for _, failCheckpoint := range []bool{false, true} {
		t.Run(fmt.Sprintf("checkpoint=%v", failCheckpoint), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			failure := errors.New("injected commit failure")
			mock.ExpectQuery(regexp.QuoteMeta("SELECT active_branch()")).
				WillReturnRows(sqlmock.NewRows([]string{"branch"}).AddRow("main"))
			mock.ExpectQuery("SELECT COUNT.*FROM dolt_log").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
			mock.ExpectQuery(regexp.QuoteMeta(flattenRootQuery)).
				WillReturnRows(sqlmock.NewRows([]string{"root"}).AddRow("root"))
			mock.ExpectQuery("SHOW TABLES AS OF").WithArgs("root").
				WillReturnRows(sqlmock.NewRows([]string{"table"}))
			mock.ExpectQuery("SELECT COUNT.*FROM dolt_status s").
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			checkpoint := mock.ExpectExec("CALL DOLT_COMMIT.*checkpoint pending changes")
			if failCheckpoint {
				checkpoint.WillReturnError(failure)
			} else {
				checkpoint.WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT DOLT_HASHOF('HEAD')")).
					WillReturnRows(sqlmock.NewRows([]string{"head"}).AddRow("checkpoint"))
				mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', ?)")).WithArgs("root").
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("CALL DOLT_COMMIT.*squash all history").WillReturnError(failure)
				mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', ?)")).WithArgs("checkpoint").
					WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if err := Flatten(context.Background(), db); !errors.Is(err, failure) {
				t.Fatalf("Flatten error = %v; want original commit failure", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// This persistence regression needs a separate database on the disposable test
// server: Flatten rewrites main, so branch-per-test isolation is insufficient.
func TestFlattenPreservesWorkingState(t *testing.T) {
	testutil.RequireDoltBinary(t)
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_PASSWORD", "")
	beadsDir := t.TempDir()
	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := doltserver.Stop(beadsDir); err != nil {
			t.Error(err)
		}
	})
	admin, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/", state.Port))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, orphan := range []bool{false, true} {
		t.Run(fmt.Sprintf("orphan=%v", orphan), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			name := fmt.Sprintf("flatten_test_%v", orphan)
			_, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"`")
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/%s", state.Port, name))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = admin.ExecContext(context.Background(), "DROP DATABASE `"+name+"`")
				db.Close()
			}()
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := conn.ExecContext(ctx, q, args...); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			// An older timestamp on a descendant must not make it the root.
			exec("CREATE TABLE config (`key` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL)")
			exec("CALL DOLT_COMMIT('-Am', 'seed config')")
			exec("CALL DOLT_COMMIT('--allow-empty', '-m', 'backdated descendant', '--date', '2000-01-01T00:00:00Z')")
			// Three ignored tables with clone-local FKs, including an existing
			// orphan. Re-adding FKs by deleting orphans is not preservation.
			exec("CREATE TABLE flatten_parent (id INT PRIMARY KEY)")
			exec("INSERT INTO flatten_parent VALUES (1)")
			for i := range 3 {
				table := fmt.Sprintf("flatten_local_%d", i)
				exec("CREATE TABLE " + table + " (id INT PRIMARY KEY, parent_id INT, CONSTRAINT fk_" + table + " FOREIGN KEY (parent_id) REFERENCES flatten_parent(id))")
				exec("INSERT INTO dolt_ignore VALUES (?, true)", table)
				exec("INSERT INTO " + table + " VALUES (1, 1)")
			}
			exec("CALL DOLT_COMMIT('-Am', 'seed preservation fixture')")
			if orphan {
				exec("SET foreign_key_checks = 0")
				exec("INSERT INTO flatten_local_0 VALUES (2, 999)")
				exec("SET foreign_key_checks = 1")
			}
			for i := range 4 {
				exec("INSERT INTO config (`key`, value) VALUES (?, ?)", fmt.Sprintf("kv.memory.flatten-%d", i), fmt.Sprintf("pending memory %d", i))
			}
			before := flattenWorkingSnapshot(t, ctx, conn)
			if err := Flatten(ctx, conn); err != nil {
				t.Fatal(err)
			}
			after := flattenWorkingSnapshot(t, ctx, conn)
			if !reflect.DeepEqual(before, after) {
				if len(before) != len(after) {
					t.Errorf("table count changed: %d -> %d", len(before), len(after))
				}
				for table, want := range before {
					if !reflect.DeepEqual(want, after[table]) {
						t.Errorf("%s changed:\nbefore %v\nafter %v", table, want, after[table])
					}
				}
			}
			var commits int
			if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&commits); err != nil || commits != 2 {
				t.Fatalf("history = %d commits, err %v; want empty root + snapshot", commits, err)
			}
		})
	}
}

// Compare every application table's SHOW CREATE and complete row multiset,
// including Dolt-ignored tables. No selected-column projection can hide loss.
func flattenWorkingSnapshot(t *testing.T, ctx context.Context, conn *sql.Conn) map[string][]string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SHOW TABLES")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	result := make(map[string][]string)
	for _, table := range tables {
		ddl := flattenQuerySnapshot(t, ctx, conn, "SHOW CREATE TABLE `"+table+"`")
		data := flattenQuerySnapshot(t, ctx, conn, "SELECT * FROM `"+table+"`")
		result[table] = append(ddl, data...)
	}
	return result
}

func flattenQuerySnapshot(t *testing.T, ctx context.Context, conn *sql.Conn, query string) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var data []string
	for rows.Next() {
		values := make([]sql.RawBytes, len(cols))
		args := make([]any, len(cols))
		for i := range values {
			args[i] = &values[i]
		}
		if err := rows.Scan(args...); err != nil {
			t.Fatal(err)
		}
		text := make([]any, len(values))
		for i, value := range values {
			if value != nil {
				text[i] = string(value)
			}
		}
		data = append(data, fmt.Sprintf("%#v", text))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	sort.Strings(data)
	return data
}
