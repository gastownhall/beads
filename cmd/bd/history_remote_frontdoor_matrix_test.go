//go:build cgo && unix

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestHistoryRemoteRefusalFrontDoorMatrix runs the real bd binary through all
// supported proxied transports. Valid command arguments are intentional: a
// typed capability refusal must win over Cobra usage validation and must not
// start a provider or touch durable state.
func TestHistoryRemoteRefusalFrontDoorMatrix(t *testing.T) {
	bd := buildEmbeddedBD(t)
	type fixture struct {
		name string
		make func(*testing.T) proxiedProject
	}
	fixtures := []fixture{{
		name: "managed-local",
		make: func(t *testing.T) proxiedProject {
			requireManagedLocalProxiedEnv(t)
			return bdManagedLocalInit(t, bd, "hm_managed", 5*time.Minute)
		},
	}}
	if os.Getenv("BEADS_TEST_PROXIED_SERVER") == "1" {
		fixtures = append(fixtures,
			fixture{name: "external-tcp", make: func(t *testing.T) proxiedProject {
				return newSharedProxiedProject(t, bd, "hm_tcp")
			}},
			fixture{name: "external-unix", make: func(t *testing.T) proxiedProject {
				requireProxiedServerEnv(t)
				upstream := testutil.StartIsolatedDoltContainerHandle(t)
				socket := filepath.Join(t.TempDir(), "dolt.sock")
				bridge := startHistoryUnixBridge(t, socket, upstream.Port)
				t.Cleanup(func() { _ = bridge.Process.Kill() })
				return bdProxiedInit(t, bd, "hm_unix", "--proxied-server-external-socket-path", socket)
			}},
		)
	}
	commands := []struct {
		name, code, message string
		args                []string
	}{
		{"branch", "proxy.branch.unsupported", "branch is not supported in proxied-server mode", []string{"branch", "feature"}},
		{"conflicts list", "proxy.conflicts.unsupported", "conflicts list is not supported in proxied-server mode", []string{"conflicts", "list"}},
		{"conflicts show", "proxy.conflicts.unsupported", "conflicts show is not supported in proxied-server mode", []string{"conflicts", "show"}},
		{"conflicts resolve", "proxy.conflicts.unsupported", "conflicts resolve is not supported in proxied-server mode", []string{"conflicts", "resolve", "--all", "--ours"}},
		{"repo add", "proxy.repo.unsupported", "repo add is not supported in proxied-server mode", []string{"repo", "add", "."}},
		{"repo remove", "proxy.repo.unsupported", "repo remove is not supported in proxied-server mode", []string{"repo", "remove", "."}},
		{"repo list", "proxy.repo.unsupported", "repo list is not supported in proxied-server mode", []string{"repo", "list"}},
		{"repo sync", "proxy.repo.unsupported", "repo sync is not supported in proxied-server mode", []string{"repo", "sync"}},
		{"federation sync", "proxy.federation.unsupported", "federation sync is not supported in proxied-server mode", []string{"federation", "sync"}},
		{"federation status", "proxy.federation.unsupported", "federation status is not supported in proxied-server mode", []string{"federation", "status"}},
		{"federation add-peer", "proxy.federation.unsupported", "federation add-peer is not supported in proxied-server mode", []string{"federation", "add-peer", "peer", "https://example.invalid/peer"}},
		{"federation remove-peer", "proxy.federation.unsupported", "federation remove-peer is not supported in proxied-server mode", []string{"federation", "remove-peer", "peer"}},
		{"federation list-peers", "proxy.federation.unsupported", "federation list-peers is not supported in proxied-server mode", []string{"federation", "list-peers"}},
		{"vc merge", "proxy.vc.unsupported", "vc merge is not supported in proxied-server mode", []string{"vc", "merge", "feature"}},
		{"vc commit", "proxy.vc.unsupported", "vc commit is not supported in proxied-server mode", []string{"vc", "commit"}},
		{"flatten", "proxy.flatten.unsupported", "flatten is not supported in proxied-server mode", []string{"flatten"}},
		{"dolt push", "proxy.dolt_push.unsupported", "dolt push is not supported in proxied-server mode", []string{"dolt", "push"}},
		{"dolt pull", "proxy.dolt_pull.unsupported", "dolt pull is not supported in proxied-server mode", []string{"dolt", "pull"}},
		{"dolt commit", "proxy.dolt_commit.unsupported", "dolt commit is not supported in proxied-server mode", []string{"dolt", "commit"}},
		{"dolt remote add", "proxy.dolt_remote.unsupported", "dolt remote add is not supported in proxied-server mode", []string{"dolt", "remote", "add", "backup", "https://example.invalid/backup"}},
		{"dolt remote list", "proxy.dolt_remote.unsupported", "dolt remote list is not supported in proxied-server mode", []string{"dolt", "remote", "list"}},
		{"dolt remote reset-data", "proxy.dolt_remote.unsupported", "dolt remote reset-data is not supported in proxied-server mode", []string{"dolt", "remote", "reset-data", "backup"}},
		{"sync", "proxy.sync.unsupported", "sync is not supported in proxied-server mode", []string{"sync"}},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			p := f.make(t)
			bdProxiedCreate(t, bd, p.dir, "matrix sentinel")
			_ = proxy.Shutdown(p.proxyRoot)
			before := snapshotHistoryState(t, bd, p)
			for _, tc := range commands {
				t.Run(tc.name, func(t *testing.T) {
					// Put the global flag before the subcommand. A few nested
					// front doors do not inherit persistent flags after their args.
					args := append([]string{"--json"}, tc.args...)
					stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, []string{"BEADS_JSON=1"}, args...)
					if err == nil {
						t.Fatalf("%v unexpectedly succeeded", tc.args)
					}
					var got struct {
						Code    string `json:"code"`
						Error   string `json:"error"`
						Mutates bool   `json:"mutates"`
					}
					if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); jsonErr != nil {
						t.Fatalf("refusal must be strict JSON: %v stdout=%q stderr=%q", jsonErr, stdout, stderr)
					}
					if got.Code != tc.code || got.Error != tc.message || got.Mutates {
						t.Fatalf("refusal=%+v, want code=%q error=%q mutates=false", got, tc.code, tc.message)
					}
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
						t.Fatalf("exit=%v, want 1", err)
					}
					if _, statErr := os.Stat(filepath.Join(p.proxyRoot, proxy.PIDFileName)); statErr == nil {
						t.Fatal("provider proxy started for direct-only refusal")
					}
					after := snapshotHistoryState(t, bd, p)
					if string(before) != string(after) {
						t.Fatal("durable artifacts changed during refusal")
					}
				})
			}
		})
	}
}

func snapshotHistoryArtifacts(t *testing.T, p proxiedProject) []byte {
	t.Helper()
	var out []byte
	for _, path := range []string{
		filepath.Join(p.beadsDir, "config.yaml"), filepath.Join(p.beadsDir, "metadata.json"),
		filepath.Join(p.beadsDir, "events.jsonl"), filepath.Join(p.beadsDir, ".local_version"),
		filepath.Join(p.proxyRoot, proxy.LockFileName), filepath.Join(p.proxyRoot, proxy.PIDFileName),
	} {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("snapshot %s: %v", path, err)
		}
		out = append(out, []byte(strconv.Itoa(len(path))+":"+path+":"+string(b)+"\n")...)
	}
	return out
}

func snapshotHistoryState(t *testing.T, bd string, p proxiedProject) []byte {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "--json", "list")
	if err != nil {
		t.Fatalf("snapshot list failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("snapshot list did not return a JSON array: %q", stdout)
	}
	var rows any
	if err := json.Unmarshal([]byte(stdout[start:]), &rows); err != nil {
		t.Fatalf("snapshot list JSON: %v\n%s", err, stdout)
	}
	canonical, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("canonicalize issue rows: %v", err)
	}
	if err := proxy.Shutdown(p.proxyRoot); err != nil {
		t.Fatalf("shutdown proxy after snapshot: %v", err)
	}
	return append(snapshotHistoryArtifacts(t, p), canonical...)
}

type directHistoryProject struct {
	dir      string
	beadsDir string
	database string
	port     int
	env      []string
}

func newDirectHistoryProject(t *testing.T, bd, prefix string) directHistoryProject {
	t.Helper()
	port := sharedProxiedServerPort(t)
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	database := uniqueProxiedDatabase()
	env := historyCleanEnv(dir)
	args := []string{
		"init", "--backend", "dolt", "--server", "--external",
		"--server-host", "127.0.0.1", "--server-port", strconv.Itoa(port),
		"--database", database, "--prefix", prefix, "--quiet", "--non-interactive",
		"--skip-hooks", "--skip-agents",
	}
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("direct Dolt init failed: %v\n%s", err, out)
	}
	t.Cleanup(func() { dropTestDatabase(database, port) })
	return directHistoryProject{dir: dir, beadsDir: beadsDir, database: database, port: port, env: env}
}

func historyCleanEnv(home string) []string {
	env := make([]string, 0, len(os.Environ())+5)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "BEADS_") {
			env = append(env, value)
		}
	}
	return append(env,
		"HOME="+home,
		"BEADS_NO_DAEMON=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
	)
}

func runDirectHistory(t *testing.T, bd string, p directHistoryProject, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = p.dir
	cmd.Env = p.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func openDirectHistoryDB(t *testing.T, p directHistoryProject) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?multiStatements=true&parseTime=true", p.port, p.database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open direct Dolt: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping direct Dolt: %v", err)
	}
	return db
}

func startHistoryUnixBridge(t *testing.T, endpoint, upstreamPort string) *exec.Cmd {
	t.Helper()
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skipf("socat is required for external Unix topology: %v", err)
	}
	if err := os.Remove(endpoint); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove stale Unix socket: %v", err)
	}
	cmd := exec.Command("socat", "UNIX-LISTEN:"+endpoint+",fork", "TCP:127.0.0.1:"+upstreamPort)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Unix bridge: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(endpoint); err == nil {
			return cmd
		}
		if time.Now().After(deadline) {
			t.Fatalf("Unix bridge did not create %s", endpoint)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHistoryRemoteSupportedFrontDoorParity checks the two operations that
// are intentionally shared by direct and proxied providers.
func TestHistoryRemoteSupportedFrontDoorParity(t *testing.T) {
	if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" && os.Getenv(managedLocalProxiedEnvVar) != "1" {
		t.Skip("set a proxied test lane to run history parity")
	}
	bd := buildEmbeddedBD(t)
	for _, tc := range []struct {
		name string
		make func(*testing.T) proxiedProject
	}{
		{"managed-local", func(t *testing.T) proxiedProject {
			requireManagedLocalProxiedEnv(t)
			return bdManagedLocalInit(t, bd, "hs_managed", 5*time.Minute)
		}},
		{"external-tcp", func(t *testing.T) proxiedProject {
			requireProxiedServerEnv(t)
			return newSharedProxiedProject(t, bd, "hs_tcp")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.make(t)
			issue := bdProxiedCreate(t, bd, p.dir, "history parity")
			bdProxiedUpdate(t, bd, p.dir, issue.ID, "--title", "history parity updated")
			out, err := bdProxiedRun(t, bd, p.dir, "--json", "history", issue.ID, "--events")
			if err != nil || !strings.Contains(string(out), issue.ID) {
				t.Fatalf("history --events failed: %v\n%s", err, out)
			}
			db := openProxiedDB(t, p)
			if _, err := db.ExecContext(context.Background(), "CALL DOLT_REMOTE('add', ?, ?)", "backup", "https://example.invalid/backup"); err != nil {
				t.Fatalf("seed remote: %v", err)
			}
			out, err = bdProxiedRun(t, bd, p.dir, "--json", "dolt", "remote", "remove", "backup")
			if err != nil || !strings.Contains(string(out), "backup") {
				t.Fatalf("remote remove failed: %v\n%s", err, out)
			}
		})
	}
}

func TestHistoryRemoteSupportedDirectAndProxiedParity(t *testing.T) {
	if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" {
		t.Skip("set BEADS_TEST_PROXIED_SERVER=1 to run direct/server parity")
	}
	bd := buildEmbeddedBD(t)
	direct := newDirectHistoryProject(t, bd, "hp_direct")
	proxied := newSharedProxiedProject(t, bd, "hp_proxy")

	directIssue := createDirectHistoryIssue(t, bd, direct)
	proxyIssue := bdProxiedCreate(t, bd, proxied.dir, "history parity")

	directEvents := exerciseHistoryParity(t, bd, directIssue, func(args ...string) (string, string, error) {
		return runDirectHistory(t, bd, direct, args...)
	}, openDirectHistoryDB(t, direct))
	proxyEvents := exerciseHistoryParity(t, bd, proxyIssue.ID, func(args ...string) (string, string, error) {
		return bdProxiedRunBuffers(t, bd, proxied.dir, args...)
	}, openProxiedDB(t, proxied))
	if !reflect.DeepEqual(directEvents, proxyEvents) {
		t.Fatalf("history event types differ: direct=%v proxied=%v", directEvents, proxyEvents)
	}
}

func createDirectHistoryIssue(t *testing.T, bd string, p directHistoryProject) string {
	t.Helper()
	stdout, stderr, err := runDirectHistory(t, bd, p, "create", "--json", "history parity")
	if err != nil {
		t.Fatalf("direct create failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	start := strings.Index(stdout, "{")
	if start < 0 {
		t.Fatalf("direct create did not return JSON: %q", stdout)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout[start:]), &issue); err != nil || issue.ID == "" {
		t.Fatalf("direct create JSON: %v\n%s", err, stdout)
	}
	return issue.ID
}

func exerciseHistoryParity(t *testing.T, bd, issueID string, run func(...string) (string, string, error), db *sql.DB) []string {
	t.Helper()
	if _, stderr, err := run("update", "--json", issueID, "--title", "history parity updated"); err != nil {
		t.Fatalf("update %s failed: %v\nstderr=%s", issueID, err, stderr)
	}
	stdout, stderr, err := run("--json", "history", issueID, "--events")
	if err != nil {
		t.Fatalf("history --events %s failed: %v\nstdout=%s\nstderr=%s", issueID, err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("history --events did not return JSON array: %q", stdout)
	}
	var events []struct {
		IssueID   string `json:"issue_id"`
		EventType string `json:"event_type"`
	}
	if err := json.Unmarshal([]byte(stdout[start:]), &events); err != nil {
		t.Fatalf("history --events JSON: %v\n%s", err, stdout)
	}
	if len(events) == 0 {
		t.Fatalf("history --events returned no events for %s", issueID)
	}
	types := make([]string, 0, len(events))
	for _, event := range events {
		if event.IssueID != issueID {
			t.Fatalf("history event issue_id=%q, want %q", event.IssueID, issueID)
		}
		types = append(types, event.EventType)
	}
	if _, err := db.ExecContext(context.Background(), "CALL DOLT_REMOTE('add', ?, ?)", "backup", "https://example.invalid/backup"); err != nil {
		t.Fatalf("seed direct/proxy remote: %v", err)
	}
	stdout, stderr, err = run("--json", "dolt", "remote", "remove", "backup")
	if err != nil || !strings.Contains(stdout+stderr, "backup") {
		t.Fatalf("remote remove failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	return types
}
