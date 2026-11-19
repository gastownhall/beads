//go:build cgo

package main

import (
	"context"
	"encoding/json"
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

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/workspacegate"
	"golang.org/x/sync/errgroup"
)

// ---------------------------------------------------------------------------
// Test configuration
// ---------------------------------------------------------------------------

func ssEnvInt(key string, def int) int {
	if s := os.Getenv(key); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ---------------------------------------------------------------------------
// TestSharedServerConcurrent
// ---------------------------------------------------------------------------

// TestSharedServerConcurrent builds the bd binary, starts a single Dolt
// container via testcontainers, initializes numDirs project directories,
// then fans out numClients concurrent workloads across those directories.
// Multiple clients may share a directory (and therefore a database),
// exercising concurrent multi-writer access to the same Dolt database.
//
// Skipped by default. It runs when either variable below is set:
//
//	BEADS_TEST_SHARED_SERVER=1            — the original manual stress run
//	BEADS_TEST_SHARED_SERVER_CONCURRENT=1 — what //cmd/bd:bd_dolt_server_test
//	                                         sets, together with small
//	                                         BEADS_TEST_SS_DIRS/CLIENTS, so
//	                                         --config=doltserver-cmd runs a
//	                                         bounded smoke of it against the
//	                                         lane's hermetic dolt sql-server
//
// Configuration via environment variables:
//
//	BEADS_TEST_SS_DIRS     — number of project directories  (default: 50)
//	BEADS_TEST_SS_CLIENTS  — number of concurrent clients   (default: 500)
//	BEADS_TEST_SS_MAXPROCS — max concurrent subprocesses    (default: GOMAXPROCS*4)
//
// Recommended: set BEADS_TEST_EMBEDDED_DOLT=1 to skip the unrelated
// singleton Dolt container that TestMain starts for other tests in this package.
func TestSharedServerConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_SHARED_SERVER") == "" && os.Getenv("BEADS_TEST_SHARED_SERVER_CONCURRENT") != "1" {
		t.Skip("skipping: set BEADS_TEST_SHARED_SERVER=1 (or BEADS_TEST_SHARED_SERVER_CONCURRENT=1) to run")
	}
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}

	numDirs := ssEnvInt("BEADS_TEST_SS_DIRS", 50)
	numClients := ssEnvInt("BEADS_TEST_SS_CLIENTS", 500)
	maxProcs := ssEnvInt("BEADS_TEST_SS_MAXPROCS", runtime.GOMAXPROCS(0)*4)
	t.Logf("config: dirs=%d clients=%d maxprocs=%d", numDirs, numClients, maxProcs)

	testStart := time.Now()

	// Build or reuse bd binary.
	phase := time.Now()
	bdBinary := buildSharedServerTestBinary(t)
	t.Logf("build bd binary: %s", time.Since(phase))

	// Start Dolt container.
	phase = time.Now()
	cp, err := testutil.NewContainerProvider()
	if err != nil {
		// A lane that exists to run the Dolt server suites must not pass
		// green having skipped this test.
		if os.Getenv(testutil.EnvRequireDoltContainer) == "1" {
			t.Fatalf("cannot start Dolt server, but %s=1: %v", testutil.EnvRequireDoltContainer, err)
		}
		t.Skipf("cannot start Dolt container: %v", err)
	}
	containerPort := cp.Port()
	t.Cleanup(func() { _ = cp.Stop() })
	t.Logf("start container (port %d): %s", containerPort, time.Since(phase))

	// Shared server directory + port file.
	sharedDir := t.TempDir()
	if err := cp.WritePortFile(sharedDir); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	// A RESIDENT SHARED HOLDER of the shared root (GH#7122): in production
	// every project with a `bd serve` running holds the machine-global
	// shared dolt dir SHARED for that serve's whole lifetime, and this
	// smoke must model it — the test process plays the holder through the
	// same gate file, WITHOUT advertising an inherited hold (this is not
	// a child of a bd command). Ordinary shared-server init takes that
	// root SHARED (AC-1/AC-7), so every init and every client command
	// below must proceed beside it; at base, where init took the root
	// EXCLUSIVELY, this holder refuses the very first init.
	rootGate, gerr := workspacegate.ForPhysicalRoot(filepath.Join(sharedDir, "dolt"))
	if gerr != nil {
		t.Fatalf("shared root gate: %v", gerr)
	}
	residentHold, gerr := rootGate.Acquire(context.Background(), workspacegate.Shared,
		workspacegate.Options{Reason: "resident bd serve (test holder)"})
	if gerr != nil {
		t.Fatalf("resident shared hold of the shared root: %v", gerr)
	}
	t.Cleanup(func() { _ = residentHold.Release() })

	// Base environment for every bd subprocess.
	baseEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOPATH=" + os.Getenv("GOPATH"),
		"GOROOT=" + os.Getenv("GOROOT"),
		"BEADS_SHARED_SERVER_DIR=" + sharedDir,
		"BEADS_DOLT_SHARED_SERVER=1",
		"BEADS_DOLT_SERVER_PORT=" + strconv.Itoa(containerPort),
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_TEST_MODE=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GT_ROOT=",
	}

	// Context inherits from -timeout.
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	// ── Init project directories ────────────────────────────────────────
	// All at once: in shared-server mode every project's physical root is
	// the one shared dolt dir, and an ORDINARY init holds that root SHARED
	// (GH#7122: init only creates a new database on the shared server;
	// destructive reinit is the plan that still takes it exclusively), so
	// concurrent inits of different projects no longer serialize on the
	// root gate at all — and no raised BEADS_INIT_GATE_TIMEOUT is needed
	// for larger runs.
	initEnv := baseEnv
	phase = time.Now()
	type project struct {
		dir, prefix string
	}
	projects := make([]project, numDirs)

	eg, egCtx := errgroup.WithContext(ctx)
	for i := range numDirs {
		i := i
		eg.Go(func() error {
			prefix := fmt.Sprintf("proj%d", i)
			dir := filepath.Join(t.TempDir(), prefix)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("project %d mkdir: %w", i, err)
			}
			if err := gitInit(egCtx, dir); err != nil {
				return fmt.Errorf("project %d git init: %w", i, err)
			}
			out, err := ssExec(egCtx, bdBinary, dir, initEnv,
				"init", "--shared-server", "--external",
				"--prefix", prefix, "--quiet", "--non-interactive")
			if err != nil {
				return fmt.Errorf("project %d init: %s: %w", i, out, err)
			}
			projects[i] = project{dir: dir, prefix: prefix}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Logf("init %d dirs: %s", numDirs, time.Since(phase))

	// ── Fan out client workloads ────────────────────────────────────────
	// Plus one late `bd init` of a fresh project that starts while the
	// workloads are running. Since GH#7122 the late init holds the shared
	// physical root SHARED, so the steady stream of client commands neither
	// waits for it nor is waited out by it — what the late init still proves
	// is that a fresh project initializes DURING live cross-project traffic
	// on the same server (and, once any regression reintroduces an
	// exclusive root hold, writer fairness both ways: clients queue behind
	// the init, BEADS_GATE_WAIT_TIMEOUT, and the init gets the gate despite
	// the stream). Its bound stays pinned to 30s, half the 60s default: a
	// generous bound would mask a fairness regression.
	phase = time.Now()
	eg, egCtx = errgroup.WithContext(ctx)
	eg.SetLimit(maxProcs + 1) // +1: the late init below must not take a client's slot
	eg.Go(func() error {
		select {
		case <-time.After(time.Second): // let the workloads get going
		case <-egCtx.Done():
			return egCtx.Err()
		}
		dir := filepath.Join(t.TempDir(), "projlate")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("late project mkdir: %w", err)
		}
		if err := gitInit(egCtx, dir); err != nil {
			return fmt.Errorf("late project git init: %w", err)
		}
		start := time.Now()
		lateEnv := append(append([]string{}, baseEnv...), initGateTimeoutEnv+"=30s")
		out, err := ssExec(egCtx, bdBinary, dir, lateEnv,
			"init", "--shared-server", "--external",
			"--prefix", "projlate", "--quiet", "--non-interactive")
		if err != nil {
			return fmt.Errorf("late init during client workloads: %s: %w", out, err)
		}
		t.Logf("late init during client workloads: %s", time.Since(start))
		if out, err := ssExec(egCtx, bdBinary, dir, baseEnv, "list", "--json", "--flat"); err != nil {
			return fmt.Errorf("list in late project: %s: %w", out, err)
		}
		return nil
	})
	for c := range numClients {
		c := c
		eg.Go(func() error {
			p := projects[c%numDirs]
			cl := &bdClient{
				tag:    fmt.Sprintf("c%d", c),
				binary: bdBinary,
				dir:    p.dir,
				env:    baseEnv,
				ctx:    egCtx,
				t:      t,
			}
			return cl.runWorkload()
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatalf("workload: %v", err)
	}
	t.Logf("workloads (%d clients x %d dirs): %s", numClients, numDirs, time.Since(phase))
	t.Logf("total: %s", time.Since(testStart))
}

// ---------------------------------------------------------------------------
// bdClient — wraps a bd subprocess invocation for one client
// ---------------------------------------------------------------------------

type bdClient struct {
	tag    string // unique client identifier (e.g. "c42")
	binary string
	dir    string
	env    []string
	ctx    context.Context
	t      *testing.T
	op     int // running operation counter
}

// bd runs an arbitrary bd command and returns combined output.
func (c *bdClient) bd(args ...string) (string, error) {
	c.op++
	start := time.Now()
	out, err := ssExec(c.ctx, c.binary, c.dir, c.env, args...)
	c.t.Logf("%s [op %d] %s — %s", c.tag, c.op, strings.Join(args, " "), time.Since(start))
	return out, err
}

// create runs bd create --json and returns the new issue ID.
func (c *bdClient) create(title string, extra ...string) (string, error) {
	c.op++
	start := time.Now()
	args := append([]string{"create", title, "--json"}, extra...)
	out, err := ssExec(c.ctx, c.binary, c.dir, c.env, args...)
	c.t.Logf("%s [op %d] create %q — %s", c.tag, c.op, title, time.Since(start))
	if err != nil {
		return "", fmt.Errorf("%s: %w", out, err)
	}
	return ssJSONField(out, "id")
}

// show runs bd show <id> --json and returns the parsed issue.
func (c *bdClient) show(id string) (map[string]any, error) {
	c.op++
	start := time.Now()
	out, err := ssExec(c.ctx, c.binary, c.dir, c.env, "show", id, "--json")
	c.t.Logf("%s [op %d] show %s — %s", c.tag, c.op, id, time.Since(start))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", out, err)
	}
	return ssParseShowJSON(out)
}

// list runs bd list --json --flat with extra args and returns the result array.
func (c *bdClient) list(extra ...string) ([]any, error) {
	c.op++
	start := time.Now()
	args := append([]string{"list", "--json", "--flat"}, extra...)
	out, err := ssExec(c.ctx, c.binary, c.dir, c.env, args...)
	c.t.Logf("%s [op %d] list %s — %s", c.tag, c.op, strings.Join(extra, " "), time.Since(start))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", out, err)
	}
	var result []any
	if err := json.Unmarshal([]byte(ssFirstJSON(out)), &result); err != nil {
		return nil, fmt.Errorf("parse list JSON: %w\noutput: %s", err, out)
	}
	return result, nil
}

// errf formats an error with the client tag and current op number.
func (c *bdClient) errf(format string, args ...any) error {
	prefix := fmt.Sprintf("%s [op %d] ", c.tag, c.op)
	return fmt.Errorf(prefix+format, args...)
}

// ---------------------------------------------------------------------------
// Workload — the actual issue management workflow
// ---------------------------------------------------------------------------

func (c *bdClient) runWorkload() error {
	ids, err := c.phaseCreate()
	if err != nil {
		return err
	}
	if err := c.phaseDeps(ids); err != nil {
		return err
	}
	if err := c.phaseUpdate(ids); err != nil {
		return err
	}
	if err := c.phaseVerify(ids); err != nil {
		return err
	}
	if err := c.phaseList(); err != nil {
		return err
	}
	return c.phaseDelete(ids)
}

// phaseCreate creates 30 issues across multiple types and returns their IDs.
//
//	0-9   tasks
//	10-14 bugs (with descriptions)
//	15-19 features
//	20-24 epics
//	25-29 chores (children of epics 20-24)
func (c *bdClient) phaseCreate() ([]string, error) {
	types := []string{
		"task", "task", "task", "task", "task",
		"task", "task", "task", "task", "task",
		"bug", "bug", "bug", "bug", "bug",
		"feature", "feature", "feature", "feature", "feature",
		"epic", "epic", "epic", "epic", "epic",
		"chore", "chore", "chore", "chore", "chore",
	}
	ids := make([]string, len(types))

	for i, typ := range types {
		var id string
		var err error
		switch {
		case i >= 10 && i <= 14:
			id, err = c.create(
				fmt.Sprintf("%s bug %d", c.tag, i),
				"--type", typ,
				"-d", fmt.Sprintf("Bug description for issue %d in %s", i, c.tag),
			)
		case i >= 25:
			id, err = c.create(
				fmt.Sprintf("%s chore %d", c.tag, i),
				"--type", typ,
				"--parent", ids[20+(i-25)],
			)
		default:
			id, err = c.create(
				fmt.Sprintf("%s %s %d", c.tag, typ, i),
				"--type", typ,
			)
		}
		if err != nil {
			return nil, c.errf("create issue %d (%s): %w", i, typ, err)
		}
		ids[i] = id
	}
	return ids, nil
}

// phaseDeps wires dependencies between issues.
func (c *bdClient) phaseDeps(ids []string) error {
	pairs := [][2]int{
		{1, 0}, {2, 0}, {3, 0}, {4, 0}, {5, 0},
		{6, 0}, {7, 0}, {8, 0}, {9, 0}, // tasks 1-9 → task 0
		{16, 15}, {17, 15}, {18, 15}, {19, 15}, // features 16-19 → feature 15
		{11, 10}, // bug 11 → bug 10
	}
	for _, p := range pairs {
		from, to := ids[p[0]], ids[p[1]]
		if out, err := c.bd("dep", "add", from, to, "--json"); err != nil {
			return c.errf("dep add %s->%s: %s: %w", from, to, out, err)
		}
	}
	return nil
}

// phaseUpdate modifies titles, statuses, labels, priorities, and descriptions.
func (c *bdClient) phaseUpdate(ids []string) error {
	// Rename tasks 0-4.
	for i := range 5 {
		if out, err := c.bd("update", ids[i], "--title", fmt.Sprintf("%s task %d UPDATED", c.tag, i)); err != nil {
			return c.errf("update title %s: %s: %w", ids[i], out, err)
		}
	}
	// Tasks 0-2 → in_progress.
	for i := range 3 {
		if out, err := c.bd("update", ids[i], "--status", "in_progress"); err != nil {
			return c.errf("update status %s: %s: %w", ids[i], out, err)
		}
	}
	// Close bugs 10-14.
	for i := 10; i <= 14; i++ {
		if out, err := c.bd("update", ids[i], "--status", "closed"); err != nil {
			return c.errf("close %s: %s: %w", ids[i], out, err)
		}
	}
	// Label tasks 3-6.
	for j, i := range []int{3, 4, 5, 6} {
		label := []string{"urgent", "backend", "frontend", "infra"}[j]
		if out, err := c.bd("update", ids[i], "--add-label", label, "--add-label", c.tag); err != nil {
			return c.errf("add-label %s: %s: %w", ids[i], out, err)
		}
	}
	// Prioritize features 15-19.
	for j, i := range []int{15, 16, 17, 18, 19} {
		pri := []string{"P0", "P1", "P2", "P3", "P4"}[j]
		if out, err := c.bd("update", ids[i], "--priority", pri); err != nil {
			return c.errf("set priority %s: %s: %w", ids[i], out, err)
		}
	}
	// Describe epics 20-22.
	for i := 20; i <= 22; i++ {
		if out, err := c.bd("update", ids[i], "-d", fmt.Sprintf("Epic %d plan for %s", i, c.tag)); err != nil {
			return c.errf("update desc %s: %s: %w", ids[i], out, err)
		}
	}
	return nil
}

// phaseVerify reads back issues and checks that mutations took effect.
func (c *bdClient) phaseVerify(ids []string) error {
	// Titles on tasks 0-4.
	for i := range 5 {
		if err := c.expectField(ids[i], "title", fmt.Sprintf("%s task %d UPDATED", c.tag, i)); err != nil {
			return err
		}
	}
	// Bugs 10-14 closed.
	for i := 10; i <= 14; i++ {
		if err := c.expectField(ids[i], "status", "closed"); err != nil {
			return err
		}
	}
	// Feature 15 priority = P0.
	if err := c.expectFieldFloat(ids[15], "priority", 0); err != nil {
		return err
	}
	// Feature 16 has dependencies.
	f16, err := c.show(ids[16])
	if err != nil {
		return c.errf("show %s: %w", ids[16], err)
	}
	if deps, _ := f16["dependencies"].([]any); len(deps) == 0 {
		return c.errf("show %s: expected dependencies, got none", ids[16])
	}
	// Chore 25 parent = epic 20.
	if err := c.expectField(ids[25], "parent", ids[20]); err != nil {
		return err
	}
	// Labels on task 3.
	t3, err := c.show(ids[3])
	if err != nil {
		return c.errf("show %s: %w", ids[3], err)
	}
	if err := checkLabels(t3, "urgent", c.tag); err != nil {
		return c.errf("show %s: %w", ids[3], err)
	}
	// Epic 20 description.
	e20, err := c.show(ids[20])
	if err != nil {
		return c.errf("show %s: %w", ids[20], err)
	}
	want := fmt.Sprintf("Epic 20 plan for %s", c.tag)
	if desc, _ := e20["description"].(string); !strings.Contains(desc, want) {
		return c.errf("show %s: description = %q, missing %q", ids[20], desc, want)
	}
	return nil
}

// phaseList runs filtered list queries as spot-checks.
// Counts use >= because multiple clients may share the same database.
func (c *bdClient) phaseList() error {
	checks := []struct {
		args []string
		min  int
	}{
		{[]string{"--label", c.tag}, 4},
		{[]string{"--all", "--type", "bug", "--limit", "5"}, 5},
		{[]string{"--status", "closed", "--limit", "5"}, 5},
		{[]string{"--type", "feature", "--priority-max", "1", "--limit", "2"}, 2},
		{[]string{"--type", "epic", "--limit", "5"}, 5},
		{[]string{"--status", "open,in_progress,blocked", "--limit", "10"}, 10},
		{[]string{"--all", "--limit", "50"}, 30},
	}
	for _, ch := range checks {
		result, err := c.list(ch.args...)
		if err != nil {
			return c.errf("list %s: %w", strings.Join(ch.args, " "), err)
		}
		if len(result) < ch.min {
			return c.errf("list %s: got %d, want >= %d", strings.Join(ch.args, " "), len(result), ch.min)
		}
	}
	return nil
}

// phaseDelete removes chores 25-29 and verifies they're gone.
func (c *bdClient) phaseDelete(ids []string) error {
	for i := 25; i <= 29; i++ {
		if out, err := c.bd("delete", ids[i], "--force", "--json"); err != nil {
			return c.errf("delete %s: %s: %w", ids[i], out, err)
		}
	}
	for i := 25; i <= 29; i++ {
		if out, err := c.bd("show", ids[i], "--json"); err == nil {
			return c.errf("show deleted %s should fail: %s", ids[i], out)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Verification helpers
// ---------------------------------------------------------------------------

// expectField shows an issue and asserts a string field matches exactly.
func (c *bdClient) expectField(id, field, want string) error {
	issue, err := c.show(id)
	if err != nil {
		return c.errf("show %s: %w", id, err)
	}
	if got, _ := issue[field].(string); got != want {
		return c.errf("show %s: %s = %q, want %q", id, field, got, want)
	}
	return nil
}

// expectFieldFloat shows an issue and asserts a numeric field matches.
func (c *bdClient) expectFieldFloat(id, field string, want float64) error {
	issue, err := c.show(id)
	if err != nil {
		return c.errf("show %s: %w", id, err)
	}
	if got, _ := issue[field].(float64); got != want {
		return c.errf("show %s: %s = %v, want %v", id, field, got, want)
	}
	return nil
}

// checkLabels verifies that an issue's labels contain all expected values.
func checkLabels(issue map[string]any, required ...string) error {
	labels, _ := issue["labels"].([]any)
	set := make(map[string]bool, len(labels))
	for _, l := range labels {
		if s, ok := l.(string); ok {
			set[s] = true
		}
	}
	for _, r := range required {
		if !set[r] {
			return fmt.Errorf("labels %v missing %q", labels, r)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Subprocess + JSON helpers
// ---------------------------------------------------------------------------

func ssExec(ctx context.Context, binary, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ssFirstJSON returns the substring starting at the first '{' or '['.
func ssFirstJSON(output string) string {
	for i, ch := range output {
		if ch == '{' || ch == '[' {
			return output[i:]
		}
	}
	return output
}

// ssJSONField extracts a string field from the first JSON object in output.
func ssJSONField(output, field string) (string, error) {
	jsonStr := ssFirstJSON(output)
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return "", fmt.Errorf("parse JSON: %w\nraw: %s", err, output)
	}
	v, ok := m[field].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("field %q not found or empty in JSON", field)
	}
	return v, nil
}

// ssParseShowJSON parses bd show --json output (an array) into a single object.
func ssParseShowJSON(output string) (map[string]any, error) {
	jsonStr := ssFirstJSON(output)
	var arr []map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &arr); err != nil {
		// Fall back to single object.
		var m map[string]any
		if err2 := json.Unmarshal([]byte(jsonStr), &m); err2 != nil {
			return nil, fmt.Errorf("parse show JSON: %w\nraw: %s", err, output)
		}
		return m, nil
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("bd show returned empty array")
	}
	return arr[0], nil
}

// ---------------------------------------------------------------------------
// Git + build helpers
// ---------------------------------------------------------------------------

func gitInit(ctx context.Context, dir string) error {
	for _, c := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.CommandContext(ctx, c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %s: %w", strings.Join(c, " "), string(out), err)
		}
	}
	return nil
}

var (
	sharedServerBdBinary  string
	sharedServerBuildOnce sync.Once
	sharedServerBuildErr  error
)

// buildSharedServerTestBinary returns the path to a bd binary.
// If BEADS_TEST_BD_BINARY is set, uses that pre-built binary.
// Otherwise builds one from source (cached across tests via sync.Once).
func buildSharedServerTestBinary(t *testing.T) string {
	t.Helper()
	sharedServerBuildOnce.Do(func() {
		prebuilt, err := findPrebuiltBDBinary()
		if err != nil {
			sharedServerBuildErr = err
			return
		}
		if prebuilt != "" {
			sharedServerBdBinary = prebuilt
			return
		}
		buildDir, err := testTempDir("beads-shared-server-bd-*")
		if err != nil {
			sharedServerBuildErr = fmt.Errorf("mkdirtemp: %w", err)
			return
		}
		bdBin := filepath.Join(buildDir, "bd")
		cmd := goBuildBDCommand(bdBin)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			_ = os.RemoveAll(buildDir)
			sharedServerBuildErr = fmt.Errorf("go build: %s: %w", string(out), err)
			return
		}
		sharedServerBdBinary = bdBin
	})
	if sharedServerBuildErr != nil {
		t.Fatalf("build bd: %v", sharedServerBuildErr)
	}
	return sharedServerBdBinary
}

// ---------------------------------------------------------------------------
// bd-xicgb8 (GH#7122): shared-server init beside a resident shared holder
// ---------------------------------------------------------------------------

// ssMetadataJSON loads a project's metadata.json as a generic object, or
// fails the test when the file is absent or unparseable.
func ssMetadataJSON(t *testing.T, projectDir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(projectDir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("read %s/.beads/metadata.json: %v", projectDir, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse metadata.json in %s: %v\nraw: %s", projectDir, err, raw)
	}
	return m
}

// TestSharedServerInitManagedResidentHolder pins GH#7122 end to end: with a
// RESIDENT SHARED HOLDER of the machine-global shared root — the test
// process plays another project's `bd serve`, which holds that root SHARED
// for its whole lifetime — different NEW projects on the same shared
// server still initialize successfully (AC-1/AC-7), end up with usable
// identities of their own, and share the provisioned global database
// (beads_global) every --global command reads. At base, where init took
// the shared root EXCLUSIVELY, this holder refuses the very first init.
func TestSharedServerInitManagedResidentHolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}

	bdBinary := buildSharedServerTestBinary(t)

	cp, err := testutil.NewContainerProvider()
	if err != nil {
		if os.Getenv(testutil.EnvRequireDoltContainer) == "1" {
			t.Fatalf("cannot start Dolt server, but %s=1: %v", testutil.EnvRequireDoltContainer, err)
		}
		t.Skipf("cannot start Dolt container: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })
	containerPort := cp.Port()

	sharedDir := t.TempDir()
	if err := cp.WritePortFile(sharedDir); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	// The resident shared holder (see TestSharedServerConcurrent's, which
	// holds through the workload phase too).
	rootGate, gerr := workspacegate.ForPhysicalRoot(filepath.Join(sharedDir, "dolt"))
	if gerr != nil {
		t.Fatalf("shared root gate: %v", gerr)
	}
	residentHold, gerr := rootGate.Acquire(context.Background(), workspacegate.Shared,
		workspacegate.Options{Reason: "resident bd serve (test holder)"})
	if gerr != nil {
		t.Fatalf("resident shared hold of the shared root: %v", gerr)
	}
	t.Cleanup(func() { _ = residentHold.Release() })

	baseEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOPATH=" + os.Getenv("GOPATH"),
		"GOROOT=" + os.Getenv("GOROOT"),
		"BEADS_SHARED_SERVER_DIR=" + sharedDir,
		"BEADS_DOLT_SHARED_SERVER=1",
		"BEADS_DOLT_SERVER_PORT=" + strconv.Itoa(containerPort),
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_TEST_MODE=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GT_ROOT=",
	}

	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	ids := make(map[string]bool)
	for _, proj := range []struct{ name, prefix string }{
		{name: "holderA", prefix: "holda"},
		{name: "holderB", prefix: "holdb"},
	} {
		dir := filepath.Join(t.TempDir(), proj.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := gitInit(ctx, dir); err != nil {
			t.Fatalf("git init %s: %v", dir, err)
		}
		out, err := ssExec(ctx, bdBinary, dir, baseEnv,
			"init", "--shared-server", "--external", "--prefix", proj.prefix, "--quiet", "--non-interactive")
		if err != nil {
			t.Fatalf("bd init for %s under the resident shared holder: %s: %v", proj.name, out, err)
		}
		meta := ssMetadataJSON(t, dir)
		id, _ := meta["project_id"].(string)
		if id == "" {
			t.Errorf("%s initialized without a project identity", proj.name)
		}
		if ids[id] {
			t.Errorf("%s reused another project's identity %s", proj.name, id)
		}
		ids[id] = true
		global, _ := meta["global_dolt_database"].(string)
		if global != doltserver.GlobalDatabaseName {
			t.Errorf("%s metadata names global database %q, want %q", proj.name, global, doltserver.GlobalDatabaseName)
		}
		// A usable identity: ordinary commands work in the new workspace.
		if out, err := ssExec(ctx, bdBinary, dir, baseEnv, "list", "--json", "--flat"); err != nil {
			t.Errorf("bd list in %s: %s: %v", proj.name, out, err)
		}
		// The shared global config is provisioned and shared: every
		// --global command in either project reads the same beads_global.
		if out, err := ssExec(ctx, bdBinary, dir, baseEnv, "--global", "list", "--json", "--flat"); err != nil {
			t.Errorf("bd --global list in %s: %s: %v", proj.name, out, err)
		}
	}
}

// TestSharedServerInitIdentityRace pins the identity half of GH#7122
// (AC-6/AC-7): two initializers racing onto ONE unidentified database on
// the shared server — both with the same explicit --database, so they
// cannot end up on different ones. The race has one winner: the loser's
// Bootstrap is refused (ErrAlreadyIdentified, decided transactionally by
// the role) and the loser fails WITHOUT publishing its freshly minted
// local identity — no metadata.json carrying an id the database never
// accepted. Re-running the loser's init with the same --database then
// ADOPTS the winner's identity, which is how a lost race must recover.
//
// The resident shared holder stays in place throughout, so this is also
// the AC-1/AC-7 shape: at base the holder refuses both inits before the
// race even starts.
func TestSharedServerInitIdentityRace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}

	bdBinary := buildSharedServerTestBinary(t)

	cp, err := testutil.NewContainerProvider()
	if err != nil {
		if os.Getenv(testutil.EnvRequireDoltContainer) == "1" {
			t.Fatalf("cannot start Dolt server, but %s=1: %v", testutil.EnvRequireDoltContainer, err)
		}
		t.Skipf("cannot start Dolt container: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })
	containerPort := cp.Port()

	sharedDir := t.TempDir()
	if err := cp.WritePortFile(sharedDir); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	rootGate, gerr := workspacegate.ForPhysicalRoot(filepath.Join(sharedDir, "dolt"))
	if gerr != nil {
		t.Fatalf("shared root gate: %v", gerr)
	}
	residentHold, gerr := rootGate.Acquire(context.Background(), workspacegate.Shared,
		workspacegate.Options{Reason: "resident bd serve (test holder)"})
	if gerr != nil {
		t.Fatalf("resident shared hold of the shared root: %v", gerr)
	}
	t.Cleanup(func() { _ = residentHold.Release() })

	baseEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOPATH=" + os.Getenv("GOPATH"),
		"GOROOT=" + os.Getenv("GOROOT"),
		"BEADS_SHARED_SERVER_DIR=" + sharedDir,
		"BEADS_DOLT_SHARED_SERVER=1",
		"BEADS_DOLT_SERVER_PORT=" + strconv.Itoa(containerPort),
		"BEADS_DOLT_AUTO_START=0",
		"BEADS_TEST_MODE=1",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GT_ROOT=",
		// Both racers take the shared root SHARED (they contend on the
		// database identity, not on the gate), so the bound only ever
		// matters for a regression that reintroduces exclusive waiting.
		"BEADS_INIT_GATE_TIMEOUT=15s",
	}

	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	type racer struct {
		name, prefix string
		dir          string
		out          string
		ranErr       error
	}
	racers := []*racer{{name: "raceA", prefix: "racea"}, {name: "raceB", prefix: "raceb"}}
	for _, r := range racers {
		r.dir = filepath.Join(t.TempDir(), r.name)
		if err := os.MkdirAll(r.dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", r.dir, err)
		}
		if err := gitInit(ctx, r.dir); err != nil {
			t.Fatalf("git init %s: %v", r.dir, err)
		}
	}

	var wg sync.WaitGroup
	for _, r := range racers {
		wg.Add(1)
		go func(r *racer) {
			defer wg.Done()
			r.out, r.ranErr = ssExec(ctx, bdBinary, r.dir, baseEnv,
				"init", "--shared-server", "--external", "--database", "ssracetest",
				"--prefix", r.prefix, "--quiet", "--non-interactive")
		}(r)
	}
	wg.Wait()

	// One winner is enough; both may succeed (a racer whose identity read
	// landed after the winner's commit adopts instead of racing).
	sharedID := ""
	for _, r := range racers {
		if r.ranErr == nil {
			id, _ := ssMetadataJSON(t, r.dir)["project_id"].(string)
			if id == "" {
				t.Fatalf("%s won the race without recording an identity", r.name)
			}
			if sharedID == "" {
				sharedID = id
			} else if id != sharedID {
				t.Errorf("%s recorded identity %s, want the one identity %s (one database)", r.name, id, sharedID)
			}
		}
	}
	if sharedID == "" {
		for _, r := range racers {
			t.Errorf("%s init: %s: %v", r.name, r.out, r.ranErr)
		}
		t.Fatal("both racing initializers failed; one must win")
	}

	for _, r := range racers {
		if r.ranErr == nil {
			continue
		}
		// The loser did not publish its freshly minted identity: either no
		// metadata.json at all, or one without a project id.
		if raw, rerr := os.ReadFile(filepath.Join(r.dir, ".beads", "metadata.json")); rerr == nil {
			var m map[string]any
			if jerr := json.Unmarshal(raw, &m); jerr != nil {
				t.Fatalf("parse %s metadata.json: %v\nraw: %s", r.name, jerr, raw)
			}
			if id, _ := m["project_id"].(string); id != "" && id != sharedID {
				t.Errorf("%s lost the race but published its minted identity %s anyway", r.name, id)
			}
		}
		// The recovery: re-running the loser's init with the same
		// --database adopts the winner's identity.
		out, err := ssExec(ctx, bdBinary, r.dir, baseEnv,
			"init", "--shared-server", "--external", "--database", "ssracetest",
			"--prefix", r.prefix, "--quiet", "--non-interactive")
		if err != nil {
			t.Fatalf("%s init after losing the identity race: %s: %v", r.name, out, err)
		}
		id, _ := ssMetadataJSON(t, r.dir)["project_id"].(string)
		if id != sharedID {
			t.Errorf("%s re-init recorded identity %s, want the adopted winner identity %s", r.name, id, sharedID)
		}
		if out, err := ssExec(ctx, bdBinary, r.dir, baseEnv, "list", "--json", "--flat"); err != nil {
			t.Errorf("bd list in %s after adopting: %s: %v", r.name, out, err)
		}
	}
}
