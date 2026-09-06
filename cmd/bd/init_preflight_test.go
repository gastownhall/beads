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
	"strings"
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
			t.Run("metadata only", func(t *testing.T) {
				beadsDir := t.TempDir()
				t.Setenv("BEADS_DIR", beadsDir)
				t.Setenv("BEADS_DOLT_AUTO_START", "0")
				cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: mode, DoltDatabase: uniqueTestDBName(t), DoltServerHost: "127.0.0.1", DoltServerPort: port}
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
				metadataPath := configfile.ConfigPath(beadsDir)
				before, err := os.ReadFile(metadataPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := runInitReinitPreflight(true, "test", ""); err != nil {
					t.Errorf("metadata-only workspace with no database refused: %v", err)
				}
				if after, err := os.ReadFile(metadataPath); err != nil || !reflect.DeepEqual(before, after) {
					t.Errorf("preflight changed workspace metadata: %v", err)
				}
				if entries, err := os.ReadDir(beadsDir); err != nil || len(entries) != 1 {
					t.Errorf("preflight created workspace artifacts: %v, %v", entries, err)
				}
				if mode == "embedded" {
					t.Run("empty data directory", func(t *testing.T) {
						dataDir := filepath.Join(beadsDir, "embeddeddolt")
						if err := os.Mkdir(dataDir, 0o700); err != nil {
							t.Fatal(err)
						}
						if err := runInitReinitPreflight(true, "test", ""); err != nil {
							t.Errorf("empty data directory refused: %v", err)
						}
						if entries, err := os.ReadDir(dataDir); err != nil || len(entries) != 0 {
							t.Errorf("preflight created database artifacts: %v, %v", entries, err)
						}
					})
				}
				if mode == "server" {
					db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					var count int
					if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", cfg.DoltDatabase).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count != 0 {
						t.Fatal("preflight created the missing server database")
					}
				}
			})
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
					if err != nil || count.Count != tc.want || (len(count.UnknownTables) != 0) != tc.fail {
						t.Errorf("count = %+v, %v; want %d, unknown schema=%v", count, err, tc.want, tc.fail)
					}
					stderr := captureStderr(t, func() {
						err = runInitReinitPreflight(true, "different_requested_prefix", "")
					})
					if tc.fail && (!strings.Contains(stderr, "Existing issues: unknown") || !strings.Contains(stderr, `Existing tables: ["precious_data"]`)) {
						t.Errorf("missing unknown-schema warning and table list: %s", stderr)
					}
					if wantRefusal := tc.fail || tc.want > 0; (err != nil) != wantRefusal {
						t.Errorf("preflight = %v; want refusal=%v", err, wantRefusal)
					}
					if tc.want > 0 || tc.fail {
						if err := runInitReinitPreflight(true, "different_requested_prefix", FormatDestroyToken("wrong_prefix")); err == nil {
							t.Error("incorrect destroy token accepted")
						}
						if err := runInitReinitPreflight(true, "different_requested_prefix", FormatDestroyToken("different_requested_prefix")); err != nil {
							t.Errorf("explicit confirmation refused: %v", err)
						}
					}
					if mode == "server" && tc.name == "older populated issues" {
						t.Run("permission denied", func(t *testing.T) {
							admin, closeAdmin := open("")
							defer closeAdmin()
							if _, err := admin.ExecContext(t.Context(), "CREATE USER 'preflight_denied'@'%' IDENTIFIED BY ''"); err != nil {
								t.Fatal(err)
							}
							t.Setenv("BEADS_DOLT_SERVER_USER", "preflight_denied")
							err := runInitReinitPreflight(true, "test", FormatDestroyToken("test"))
							if err == nil || !strings.Contains(strings.ToLower(err.Error()), "access denied") {
								t.Fatalf("want permission refusal even with destroy token, got %v", err)
							}
						})
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
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runInitReinitPreflight(true, "test", ""); err != nil {
		t.Fatalf("pre-created empty workspace refused: %v", err)
	}
	if entries, err := os.ReadDir(beadsDir); err != nil || len(entries) != 0 {
		t.Fatalf("preflight changed empty workspace: %v, %v", entries, err)
	}
}

func TestInitReinitPreflightRefusesUnreadableEmbeddedDirectory(t *testing.T) {
	initConfigForTest(t)
	for _, name := range []string{"permission denied", "dangling symlink"} {
		t.Run(name, func(t *testing.T) {
			beadsDir := t.TempDir()
			t.Setenv("BEADS_DIR", beadsDir)
			cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: "embedded", DoltDatabase: "test"}
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			dataDir := filepath.Join(beadsDir, "embeddeddolt")
			if name == "dangling symlink" {
				if err := os.Symlink(filepath.Join(beadsDir, "missing"), dataDir); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else {
				if err := os.Mkdir(dataDir, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dataDir, 0o700) })
				if _, err := os.ReadDir(dataDir); err == nil {
					t.Skip("directory permissions not enforced for this user")
				}
			}
			if err := runInitReinitPreflight(true, "test", FormatDestroyToken("test")); err == nil {
				t.Fatal("unreadable storage accepted as absent even with a destroy token")
			}
		})
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
