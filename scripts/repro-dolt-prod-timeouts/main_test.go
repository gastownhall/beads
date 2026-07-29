package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

const (
	fakeBDModeEnv      = "BEADS_REPRO_TIMEOUTS_TEST_BD_MODE"
	fakeBDParentPIDEnv = "BEADS_REPRO_TIMEOUTS_TEST_BD_PARENT_PID"
	fakeBDSuccess      = "BEADS_REPRO_TIMEOUTS_TEST_BD_OK"
)

// init handles the self-reexec fixture before the generated test runner parses
// the production-style bd arguments appended by runBD. Both executable name
// and live parent identity keep an ambient environment value from turning the
// ordinary test runner into a successful helper process.
func init() {
	mode := os.Getenv(fakeBDModeEnv)
	if mode == "" || filepath.Base(os.Args[0]) != nativeBDExecutableName() {
		return
	}
	if os.Getenv(fakeBDParentPIDEnv) != strconv.Itoa(os.Getppid()) {
		fmt.Fprintln(os.Stderr, "fake bd parent identity mismatch")
		os.Exit(95)
	}
	os.Exit(runFakeBDTestProcess(mode))
}

func runFakeBDTestProcess(mode string) int {
	if mode != "assert-no-dolt-overrides" {
		fmt.Fprintf(os.Stderr, "unknown fake bd mode %q\n", mode)
		return 97
	}
	if !slices.Equal(os.Args[1:], []string{"status"}) {
		fmt.Fprintf(os.Stderr, "fake bd args = %q, want [status]\n", os.Args[1:])
		return 96
	}
	for _, key := range subprocessEnvDenylist {
		if value := os.Getenv(key); value != "" {
			fmt.Fprintf(os.Stderr, "%s inherited: %s\n", key, value)
			return 42
		}
	}
	fmt.Fprintln(os.Stderr, fakeBDSuccess)
	return 0
}

func TestIsDriverReadTimeout(t *testing.T) {
	result := opResult{
		StderrTail: "[mysql] 2026/05/13 18:13:48 packets.go:58 read tcp 127.0.0.1:39308->127.0.0.1:21791: i/o timeout",
	}
	if !isDriverReadTimeout(result) {
		t.Fatal("expected MySQL driver read timeout to be classified")
	}
}

func TestIsDriverReadTimeoutIgnoresHarnessTimeout(t *testing.T) {
	result := opResult{
		TimedOut:   true,
		StderrTail: "signal: killed",
	}
	if isDriverReadTimeout(result) {
		t.Fatal("harness timeout should not be classified as driver read timeout")
	}
}

func TestMixedBackgroundJobsIncludesSessionLoadShapes(t *testing.T) {
	jobs := mixedBackgroundJobs(12)
	seen := map[string]bool{}
	for _, job := range jobs {
		seen[job.Kind] = true
	}
	for _, want := range []string{"session-ready", "control-ready", "route-ready", "show", "list", "claim"} {
		if !seen[want] {
			t.Fatalf("mixed background jobs missing %q; seen=%v", want, seen)
		}
	}
}

func TestDepFixtureIssueCountIncludesChainTargets(t *testing.T) {
	if got := depFixtureIssueCount(10, 0); got != 20 {
		t.Fatalf("without chains got %d, want 20", got)
	}
	if got := depFixtureIssueCount(10, 100); got != 1132 {
		t.Fatalf("with chains got %d, want 1132", got)
	}
}

func TestResolveExecutablePathReturnsAbsolutePath(t *testing.T) {
	tmp := t.TempDir()
	bd := writeNativeBDTestExecutable(t, tmp)
	t.Chdir(tmp)

	got, err := resolveExecutablePath("./bd")
	if err != nil {
		t.Fatal(err)
	}
	if got != bd {
		t.Fatalf("got %q, want %q", got, bd)
	}
}

func TestParseFlagsDefaultsSyntheticWorkspaceToFullSeed(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SeedMode != "full" {
		t.Fatalf("SeedMode = %q, want full", cfg.SeedMode)
	}
}

func TestParseFlagsDefaultsExistingWorkspaceToNoSeed(t *testing.T) {
	cfg, err := parseFlags([]string{"--workspace", "/tmp/existing-beads-workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SeedMode != "none" {
		t.Fatalf("SeedMode = %q, want none", cfg.SeedMode)
	}
}

func TestParseFlagsPreservesExplicitExistingWorkspaceSeedMode(t *testing.T) {
	cfg, err := parseFlags([]string{"--workspace", "/tmp/existing-beads-workspace", "--seed-mode", "full"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SeedMode != "full" {
		t.Fatalf("SeedMode = %q, want full", cfg.SeedMode)
	}
}

func TestBenchmarkSubprocessesStripDoltEnvOverrides(t *testing.T) {
	for _, key := range subprocessEnvDenylist {
		t.Setenv(key, "caller-"+strings.ToLower(key))
	}

	tmp := t.TempDir()
	bd := writeNativeBDTestExecutable(t, tmp)

	cfg := config{BDPath: bd, Timeout: 5 * time.Second}
	ws := &workspace{Dir: tmp}
	fakeEnv := []string{
		fakeBDModeEnv + "=assert-no-dolt-overrides",
		fakeBDParentPIDEnv + "=" + strconv.Itoa(os.Getpid()),
	}
	got := runBD(context.Background(), cfg, ws, job{
		Kind: "env",
		Argv: []string{"status"},
		Env:  fakeEnv,
	})
	if got.Err != "" {
		t.Fatalf("runBD inherited denied env: err=%q stderr=%q", got.Err, got.StderrTail)
	}
	if got.StderrTail != fakeBDSuccess {
		t.Fatalf("runBD fake receipt = %q, want %q", got.StderrTail, fakeBDSuccess)
	}
	if got := runShell(context.Background(), cfg, ws, job{Kind: "env", Sh: noDoltOverrideEnvScript()}); got.Err != "" {
		t.Fatalf("runShell inherited denied env: err=%q stderr=%q", got.Err, got.StderrTail)
	}
}

func TestNativeBDHelperRejectsWrongParent(t *testing.T) {
	bd := writeNativeBDTestExecutable(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bd, "-test.run=^$")
	cmd.WaitDelay = time.Second
	cmd.Env = subprocessEnv(
		fakeBDModeEnv+"=assert-no-dolt-overrides",
		fakeBDParentPIDEnv+"=0",
	)

	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("wrong-parent fake bd did not terminate promptly: %v", ctx.Err())
	}
	if err == nil {
		t.Fatalf("wrong-parent fake bd succeeded: %s", output)
	}
	if !strings.Contains(string(output), "fake bd parent identity mismatch") {
		t.Fatalf("wrong-parent output = %q", output)
	}
}

func TestControlQueryScriptPreservesReadyProbeFailure(t *testing.T) {
	tmp := t.TempDir()
	bd := filepath.Join(tmp, "bd")
	if err := os.WriteFile(bd, []byte("#!/bin/sh\necho ready failed >&2\nexit 17\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config{BDPath: bd, Timeout: time.Second}
	ws := &workspace{Dir: tmp}
	result := runShell(context.Background(), cfg, ws, controlQueryJobs(1)[0])
	if result.Err == "" {
		t.Fatalf("expected control query shell to fail when bd ready fails; stderr=%q", result.StderrTail)
	}
	if !strings.Contains(result.StderrTail, "ready failed") {
		t.Fatalf("stderr tail = %q, want ready probe stderr", result.StderrTail)
	}
}

func writeNativeBDTestExecutable(t *testing.T, directory string) string {
	t.Helper()

	sourcePath, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve current test executable: %v", err)
	}
	targetPath := filepath.Join(directory, nativeBDExecutableName())

	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open current test executable: %v", err)
	}
	defer source.Close()

	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatalf("create native test executable: %v", err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatalf("copy native test executable: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close native test executable: %v", err)
	}
	return targetPath
}

func nativeBDExecutableName() string {
	if runtime.GOOS == "windows" {
		return "bd.exe"
	}
	return "bd"
}

func noDoltOverrideEnvScript() string {
	var b strings.Builder
	b.WriteString("for key in")
	for _, key := range subprocessEnvDenylist {
		b.WriteByte(' ')
		b.WriteString(key)
	}
	b.WriteString(`; do
	eval "value=\${$key:-}"
	if [ -n "$value" ]; then
		echo "$key inherited: $value" >&2
		exit 42
	fi
done
exit 0
`)
	return b.String()
}

func TestScenarioNamesAllIncludesEveryAdvertisedScenario(t *testing.T) {
	got, err := scenarioNames("all")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ready",
		"dep",
		"control",
		"mixed",
		"outage",
		"cycle-current",
		"cycle-deps-only",
		"cycle-wisps-only",
		"cycle-bfs",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scenarioNames(all) got %v, want %v", got, want)
	}
}

func TestInsertDependenciesWritesTypedIssueTargetColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(`INSERT INTO dependencies\s+\(id, issue_id, depends_on_issue_id, type, created_by, metadata\)\s+VALUES`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := insertDependencies(context.Background(), db, 1, 100000); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDependenciesUsesConfiguredIssueRange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(`INSERT INTO dependencies\s+\(id, issue_id, depends_on_issue_id, type, created_by, metadata\)\s+VALUES`).
		WithArgs(
			depid.New("perf-000000", "perf-000004"), "perf-000000", "perf-000004", "blocks", "bench", "{}",
			depid.New("perf-000001", "perf-000000"), "perf-000001", "perf-000000", "blocks", "bench", "{}",
			depid.New("perf-000002", "perf-000001"), "perf-000002", "perf-000001", "blocks", "bench", "{}",
			depid.New("perf-000003", "perf-000002"), "perf-000003", "perf-000002", "blocks", "bench", "{}",
			depid.New("perf-000004", "perf-000003"), "perf-000004", "perf-000003", "blocks", "bench", "{}",
		).
		WillReturnResult(sqlmock.NewResult(0, 5))

	if err := insertDependencies(context.Background(), db, 5, 5); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertDependenciesRejectsImpossibleUniquePairCount(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	err = insertDependencies(context.Background(), db, 3, 2)
	if err == nil {
		t.Fatal("expected impossible pair count error")
	}
	if !strings.Contains(err.Error(), "exceeds unique dependency pairs") {
		t.Fatalf("error = %v, want unique dependency pair failure", err)
	}
}

func TestInsertDepAddChainsWritesTypedIssueTargetColumn(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(`INSERT INTO dependencies\s+\(id, issue_id, depends_on_issue_id, type, created_by, metadata\)\s+VALUES`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := insertDepAddChains(context.Background(), db, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCycleCheckCurrentSQLUsesTypedDependencyTargetProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(dependencyTargetExpr)).
		WithArgs("perf-000002", "perf-000001").
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(0))

	if err := cycleCheckCurrentSQL(context.Background(), db, "perf-000001", "perf-000002", 0); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFetchBlockingTargetsUsesTypedDependencyTargetProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(dependencyTargetExpr)).
		WithArgs("perf-000001", "perf-000001").
		WillReturnRows(sqlmock.NewRows([]string{"target_id"}).AddRow("perf-000002"))

	got, err := fetchBlockingTargets(context.Background(), db, []string{"perf-000001"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"perf-000002"}) {
		t.Fatalf("targets = %v, want [perf-000002]", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSeedProductionShapeFullCoversTypedDependencyInserts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(`INSERT INTO issues\s+\(id, title, description, design, acceptance_criteria, notes,\s+status, priority, issue_type, assignee, metadata\)\s+VALUES`).
		WillReturnResult(sqlmock.NewResult(0, 8))
	mock.ExpectExec(`INSERT INTO dependencies\s+\(id, issue_id, depends_on_issue_id, type, created_by, metadata\)\s+VALUES`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO dependencies\s+\(id, issue_id, depends_on_issue_id, type, created_by, metadata\)\s+VALUES`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_ADD('-A')")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', 'seed production timeout fixture')")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	cfg := config{SeedMode: "full", IssueCount: 1, DepCount: 1, Ops: 1, ChainDepth: 1}
	if err := seedProductionShape(context.Background(), db, cfg); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSeedProductionShapeFullSmallIssueCountRealSchema(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed, skipping real-schema smoke")
	}
	skipOldDoltForCurrentSchema(t)

	ctx := context.Background()
	baseDir := t.TempDir()
	dbName := "testdb"
	dbDir := filepath.Join(baseDir, dbName)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestCmd(t, dbDir, "dolt", "init", "--name", "test", "--email", "test@example.com")

	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatal(err)
	}
	serverCmd := exec.Command("dolt", "sql-server",
		"-H", "127.0.0.1",
		"-P", fmt.Sprintf("%d", port),
	)
	serverCmd.Dir = baseDir
	if err := serverCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	})
	if !testutil.WaitForServer(port, 15*time.Second) {
		t.Fatal("dolt sql-server did not become ready")
	}

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: dbName, Timeout: 10 * time.Second}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	if _, err := schema.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	cfg := config{SeedMode: "full", IssueCount: 50, DepCount: 50, Ops: 5, ChainDepth: 2}
	if err := seedProductionShape(ctx, db, cfg); err != nil {
		t.Fatal(err)
	}

	var depRows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dependencies").Scan(&depRows); err != nil {
		t.Fatal(err)
	}
	wantDeps := cfg.DepCount + cfg.Ops*cfg.ChainDepth
	if depRows != wantDeps {
		t.Fatalf("dependency rows = %d, want %d", depRows, wantDeps)
	}

	sourceID, targetID := dependencyEndpoints(0, cfg.IssueCount, 0, 200)
	if err := cycleCheckCurrentSQL(ctx, db, perfIssueID(1), sourceID, 0); err != nil {
		t.Fatalf("cycleCheckCurrentSQL: %v", err)
	}
	targets, err := fetchBlockingTargets(ctx, db, []string{sourceID})
	if err != nil {
		t.Fatalf("fetchBlockingTargets: %v", err)
	}
	if !slices.Contains(targets, targetID) {
		t.Fatalf("fetchBlockingTargets(%q) = %v, want %q", sourceID, targets, targetID)
	}
}

func skipOldDoltForCurrentSchema(t *testing.T) {
	t.Helper()
	output, err := exec.Command("dolt", "version").CombinedOutput()
	if err != nil {
		t.Skipf("dolt version unavailable, skipping real-schema smoke: %v", err)
	}
	if regexp.MustCompile(`\bdolt version 1\.`).Match(output) {
		t.Skipf("dolt 1.x cannot initialize the current migration set: %s", strings.TrimSpace(string(output)))
	}
}

func runTestCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed in %s: %v\nOutput: %s", name, args, dir, err, output)
	}
}

func runTestDoltSQL(t *testing.T, dir, query string) {
	t.Helper()
	cmd := exec.Command("dolt", "sql", "-q", query)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt sql failed in %s: %v\nQuery: %.200s...\nOutput: %s", dir, err, query, output)
	}
}
