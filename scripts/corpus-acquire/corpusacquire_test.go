// Package main implements the Phase 4 corpus-acquisition tool (be-hs42e.5.1):
// a read-only, filesystem-level acquirer of a point-in-time Dolt corpus clone
// from the shared multi-database Dolt data_dir, plus depth measurement, for
// the downstream replay-with-oracle harness (be-hs42e.5.2) to consume.
//
// Test-to-acceptance-criterion map (see be-hs42e.5.1's exit_contract):
//   - filesystem-level copy, never git, never a MySQL-protocol connection to
//     the server, including while a dolt sql-server is serving the source:
//     TestAcquireCorpus_ProducesWorkingClone, TestAcquireCorpus_NeverInvokesGit,
//     TestAcquireCorpus_OnlyFileSchemeNeverNetworkOrServer,
//     TestAcquireCorpus_ServedSourceNeverConnects,
//     TestAcquireCorpus_RefusesDestinationInsideServedDataDir
//   - never a write-capable connection to the shared server; measurement is
//     local-clone-only: TestMeasureCorpus_NeverTouchesNetworkOrServer
//   - never creates a scratch/test DB on the production server:
//     TestAcquireCorpus_MissingSourceDB_ErrorsWithoutCreating (state-based),
//     TestAcquireCorpus_SanitizesChildEnv (env-leak-based — this is the exact
//     mechanism of the beads-testdb-production-leak incident)
//   - env-sanitized entry point: TestSanitizedEnv_StripsDoltServerAndActorVars,
//     TestAcquireCorpus_SanitizesChildEnv
//   - CORPUS_CLONE-shaped output: TestMeasureCorpus_PopulatesCorpusCloneFields
//   - minted names avoid the ^[0-9a-v]{16,31}$ shape (NFR5/R2, upstream
//     issueops.ValidateRef gap on PR #6730): TestValidateMintedName,
//     TestGenerateCloneID_NeverMatchesDangerousShape
//   - re-measures depth every call, flags partial vs. a supplied baseline:
//     TestMeasureCorpus_FlagsPartialWhenDepthBelowBaseline,
//     TestMeasureCorpus_NoFlagWhenBaselineAtOrBelowActual
//   - test suite exercises a throwaway local fixture DB, never the real
//     production server on :28231: satisfied by construction (every fixture
//     below is t.TempDir()-scoped) and cross-checked by the shim-based tests
//     above, which explicitly assert no subprocess argv ever names port 28231
//     or a sql-server invocation.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// realDolt is resolved once, in TestMain, before any test in this package
// installs a shim ahead of it on PATH. Every fixture-building helper and
// every shim's passthrough target uses this absolute path, never a bare
// "dolt" lookup that could resolve to a shim installed earlier in the same
// process.
var realDolt string

func TestMain(m *testing.M) {
	// A missing dolt leaves realDolt empty: the tests that need it skip via
	// requireDolt, like the other scripts/ packages, and the rest still run.
	realDolt, _ = exec.LookPath("dolt")
	os.Exit(m.Run())
}

// requireDolt skips a test that needs the real dolt CLI when none is on PATH.
func requireDolt(t *testing.T) {
	t.Helper()
	if realDolt == "" {
		t.Skip("dolt not installed, skipping corpus-acquire fixture test")
	}
}

// ---- fixture helpers ----------------------------------------------------

// runDolt runs the real dolt CLI with dir as its working directory, and
// fails the test on error.
func runDolt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	requireDolt(t)
	cmd := exec.Command(realDolt, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt %s (dir=%s) failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

var logHashRE = regexp.MustCompile(`commit\s+([0-9a-v]+)`)

// firstCommitHashFromLog extracts the HEAD commit hash from `dolt log -n 1`
// plain-text output (this dolt version has no --format flag to ask for the
// hash alone).
func firstCommitHashFromLog(t *testing.T, log string) string {
	t.Helper()
	m := logHashRE.FindStringSubmatch(log)
	if m == nil {
		t.Fatalf("could not find a commit hash in dolt log output:\n%s", log)
	}
	return m[1]
}

// newFixtureDB creates a throwaway Dolt database directory (chunk-journal
// storage format — matching the real shared server's on-disk shape, see
// be-hs42e.5.1's design-finding notes) under dataDir/db, seeded with an
// `issues` table and issueCount rows, one row committed per commit so the
// resulting dolt_log commit count is deterministic: 1 commit from `dolt
// init` itself + 1 table-creation commit + issueCount seed commits. Returns
// the HEAD commit hash.
func newFixtureDB(t *testing.T, dataDir, db string, issueCount int) string {
	t.Helper()
	requireDolt(t)
	dir := filepath.Join(dataDir, db)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	runDolt(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDolt(t, dir, "sql", "-q", "CREATE TABLE issues (id VARCHAR(64) PRIMARY KEY, title VARCHAR(255));")
	runDolt(t, dir, "add", "-A")
	runDolt(t, dir, "commit", "-m", "create issues table")
	for i := 0; i < issueCount; i++ {
		id := fmt.Sprintf("t-%d", i)
		runDolt(t, dir, "sql", "-q", fmt.Sprintf("INSERT INTO issues VALUES ('%s', 'issue %d');", id, i))
		runDolt(t, dir, "add", "-A")
		runDolt(t, dir, "commit", "-m", fmt.Sprintf("add %s", id))
	}
	return firstCommitHashFromLog(t, runDolt(t, dir, "log", "-n", "1"))
}

// doltSQLCSV runs a read-only query against the dolt repo at dir and returns
// the single-column, single-row string result. Test-only fixture-inspection
// helper.
func doltSQLCSV(t *testing.T, dir, query string) string {
	t.Helper()
	out := runDolt(t, dir, "sql", "-q", query, "-r", "csv")
	r := csv.NewReader(strings.NewReader(out))
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parsing CSV from %q: %v\noutput:\n%s", query, err, out)
	}
	if len(rows) < 2 || len(rows[1]) < 1 {
		t.Fatalf("expected a header row + 1 data row from %q, got: %v", query, rows)
	}
	return rows[1][0]
}

// hashTree returns a stable digest of every file's path and content under
// dir, excluding well-known lock/index bookkeeping files that a live dolt
// process is expected to touch (LOCK, journal.idx) without changing any
// logical content — the same exclusion validated empirically in this bead's
// design-finding smoke test.
func hashTree(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if name := d.Name(); name == "LOCK" || name == "journal.idx" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\n", path)
		h.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("hashing %s: %v", dir, err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ---- shim helpers: prove the safety contract without touching the real
// shared Dolt server -------------------------------------------------------

// installDoltShim puts a passthrough `dolt` shim on PATH ahead of realDolt.
// The shim appends one record per invocation to logPath — argv on its own
// "ARGV:" line, then any of watchEnvNames that is currently set as an
// "ENV:name=value" line, then a "---" separator — and always execs realDolt
// with the original argv and environment, so callers observe normal
// behavior. Reverted automatically at test cleanup via t.Setenv.
func installDoltShim(t *testing.T, logPath string, watchEnvNames ...string) {
	t.Helper()
	binDir := t.TempDir()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n{\n")
	b.WriteString("  echo \"ARGV:$*\"\n")
	for _, name := range watchEnvNames {
		fmt.Fprintf(&b, "  if [ -n \"$%s\" ]; then echo \"ENV:%s=$%s\"; fi\n", name, name, name)
	}
	b.WriteString("  echo \"---\"\n")
	fmt.Fprintf(&b, "} >> %s\n", shellQuote(logPath))
	fmt.Fprintf(&b, "exec %s \"$@\"\n", shellQuote(realDolt))
	writeShim(t, binDir, "dolt", b.String())
	prependPATH(t, binDir)
}

// installGitTripwire puts a `git` shim on PATH that never delegates to a
// real git — any invocation at all is itself the violation, since the
// corpus-acquisition tool has no legitimate reason to ever call git. It
// records argv to calledFile and exits nonzero.
func installGitTripwire(t *testing.T, calledFile string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(calledFile) + "\n" +
		"exit 1\n"
	writeShim(t, binDir, "git", script)
	prependPATH(t, binDir)
}

func writeShim(t *testing.T, binDir, name, script string) {
	t.Helper()
	p := filepath.Join(binDir, name)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("writing shim %s: %v", p, err)
	}
}

func prependPATH(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---- AcquireCorpus -------------------------------------------------------

func TestAcquireCorpus_ProducesWorkingClone(t *testing.T) {
	dataDir := t.TempDir()
	wantHead := newFixtureDB(t, dataDir, "my_test_db", 2)

	destDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", destDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, ".dolt")); err != nil {
		t.Fatalf("destDir is not a dolt working copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, ".git")); err == nil {
		t.Fatalf("destDir has a .git directory — AcquireCorpus must never use git")
	}

	gotHead := firstCommitHashFromLog(t, runDolt(t, destDir, "log", "-n", "1"))
	if gotHead != wantHead {
		t.Fatalf("cloned HEAD = %s, want %s (source)", gotHead, wantHead)
	}
	if got := doltSQLCSV(t, destDir, "SELECT COUNT(*) FROM issues"); got != "2" {
		t.Fatalf("cloned issues count = %s, want 2", got)
	}
}

func TestAcquireCorpus_SourceUntouched(t *testing.T) {
	dataDir := t.TempDir()
	newFixtureDB(t, dataDir, "my_test_db", 1)
	srcDir := filepath.Join(dataDir, "my_test_db")
	before := hashTree(t, srcDir)

	destDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", destDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}

	after := hashTree(t, srcDir)
	if before != after {
		t.Fatalf("AcquireCorpus modified source directory content (excl. LOCK/journal.idx bookkeeping files):\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestAcquireCorpus_NeverInvokesGit(t *testing.T) {
	dataDir := t.TempDir()
	newFixtureDB(t, dataDir, "my_test_db", 1)

	gitCalled := filepath.Join(t.TempDir(), "git-called.log")
	installGitTripwire(t, gitCalled)

	destDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", destDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}
	if b, err := os.ReadFile(gitCalled); err == nil {
		t.Fatalf("AcquireCorpus invoked git, which it must never do:\n%s", b)
	}
}

func TestAcquireCorpus_OnlyFileSchemeNeverNetworkOrServer(t *testing.T) {
	dataDir := t.TempDir()
	newFixtureDB(t, dataDir, "my_test_db", 1)

	logPath := filepath.Join(t.TempDir(), "dolt-invocations.log")
	installDoltShim(t, logPath)

	destDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", destDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading shim log: %v", err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "ARGV:") {
		t.Fatalf("shim recorded no dolt invocations at all — AcquireCorpus did not run through the shimmed dolt")
	}
	if strings.Contains(log, "sql-server") {
		t.Fatalf("AcquireCorpus must never start or address a dolt sql-server:\n%s", log)
	}
	if strings.Contains(log, "28231") {
		t.Fatalf("AcquireCorpus must never reference the shared server's port in any subprocess argument:\n%s", log)
	}
	for _, line := range strings.Split(log, "\n") {
		if !strings.HasPrefix(line, "ARGV:") {
			continue
		}
		if strings.Contains(line, "://") && !strings.Contains(line, "file://") {
			t.Fatalf("dolt invoked with a non-file:// URL — must be filesystem-level only: %s", line)
		}
	}
}

func TestAcquireCorpus_SanitizesChildEnv(t *testing.T) {
	dataDir := t.TempDir()
	newFixtureDB(t, dataDir, "my_test_db", 1)

	watched := []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_ACTOR", "BD_ACTOR", "GT_ROOT"}
	for _, name := range watched {
		t.Setenv(name, "poisoned-"+name)
	}

	logPath := filepath.Join(t.TempDir(), "dolt-env.log")
	installDoltShim(t, logPath, watched...)

	destDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", destDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading shim log: %v", err)
	}
	if strings.Contains(string(logBytes), "poisoned-") {
		t.Fatalf("AcquireCorpus leaked an ambient ...PORT/...ACTOR/...ROOT env var into the dolt subprocess (the exact beads-testdb-production-leak mechanism):\n%s", logBytes)
	}
}

func TestAcquireCorpus_MissingSourceDB_ErrorsWithoutCreating(t *testing.T) {
	dataDir := t.TempDir() // deliberately empty — no "my_test_db" subdirectory exists
	destDir := filepath.Join(t.TempDir(), "clone1")

	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", destDir); err == nil {
		t.Fatalf("AcquireCorpus succeeded against a data_dir with no such database — want an error")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "my_test_db")); err == nil {
		t.Fatalf("AcquireCorpus created a database at the missing source path — it must only ever read, never create")
	}
}

// startDoltSQLServer serves dataDir with a real `dolt sql-server` on a free
// loopback port, logging at debug level to the returned path, and stops it at
// test cleanup. It returns once the server has written
// <dataDir>/.dolt/sql-server.info, the record the dolt CLI uses to route
// commands for any database under dataDir through it, and has accepted a
// readiness probe. The returned offset is the log length after the probe's
// own NewConnection line, so callers inspect only what came later.
func startDoltSQLServer(t *testing.T, dataDir string) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	logPath := filepath.Join(t.TempDir(), "sql-server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("creating server log: %v", err)
	}
	cmd := exec.Command(realDolt, "sql-server", "--host", "127.0.0.1", "--port", fmt.Sprint(port), "--data-dir", dataDir, "-l", "debug")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = sanitizedEnv(os.Environ())
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting dolt sql-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
	})

	infoPath := filepath.Join(dataDir, ".dolt", "sql-server.info")
	deadline := time.Now().Add(30 * time.Second)
	probed := false
	for {
		if !probed {
			if _, err := os.Stat(infoPath); err == nil {
				if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
					_ = conn.Close()
					probed = true
				}
			}
		}
		if probed {
			if log, err := os.ReadFile(logPath); err == nil && strings.Contains(string(log), "NewConnection") {
				return logPath, len(log)
			}
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(logPath)
			t.Fatalf("dolt sql-server did not come up on port %d within 30s:\n%s", port, log)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestAcquireCorpus_ServedSourceNeverConnects covers the deployment this tool
// exists for: a dolt sql-server is serving the source's data dir. The dolt CLI
// routes any command for a database under a served data dir through the
// server instead of opening the files, with no trace in its argv, so this
// asserts on the server's own connection log. The positive control at the end
// proves that log does record a connection when one is made.
func TestAcquireCorpus_ServedSourceNeverConnects(t *testing.T) {
	dataDir := t.TempDir()
	wantHead := newFixtureDB(t, dataDir, "my_test_db", 2)
	serverLog, offset := startDoltSQLServer(t, dataDir)
	logSinceStart := func() string {
		t.Helper()
		b, err := os.ReadFile(serverLog)
		if err != nil {
			t.Fatalf("reading server log: %v", err)
		}
		return string(b[offset:])
	}

	destDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", destDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}
	got, err := MeasureCorpus(context.Background(), destDir, "my_test_db", "127.0.0.1:28231", 0)
	if err != nil {
		t.Fatalf("MeasureCorpus: %v", err)
	}
	if got.SnapshotCommitHash != wantHead || got.IssueCount != 2 {
		t.Fatalf("clone of a served source = head %s, %d issues; want head %s, 2 issues", got.SnapshotCommitHash, got.IssueCount, wantHead)
	}
	if log := logSinceStart(); strings.Contains(log, "NewConnection") {
		t.Fatalf("acquiring and measuring a served source opened a connection to its dolt sql-server:\n%s", log)
	}

	// Positive control: the same query run against the served source itself
	// is routed through the server and shows up in its log.
	runDolt(t, filepath.Join(dataDir, "my_test_db"), "sql", "-q", "SELECT 1")
	if log := logSinceStart(); !strings.Contains(log, "NewConnection") {
		t.Fatalf("positive control failed: a query against the served source left no NewConnection in the server log, so this test cannot detect a connection:\n%s", log)
	}
}

func TestAcquireCorpus_RefusesDestinationInsideServedDataDir(t *testing.T) {
	dataDir := t.TempDir()
	newFixtureDB(t, dataDir, "my_test_db", 1)
	startDoltSQLServer(t, dataDir)

	destDir := filepath.Join(dataDir, "corpus-copy")
	err := AcquireCorpus(context.Background(), dataDir, "my_test_db", destDir)
	if err == nil || !strings.Contains(err.Error(), dataDir) {
		t.Fatalf("AcquireCorpus into a served data dir = %v, want a refusal naming the served data dir %s", err, dataDir)
	}
	if _, statErr := os.Stat(destDir); !os.IsNotExist(statErr) {
		t.Fatalf("AcquireCorpus created %s before refusing (stat: %v)", destDir, statErr)
	}
}

// ---- MeasureCorpus --------------------------------------------------------

func TestMeasureCorpus_PopulatesCorpusCloneFields(t *testing.T) {
	dataDir := t.TempDir()
	const issueCount = 3
	wantHead := newFixtureDB(t, dataDir, "my_test_db", issueCount)
	wantCommits := issueCount + 2 // dolt init's own commit + the create-table commit

	cloneDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", cloneDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}

	got, err := MeasureCorpus(context.Background(), cloneDir, "my_test_db", "127.0.0.1:28231", 0)
	if err != nil {
		t.Fatalf("MeasureCorpus: %v", err)
	}
	if got.SourceDB != "my_test_db" {
		t.Errorf("SourceDB = %q, want %q", got.SourceDB, "my_test_db")
	}
	if got.SourceServer != "127.0.0.1:28231" {
		t.Errorf("SourceServer = %q, want %q", got.SourceServer, "127.0.0.1:28231")
	}
	if got.SnapshotCommitHash != wantHead {
		t.Errorf("SnapshotCommitHash = %q, want %q", got.SnapshotCommitHash, wantHead)
	}
	if got.IssueCount != issueCount {
		t.Errorf("IssueCount = %d, want %d", got.IssueCount, issueCount)
	}
	if got.DoltLogCommitCount != wantCommits {
		t.Errorf("DoltLogCommitCount = %d, want %d", got.DoltLogCommitCount, wantCommits)
	}
	if got.OldestCommitAt.IsZero() || got.NewestCommitAt.IsZero() {
		t.Fatalf("OldestCommitAt/NewestCommitAt not populated: %+v", got)
	}
	if got.OldestCommitAt.After(got.NewestCommitAt) {
		t.Errorf("OldestCommitAt (%v) is after NewestCommitAt (%v)", got.OldestCommitAt, got.NewestCommitAt)
	}
	if got.Partial {
		t.Errorf("Partial = true with no baseline supplied (0), want false")
	}
}

func TestMeasureCorpus_FlagsPartialWhenDepthBelowBaseline(t *testing.T) {
	dataDir := t.TempDir()
	newFixtureDB(t, dataDir, "my_test_db", 2) // 4 commits total
	cloneDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", cloneDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}

	got, err := MeasureCorpus(context.Background(), cloneDir, "my_test_db", "127.0.0.1:28231", 500)
	if err != nil {
		t.Fatalf("MeasureCorpus: %v", err)
	}
	if !got.Partial {
		t.Fatalf("Partial = false, want true: actual depth (%d) is materially below the supplied baseline (500)", got.DoltLogCommitCount)
	}
	if got.PartialReason == "" {
		t.Errorf("PartialReason is empty, want an explanation naming the baseline and actual depth")
	}
}

func TestMeasureCorpus_NoFlagWhenBaselineAtOrBelowActual(t *testing.T) {
	dataDir := t.TempDir()
	newFixtureDB(t, dataDir, "my_test_db", 2) // 4 commits total
	cloneDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", cloneDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}

	got, err := MeasureCorpus(context.Background(), cloneDir, "my_test_db", "127.0.0.1:28231", 4)
	if err != nil {
		t.Fatalf("MeasureCorpus: %v", err)
	}
	if got.Partial {
		t.Fatalf("Partial = true, want false: actual depth (%d) meets the supplied baseline (4)", got.DoltLogCommitCount)
	}
}

func TestMeasureCorpus_NeverTouchesNetworkOrServer(t *testing.T) {
	dataDir := t.TempDir()
	newFixtureDB(t, dataDir, "my_test_db", 1)
	cloneDir := filepath.Join(t.TempDir(), "clone1")
	if err := AcquireCorpus(context.Background(), dataDir, "my_test_db", cloneDir); err != nil {
		t.Fatalf("AcquireCorpus: %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "dolt-measure.log")
	installDoltShim(t, logPath)

	if _, err := MeasureCorpus(context.Background(), cloneDir, "my_test_db", "127.0.0.1:28231", 0); err != nil {
		t.Fatalf("MeasureCorpus: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading shim log: %v", err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "ARGV:") {
		t.Fatalf("shim recorded no dolt invocations — MeasureCorpus did not run through the shimmed dolt")
	}
	if strings.Contains(log, "sql-server") || strings.Contains(log, "--host") || strings.Contains(log, "28231") {
		t.Fatalf("MeasureCorpus must query the local clone only, never address the shared server:\n%s", log)
	}
}

// ---- naming guard (NFR5/R2) -----------------------------------------------

func TestValidateMintedName(t *testing.T) {
	// pureAV is a source of characters entirely within [0-9a-v] to slice
	// boundary-length test cases from, so length arithmetic is verified by
	// the slice expression itself rather than by hand-counting a literal.
	pureAV := strings.Repeat("0123456789abcdefghijklmnopqrstuv", 2) // 64 chars

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty string", "", false},
		{"real-world false positive: productionmirror (16 chars, all a-v)", "productionmirror", true},
		{"real-world false positive: releasecandidate (16 chars, all a-v)", "releasecandidate", true},
		{"real-world false positive: integrationtests (16 chars, all a-v)", "integrationtests", true},
		{"lower boundary: 16 chars all [0-9a-v]", pureAV[:16], true},
		{"upper boundary: 31 chars all [0-9a-v]", pureAV[:31], true},
		{"just below lower boundary: 15 chars", pureAV[:15], false},
		{"just above upper boundary: 32 chars (a real dolt commit hash length)", pureAV[:32], false},
		{"hyphens always break the shape regardless of length", "phase4-replay-my_db-run1", false},
		{"underscores always break the shape regardless of length", "my_db_corpus_run_2026", false},
		{"uppercase always breaks the shape regardless of length", "MyDb2026Corpus", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMintedName(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateMintedName(%q) = nil, want an error (matches the dangerous ^[0-9a-v]{16,31}$ shape)", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateMintedName(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

func TestGenerateCloneID_NeverMatchesDangerousShape(t *testing.T) {
	cases := []struct {
		db  string
		now time.Time
	}{
		{"my_db", time.Date(2026, 9, 26, 19, 50, 0, 0, time.UTC)},
		{"gascity", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"my_db", time.Date(2030, 12, 31, 23, 59, 59, 0, time.UTC)},
	}
	for _, c := range cases {
		id := GenerateCloneID(c.db, c.now)
		if id == "" {
			t.Errorf("GenerateCloneID(%q, %v) returned an empty string", c.db, c.now)
			continue
		}
		if err := ValidateMintedName(id); err != nil {
			t.Errorf("GenerateCloneID(%q, %v) = %q, which fails this tool's own naming guard: %v", c.db, c.now, id, err)
		}
	}
}

// ---- env sanitization (unit-level) ----------------------------------------

func TestSanitizedEnv_StripsDoltServerAndActorVars(t *testing.T) {
	in := []string{
		"BEADS_DOLT_SERVER_PORT=28231",
		"BEADS_DOLT_PORT=28231",
		"BEADS_ACTOR=someone",
		"BD_ACTOR=someone",
		"GT_ROOT=/should/not/leak",
		"PATH=/usr/bin:/bin",
		"HOME=/home/jaword",
	}
	stripped := map[string]bool{
		"BEADS_DOLT_SERVER_PORT": true,
		"BEADS_DOLT_PORT":        true,
		"BEADS_ACTOR":            true,
		"BD_ACTOR":               true,
		"GT_ROOT":                true,
	}

	out := sanitizedEnv(in)

	for _, kv := range out {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if stripped[name] {
			t.Errorf("sanitizedEnv left %q in the output, want it stripped", kv)
		}
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "PATH=") || !strings.Contains(joined, "HOME=") {
		t.Errorf("sanitizedEnv stripped an unrelated var — PATH/HOME must survive:\n%s", joined)
	}
}
