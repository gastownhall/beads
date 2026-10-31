//go:build cgo && integration

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/testutil"
)

// startForeignDoltServer runs a dolt sql-server bd does not run, over a data
// directory of its own, as docs/architecture/dolt.md's Server Mode section
// shows, and returns its port. It is killed when the test ends.
func startForeignDoltServer(t *testing.T) int {
	t.Helper()
	testutil.RequireDoltBinary(t)
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	// #nosec G204 -- fixed args, no user input
	cmd := exec.Command("dolt", "sql-server", "-H", "127.0.0.1", "-P", strconv.Itoa(port), "--data-dir", t.TempDir())
	cmd.Env = append(os.Environ(), "HOME="+home, "DOLT_ROOT_PATH="+home)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if !testutil.WaitForServer(port, 20*time.Second) {
		t.Fatal("dolt sql-server did not come up")
	}
	return port
}

// foreignServerIssueTitles lists the titles of db's issues on the server at
// port, and reports whether the server holds db at all.
func foreignServerIssueTitles(t *testing.T, port int, db string) ([]string, bool) {
	t.Helper()
	conn, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.Query("SHOW DATABASES")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		found = found || name == db
	}
	rows.Close()
	if !found {
		return nil, false
	}
	r, err := conn.Query(fmt.Sprintf("SELECT title FROM `%s`.issues ORDER BY title", db))
	if err != nil {
		t.Fatalf("read issues of %s: %v", db, err)
	}
	defer r.Close()
	var titles []string
	for r.Next() {
		var title string
		_ = r.Scan(&title)
		titles = append(titles, title)
	}
	return titles, true
}

// TestE2E_BootstrapProvisionsTheWorkspaceServer runs bd bootstrap in a
// server-mode workspace (metadata.json dolt_mode server, the port named by
// its dolt_server_port or by its config.yaml dolt.port) whose server, one bd
// does not run, lacks the database, with a git-tracked issues.jsonl and no
// Dolt remote. Main's jsonl-import and init actions built an embedded store
// config, so bootstrap imported into .beads/embeddeddolt and exited 0 while
// the server still lacked the database, and every later command failed with
// "database not found". The actions now create the database on the
// workspace's server and write there. A database the server already holds
// is left as it is: the plan can reach these actions with nothing on this
// machine tying that database to the workspace, and the tracked copy must
// not overwrite live rows. The restore action refuses in server mode before
// creating anything (main exited 1 too, after creating .beads/embeddeddolt),
// and over a database the server holds names no --force restore.
// An embedded workspace imports into .beads/embeddeddolt as before.
// A first bootstrap whose start of bd's own server fails after dolt init in
// .beads/dolt leaves the version witness behind it, so the rerun is not
// refused as a legacy workspace; a server workspace whose .beads/dolt holds
// data and no witness still is, and bootstrap records no witness there.
func TestE2E_BootstrapProvisionsTheWorkspaceServer(t *testing.T) {
	testutil.RequireDoltBinary(t)
	bd := buildBDForInitTests(t)
	tracked := `{"_type":"issue","id":"jp-a1","title":"tracked one","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
{"_type":"issue","id":"jp-a2","title":"tracked two","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
`
	for _, tc := range []struct {
		name        string
		metaPort    bool // dolt_server_port in metadata.json, else dolt.port in .beads/config.yaml
		projectID   bool
		jsonl       bool
		backup      bool
		embedded    bool
		ownServer   bool // no port named anywhere: bd's own server, which the store starts over .beads/dolt
		serverHeld  bool // the server already holds the database, with a live issue
		failedStart bool // an earlier bd bootstrap's start of bd's own server failed after dolt init in .beads/dolt
		legacyRoot  bool // .beads/dolt holds data and there is no version witness: a pre-1.0 server workspace
		wantExit    int
		wantTitles  []string // on the server, and in bd list afterwards; nil: the database is absent there
		wantOut     string   // in bootstrap's output, when set
	}{
		{name: "jsonl-import, metadata port, project_id", metaPort: true, projectID: true, jsonl: true, wantTitles: []string{"tracked one", "tracked two"}},
		{name: "jsonl-import, config.yaml port, no project_id", jsonl: true, wantTitles: []string{"tracked one", "tracked two"}},
		{name: "init, config.yaml port, no project_id", wantTitles: []string{}},
		{name: "jsonl-import, server already holds the database", metaPort: true, projectID: true, jsonl: true, serverHeld: true, wantTitles: []string{"live on the server"}},
		{name: "restore, config.yaml port", jsonl: true, backup: true, wantExit: 1, wantOut: "once database"},
		{name: "restore, server already holds the database", projectID: true, backup: true, serverHeld: true, wantExit: 1, wantTitles: []string{"live on the server"}, wantOut: "did not restore"},
		{name: "jsonl-import, bd's own server, no port named", ownServer: true, jsonl: true, wantTitles: []string{"tracked one", "tracked two"}},
		{name: "jsonl-import, bd's own server, after a first start that failed", ownServer: true, jsonl: true, failedStart: true, wantTitles: []string{"tracked one", "tracked two"}},
		{name: "legacy server workspace, no version witness", ownServer: true, jsonl: true, legacyRoot: true, wantExit: 1, wantOut: "legacy Dolt server workspace"},
		{name: "embedded jsonl-import", embedded: true, jsonl: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.failedStart && runtime.GOOS == windowsOS {
				t.Skip("the dolt shim is a POSIX shell script")
			}
			// A server of the row's own: bootstrap's metadata reconcile adopts
			// the only Beads database on a server for a workspace with no
			// project_id, so rows sharing one would see each other's.
			port := 0
			if !tc.ownServer && !tc.embedded {
				port = startForeignDoltServer(t)
			}
			root := t.TempDir()
			home := t.TempDir()
			dir := filepath.Join(root, "ws")
			beadsDir := filepath.Join(dir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.ownServer {
				stopDoltServerCleanup(t, beadsDir)
			}
			db := fmt.Sprintf("jec_%d", time.Now().UnixNano()%1000000000)
			env := append(removedBackendTestEnv(beadsDir), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
				"BD_DISABLE_METRICS=1", "DO_NOT_TRACK=1", "BD_NON_INTERACTIVE=1", "GIT_TERMINAL_PROMPT=0")
			if tc.serverHeld {
				src := filepath.Join(root, "src")
				if err := os.MkdirAll(src, 0o700); err != nil {
					t.Fatal(err)
				}
				_ = runCommandInDir(src, "git", "init", "-q")
				srcEnv := append(removedBackendTestEnv(filepath.Join(src, ".beads")), "HOME="+home, "BD_DISABLE_METRICS=1", "BD_NON_INTERACTIVE=1")
				out, err := runBDExecWithBinary(t, bd, src, srcEnv, "init", "--server", "--server-port", strconv.Itoa(port), "--prefix", db, "--quiet", "--skip-hooks", "--skip-agents")
				if err != nil {
					t.Fatalf("init the held database: %v\n%s", err, out)
				}
				if out, err := runBDExecWithBinary(t, bd, src, append(srcEnv, "BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port)), "create", "live on the server", "--quiet"); err != nil {
					t.Fatalf("create the live issue: %v\n%s", err, out)
				}
				data, _ := os.ReadFile(filepath.Join(src, ".beads", "metadata.json"))
				var meta map[string]any
				_ = json.Unmarshal(data, &meta)
				db = meta["dolt_database"].(string)
			}
			meta := map[string]any{"database": "dolt", "backend": "dolt", "dolt_mode": "server", "dolt_database": db}
			if tc.embedded {
				meta = map[string]any{}
			}
			if tc.metaPort {
				meta["dolt_server_port"] = port
			} else if !tc.embedded && !tc.ownServer {
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(fmt.Sprintf("dolt:\n  port: %d\n", port)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.projectID {
				meta["project_id"] = "11111111-2222-3333-4444-555555555555"
				if tc.serverHeld {
					data, _ := os.ReadFile(filepath.Join(root, "src", ".beads", "metadata.json"))
					var src map[string]any
					_ = json.Unmarshal(data, &src)
					meta["project_id"] = src["project_id"]
				}
			}
			data, _ := json.Marshal(meta)
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.jsonl {
				if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(tracked), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.backup {
				if err := os.MkdirAll(filepath.Join(beadsDir, "backup"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(beadsDir, "backup", "issues.jsonl"), []byte(tracked), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_ = runCommandInDir(dir, "git", "init", "-q")
			if tc.legacyRoot {
				// What a pre-1.0 bd's server left in .beads/dolt: a database
				// directory, with no version witness beside it.
				if err := os.MkdirAll(filepath.Join(beadsDir, "dolt", db, ".dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failedStart {
				// A stand-in dolt runs the real one for every command but
				// sql-server, which never listens, so the store's start of
				// bd's own server times out after dolt init in .beads/dolt.
				realDolt, err := exec.LookPath("dolt")
				if err == nil {
					realDolt, err = filepath.Abs(realDolt)
				}
				if err != nil {
					t.Fatal(err)
				}
				shimDir := t.TempDir()
				shim := "#!/bin/sh\nif [ \"$1\" = sql-server ]; then exec sleep 30; fi\nexec \"$BOOTSTRAP_REAL_DOLT\" \"$@\"\n"
				if err := os.WriteFile(filepath.Join(shimDir, "dolt"), []byte(shim), 0o755); err != nil {
					t.Fatal(err)
				}
				failEnv := append(env, "PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
					"BOOTSTRAP_REAL_DOLT="+realDolt, "BEADS_DOLT_READY_TIMEOUT=1")
				failOut, err := runBDExecWithBinary(t, bd, dir, failEnv, "bootstrap", "--yes")
				if err == nil {
					t.Fatalf("bd bootstrap succeeded with a dolt sql-server that never listens:\n%s", failOut)
				}
				if _, err := os.Stat(filepath.Join(beadsDir, "dolt", ".dolt")); err != nil {
					t.Fatalf("the failed start left no Dolt data in .beads/dolt (%v):\n%s", err, failOut)
				}
			}
			out, err := runBDExecWithBinary(t, bd, dir, env, "bootstrap", "--yes")
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.wantExit {
				t.Fatalf("bd bootstrap exit %d, want %d:\n%s", code, tc.wantExit, out)
			}
			if tc.wantOut != "" && !strings.Contains(out, tc.wantOut) {
				t.Errorf("bd bootstrap output does not say %q:\n%s", tc.wantOut, out)
			}
			_, embErr := os.Stat(filepath.Join(beadsDir, "embeddeddolt"))
			if tc.embedded {
				if embErr != nil {
					t.Fatalf("embedded workspace: no .beads/embeddeddolt after bootstrap:\n%s", out)
				}
				return
			}
			if embErr == nil {
				t.Errorf("bootstrap created .beads/embeddeddolt in a server-mode workspace:\n%s", out)
			}
			if tc.legacyRoot {
				if _, err := os.Stat(filepath.Join(beadsDir, localVersionFile)); !os.IsNotExist(err) {
					t.Errorf("bd bootstrap recorded a version witness in a legacy server workspace (stat: %v):\n%s", err, out)
				}
			}
			if tc.wantTitles != nil {
				// Every later command opens the database bootstrap wrote.
				listOut, err := runBDExecWithBinary(t, bd, dir, env, "list", "--json", "--all")
				if err != nil {
					t.Fatalf("bd list after bootstrap: %v\n%s\nbootstrap:\n%s", err, listOut, out)
				}
				for _, title := range tc.wantTitles {
					if !strings.Contains(listOut, title) {
						t.Errorf("bd list after bootstrap does not show %q:\n%s", title, listOut)
					}
				}
			}
			if tc.ownServer {
				return
			}
			titles, present := foreignServerIssueTitles(t, port, db)
			switch {
			case tc.wantTitles == nil && present:
				t.Errorf("database %s exists on the server after a refused bootstrap:\n%s", db, out)
			case tc.wantTitles != nil && !present:
				t.Errorf("database %s is not on the server:\n%s", db, out)
			case tc.wantTitles != nil && strings.Join(titles, "|") != strings.Join(tc.wantTitles, "|"):
				t.Errorf("issues on the server = %q, want %q:\n%s", titles, tc.wantTitles, out)
			}
		})
	}
}

// TestE2E_InitRemoteClonesThroughAServerOnAConfigPort runs bd init --server
// --remote where a config.yaml dolt.port, user-level or the workspace's own,
// names the port of a dolt sql-server you run over a data directory of your
// own. Main cloned the remote with the dolt CLI into .beads/dolt, which that
// server does not serve, and init's store then created an empty database on
// the server, so bd list showed none of the remote's issues. The clone now
// goes through that server. Where nothing answers on the port, main's CLI
// clone is kept: bd starts its own server on that port over .beads/dolt.
func TestE2E_InitRemoteClonesThroughAServerOnAConfigPort(t *testing.T) {
	testutil.RequireDoltBinary(t)
	bd := buildBDForInitTests(t)
	root := t.TempDir()
	remote := filepath.Join(root, "remote")
	src := filepath.Join(root, "src")
	srcHome := t.TempDir()
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = runCommandInDir(src, "git", "init", "-q")
	srcEnv := append(removedBackendTestEnv(filepath.Join(src, ".beads")), "HOME="+srcHome, "BD_DISABLE_METRICS=1", "BD_NON_INTERACTIVE=1")
	for _, args := range [][]string{
		{"init", "--prefix", "src", "--quiet", "--skip-hooks", "--skip-agents"},
		{"create", "remote issue one", "--quiet"},
		{"dolt", "remote", "add", "origin", "file://" + filepath.ToSlash(remote)},
		{"dolt", "push"},
	} {
		if out, err := runBDExecWithBinary(t, bd, src, srcEnv, args...); err != nil {
			t.Fatalf("source %v: %v\n%s", args, err, out)
		}
	}
	for _, tc := range []struct {
		name    string
		where   string // user or project config.yaml names dolt.port
		foreign bool   // a server bd does not run answers on it; else nothing does
	}{
		{name: "user-level dolt.port, a server you run", where: "user", foreign: true},
		{name: "project dolt.port, a server you run", where: "project", foreign: true},
		{name: "user-level dolt.port nothing answers on", where: "user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := freeLoopbackPort(t)
			if tc.foreign {
				port = startForeignDoltServer(t)
			}
			home := t.TempDir()
			dir := filepath.Join(t.TempDir(), "joiner")
			beadsDir := filepath.Join(dir, ".beads")
			stopDoltServerCleanup(t, beadsDir)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			_ = runCommandInDir(dir, "git", "init", "-q")
			cfgPath := filepath.Join(home, ".config", "bd", "config.yaml")
			if tc.where == "project" {
				cfgPath = filepath.Join(beadsDir, "config.yaml")
			}
			writeClonePortFile(t, cfgPath, fmt.Sprintf("dolt:\n  mode: server\n  host: 127.0.0.1\n  port: %d\n", port))
			env := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "BEADS_DIR=",
				"BEADS_TEST_MODE=", "BEADS_DOLT_AUTO_START=", "BEADS_DOLT_SERVER_PORT=", "BEADS_DOLT_PORT=", "BEADS_DOLT_SERVER_HOST=",
				"BEADS_DOLT_SERVER_SOCKET=", "BEADS_DOLT_SERVER_MODE=", "BEADS_DOLT_SHARED_SERVER=", "BEADS_CEILING_DIRECTORIES="+filepath.Dir(dir),
				"BD_NON_INTERACTIVE=1", "BD_DISABLE_METRICS=1", "GIT_TERMINAL_PROMPT=0")
			out, err := runBDExecWithBinary(t, bd, dir, env, "init", "--server", "--remote", "file://"+filepath.ToSlash(remote), "--quiet", "--skip-hooks", "--skip-agents")
			if err != nil {
				t.Fatalf("bd init --server --remote: %v\n%s", err, out)
			}
			listOut, err := runBDExecWithBinary(t, bd, dir, env, "list", "--json")
			if err != nil {
				t.Fatalf("bd list: %v\n%s", err, listOut)
			}
			if !strings.Contains(listOut, "remote issue one") {
				t.Errorf("bd list does not show the remote's issue:\n%s\ninit:\n%s", listOut, out)
			}
			data, _ := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
			var meta map[string]any
			_ = json.Unmarshal(data, &meta)
			db, _ := meta["dolt_database"].(string)
			if tc.foreign {
				if titles, ok := foreignServerIssueTitles(t, port, db); !ok || strings.Join(titles, "|") != "remote issue one" {
					t.Errorf("the server you run holds %q (database present %v), want the remote's issue", titles, ok)
				}
				if _, err := os.Stat(filepath.Join(beadsDir, "dolt", db, ".dolt")); err == nil {
					t.Errorf("init left a clone in .beads/dolt, which the server you run does not serve")
				}
			}
		})
	}
}
