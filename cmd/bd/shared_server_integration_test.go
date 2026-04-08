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

	"github.com/steveyegge/beads/internal/testutil"
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
// Requires BEADS_TEST_SHARED_SERVER=1 to run (skipped by default).
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
	if os.Getenv("BEADS_TEST_SHARED_SERVER") == "" {
		t.Skip("skipping: set BEADS_TEST_SHARED_SERVER=1 to run")
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
	phase = time.Now()
	type project struct {
		dir, prefix string
	}
	projects := make([]project, numDirs)

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(maxProcs)
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
			out, err := ssExec(egCtx, bdBinary, dir, baseEnv,
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
	phase = time.Now()
	eg, egCtx = errgroup.WithContext(ctx)
	eg.SetLimit(maxProcs)
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
		if prebuilt := os.Getenv("BEADS_TEST_BD_BINARY"); prebuilt != "" {
			if _, err := os.Stat(prebuilt); err != nil {
				sharedServerBuildErr = fmt.Errorf("BEADS_TEST_BD_BINARY=%q not found: %w", prebuilt, err)
				return
			}
			sharedServerBdBinary = prebuilt
			return
		}
		pkgDir, err := os.Getwd()
		if err != nil {
			sharedServerBuildErr = fmt.Errorf("getwd: %w", err)
			return
		}
		buildDir, err := os.MkdirTemp("", "beads-shared-server-bd-*")
		if err != nil {
			sharedServerBuildErr = fmt.Errorf("mkdirtemp: %w", err)
			return
		}
		bdBin := filepath.Join(buildDir, "bd")
		cmd := exec.Command("go", "build", "-o", bdBin, ".")
		cmd.Dir = pkgDir
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
