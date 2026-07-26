//go:build cgo

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

func requireProxiedServerEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" {
		t.Skip("set BEADS_TEST_PROXIED_SERVER=1 to run proxied-server integration tests")
	}
	testutil.RequireDoltBinary(t)
}

func bdProxiedEnv(dir string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BEADS_") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"HOME="+dir,
		"BEADS_DOLT_PROXIED_SERVER=1",
		"BEADS_NO_DAEMON=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
	)
}

func bdProxiedRun(t *testing.T, bd, dir string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), err
	}
	return stdout.Bytes(), nil
}

func bdProxiedCreate(t *testing.T, bd, dir string, args ...string) *types.Issue {
	t.Helper()
	fullArgs := append([]string{"create", "--json"}, args...)
	out, err := bdProxiedRun(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd create %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return parseIssueJSON(t, out)
}

func bdProxiedCreateSilent(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"create", "--silent"}, args...)
	out, err := bdProxiedRun(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd create --silent %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func bdProxiedCreateFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"create"}, args...)
	out, err := bdProxiedRun(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("bd create %s should have failed; got:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func bdProxiedList(t *testing.T, bd string, p proxiedProject, args ...string) string {
	t.Helper()
	stdout, _ := bdProxiedListCapture(t, bd, p, args...)
	return stdout
}

func bdProxiedListJSON(t *testing.T, bd string, p proxiedProject, args ...string) []*types.IssueWithCounts {
	t.Helper()
	fullArgs := append([]string{"list", "--json"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd list --json %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		if strings.Contains(stdout, "null") || strings.TrimSpace(stdout) == "" {
			return nil
		}
		t.Fatalf("no JSON array found in output:\n%s", stdout)
	}
	var issues []*types.IssueWithCounts
	if err := json.Unmarshal([]byte(stdout[start:]), &issues); err != nil {
		t.Fatalf("failed to parse JSON list output: %v\nraw: %s", err, stdout[start:])
	}
	return issues
}

func bdProxiedListCapture(t *testing.T, bd string, p proxiedProject, args ...string) (string, string) {
	t.Helper()
	fullArgs := append([]string{"list"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd list %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout, stderr
}

func bdProxiedListFail(t *testing.T, bd string, p proxiedProject, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"list"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd list %s to fail, but it succeeded:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func bdProxiedRunBuffers(t *testing.T, bd, dir string, args ...string) (string, string, error) {
	t.Helper()
	return bdProxiedRunBuffersWithEnv(t, bd, dir, nil, args...)
}

// bdProxiedRunBuffersWithEnv is bdProxiedRunBuffers with extra environment
// variables appended after the standard proxied env (so they can override
// it, e.g. BEADS_MAX_ROWS for the proxied-server MaxRows-rejection tests).
func bdProxiedRunBuffersWithEnv(t *testing.T, bd, dir string, envExtras []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = append(bdProxiedEnv(dir), envExtras...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func bdProxiedUpdate(t *testing.T, bd, dir string, args ...string) []*types.Issue {
	t.Helper()
	fullArgs := append([]string{"update", "--json"}, args...)
	out, err := bdProxiedRun(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd update %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := string(out)
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array found in update output:\n%s", s)
	}
	var issues []*types.Issue
	if err := json.Unmarshal([]byte(s[start:]), &issues); err != nil {
		t.Fatalf("failed to parse update JSON: %v\nraw: %s", err, s[start:])
	}
	if len(issues) == 0 {
		t.Fatalf("update returned empty JSON array:\n%s", s)
	}
	return issues
}

func bdProxiedUpdateOne(t *testing.T, bd, dir string, args ...string) *types.Issue {
	t.Helper()
	issues := bdProxiedUpdate(t, bd, dir, args...)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue in update output, got %d", len(issues))
	}
	return issues[0]
}

func bdProxiedUpdateRaw(t *testing.T, bd, dir string, args ...string) (string, string, error) {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	return bdProxiedRunBuffers(t, bd, dir, fullArgs...)
}

func bdProxiedUpdateFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := bdProxiedUpdateRaw(t, bd, dir, args...)
	if err == nil {
		t.Fatalf("bd update %s should have failed; got:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func bdProxiedShow(t *testing.T, bd, dir, id string) *types.Issue {
	t.Helper()
	out, err := bdProxiedRun(t, bd, dir, "show", id, "--json")
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\n%s", id, err, out)
	}
	return parseIssueJSON(t, out)
}

func bdProxiedShowRaw(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"show"}, args...)
	out, err := bdProxiedRun(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd show %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func bdProxiedShowFail(t *testing.T, bd, dir string, args ...string) (string, string) {
	t.Helper()
	fullArgs := append([]string{"show"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("bd show %s should have failed; got stdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout, stderr
}

func bdProxiedShowDetailsAll(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"show", "--json"}, args...)
	out, err := bdProxiedRun(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd show --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		t.Fatalf("no JSON in show output: %s", s)
	}
	s = s[start:]
	if strings.HasPrefix(s, "[") {
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			t.Fatalf("parse show JSON array: %v\n%s", err, s)
		}
		return arr
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse show JSON: %v\n%s", err, s)
	}
	return []map[string]interface{}{m}
}

func bdProxiedShowDetailsFirst(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	arr := bdProxiedShowDetailsAll(t, bd, dir, args...)
	if len(arr) == 0 {
		t.Fatalf("bd show --json %s returned empty array", strings.Join(args, " "))
	}
	return arr[0]
}

type proxiedProject struct {
	dir       string
	beadsDir  string
	proxyRoot string
	database  string
	prefix    string
}

func bdProxiedInitWithHooks(t *testing.T, bd, prefix string, hooks map[string]string, extraInitArgs ...string) proxiedProject {
	t.Helper()
	p := bdProxiedInitInternal(t, bd, prefix, false, extraInitArgs...)
	hooksDir := filepath.Join(p.beadsDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	for name, body := range hooks {
		hookPath := filepath.Join(hooksDir, name)
		if err := os.WriteFile(hookPath, []byte(body), 0o755); err != nil {
			t.Fatalf("write hook %s: %v", name, err)
		}
	}
	return p
}

func bdProxiedInit(t *testing.T, bd, prefix string, extraInitArgs ...string) proxiedProject {
	t.Helper()
	return bdProxiedInitInternal(t, bd, prefix, true, extraInitArgs...)
}

func bdProxiedInitInternal(t *testing.T, bd, prefix string, skipHooks bool, extraInitArgs ...string) proxiedProject {
	t.Helper()

	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	proxyRoot := filepath.Join(beadsDir, "dolt")
	t.Cleanup(func() {
		if err := proxy.Shutdown(proxyRoot); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", proxyRoot, err)
		}
	})
	shutdownProxyOnInterrupt(t, proxyRoot)

	baseArgs := []string{
		"init",
		"--proxied-server",
		"--quiet",
		"--prefix", prefix,
		"--non-interactive",
		"--skip-agents",
	}
	if skipHooks {
		baseArgs = append(baseArgs, "--skip-hooks")
	}
	args := append(baseArgs, extraInitArgs...)

	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd init --proxied-server failed: %v\nstdout:\n%s\nstderr:\n%s",
			err, stdout.String(), stderr.String())
	}

	database := sanitizePrefixForDB(prefix)

	return proxiedProject{
		dir:       dir,
		beadsDir:  beadsDir,
		proxyRoot: proxyRoot,
		database:  database,
		prefix:    prefix,
	}
}

func sanitizePrefixForDB(p string) string {
	p = strings.TrimLeft(p, ".")
	p = strings.TrimRight(p, "-")
	p = strings.ReplaceAll(p, ".", "_")
	if len(p) == 0 {
		return "bd"
	}
	c := p[0]
	if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
		p = "bd_" + p
	}
	return p
}

func shutdownProxyOnInterrupt(t *testing.T, proxyRoot string) {
	t.Helper()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			_ = proxy.Shutdown(proxyRoot)
			os.Exit(1)
		case <-done:
		}
	}()
	t.Cleanup(func() {
		signal.Stop(ch)
		close(done)
	})
}

func openProxiedDB(t *testing.T, p proxiedProject) *sql.DB {
	t.Helper()
	pf, err := pidfile.Read(p.proxyRoot, proxy.PIDFileName)
	if err != nil || pf == nil {
		t.Fatalf("read proxy pidfile %s: %v (pf=%v)", p.proxyRoot, err, pf)
	}

	dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?multiStatements=true&parseTime=true",
		pf.Port, p.database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open mysql %s: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping proxied db: %v", err)
	}
	return db
}

func assertProxiedDepExists(t *testing.T, db *sql.DB, issueID, dependsOnID string) {
	t.Helper()
	var count int
	err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		issueID, dependsOnID).Scan(&count)
	if err != nil {
		t.Fatalf("query dep %s -> %s: %v", issueID, dependsOnID, err)
	}
	if count != 1 {
		t.Fatalf("expected one dep row %s -> %s, got %d", issueID, dependsOnID, count)
	}
}

func assertProxiedDepExistsWithType(t *testing.T, db *sql.DB, issueID, dependsOnID, depType string) {
	t.Helper()
	var count int
	err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ? AND type = ?",
		issueID, dependsOnID, depType).Scan(&count)
	if err != nil {
		t.Fatalf("query dep %s -> %s (%s): %v", issueID, dependsOnID, depType, err)
	}
	if count != 1 {
		t.Fatalf("expected one dep row %s -> %s of type %s, got %d", issueID, dependsOnID, depType, count)
	}
}

func getProxiedLabels(t *testing.T, db *sql.DB, issueID string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT label FROM labels WHERE issue_id = ?", issueID)
	if err != nil {
		t.Fatalf("query labels for %s: %v", issueID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatalf("scan label: %v", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("label rows iter: %v", err)
	}
	return out
}
