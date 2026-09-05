//go:build cgo

package main

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// Exercise the real open boundary: a fake store cannot detect migrations
// started while opening it, before the issue count is even requested.
func TestInitReinitPreflightReadOnly(t *testing.T) {
	initConfigForTest(t)
	for _, key := range []string{"BEADS_DOLT_SHARED_SERVER", "BEADS_SHARED_SERVER_DIR", "BEADS_DOLT_DATA_DIR", "BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_PASSWORD", "GT_ROOT"} {
		t.Setenv(key, "")
	}
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_USER", "root")
	for _, mode := range []string{"embedded", "server"} {
		t.Run(mode, func(t *testing.T) {
			port := 0
			if mode == "server" {
				if _, err := exec.LookPath("dolt"); err != nil {
					t.Skip("native Dolt binary required")
				}
				serverDir := t.TempDir()
				port = unusedInitPreflightPort(t)
				t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
				if _, err := doltserver.Start(serverDir); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := doltserver.Stop(serverDir); err != nil {
						t.Errorf("stop owned server: %v", err)
					}
				})
			}
			for _, tc := range []struct {
				name string
				sql  []string
				want int
				fail bool
			}{
				{name: "zero tables"},
				{name: "older empty issues", sql: []string{"CREATE TABLE issues (id VARCHAR(64) PRIMARY KEY)"}},
				{name: "older populated issues", sql: []string{"CREATE TABLE issues (id VARCHAR(64) PRIMARY KEY)", "INSERT INTO issues VALUES ('keep-1'), ('keep-2')"}, want: 2},
				{name: "wisps only", sql: []string{"CREATE TABLE issues (id VARCHAR(64) PRIMARY KEY)", "CREATE TABLE wisps (id VARCHAR(64) PRIMARY KEY)", "INSERT INTO wisps VALUES ('keep-wisp')"}, want: 1},
				{name: "unrecognized schema", sql: []string{"CREATE TABLE precious_data (id INT PRIMARY KEY)", "INSERT INTO precious_data VALUES (1)"}, fail: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					beadsDir := t.TempDir()
					t.Setenv("BEADS_DIR", beadsDir)
					t.Setenv("BEADS_DOLT_AUTO_START", "0")
					cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: mode, DoltDatabase: uniqueTestDBName(t), DoltServerHost: "127.0.0.1", DoltServerPort: port}
					if err := cfg.Save(beadsDir); err != nil {
						t.Fatal(err)
					}
					open := func(database string) (*sql.DB, func() error) {
						t.Helper()
						if mode == "server" {
							db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/%s", port, database))
							if err != nil {
								t.Fatal(err)
							}
							return db, db.Close
						}
						dataDir := filepath.Join(beadsDir, "embeddeddolt")
						if err := os.MkdirAll(dataDir, 0o700); err != nil {
							t.Fatal(err)
						}
						db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
						if err != nil {
							t.Fatal(err)
						}
						return db, cleanup
					}
					admin, closeAdmin := open("")
					_, err := admin.ExecContext(t.Context(), "CREATE DATABASE `"+cfg.DoltDatabase+"`")
					if closeErr := closeAdmin(); closeErr != nil {
						t.Fatal(closeErr)
					}
					if err != nil {
						t.Fatal(err)
					}
					db, cleanup := open(cfg.DoltDatabase)
					for _, statement := range tc.sql {
						if _, err := db.ExecContext(t.Context(), statement); err != nil {
							t.Fatal(err)
						}
					}
					before := initPreflightState(t, db)
					if err := cleanup(); err != nil {
						t.Fatal(err)
					}
					count, err := countExistingIssues("different_requested_prefix")
					if (err != nil) != tc.fail || (!tc.fail && count != tc.want) {
						t.Errorf("count = %d, %v; want %d, error=%v", count, err, tc.want, tc.fail)
					}
					err = runInitReinitPreflight(true, "different_requested_prefix", "")
					if wantRefusal := tc.fail || tc.want > 0; (err != nil) != wantRefusal {
						t.Errorf("preflight = %v; want refusal=%v", err, wantRefusal)
					}
					if tc.want > 0 {
						if err := runInitReinitPreflight(true, "different_requested_prefix", FormatDestroyToken("different_requested_prefix")); err != nil {
							t.Errorf("explicit confirmation refused: %v", err)
						}
					}
					db, cleanup = open(cfg.DoltDatabase)
					defer cleanup()
					if after := initPreflightState(t, db); !reflect.DeepEqual(before, after) {
						t.Errorf("preflight changed schema, data, or history (%d state entries before, %d after)", len(before), len(after))
					}
				})
			}
		})
	}
}

func TestInitReinitPreflightRefusesUnreadableStore(t *testing.T) {
	initConfigForTest(t)
	t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	for _, metadata := range []string{
		`{broken`,
		fmt.Sprintf(`{"backend":"dolt","dolt_mode":"server","dolt_database":"unreachable","dolt_server_host":"127.0.0.1","dolt_server_port":%d}`, unusedInitPreflightPort(t)),
	} {
		t.Run(metadata, func(t *testing.T) {
			beadsDir := t.TempDir()
			t.Setenv("BEADS_DIR", beadsDir)
			t.Setenv("BEADS_DOLT_AUTO_START", "0")
			writeFile(t, filepath.Join(beadsDir, "metadata.json"), []byte(metadata))
			if err := runInitReinitPreflight(true, "test", FormatDestroyToken("test")); err == nil {
				t.Fatal("unreadable database must not be treated as empty, even with a destroy token")
			}
		})
	}
}

func TestInitReinitPreflightFreshWorkspace(t *testing.T) {
	initConfigForTest(t)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	t.Setenv("BEADS_DIR", beadsDir)
	if err := runInitReinitPreflight(true, "test", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
		t.Fatalf("preflight created workspace artifacts: %v", err)
	}
}

func unusedInitPreflightPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func initPreflightState(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT CONCAT(table_name, ':', column_name, ':', column_type) FROM information_schema.columns WHERE table_schema = DATABASE() ORDER BY table_name, ordinal_position")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var schema []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		schema = append(schema, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var working, head string
	if err := db.QueryRowContext(t.Context(), "SELECT DOLT_HASHOF_DB(), DOLT_HASHOF('HEAD')").Scan(&working, &head); err != nil {
		t.Fatal(err)
	}
	schema = append(schema, "working:"+working, "head:"+head)
	return schema
}
