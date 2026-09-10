//go:build integration

package dolt

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// Git Remote Integration Tests
//
// These tests validate Dolt's native git remote support: push/pull/clone
// to/from standard bare git repositories. Unlike the federation tests
// (which use Dolt's remotesapi protocol over HTTP), these tests use
// file:// URLs pointing to local bare git repos — no network, CI-friendly.
//
// Architecture:
//   - All operations (source + clone) use the `dolt` CLI exclusively.
//   - The embedded Dolt driver panics on Close in multi-store processes,
//     so we avoid it entirely and verify via `dolt sql -q ... -r csv`.
//
// Prerequisites:
//   - dolt >= 2.2.0
//   - git CLI available
//
// Run:
//   go test -tags='cgo integration' -run TestGitRemote ./internal/storage/dolt/

// gitRemoteSetup holds resources for a git-remote test scenario.
type gitRemoteSetup struct {
	baseDir    string // root temp dir
	remoteDir  string // bare git repo path
	remoteURL  string // file:// URL for the bare repo
	sourceDir  string // dolt source repo directory
	serverPort int    // local dolt sql-server port (0 for CLI-only setups)
}

// startLocalDoltServer starts a `dolt sql-server` rooted at dataDir and
// returns its port and an idempotent stop function.
//
// The suite's shared Dolt server (testmain_test.go) runs inside a Docker
// container: its only mount is the image's own /var/lib/dolt volume, so it
// can reach neither a host file:// git remote nor the store's own Path.
// Tests that push to a local bare git repo, or that inspect the engine's
// on-disk state, need a server that shares this process's filesystem.
// TestGitRemoteExternalServerRouting, TestSQLRemotePersistsAcrossExternalServerRestart
// and TestCredentialCLIRoutingE2E use the same arrangement.
//
// The server is spawned with doltserver.ServerSpawnEnv(), the same environment
// bd gives the sql-server it starts. That is load-bearing, not cosmetic: a
// `dolt` CLI command run against a directory a sql-server is already serving
// is proxied to that server, so the git subprocess is spawned by the server,
// not by the CLI — env guards applied to the CLI process (git tracing,
// core.hooksPath) only take effect if the server carries them too.
func startLocalDoltServer(t *testing.T, dataDir string) (int, func()) {
	t.Helper()
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	cmd := exec.Command("dolt", "sql-server", "-H", "127.0.0.1", "-P", strconv.Itoa(port))
	cmd.Dir = dataDir
	cmd.Env = doltserver.ServerSpawnEnv()
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start dolt sql-server in %s: %v", dataDir, err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(stop)
	if !testutil.WaitForServer(port, 15*time.Second) {
		stop()
		t.Fatalf("dolt sql-server in %s did not become ready within timeout", dataDir)
	}
	return port, stop
}

// setupGitRemote creates a bare git repo (seeded with an initial commit)
// and a Dolt source repo with the bare repo configured as "origin".
// Schema and config are initialized; ready for data writes and push.
func setupGitRemote(t *testing.T) *gitRemoteSetup {
	t.Helper()
	skipIfNoDolt(t)
	skipIfNoGit(t)

	baseDir, err := os.MkdirTemp("", "git-remote-test-*")
	if err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}

	// Create bare git repo
	remoteDir := filepath.Join(baseDir, "remote.git")
	runCmd(t, baseDir, "git", "init", "--bare", "-b", "main", remoteDir)

	// Seed with an initial commit (Dolt requires at least one branch)
	seedDir := filepath.Join(baseDir, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create seed dir: %v", err)
	}
	runCmd(t, seedDir, "git", "init", "-b", "main")
	runCmd(t, seedDir, "git", "commit", "--allow-empty", "-m", "init")
	runCmd(t, seedDir, "git", "remote", "add", "origin", remoteDir)
	runCmd(t, seedDir, "git", "push", "-u", "origin", "main")

	remoteURL := "file://" + remoteDir

	// Initialize dolt repo, configure remote, create schema
	sourceDir := filepath.Join(baseDir, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create source dir: %v", err)
	}
	runCmd(t, sourceDir, "dolt", "init")
	runCmd(t, sourceDir, "dolt", "remote", "add", "origin", remoteURL)

	// Initialize beads schema via CLI (mirrors what New() does).
	// dolt sql in the repo dir already defaults to the repo's database.
	initSchemaSQL := schema.AllMigrationsSQL() + "\nCALL DOLT_ADD('.');\nCALL DOLT_COMMIT('-Am', 'Genesis: schema and config');"
	runDoltSQL(t, sourceDir, initSchemaSQL)

	return &gitRemoteSetup{
		baseDir:   baseDir,
		remoteDir: remoteDir,
		remoteURL: remoteURL,
		sourceDir: sourceDir,
	}
}

// cleanup removes all temp dirs.
func (s *gitRemoteSetup) cleanup() {
	os.RemoveAll(s.baseDir)
}

// --- CLI helpers ---

// doltPush pushes to "origin" via CLI.
func doltPush(t *testing.T, dir string) {
	t.Helper()
	runCmd(t, dir, "dolt", "push", "origin", "main")
}

// doltPull pulls from "origin" via CLI.
func doltPull(t *testing.T, dir string) {
	t.Helper()
	runCmd(t, dir, "dolt", "pull", "origin")
}

// doltClone clones from remoteURL into cloneDir via CLI.
func doltClone(t *testing.T, remoteURL, cloneDir string) {
	t.Helper()
	cmd := exec.Command("dolt", "clone", remoteURL, cloneDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt clone failed: %v\nOutput: %s", err, output)
	}
}

// runCmd executes a command in the given directory.
func runCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed in %s: %v\nOutput: %s", name, args, dir, err, output)
	}
}

// runDoltSQL lives in dolt_sql_large_script_test.go (untagged, so it's
// always in scope here too) rather than in this integration-tagged file,
// since TestRunDoltSQLHandlesLargeScript there needs it without the
// integration tag.

// skipIfNoGit skips if git is not available.
func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed, skipping test")
	}
}

// sourceInsertIssue inserts an issue into the source via CLI SQL.
func sourceInsertIssue(t *testing.T, dir, id, title string) {
	t.Helper()
	q := fmt.Sprintf(
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) `+
			`VALUES ('%s', '%s', '', '', '', '', 'open', 2, 'task', NOW(), NOW())`,
		escapeSQL(id), escapeSQL(title))
	runDoltSQL(t, dir, q)
}

// sourceInsertIssueDesc inserts an issue with a description via CLI SQL.
func sourceInsertIssueDesc(t *testing.T, dir, id, title, desc string) {
	t.Helper()
	q := fmt.Sprintf(
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) `+
			`VALUES ('%s', '%s', '%s', '', '', '', 'open', 2, 'task', NOW(), NOW())`,
		escapeSQL(id), escapeSQL(title), escapeSQL(desc))
	runDoltSQL(t, dir, q)
}

// sourceCommitAndPush commits all changes and pushes to origin.
func sourceCommitAndPush(t *testing.T, dir, msg string) {
	t.Helper()
	runDoltSQL(t, dir, fmt.Sprintf("CALL DOLT_ADD('.'); CALL DOLT_COMMIT('-Am', '%s')", escapeSQL(msg)))
	doltPush(t, dir)
}

// doltGitRemoteReadCacheTTL mirrors dolt's own defaultSyncForReadTTL
// (store/blobstore/git_blobstore.go). Dolt resolves a file:// URL that points
// at a git repo to a git+file:// remote served by GitBlobstore, and
// GitBlobstore.syncForRead skips the underlying `git fetch` entirely when it
// last synced less than this long ago:
//
//	if ttl := gbs.syncForReadTTL; ttl > 0 { if sinceLast < ttl { return nil } }
//
// The blobstore is cached for the life of the sql-server process
// (dbfactory.gitRemoteCache), so the window is per-server, not per-connection.
const doltGitRemoteReadCacheTTL = 1 * time.Second

// waitOutGitRemoteReadCache blocks until a push made by a *different* process
// is guaranteed visible to the next remote read performed by a sql-server this
// test already used to touch the same remote.
//
// Without it these tests race a silent upstream staleness window: a pull
// issued inside doltGitRemoteReadCacheTTL of the server's previous remote sync
// reads the cached view, finds the peer's commit absent, and reports
// "Everything up-to-date" with fast_forward=0 — so store.Pull() returns nil
// having merged nothing and the peer's rows never arrive. Verified against
// dolt 2.3.1: with the peer's push and the pull 0.74s apart the pull reports
// success and delivers nothing; at 1.18s apart the same sequence reports
// "merge successful" and the row arrives. That is why these tests failed only
// on CI, whose runners complete the intervening clone/insert/push faster than
// a loaded developer machine.
//
// This sleep removes an unintended dependency on an upstream cache; it does
// not weaken any assertion below it. If bd's push/pull were actually broken,
// every assertion still fails exactly as before.
func waitOutGitRemoteReadCache() {
	time.Sleep(doltGitRemoteReadCacheTTL + 500*time.Millisecond)
}

// --- Clone verification helpers (all CLI-based) ---

// queryCSV runs a SQL query via dolt CLI and returns parsed rows as maps.
func queryCSV(t *testing.T, dir, query string) []map[string]string {
	t.Helper()
	cmd := exec.Command("dolt", "sql", "-q", query, "-r", "csv")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt sql query failed: %v\nQuery: %s\nOutput: %s", err, query, output)
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return nil
	}
	reader := csv.NewReader(strings.NewReader(trimmed))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("csv parse failed: %v\nRaw: %s", err, output)
	}
	if len(records) < 2 {
		return nil // header only, no data rows
	}
	headers := records[0]
	var rows []map[string]string
	for _, rec := range records[1:] {
		row := make(map[string]string)
		for i, h := range headers {
			if i < len(rec) {
				row[h] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// queryScalar runs a query expected to return a single value.
func queryScalar(t *testing.T, dir, query string) string {
	t.Helper()
	rows := queryCSV(t, dir, query)
	if len(rows) == 0 {
		return ""
	}
	for _, v := range rows[0] {
		return v
	}
	return ""
}

// queryCount runs a COUNT(*) query and returns the integer result.
func queryCount(t *testing.T, dir, query string) int {
	t.Helper()
	s := queryScalar(t, dir, query)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("expected integer from query, got %q: %v", s, err)
	}
	return n
}

// --- Tests ---

func TestGitRemoteAdd(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	// Verify remote via CLI
	cmd := exec.Command("dolt", "remote", "-v")
	cmd.Dir = setup.sourceDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt remote -v: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "origin") {
		t.Fatalf("expected origin remote, got:\n%s", output)
	}
	t.Logf("Remotes:\n%s", output)
}

func TestGitRemotePushEmptyDB(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	// Push schema-only database
	doltPush(t, setup.sourceDir)

	// Clone and verify schema via CLI
	cloneDir := filepath.Join(setup.baseDir, "clone-empty")
	doltClone(t, setup.remoteURL, cloneDir)

	val := queryScalar(t, cloneDir, "SELECT value FROM config WHERE `key` = 'compaction_enabled'")
	if val != "false" {
		t.Errorf("clone: compaction_enabled = %q, want %q", val, "false")
	}
}

func TestGitRemotePushWithData(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	sourceInsertIssue(t, setup.sourceDir, "git-001", "First git remote issue")
	sourceCommitAndPush(t, setup.sourceDir, "Add git-001")

	// Clone and verify
	cloneDir := filepath.Join(setup.baseDir, "clone-data")
	doltClone(t, setup.remoteURL, cloneDir)

	rows := queryCSV(t, cloneDir, "SELECT id, title FROM issues WHERE id = 'git-001'")
	if len(rows) == 0 {
		t.Fatal("clone: expected git-001 to exist")
	}
	if rows[0]["title"] != "First git remote issue" {
		t.Errorf("clone: title = %q, want %q", rows[0]["title"], "First git remote issue")
	}
}

func TestGitRemotePushIdempotent(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	sourceInsertIssue(t, setup.sourceDir, "git-idem-1", "Idempotent test")
	sourceCommitAndPush(t, setup.sourceDir, "Add data")

	// Second push with no new changes — should not error
	doltPush(t, setup.sourceDir)
}

func TestGitRemotePushIncremental(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	// First batch
	sourceInsertIssue(t, setup.sourceDir, "git-inc-1", "Incremental 1")
	sourceCommitAndPush(t, setup.sourceDir, "First batch")

	// Second batch
	sourceInsertIssue(t, setup.sourceDir, "git-inc-2", "Incremental 2")
	sourceInsertIssue(t, setup.sourceDir, "git-inc-3", "Incremental 3")
	sourceCommitAndPush(t, setup.sourceDir, "Second batch")

	// Clone and verify all three
	cloneDir := filepath.Join(setup.baseDir, "clone-inc")
	doltClone(t, setup.remoteURL, cloneDir)

	for _, id := range []string{"git-inc-1", "git-inc-2", "git-inc-3"} {
		count := queryCount(t, cloneDir, fmt.Sprintf("SELECT COUNT(*) FROM issues WHERE id = '%s'", id))
		if count != 1 {
			t.Errorf("clone: expected %s to exist", id)
		}
	}

	commitCount := queryCount(t, cloneDir, "SELECT COUNT(*) FROM dolt_log")
	if commitCount < 3 {
		t.Errorf("clone: expected at least 3 commits (genesis + 2 batches), got %d", commitCount)
	}
	t.Logf("Clone has %d commits", commitCount)
}

func TestGitRemoteClone(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	for i := 1; i <= 5; i++ {
		sourceInsertIssue(t, setup.sourceDir, fmt.Sprintf("clone-%03d", i), fmt.Sprintf("Clone test issue %d", i))
	}
	sourceCommitAndPush(t, setup.sourceDir, "Batch for clone test")

	cloneDir := filepath.Join(setup.baseDir, "full-clone")
	doltClone(t, setup.remoteURL, cloneDir)

	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("clone-%03d", i)
		rows := queryCSV(t, cloneDir, fmt.Sprintf("SELECT title FROM issues WHERE id = '%s'", id))
		if len(rows) == 0 {
			t.Errorf("clone: expected %s to exist", id)
			continue
		}
		expected := fmt.Sprintf("Clone test issue %d", i)
		if rows[0]["title"] != expected {
			t.Errorf("clone: %s title = %q, want %q", id, rows[0]["title"], expected)
		}
	}

	// Verify origin remote on clone
	remoteCount := queryCount(t, cloneDir, "SELECT COUNT(*) FROM dolt_remotes WHERE name = 'origin'")
	if remoteCount != 1 {
		t.Error("clone: expected 'origin' remote")
	}
}

func TestGitRemotePull(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	// Push initial data
	sourceInsertIssue(t, setup.sourceDir, "pull-001", "Before pull")
	sourceCommitAndPush(t, setup.sourceDir, "Initial data")

	// Clone
	cloneDir := filepath.Join(setup.baseDir, "pull-clone")
	doltClone(t, setup.remoteURL, cloneDir)

	// Push new data from source
	sourceInsertIssue(t, setup.sourceDir, "pull-002", "After initial clone")
	sourceCommitAndPush(t, setup.sourceDir, "New data")

	// Pull into clone
	doltPull(t, cloneDir)

	// Verify new issue appeared
	rows := queryCSV(t, cloneDir, "SELECT title FROM issues WHERE id = 'pull-002'")
	if len(rows) == 0 {
		t.Fatal("clone: expected pull-002 to exist after pull")
	}
	if rows[0]["title"] != "After initial clone" {
		t.Errorf("clone: pull-002 title = %q, want %q", rows[0]["title"], "After initial clone")
	}
}

func TestGitRemotePullWithLocalChanges(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	// Push initial data
	sourceInsertIssue(t, setup.sourceDir, "local-001", "Shared issue")
	sourceCommitAndPush(t, setup.sourceDir, "Initial")

	// Clone
	cloneDir := filepath.Join(setup.baseDir, "local-clone")
	doltClone(t, setup.remoteURL, cloneDir)

	// Make local changes in clone (different issue, no conflict)
	runDoltSQL(t, cloneDir,
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) `+
			`VALUES ('local-clone-001', 'Clone-only issue', '', '', '', '', 'open', 2, 'task', NOW(), NOW()); `+
			`CALL DOLT_ADD('.'); CALL DOLT_COMMIT('-Am', 'Local change')`)

	// Push new data from source (different issue, no conflict)
	sourceInsertIssue(t, setup.sourceDir, "local-002", "Source-only issue")
	sourceCommitAndPush(t, setup.sourceDir, "Source change")

	// Pull into clone (should merge cleanly)
	doltPull(t, cloneDir)

	// Verify all three issues
	for _, id := range []string{"local-001", "local-002", "local-clone-001"} {
		count := queryCount(t, cloneDir, fmt.Sprintf("SELECT COUNT(*) FROM issues WHERE id = '%s'", id))
		if count != 1 {
			t.Errorf("clone: expected %s to exist after pull", id)
		}
	}
}

func TestGitRemoteRoundTripAllTables(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	// Insert parent epic
	runDoltSQL(t, setup.sourceDir,
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) `+
			`VALUES ('rt-parent', 'Parent Epic', 'Round-trip parent', '', '', '', 'open', 1, 'epic', NOW(), NOW())`)

	// Insert child task
	runDoltSQL(t, setup.sourceDir,
		`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, created_at, updated_at) `+
			`VALUES ('rt-child', 'Child Task', 'Round-trip child with details', '', '', '', 'in_progress', 2, 'task', 'alice', NOW(), NOW())`)

	// Labels
	runDoltSQL(t, setup.sourceDir,
		`INSERT INTO labels (issue_id, label) VALUES ('rt-child', 'urgent'), ('rt-child', 'backend')`)

	// Comments
	runDoltSQL(t, setup.sourceDir,
		`INSERT INTO comments (id, issue_id, author, text, created_at) VALUES `+
			`(UUID(), 'rt-child', 'alice', 'Working on this', NOW()), `+
			`(UUID(), 'rt-child', 'bob', 'Looks good', NOW())`)

	// Dependency
	runDoltSQL(t, setup.sourceDir,
		`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) `+
			`VALUES (UUID(), 'rt-child', 'rt-parent', 'blocks', NOW(), 'test')`)

	// Config
	runDoltSQL(t, setup.sourceDir,
		"INSERT INTO config (`key`, value) VALUES ('issue_prefix', 'test') ON DUPLICATE KEY UPDATE value='test'")

	sourceCommitAndPush(t, setup.sourceDir, "Rich data for round-trip")

	// Clone and verify via CLI SQL
	cloneDir := filepath.Join(setup.baseDir, "clone-rt")
	doltClone(t, setup.remoteURL, cloneDir)

	// Verify parent epic
	rows := queryCSV(t, cloneDir, "SELECT title, issue_type FROM issues WHERE id = 'rt-parent'")
	if len(rows) == 0 {
		t.Fatal("clone: rt-parent not found")
	}
	if rows[0]["title"] != "Parent Epic" {
		t.Errorf("clone: parent title = %q, want %q", rows[0]["title"], "Parent Epic")
	}
	if rows[0]["issue_type"] != "epic" {
		t.Errorf("clone: parent type = %q, want %q", rows[0]["issue_type"], "epic")
	}

	// Verify child task
	rows = queryCSV(t, cloneDir, "SELECT title, status, assignee FROM issues WHERE id = 'rt-child'")
	if len(rows) == 0 {
		t.Fatal("clone: rt-child not found")
	}
	if rows[0]["title"] != "Child Task" {
		t.Errorf("clone: child title = %q, want %q", rows[0]["title"], "Child Task")
	}
	if rows[0]["status"] != "in_progress" {
		t.Errorf("clone: child status = %q, want %q", rows[0]["status"], "in_progress")
	}
	if rows[0]["assignee"] != "alice" {
		t.Errorf("clone: child assignee = %q, want %q", rows[0]["assignee"], "alice")
	}

	// Verify labels
	labelCount := queryCount(t, cloneDir, "SELECT COUNT(*) FROM labels WHERE issue_id = 'rt-child'")
	if labelCount != 2 {
		t.Errorf("clone: expected 2 labels, got %d", labelCount)
	}
	labelRows := queryCSV(t, cloneDir, "SELECT label FROM labels WHERE issue_id = 'rt-child' ORDER BY label")
	labelSet := map[string]bool{}
	for _, r := range labelRows {
		labelSet[r["label"]] = true
	}
	if !labelSet["urgent"] || !labelSet["backend"] {
		t.Errorf("clone: labels = %v, want {urgent, backend}", labelSet)
	}

	// Verify comments
	commentCount := queryCount(t, cloneDir, "SELECT COUNT(*) FROM comments WHERE issue_id = 'rt-child'")
	if commentCount != 2 {
		t.Errorf("clone: expected 2 comments, got %d", commentCount)
	}

	// Verify dependency
	depRows := queryCSV(t, cloneDir, "SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS depends_on_id FROM dependencies WHERE issue_id = 'rt-child'")
	if len(depRows) != 1 {
		t.Errorf("clone: expected 1 dependency, got %d", len(depRows))
	} else if depRows[0]["depends_on_id"] != "rt-parent" {
		t.Errorf("clone: dependency target = %q, want %q", depRows[0]["depends_on_id"], "rt-parent")
	}

	// Verify blocked status (rt-child depends on open rt-parent)
	blockerCount := queryCount(t, cloneDir,
		`SELECT COUNT(*) FROM dependencies d JOIN issues i ON d.depends_on_issue_id = i.id `+
			`WHERE d.issue_id = 'rt-child' AND i.status IN ('open', 'in_progress')`)
	if blockerCount != 1 {
		t.Errorf("clone: expected rt-child to be blocked by 1 issue, got %d", blockerCount)
	}

	// Verify config
	prefix := queryScalar(t, cloneDir, "SELECT value FROM config WHERE `key` = 'issue_prefix'")
	if prefix != "test" {
		t.Errorf("clone: issue_prefix = %q, want %q", prefix, "test")
	}
}

func TestGitRemoteSpecialCharacters(t *testing.T) {
	setup := setupGitRemote(t)
	defer setup.cleanup()

	specials := []struct {
		id    string
		title string
		desc  string
	}{
		{"spec-unicode", "日本語テスト: Dolt リモート", "Unicode: 你好世界"},
		{"spec-quotes", `Title with "double quotes"`, "Description with `backticks`"},
		{"spec-html", "Title <b>bold</b> & entities", "<script>alert(1)</script>"},
		{"spec-long", "A very long title that exceeds typical display widths and contains lots of words to test truncation behavior across the git remote boundary", "Short desc"},
		{"spec-empty-desc", "No description issue", ""},
	}

	for _, s := range specials {
		sourceInsertIssueDesc(t, setup.sourceDir, s.id, s.title, s.desc)
	}
	sourceCommitAndPush(t, setup.sourceDir, "Special characters batch")

	// Clone and verify
	cloneDir := filepath.Join(setup.baseDir, "clone-special")
	doltClone(t, setup.remoteURL, cloneDir)

	for _, s := range specials {
		rows := queryCSV(t, cloneDir, fmt.Sprintf(
			"SELECT title, description FROM issues WHERE id = '%s'", escapeSQL(s.id)))
		if len(rows) == 0 {
			t.Errorf("clone: expected %s to exist", s.id)
			continue
		}
		if rows[0]["title"] != s.title {
			t.Errorf("clone: %s title mismatch:\n  got:  %q\n  want: %q", s.id, rows[0]["title"], s.title)
		}
		if rows[0]["description"] != s.desc {
			t.Errorf("clone: %s desc mismatch:\n  got:  %q\n  want: %q", s.id, rows[0]["description"], s.desc)
		}
	}
}

// --- SQL-driver git remote tests ---
//
// These tests verify that Dolt's git remote support works through the
// SQL driver, not just the CLI. This is the critical question for the
// Dolt-in-Git spike: can we use store.Push() and store.Pull() with a
// bare git repo as the remote?
//
// CALL DOLT_PUSH runs inside the sql-server process, so the server must be
// able to see the bare repo and the store's own data directory. These tests
// therefore run against their own local sql-server (startLocalDoltServer),
// not the suite's containerized shared server.

// setupEmbeddedGitRemote creates a bare git repo and returns a DoltStore
// connected with the bare repo configured as "origin".
func setupEmbeddedGitRemote(t *testing.T) (*DoltStore, *gitRemoteSetup, func()) {
	t.Helper()
	testutil.RequireDoltBinary(t)
	skipIfNoGit(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	baseDir, err := os.MkdirTemp("", "embedded-git-remote-test-*")
	if err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}

	// Create bare git repo with initial commit (same as setupGitRemote)
	remoteDir := filepath.Join(baseDir, "remote.git")
	runCmd(t, baseDir, "git", "init", "--bare", "-b", "main", remoteDir)

	seedDir := filepath.Join(baseDir, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create seed dir: %v", err)
	}
	runCmd(t, seedDir, "git", "init", "-b", "main")
	runCmd(t, seedDir, "git", "commit", "--allow-empty", "-m", "init")
	runCmd(t, seedDir, "git", "remote", "add", "origin", remoteDir)
	runCmd(t, seedDir, "git", "push", "-u", "origin", "main")

	remoteURL := "file://" + remoteDir

	// Serve the store's own data directory from a local sql-server so the
	// engine, the bare git repo and this test process all share one
	// filesystem.
	doltDir := filepath.Join(baseDir, "embedded-dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create dolt dir: %v", err)
	}
	serverPort, stopServer := startLocalDoltServer(t, doltDir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dbName := uniqueTestDBName(t)
	store, err := New(ctx, &Config{
		Path:            doltDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      serverPort,
		ServerUser:      "root",
		AutoStart:       false,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true, // test creates a fresh database
	})
	if err != nil {
		stopServer()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to create DoltStore: %v", err)
	}

	// The whole point of the local server: the database it just created must
	// be on this process's filesystem, under the store's own Path. Tests here
	// push to a host bare repo and read the engine's git-remote cache mirror,
	// and both silently stop being meaningful if the store ever drifts back
	// onto a containerized server.
	if _, statErr := os.Stat(filepath.Join(doltDir, dbName, ".dolt")); statErr != nil {
		store.Close()
		stopServer()
		os.RemoveAll(baseDir)
		t.Fatalf("store did not materialize %s/.dolt on this filesystem — the engine is not local to the test: %v", filepath.Join(doltDir, dbName), statErr)
	}

	// Set issue prefix (required for CreateIssue)
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		store.Close()
		stopServer()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to set prefix: %v", err)
	}

	// Add git remote via SQL
	if err := store.AddRemote(ctx, "origin", remoteURL); err != nil {
		store.Close()
		stopServer()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to add remote: %v", err)
	}

	// Genesis commit, sweeping config too — the CLI sibling setupGitRemote
	// does the same with DOLT_COMMIT('-Am', 'Genesis: schema and config').
	// Commit() deliberately skips config (GH#2455), so without this
	// issue_prefix stays dirty forever: Pull() then refuses to auto-commit a
	// dirty internal config key, and a peer cloning this database gets no
	// prefix at all.
	if _, err := store.CommitAll(ctx, "Genesis: schema and config"); err != nil {
		store.Close()
		stopServer()
		os.RemoveAll(baseDir)
		t.Fatalf("failed to commit genesis config: %v", err)
	}

	setup := &gitRemoteSetup{
		baseDir:    baseDir,
		remoteDir:  remoteDir,
		remoteURL:  remoteURL,
		sourceDir:  doltDir,
		serverPort: serverPort,
	}

	cleanup := func() {
		store.Close()
		stopServer()
		os.RemoveAll(baseDir)
	}

	return store, setup, cleanup
}

func TestGitRemoteEmbeddedPushPull(t *testing.T) {
	store, setup, cleanup := setupEmbeddedGitRemote(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create test data via embedded store
	issue := &types.Issue{
		ID:        "emb-git-001",
		Title:     "Embedded git remote test",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Commit
	if err := store.Commit(ctx, "Add emb-git-001"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Push via embedded driver — this is the key verification
	if err := store.Push(ctx); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// Verify: clone via CLI and check data arrived
	cloneDir := filepath.Join(setup.baseDir, "clone-verify")
	doltClone(t, setup.remoteURL, cloneDir)

	rows := queryCSV(t, cloneDir, "SELECT id, title FROM issues WHERE id = 'emb-git-001'")
	if len(rows) == 0 {
		t.Fatal("clone: expected emb-git-001 to exist after embedded push")
	}
	if rows[0]["title"] != "Embedded git remote test" {
		t.Errorf("clone: title = %q, want %q", rows[0]["title"], "Embedded git remote test")
	}

	// Now test Pull: add data in the clone, push via CLI, pull into embedded store
	sourceInsertIssue(t, cloneDir, "emb-git-002", "Added in clone")
	sourceCommitAndPush(t, cloneDir, "Add emb-git-002 from clone")

	// The clone pushed from its own process; this store's sql-server last read
	// the remote during store.Push() above and caches that view briefly.
	waitOutGitRemoteReadCache()

	// Pull via embedded driver
	if err := store.Pull(ctx); err != nil {
		t.Fatalf("Pull failed: %v", err)
	}

	// Verify pulled data
	var title string
	err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = 'emb-git-002'").Scan(&title)
	if err != nil {
		t.Fatalf("query after pull failed: %v", err)
	}
	if title != "Added in clone" {
		t.Errorf("pull: title = %q, want %q", title, "Added in clone")
	}

	t.Log("Embedded driver git remote push/pull verified successfully")
}

func TestGitRemoteEmbeddedHasRemote(t *testing.T) {
	store, _, cleanup := setupEmbeddedGitRemote(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// HasRemote should find "origin"
	has, err := store.HasRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("HasRemote failed: %v", err)
	}
	if !has {
		t.Error("HasRemote('origin') = false, want true")
	}

	// HasRemote should not find nonexistent remote
	has, err = store.HasRemote(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("HasRemote failed: %v", err)
	}
	if has {
		t.Error("HasRemote('nonexistent') = true, want false")
	}
}

// TestGitRemotePushSkipsUserPrePushHook is a regression test for GH#3724.
//
// `bd dolt push` shells out to `dolt push`, which in turn runs
// `git push refs/dolt/data` against the embedded Dolt cache-mirror at
// `<doltDir>/<db>/.dolt/git-remote-cache/<hash>/repo.git/`. If the user has
// `init.templateDir` set globally with pre-commit-framework hooks, those
// templates land in the cache-mirror's `hooks/` dir (because Dolt's
// internal `git init` honours `init.templateDir`). The user's templated
// `pre-push` hook then runs `git diff` inside the bare-style cache mirror
// and fails with `fatal: this operation must be run in a work tree`.
//
// This test installs a deliberately failing `pre-push` hook directly into
// the cache-mirror after the first push materialises it, then performs a
// second push. With the fix in place, `doltCLIPush` sets
// `GIT_CONFIG_PARAMETERS='core.hooksPath=/dev/null'` on the dolt
// subprocess, so the hook is bypassed and the push succeeds. Without the
// fix, the hook runs and the second push fails.
//
// Mirrors PR #3626 / GH#3340 (the commit-side sibling) at the push site.
func TestGitRemotePushSkipsUserPrePushHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook script uses POSIX shell; the bug + fix are platform-agnostic but this assertion isn't")
	}

	store, setup, cleanup := setupEmbeddedGitRemote(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// First push: materialises the cache-mirror at
	// <doltDir>/<db>/.dolt/git-remote-cache/<hash>/repo.git/.
	first := &types.Issue{
		ID:        "hookpush-001",
		Title:     "Materialise cache mirror",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, first, "tester"); err != nil {
		t.Fatalf("first CreateIssue failed: %v", err)
	}
	if err := store.Commit(ctx, "Add hookpush-001"); err != nil {
		t.Fatalf("first Commit failed: %v", err)
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("first Push failed (cache-mirror not materialised): %v", err)
	}

	// Locate the cache-mirror's hooks directory by walking
	// .dolt/git-remote-cache/<hash>/repo.git/hooks. There is exactly one
	// such directory per configured git remote.
	cacheBase := findGitRemoteCacheRepoGit(t, setup.sourceDir)
	hooksDir := filepath.Join(cacheBase, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}

	// Install a pre-push hook that touches a sentinel and fails. Pre-fix,
	// the second push fires this hook (because `git push` honours
	// repo-local `core.hooksPath` defaults) and fails. Post-fix,
	// `core.hooksPath=/dev/null` from GIT_CONFIG_PARAMETERS suppresses it.
	sentinel := filepath.Join(setup.baseDir, "pre-push-hook-fired")
	hookPath := filepath.Join(hooksDir, "pre-push")
	hookScript := fmt.Sprintf("#!/bin/sh\ntouch %q\necho 'GH#3724: bd-internal git push must not run user pre-push hook' >&2\nexit 1\n",
		sentinel)
	if err := os.WriteFile(hookPath, []byte(hookScript), 0o755); err != nil { // #nosec G306 -- hook scripts must be executable
		t.Fatalf("write pre-push hook: %v", err)
	}

	// Second push: should succeed despite the failing pre-push hook.
	second := &types.Issue{
		ID:        "hookpush-002",
		Title:     "Push past failing pre-push hook",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, second, "tester"); err != nil {
		t.Fatalf("second CreateIssue failed: %v", err)
	}
	if err := store.Commit(ctx, "Add hookpush-002"); err != nil {
		t.Fatalf("second Commit failed: %v", err)
	}

	// Confirm the hook is still in place — guards against the test passing
	// for the wrong reason if Dolt re-templates the hooks dir between
	// pushes.
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("pre-push hook disappeared between pushes: %v", err)
	}

	if err := store.Push(ctx); err != nil {
		t.Fatalf("GH#3724 regression: second Push failed — bd's internal `dolt push` is running the user's pre-push hook against the cache-mirror. doltCLIPush must pass GIT_CONFIG_PARAMETERS='core.hooksPath=/dev/null' to suppress client-side hooks: %v", err)
	}

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("GH#3724 regression: pre-push hook executed (sentinel %s exists). bd's internal git push must skip user hooks", sentinel)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stat error for sentinel: %v", err)
	}
}

// findGitRemoteCacheRepoGit walks doltDir for the single
// .dolt/git-remote-cache/<hash>/repo.git directory created when a
// git-protocol remote is pushed. Fails the test if zero or more than one
// is found.
func findGitRemoteCacheRepoGit(t *testing.T, doltDir string) string {
	t.Helper()
	var matches []string
	err := filepath.WalkDir(doltDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == "repo.git" &&
			strings.Contains(filepath.ToSlash(path), "/.dolt/git-remote-cache/") {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", doltDir, err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one git-remote-cache/.../repo.git under %s, got %d: %v", doltDir, len(matches), matches)
	}
	return matches[0]
}

func TestGitRemoteSyncRoundTrip(t *testing.T) {
	// Full bidirectional sync test:
	// 1. Source creates issues, commits, pushes to git remote
	// 2. Clone bootstraps from git remote (BootstrapFromGitRemote path)
	// 3. Clone adds issues, commits, pushes
	// 4. Source pulls — verifies bidirectional sync
	// All via embedded DoltStore methods.

	sourceStore, setup, cleanup := setupEmbeddedGitRemote(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Step 1: Source creates data and pushes
	for i := 1; i <= 3; i++ {
		issue := &types.Issue{
			ID:        fmt.Sprintf("rt-src-%03d", i),
			Title:     fmt.Sprintf("Source issue %d", i),
			IssueType: types.TypeTask,
			Status:    types.StatusOpen,
			Priority:  2,
		}
		if err := sourceStore.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("source CreateIssue %d failed: %v", i, err)
		}
	}

	if err := sourceStore.Commit(ctx, "Add source issues"); err != nil {
		t.Fatalf("source Commit failed: %v", err)
	}
	if err := sourceStore.Push(ctx); err != nil {
		t.Fatalf("source Push failed: %v", err)
	}

	// Step 2: Bootstrap clone from git remote
	// Close source store first to avoid embedded driver conflicts,
	// then use BootstrapFromGitRemoteWithDB + open a new embedded store.
	sourceStore.Close()

	cloneDoltDir := filepath.Join(setup.baseDir, "clone-dolt")
	cloneDBName := "clonedb"
	bootstrapped, err := BootstrapFromGitRemoteWithDB(ctx, cloneDoltDir, setup.remoteURL, cloneDBName)
	if err != nil {
		t.Fatalf("BootstrapFromGitRemoteWithDB failed: %v", err)
	}
	if !bootstrapped {
		t.Fatal("expected bootstrap to occur (no existing dolt dir)")
	}

	// The clone is a second peer with its own data directory, so it needs its
	// own local server for the same reason the source does.
	clonePort, _ := startLocalDoltServer(t, cloneDoltDir)
	cloneStore, err := New(ctx, &Config{
		Path:            cloneDoltDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      clonePort,
		ServerUser:      "root",
		AutoStart:       false,
		CommitterName:   "clone-user",
		CommitterEmail:  "clone@example.com",
		Database:        cloneDBName,
		CreateIfMissing: true, // clone creates a new database
	})
	if err != nil {
		t.Fatalf("failed to open cloned store: %v", err)
	}

	// Verify source data arrived in clone
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("rt-src-%03d", i)
		issue, getErr := cloneStore.GetIssue(ctx, id)
		if getErr != nil {
			t.Fatalf("clone GetIssue(%s) failed: %v", id, getErr)
		}
		if issue == nil {
			t.Fatalf("clone: expected %s to exist", id)
		}
		expected := fmt.Sprintf("Source issue %d", i)
		if issue.Title != expected {
			t.Errorf("clone: %s title = %q, want %q", id, issue.Title, expected)
		}
	}

	// Step 3: Clone adds data and pushes back
	cloneIssue := &types.Issue{
		ID:        "rt-clone-001",
		Title:     "Clone-originated issue",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := cloneStore.CreateIssue(ctx, cloneIssue, "clone-user"); err != nil {
		t.Fatalf("clone CreateIssue failed: %v", err)
	}

	if err := cloneStore.Commit(ctx, "Add clone issue"); err != nil {
		t.Fatalf("clone Commit failed: %v", err)
	}
	if err := cloneStore.Push(ctx); err != nil {
		t.Fatalf("clone Push failed: %v", err)
	}

	// Close clone store before re-opening source
	cloneStore.Close()

	// The clone pushed from its own sql-server; the source's server last read
	// the remote during step 1's push and caches that view briefly.
	waitOutGitRemoteReadCache()

	// Step 4: Re-open source and pull — verify bidirectional sync
	sourceStore2, err := New(ctx, &Config{
		Path:            filepath.Join(setup.baseDir, "embedded-dolt"),
		ServerHost:      "127.0.0.1",
		ServerPort:      setup.serverPort,
		ServerUser:      "root",
		AutoStart:       false,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        findClonedDBName(t, filepath.Join(setup.baseDir, "embedded-dolt")),
		CreateIfMissing: true, // re-open may use dynamically discovered DB name
	})
	if err != nil {
		t.Fatalf("failed to re-open source store: %v", err)
	}
	defer sourceStore2.Close()

	if err := sourceStore2.Pull(ctx); err != nil {
		t.Fatalf("source Pull failed: %v", err)
	}

	// Verify clone's issue arrived in source
	issue, err := sourceStore2.GetIssue(ctx, "rt-clone-001")
	if err != nil {
		t.Fatalf("source GetIssue(rt-clone-001) failed: %v", err)
	}
	if issue == nil {
		t.Fatal("source: expected rt-clone-001 to exist after pull")
	}
	if issue.Title != "Clone-originated issue" {
		t.Errorf("source: rt-clone-001 title = %q, want %q", issue.Title, "Clone-originated issue")
	}

	// Verify original source data still intact
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("rt-src-%03d", i)
		srcIssue, getErr := sourceStore2.GetIssue(ctx, id)
		if getErr != nil || srcIssue == nil {
			t.Errorf("source: expected %s to still exist after pull", id)
		}
	}

	t.Log("Full round-trip sync verified: source -> git remote -> clone -> git remote -> source")
}

func TestCreateIssueAfterPull(t *testing.T) {
	store, setup, cleanup := setupEmbeddedGitRemote(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Create an issue via the store API (generates UUID event rows)
	sourceIssue := &types.Issue{
		ID:        "ai-src-001",
		Title:     "Source issue before push",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, sourceIssue, "tester"); err != nil {
		t.Fatalf("source CreateIssue failed: %v", err)
	}
	if err := store.Commit(ctx, "Add ai-src-001"); err != nil {
		t.Fatalf("source Commit failed: %v", err)
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("source Push failed: %v", err)
	}

	// Simulate a second peer via CLI: clone, add an issue row, commit, and
	// push back to the shared remote. events is dolt_ignored since 0062
	// (bd-red8u): the table is not part of committed history, so a fresh
	// clone arrives without it and audit rows never cross a remote — the
	// peer's contribution is the issue row alone.
	cloneDir := filepath.Join(setup.baseDir, "clone-ai")
	doltClone(t, setup.remoteURL, cloneDir)
	eventsProbe := exec.Command("dolt", "sql", "-q", "SELECT COUNT(*) FROM events")
	eventsProbe.Dir = cloneDir
	if out, err := eventsProbe.CombinedOutput(); err == nil {
		t.Fatalf("fresh clone materialized the events table from the remote; want it absent (dolt_ignored, 0062)\noutput: %s", out)
	}
	sourceInsertIssue(t, cloneDir, "ai-clone-001", "Clone issue generating events")
	sourceCommitAndPush(t, cloneDir, "Add ai-clone-001")

	// The peer pushed from its own process; this store's sql-server last read
	// the remote during store.Push() above and caches that view briefly.
	waitOutGitRemoteReadCache()

	// Pull into the source store — this is the code path under test.
	// With UUID primary keys, there are no counter collisions after pull.
	// This test verifies that CreateIssue works correctly after pulling
	// rows created by a different clone.
	if err := store.Pull(ctx); err != nil {
		t.Fatalf("Pull failed: %v", err)
	}

	// Pin what the pull itself delivered, before any further write. Pull()
	// reporting success while the peer's row never arrived, and Pull()
	// delivering the row only for a later write to drop it, are different
	// bugs; asserting only at the end of the test cannot tell them apart.
	if pulled, pulledErr := store.GetIssue(ctx, "ai-clone-001"); pulledErr != nil || pulled == nil {
		t.Fatalf("Pull reported success but the peer's ai-clone-001 is not in the source store (err=%v)", pulledErr)
	}

	postPullIssue := &types.Issue{
		ID:        "ai-src-002",
		Title:     "Source issue after pull",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, postPullIssue, "tester"); err != nil {
		t.Fatalf("CreateIssue after pull failed: %v", err)
	}

	var eventCount int
	err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&eventCount)
	if err != nil {
		t.Fatalf("failed to count events: %v", err)
	}
	// At least 2 node-local events: source created (ai-src-001) and post-pull
	// created (ai-src-002). The clone's own audit trail is node-local and
	// never arrives via pull.
	if eventCount < 2 {
		t.Errorf("expected at least 2 events, got %d", eventCount)
	}

	for _, id := range []string{"ai-src-001", "ai-clone-001", "ai-src-002"} {
		issue, getErr := store.GetIssue(ctx, id)
		if getErr != nil {
			t.Errorf("GetIssue(%s) failed: %v", id, getErr)
		}
		if issue == nil {
			t.Errorf("expected %s to exist", id)
		}
	}
}

// findClonedDBName discovers the database name inside a dolt directory
// by looking for subdirectories containing .dolt.
func findClonedDBName(t *testing.T, doltDir string) string {
	t.Helper()
	entries, err := os.ReadDir(doltDir)
	if err != nil {
		t.Fatalf("failed to read dolt dir %s: %v", doltDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		doltSubDir := filepath.Join(doltDir, entry.Name(), ".dolt")
		if info, statErr := os.Stat(doltSubDir); statErr == nil && info.IsDir() {
			return entry.Name()
		}
	}
	t.Fatalf("no dolt database found in %s", doltDir)
	return ""
}

// TestGitRemoteExternalServerRouting verifies that SQL-visible git-protocol
// remotes on an external server materialize the local CLI remote needed for
// subprocess routing.
func TestGitRemoteExternalServerRouting(t *testing.T) {
	testutil.RequireDoltBinary(t)
	skipIfNoGit(t)

	baseDir, err := os.MkdirTemp("", "external-server-routing-*")
	if err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(baseDir) })

	serverDataDir := filepath.Join(baseDir, "server-data")
	if err := os.MkdirAll(serverDataDir, 0o755); err != nil {
		t.Fatalf("failed to create server data dir: %v", err)
	}
	runCmd(t, serverDataDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	testdbDir := filepath.Join(serverDataDir, "testdb")
	if err := os.MkdirAll(testdbDir, 0o755); err != nil {
		t.Fatalf("failed to create testdb dir: %v", err)
	}
	runCmd(t, testdbDir, "dolt", "init", "--name", "test", "--email", "test@test.com")
	runCmd(t, testdbDir, "dolt", "remote", "add", "origin", "git+https://example.com/test.git")

	// Start the server before opening the store so New() initializes schema via
	// the normal migration path. A single dolt sql -q script over all migrations
	// can leave Dolt's analyzer unaware of columns added earlier in the script.
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	serverCmd := exec.Command("dolt", "sql-server",
		"-H", "127.0.0.1",
		"-P", fmt.Sprintf("%d", port),
	)
	serverCmd.Dir = serverDataDir
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	})

	if !testutil.WaitForServer(port, 15*time.Second) {
		t.Fatal("dolt sql-server did not become ready within timeout")
	}

	clientDataDir := filepath.Join(baseDir, "client-data")
	clientTestdbDir := filepath.Join(clientDataDir, "testdb")
	if err := os.MkdirAll(clientTestdbDir, 0o755); err != nil {
		t.Fatalf("failed to create client testdb dir: %v", err)
	}
	runCmd(t, clientTestdbDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, env := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_TEST_MODE"} {
		if prev, ok := os.LookupEnv(env); ok {
			t.Cleanup(func() { os.Setenv(env, prev) })
		} else {
			t.Cleanup(func() { os.Unsetenv(env) })
		}
		os.Unsetenv(env)
	}

	store, err := New(ctx, &Config{
		Path:            clientDataDir,
		Database:        "testdb",
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		ServerUser:      "root",
		CommitterName:   "test",
		CommitterEmail:  "test@test.com",
		AutoStart:       false,
		CreateIfMissing: false,
		Remote:          "origin",
		RemoteUser:      "testuser",
	})
	if err != nil {
		t.Fatalf("failed to create DoltStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	require.Equal(t, "", doltutil.FindCLIRemote(clientTestdbDir, store.remote), "precondition: client CLI remote should be absent")
	require.True(t, store.isGitProtocolRemote(ctx, store.remote), "SQL-visible git remote should materialize CLI routing")
	require.True(t,
		doltutil.RemoteURLsMatch(doltutil.FindCLIRemote(clientTestdbDir, store.remote), "git+https://example.com/test.git"),
		"git-protocol routing should create a matching client CLI remote",
	)
	require.True(t, store.shouldUseCLIForCredentials(ctx, store.remote, store.mainRemoteCredentials()), "credential route should reuse matching CLI remote")
	useLocalCLI, err := store.shouldUseCLIForLocalRemoteWithError(ctx, store.remote)
	require.NoError(t, err)
	require.True(t, useLocalCLI, "local remote guard should pass after materialization")
}

func TestSQLRemotePersistsAcrossExternalServerRestart(t *testing.T) {
	testutil.RequireDoltBinary(t)
	skipIfNoGit(t)

	baseDir, err := os.MkdirTemp("", "sql-remote-restart-*")
	if err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(baseDir) })

	remoteDir := filepath.Join(baseDir, "remote.git")
	runCmd(t, baseDir, "git", "init", "--bare", "-b", "main", remoteDir)
	remoteURL := "file://" + remoteDir

	serverDataDir := filepath.Join(baseDir, "server-data")
	if err := os.MkdirAll(serverDataDir, 0o755); err != nil {
		t.Fatalf("failed to create server data dir: %v", err)
	}
	runCmd(t, serverDataDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	testdbDir := filepath.Join(serverDataDir, "testdb")
	if err := os.MkdirAll(testdbDir, 0o755); err != nil {
		t.Fatalf("failed to create testdb dir: %v", err)
	}
	runCmd(t, testdbDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	// Start the server before opening the store so New() initializes schema via
	// the normal migration path. A single dolt sql -q script over all migrations
	// can leave Dolt's analyzer unaware of columns added earlier in the script.
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	startServer := func() *exec.Cmd {
		t.Helper()
		cmd := exec.Command("dolt", "sql-server",
			"-H", "127.0.0.1",
			"-P", fmt.Sprintf("%d", port),
		)
		cmd.Dir = serverDataDir
		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start dolt sql-server: %v", err)
		}
		if !testutil.WaitForServer(port, 15*time.Second) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("dolt sql-server did not become ready within timeout")
		}
		return cmd
	}
	stopServer := func(cmd *exec.Cmd) {
		t.Helper()
		if cmd == nil || cmd.Process == nil {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	serverCmd := startServer()
	t.Cleanup(func() { stopServer(serverCmd) })

	clientDataDir := filepath.Join(baseDir, "client-data")
	clientTestdbDir := filepath.Join(clientDataDir, "testdb")
	if err := os.MkdirAll(clientTestdbDir, 0o755); err != nil {
		t.Fatalf("failed to create client testdb dir: %v", err)
	}
	runCmd(t, clientTestdbDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	for _, env := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_TEST_MODE"} {
		if prev, ok := os.LookupEnv(env); ok {
			t.Cleanup(func() { os.Setenv(env, prev) })
		} else {
			t.Cleanup(func() { os.Unsetenv(env) })
		}
		os.Unsetenv(env)
	}

	openStore := func(ctx context.Context) *DoltStore {
		t.Helper()
		store, err := New(ctx, &Config{
			Path:            clientDataDir,
			Database:        "testdb",
			ServerHost:      "127.0.0.1",
			ServerPort:      port,
			ServerUser:      "root",
			CommitterName:   "test",
			CommitterEmail:  "test@test.com",
			AutoStart:       false,
			CreateIfMissing: false,
			Remote:          "origin",
		})
		if err != nil {
			t.Fatalf("failed to create DoltStore: %v", err)
		}
		return store
	}
	remoteURLFor := func(remotes []storage.RemoteInfo, name string) (string, bool) {
		for _, remote := range remotes {
			if remote.Name == name {
				return remote.URL, true
			}
		}
		return "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	store := openStore(ctx)
	if err := store.AddRemote(ctx, "origin", remoteURL); err != nil {
		_ = store.Close()
		t.Fatalf("AddRemote through SQL/store API: %v", err)
	}
	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		_ = store.Close()
		t.Fatalf("ListRemotes before restart: %v", err)
	}
	persistedURL, ok := remoteURLFor(remotes, "origin")
	require.True(t, ok, "origin remote should exist before restart")
	store.Close()

	stopServer(serverCmd)
	serverCmd = startServer()

	store = openStore(ctx)
	t.Cleanup(func() { store.Close() })
	remotes, err = store.ListRemotes(ctx)
	if err != nil {
		t.Fatalf("ListRemotes after restart: %v", err)
	}
	restartedURL, ok := remoteURLFor(remotes, "origin")
	require.True(t, ok, "origin remote should exist after restart")
	require.Equal(t, persistedURL, restartedURL)
}

// TestCredentialCLIRoutingE2E verifies that Push succeeds via CLI subprocess
// routing when DOLT_REMOTE_USER is set and the dolt server is external.
//
// Setup:
//   - Native Dolt file:// target, no auth needed
//   - dolt sql-server started from serverDataDir (with testdb + schema + remote)
//   - DoltStore in server mode with remoteUser set, CLI dir has the remote
//
// The test proves routing works end-to-end: if shouldUseCLIForCredentials
// routes to doltCLIPush, the CLI uses the file:// remote and push succeeds.
// If the guard fails and falls through to SQL withEnvCredentials, the external
// server process cannot see the env vars and push fails (SC-001).
func TestCredentialCLIRoutingE2E(t *testing.T) {
	testutil.RequireDoltBinary(t)

	baseDir, err := os.MkdirTemp("", "credential-cli-routing-e2e-*")
	if err != nil {
		t.Fatalf("failed to create base dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(baseDir) })

	// 1. Use an uninitialized native Dolt file target. Current Dolt normalizes
	// file:// URLs pointing at bare git repos to git+file://, which would route
	// through the git-protocol guard before this credential-routing guard.
	remoteDir := filepath.Join(baseDir, "remote-dolt")
	remoteURL := "file://" + remoteDir

	// 2. Server data directory: init dolt, create testdb with schema
	serverDataDir := filepath.Join(baseDir, "server-data")
	if err := os.MkdirAll(serverDataDir, 0o755); err != nil {
		t.Fatalf("failed to create server data dir: %v", err)
	}
	runCmd(t, serverDataDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	testdbDir := filepath.Join(serverDataDir, "testdb")
	if err := os.MkdirAll(testdbDir, 0o755); err != nil {
		t.Fatalf("failed to create testdb dir: %v", err)
	}
	runCmd(t, testdbDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	// Start the server before opening the store so New() initializes schema via
	// the normal migration path. A single dolt sql -q script over all migrations
	// can leave Dolt's analyzer unaware of columns added earlier in the script.
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	serverCmd := exec.Command("dolt", "sql-server",
		"-H", "127.0.0.1",
		"-P", fmt.Sprintf("%d", port),
	)
	serverCmd.Dir = serverDataDir
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	})

	if !testutil.WaitForServer(port, 15*time.Second) {
		t.Fatal("dolt sql-server did not become ready within timeout")
	}

	// 4. Client CLI directory: separate dolt init WITHOUT the file:// remote.
	// The bd setup path below writes the remote through SQL/store only; the
	// push routing guard must materialize the local CLI remote from that SQL
	// source of truth.
	clientDataDir := filepath.Join(baseDir, "client-data")
	clientTestdbDir := filepath.Join(clientDataDir, "testdb")
	if err := os.MkdirAll(clientTestdbDir, 0o755); err != nil {
		t.Fatalf("failed to create client testdb dir: %v", err)
	}
	runCmd(t, clientTestdbDir, "dolt", "init", "--name", "test", "--email", "test@test.com")

	// 5. Clean env to prevent interference from test harness
	for _, env := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_TEST_MODE"} {
		if prev, ok := os.LookupEnv(env); ok {
			t.Cleanup(func() { os.Setenv(env, prev) })
		} else {
			t.Cleanup(func() { os.Unsetenv(env) })
		}
		os.Unsetenv(env)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 6. Create DoltStore in server mode with credentials
	store, err := New(ctx, &Config{
		Path:            clientDataDir,
		Database:        "testdb",
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		ServerUser:      "root",
		CommitterName:   "test",
		CommitterEmail:  "test@test.com",
		AutoStart:       false,
		CreateIfMissing: false,
		Remote:          "origin",
		RemoteUser:      "testuser",     // triggers credential CLI routing
		RemotePassword:  "testpassword", // passed via applyToCmd to subprocess env
	})
	if err != nil {
		t.Fatalf("failed to create DoltStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.AddRemote(ctx, "origin", remoteURL); err != nil {
		t.Fatalf("AddRemote through bd setup path: %v", err)
	}
	require.Equal(t, "", doltutil.FindCLIRemote(clientTestdbDir, store.remote), "precondition: bd setup path should not manually seed CLI remote")

	// Verify preconditions: not a git-protocol remote, but credentials trigger CLI routing
	require.False(t, store.isGitProtocolRemote(ctx, store.remote), "file:// is not git-protocol")
	if !store.shouldUseCLIForCredentials(ctx, store.remote, store.mainRemoteCredentials()) {
		remotes, listErr := store.ListRemotes(ctx)
		ensureErr := doltutil.EnsureCLIRemote(clientTestdbDir, store.remote, remoteURL)
		t.Fatalf("should route through CLI for credentials; serverMode=%v remotes=%v listErr=%v cliRemote=%q ensureErr=%v",
			store.serverMode, remotes, listErr, doltutil.FindCLIRemote(clientTestdbDir, store.remote), ensureErr)
	}
	require.True(t,
		doltutil.RemoteURLsMatch(doltutil.FindCLIRemote(clientTestdbDir, store.remote), remoteURL),
		"credential routing should materialize a matching client CLI remote",
	)
	require.True(t, store.serverMode, "store should be in server mode")

	// 7. Push should succeed via CLI credential routing
	// If the guard works: doltCLIPush uses CLI dir's file:// remote → success
	// If guard fails: withEnvCredentials + SQL CALL DOLT_PUSH('--user',...) → fails
	// (external server can't see env vars set on bd client process)
	err = store.Push(ctx)
	require.NoError(t, err, "Push should succeed via CLI credential routing (SC-001)")
}

// TestPullReportsSuccessOnlyWhenTheMergeLanded is the regression test for
// ga-ivaps: Pull() returning nil having merged nothing.
//
// A sync that lies is worse than a sync that fails. Pull() collapses three
// different outcomes into the single value nil — "I merged the peer's commits",
// "there was nothing to merge", and "I reported success but the branch you read
// did not receive anything" — and no caller can tell them apart. The third is
// silent divergence: bd sync reports success while the local database quietly
// falls behind the remote.
//
// THE DIVERGENCE IS CONSTRUCTED, NOT WAITED FOR. The CI symptom is intermittent
// and nobody has reproduced it on demand, so this test does not chase the race.
// It builds the *observable end state* that any such pull leaves behind — the
// remote-tracking ref for (remote, branch) advanced past the branch the store
// reads — and pins that Pull() refuses to call it success. Whatever made the
// transport miss (a route that no-ops, a merge landing on another branch, a CLI
// subprocess operating on a database the SQL session does not serve), it ends
// here, and this is the assertion that catches it.
//
// The store is moved onto a branch the CLI directory is not checked out to,
// which makes `dolt pull <remote> <branch>` merge into the CLI directory's
// branch and leave the store's own branch untouched. That is a real route
// through pullTransport, not a stub.
//
// Two controls, both required:
//
//   - A pull with real work to do must succeed AND deliver the peer's row. A
//     post-condition that rejected everything would satisfy the subject
//     assertion while breaking every pull in the product.
//   - A pull with genuinely nothing to merge must still succeed QUIETLY. This is
//     the control that keeps the fix from turning every no-op pull into an
//     error, which is the obvious wrong way to make a lying pull loud.
func TestPullReportsSuccessOnlyWhenTheMergeLanded(t *testing.T) {
	store, setup, cleanup := setupEmbeddedGitRemote(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	seed := &types.Issue{
		ID:        "pl-src-001",
		Title:     "Source issue before push",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, seed, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.Commit(ctx, "Add pl-src-001"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// Control 1: a pull with real work to do succeeds and delivers the row.
	cloneDir := filepath.Join(setup.baseDir, "clone-pl")
	doltClone(t, setup.remoteURL, cloneDir)
	sourceInsertIssue(t, cloneDir, "pl-clone-001", "Clone issue")
	sourceCommitAndPush(t, cloneDir, "Add pl-clone-001")

	// The peer pushed from its own process; this store's sql-server last read
	// the remote during store.Push() above and caches that view briefly.
	waitOutGitRemoteReadCache()

	if err := store.Pull(ctx); err != nil {
		t.Fatalf("control broken: a pull with real work to do failed: %v", err)
	}
	if got, err := store.GetIssue(ctx, "pl-clone-001"); err != nil || got == nil {
		t.Fatalf("control broken: Pull reported success but the peer's pl-clone-001 is absent (err=%v)", err)
	}

	// Control 2: the repeated pull has nothing to merge. It must still succeed,
	// and say nothing about it. Waiting the cache out again is what makes this
	// a genuine no-op rather than a cached one: inside the TTL the fetch is
	// skipped, so the pull would report "nothing to merge" without ever asking
	// the remote, and the control would hold even if a real no-op pull errored.
	waitOutGitRemoteReadCache()

	if err := store.Pull(ctx); err != nil {
		t.Fatalf("control broken: a pull with genuinely nothing to merge must succeed quietly, got: %v", err)
	}

	// Subject: move the store onto a branch the CLI directory is not checked
	// out to, so the pull's merge cannot land where the store reads.
	if err := store.Branch(ctx, "feature"); err != nil {
		t.Fatalf("Branch(feature) failed: %v", err)
	}
	if err := store.Checkout(ctx, "feature"); err != nil {
		t.Fatalf("Checkout(feature) failed: %v", err)
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("pushing the feature branch failed: %v", err)
	}

	runCmd(t, cloneDir, "dolt", "fetch", "origin")
	runCmd(t, cloneDir, "dolt", "checkout", "feature")
	sourceInsertIssue(t, cloneDir, "pl-clone-002", "Clone issue on feature")
	// Pushed to origin/feature explicitly: the sourceCommitAndPush helper
	// pushes origin main, which from a feature checkout would push an
	// unchanged main and leave the peer's commit nowhere. A fixture that never
	// publishes the row makes Pull correct to merge nothing, and the case would
	// be asserting on its own bug instead of the product's.
	runDoltSQL(t, cloneDir, "CALL DOLT_ADD('.'); CALL DOLT_COMMIT('-Am', 'Add pl-clone-002 on feature')")
	runCmd(t, cloneDir, "dolt", "push", "origin", "feature")

	// Same cache, and the subject needs it out of the way even more than the
	// controls do: inside the TTL the pull's fetch is skipped, so
	// remotes/origin/feature never moves, the post-condition sees a local
	// branch that trivially contains it, and the divergence this case exists
	// to build is never constructed.
	waitOutGitRemoteReadCache()

	pullErr := store.Pull(ctx)
	landed, getErr := store.GetIssue(ctx, "pl-clone-002")
	delivered := getErr == nil && landed != nil

	switch {
	case pullErr == nil && delivered:
		// The merge landed on the branch the store reads, so this run built no
		// divergence and has nothing to say about detecting one. Not a pass.
		t.Skip("the pull delivered pl-clone-002 onto the store's branch: no divergence was constructed")
	case pullErr == nil && !delivered:
		t.Fatalf("Pull reported success (nil) but pl-clone-002 never arrived on %q, the branch this store "+
			"reads: a pull that merged nothing must not report success", "feature")
	case delivered:
		t.Fatalf("Pull failed with %v even though pl-clone-002 did arrive: the post-condition rejected a "+
			"pull that landed", pullErr)
	}

	// The refusal has to be THIS refusal. A transport that failed for an
	// unrelated reason would also make pullErr non-nil and would otherwise let
	// the case pass without the post-condition existing at all.
	if msg := pullErr.Error(); !strings.Contains(msg, "merged nothing") || !strings.Contains(msg, "remotes/origin/feature") {
		t.Fatalf("Pull failed, but not with the merged-nothing post-condition: %v", pullErr)
	}
	t.Logf("Pull correctly refused to call this success: %v", pullErr)
}

// TestPullVerifyUsesBranchQualifiedPreHead pins ga-ivaps Finding 1 (attempt 2):
// verifyPullLanded's cheap fast path skips the containment check when the branch
// head MOVED across the pull — a head that moved is proof the transport landed.
// That inference is only sound when the pre-pull head was read from the SAME
// branch the post-pull comparison reads (s.branch). Pull now captures it via
// branchHash(s.branch); a regression to GetCurrentCommit (session HEAD) would,
// on a pooled connection sitting on the database's default branch, hand back a
// different branch's head, so a merge that never reached s.branch would look
// like it had — the fast path would fire and wave a lying pull through.
//
// It exercises that fast path directly and DETERMINISTICALLY by calling
// verifyPullLanded with the two candidate pre-pull heads rather than trying to
// force a pooled connection onto the wrong branch (which an effectively
// single-connection server-mode store makes unreachable in-process — the very
// reason TestPullReportsSuccessOnlyWhenTheMergeLanded has to t.Skip). The
// wrong-branch head (main's tip, what the bug reads) must skip the check; the
// branch-qualified head (feature's tip, what the fix reads) must run it and
// catch the divergence. This is the non-environment-dependent coverage the
// attempt-2 scorecard asked for.
func TestPullVerifyUsesBranchQualifiedPreHead(t *testing.T) {
	store, setup, cleanup := setupEmbeddedGitRemote(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// A committed, pushed main so branchHash(main) is a real, distinct hash to
	// stand in for "the branch a stray pooled connection is parked on".
	seed := &types.Issue{
		ID:        "bq-main-001",
		Title:     "Main seed",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, seed, "tester"); err != nil {
		t.Fatalf("CreateIssue(main seed) failed: %v", err)
	}
	if err := store.Commit(ctx, "Add bq-main-001"); err != nil {
		t.Fatalf("Commit(main seed) failed: %v", err)
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("Push(main) failed: %v", err)
	}

	// The store operates on feature; publish it so a peer can clone and advance it.
	if err := store.Branch(ctx, "feature"); err != nil {
		t.Fatalf("Branch(feature) failed: %v", err)
	}
	if err := store.Checkout(ctx, "feature"); err != nil {
		t.Fatalf("Checkout(feature) failed: %v", err)
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("pushing the feature branch failed: %v", err)
	}

	// feature gets a LOCAL-ONLY commit — the reviewer's exact scenario. It moves
	// feature's tip off both main and the pushed feature tip, and makes the
	// divergence a genuine one: the peer's branch below shares only the pushed
	// feature tip as an ancestor, so neither head is the other's. An --allow-empty
	// commit is the minimal way to advance the tip: what this test needs from the
	// commit is the new hash on feature, nothing in its tree. Going through
	// CreateIssue would drag in the write path's cross-table ID-collision probe —
	// unrelated to verifyPullLanded — and couple the fixture to that schema. The
	// store session is on feature (Checkout set s.branch above), so DOLT_COMMIT
	// lands here.
	if _, err := store.db.ExecContext(ctx,
		"CALL DOLT_COMMIT('--allow-empty', '-m', 'feature local-only commit')"); err != nil {
		t.Fatalf("local-only feature commit failed: %v", err)
	}

	// A peer advances origin/feature from its own process, diverging it from the
	// local feature branch.
	cloneDir := filepath.Join(setup.baseDir, "clone-bq")
	doltClone(t, setup.remoteURL, cloneDir)
	runCmd(t, cloneDir, "dolt", "fetch", "origin")
	runCmd(t, cloneDir, "dolt", "checkout", "feature")
	sourceInsertIssue(t, cloneDir, "bq-clone-001", "Clone issue on feature")
	runDoltSQL(t, cloneDir, "CALL DOLT_ADD('.'); CALL DOLT_COMMIT('-Am', 'Add bq-clone-001 on feature')")
	runCmd(t, cloneDir, "dolt", "push", "origin", "feature")

	// The sql-server caches its last read of the remote, and the verify's own
	// refresh fetch is served from that cache inside the TTL. Wait it out so the
	// refresh actually sees the peer's commit and the divergence is real.
	waitOutGitRemoteReadCache()

	featureTip, err := store.branchHash(ctx, "feature")
	if err != nil || featureTip == "" {
		t.Fatalf("branchHash(feature) failed: hash=%q err=%v", featureTip, err)
	}
	mainTip, err := store.branchHash(ctx, "main")
	if err != nil || mainTip == "" {
		t.Fatalf("branchHash(main) failed: hash=%q err=%v", mainTip, err)
	}
	if featureTip == mainTip {
		t.Fatalf("scenario broken: feature and main share tip %q, so a wrong-branch preHead would not differ from the branch-qualified one", featureTip)
	}

	// The bug's input: preHead read from the wrong branch (main). localHash is
	// feature's tip, so localHash != preHead trips the fast path and the check is
	// skipped — the lying pull is (wrongly) called a success. This is the fast
	// path's load-bearing contract: it is only ever safe when preHead is s.branch.
	if err := store.verifyPullLanded(ctx, "origin", mainTip); err != nil {
		t.Fatalf("fast-path contract broken: a wrong-branch preHead must skip the check (return nil), got: %v", err)
	}

	// The fix's input: preHead read from s.branch (feature). localHash == preHead,
	// so the fast path does not fire, the containment check runs, and it catches
	// the divergence the merge left on the wrong branch.
	got := store.verifyPullLanded(ctx, "origin", featureTip)
	if got == nil {
		t.Fatalf("branch-qualified preHead must run the containment check and catch the divergence, got nil")
	}
	if msg := got.Error(); !strings.Contains(msg, "merged nothing") || !strings.Contains(msg, "remotes/origin/feature") {
		t.Fatalf("caught an error, but not the merged-nothing post-condition: %v", got)
	}
	// The divergence is genuine — sibling histories whose common ancestor is
	// neither tip — so it must stay a HARD error, not the fast-forwardable
	// retryable class bd sync would loop on.
	if errors.Is(got, versioncontrolops.ErrPullBehindFastForwardable) {
		t.Fatalf("a genuine divergence must not be classified fast-forwardable-retryable: %v", got)
	}
	t.Logf("branch-qualified preHead correctly caught the divergence: %v", got)
}
