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

// ssEnvInt reads a positive integer from an env var, falling back to def.
func ssEnvInt(key string, def int) int {
	if s := os.Getenv(key); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// TestSharedServerConcurrent builds the bd binary, starts a single Dolt
// container via testcontainers, initializes numDirs project directories,
// then fans out numClients concurrent workloads across those directories.
// Multiple clients may share a directory (and therefore a database),
// exercising concurrent multi-writer access to the same Dolt database.
//
// Configuration via environment variables:
//
//	BEADS_TEST_SS_DIRS     — number of project directories  (default: 50)
//	BEADS_TEST_SS_CLIENTS  — number of concurrent clients   (default: 250)
//	BEADS_TEST_SS_MAXPROCS — max concurrent subprocesses    (default: GOMAXPROCS*4)
//
// Recommended: set BEADS_TEST_EMBEDDED_DOLT=1 to skip the unrelated
// singleton Dolt container that TestMain starts for other tests in this package.
func TestSharedServerConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_SHARED_SERVER") == "" {
		t.Skip("skipping: set BEADS_TEST_SHARED_SERVER=1 to run")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shared server integration test not supported on Windows")
	}

	numDirs := ssEnvInt("BEADS_TEST_SS_DIRS", 50)
	numClients := ssEnvInt("BEADS_TEST_SS_CLIENTS", 500)
	maxProcs := ssEnvInt("BEADS_TEST_SS_MAXPROCS", runtime.GOMAXPROCS(0)*4)
	t.Logf("config: dirs=%d clients=%d maxprocs=%d", numDirs, numClients, maxProcs)

	testStart := time.Now()

	// ── 1. Build bd binary ──────────────────────────────────────────────
	phase := time.Now()
	bdBinary := buildSharedServerTestBinary(t)
	t.Logf("build bd binary: %s", time.Since(phase))

	// ── 2. Start testcontainers Dolt server ─────────────────────────────
	phase = time.Now()
	cp, err := testutil.NewContainerProvider()
	if err != nil {
		t.Skipf("cannot start Dolt container: %v", err)
	}
	containerPort := cp.Port()
	t.Cleanup(func() { _ = cp.Stop() })
	t.Logf("start container (port %d): %s", containerPort, time.Since(phase))

	// ── 3. Prepare shared server directory ──────────────────────────────
	sharedDir := t.TempDir()
	if err := cp.WritePortFile(sharedDir); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	// ── 4. Base environment for all subprocesses ────────────────────────
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

	// Use the test's deadline so the context respects -timeout.
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	// ── 5. Init project directories ─────────────────────────────────────
	phase = time.Now()
	type project struct {
		dir    string
		prefix string
	}
	projects := make([]project, numDirs)

	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(maxProcs)
	for i := range numDirs {
		i := i
		eg.Go(func() error {
			prefix := fmt.Sprintf("proj%d", i)
			projectDir := filepath.Join(t.TempDir(), prefix)
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				return fmt.Errorf("project %d mkdir: %w", i, err)
			}
			if err := gitInit(egCtx, projectDir); err != nil {
				return fmt.Errorf("project %d git init: %w", i, err)
			}

			out, err := ssBdExec(egCtx, bdBinary, projectDir, baseEnv,
				"init", "--shared-server", "--external",
				"--prefix", prefix, "--quiet", "--non-interactive")
			if err != nil {
				return fmt.Errorf("project %d init: %s: %w", i, out, err)
			}

			projects[i] = project{dir: projectDir, prefix: prefix}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Logf("init %d dirs: %s", numDirs, time.Since(phase))

	// ── 6. Fan out client workloads ─────────────────────────────────────
	phase = time.Now()
	eg, egCtx = errgroup.WithContext(ctx)
	eg.SetLimit(maxProcs)
	for c := range numClients {
		c := c
		eg.Go(func() error {
			// Round-robin assignment: client c uses project c % numDirs.
			p := projects[c%numDirs]
			clientTag := fmt.Sprintf("c%d", c)
			return runWorkload(egCtx, t, bdBinary, p.dir, clientTag, baseEnv)
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatalf("workload: %v", err)
	}
	t.Logf("workloads (%d clients x %d dirs): %s", numClients, numDirs, time.Since(phase))
	t.Logf("total: %s", time.Since(testStart))
}

// runWorkload performs a realistic issue management workflow against the shared
// server: creates issues across multiple types, wires dependencies, updates
// fields, verifies via show, runs filtered list queries, and deletes issues.
// Every bd command must succeed; any error is returned immediately.
// Concurrency is controlled by the caller's errgroup.SetLimit.
func runWorkload(ctx context.Context, t *testing.T, bdBinary, dir, clientTag string, env []string) error {
	t.Helper()

	// op tracks the running operation count for error messages.
	op := 0
	bd := func(args ...string) (string, error) {
		op++
		start := time.Now()
		out, err := ssBdExec(ctx, bdBinary, dir, env, args...)
		t.Logf("%s [op %d] %s — %s", clientTag, op, strings.Join(args, " "), time.Since(start))
		return out, err
	}
	create := func(title string, extra ...string) (string, error) {
		op++
		start := time.Now()
		args := append([]string{"create", title, "--json"}, extra...)
		id, err := ssBdCreateJSON(ctx, bdBinary, dir, env, args...)
		t.Logf("%s [op %d] create %q — %s", clientTag, op, title, time.Since(start))
		return id, err
	}
	show := func(id string) (map[string]any, error) {
		op++
		start := time.Now()
		m, err := ssBdShowJSON(ctx, bdBinary, dir, env, id)
		t.Logf("%s [op %d] show %s — %s", clientTag, op, id, time.Since(start))
		return m, err
	}
	list := func(extra ...string) ([]any, error) {
		op++
		start := time.Now()
		arr, err := ssBdListJSON(ctx, bdBinary, dir, env, extra...)
		t.Logf("%s [op %d] list %s — %s", clientTag, op, strings.Join(extra, " "), time.Since(start))
		return arr, err
	}

	ids := make([]string, 30)

	// ── Phase 1: Create issues ──────────────────────────────────────────
	//
	// Mix of types:
	//   0-9   tasks
	//   10-14 bugs with descriptions
	//   15-19 features
	//   20-24 epics
	//   25-29 chores (children of epics 20-24)

	issueTypes := []string{
		"task", "task", "task", "task", "task",
		"task", "task", "task", "task", "task",
		"bug", "bug", "bug", "bug", "bug",
		"feature", "feature", "feature", "feature", "feature",
		"epic", "epic", "epic", "epic", "epic",
		"chore", "chore", "chore", "chore", "chore",
	}
	for i := range 30 {
		var id string
		var err error
		switch {
		case i >= 10 && i <= 14: // bugs with descriptions
			id, err = create(
				fmt.Sprintf("%s bug %d", clientTag, i),
				"--type", issueTypes[i],
				"-d", fmt.Sprintf("Bug description for issue %d in %s", i, clientTag),
			)
		case i >= 25: // chores parented to epics 20-24
			epicIdx := 20 + (i - 25)
			id, err = create(
				fmt.Sprintf("%s chore %d", clientTag, i),
				"--type", issueTypes[i],
				"--parent", ids[epicIdx],
			)
		default:
			id, err = create(
				fmt.Sprintf("%s %s %d", clientTag, issueTypes[i], i),
				"--type", issueTypes[i],
			)
		}
		if err != nil {
			return fmt.Errorf("%s [op %d] create issue %d (%s): %w", clientTag, op, i, issueTypes[i], err)
		}
		ids[i] = id
	}

	// ── Phase 2: Wire dependencies ──────────────────────────────────────

	depPairs := [][2]int{
		// tasks 1-9 depend on task 0
		{1, 0}, {2, 0}, {3, 0}, {4, 0}, {5, 0},
		{6, 0}, {7, 0}, {8, 0}, {9, 0},
		// features 16-19 depend on feature 15
		{16, 15}, {17, 15}, {18, 15}, {19, 15},
		// bug chain: 11 depends on 10
		{11, 10},
	}
	for _, pair := range depPairs {
		from, to := ids[pair[0]], ids[pair[1]]
		if out, err := bd("dep", "add", from, to, "--json"); err != nil {
			return fmt.Errorf("%s [op %d] dep add %s->%s: %s: %w", clientTag, op, from, to, out, err)
		}
	}

	// ── Phase 3: Update issues ──────────────────────────────────────────

	// Update titles on tasks 0-4 (5 ops)
	for i := range 5 {
		if out, err := bd("update", ids[i], "--title", fmt.Sprintf("%s task %d UPDATED", clientTag, i)); err != nil {
			return fmt.Errorf("%s [op %d] update title %s: %s: %w", clientTag, op, ids[i], out, err)
		}
	}

	// Set tasks 0-2 to in_progress (3 ops)
	for i := range 3 {
		if out, err := bd("update", ids[i], "--status", "in_progress"); err != nil {
			return fmt.Errorf("%s [op %d] update status %s: %s: %w", clientTag, op, ids[i], out, err)
		}
	}

	// Close bugs 10-14 (5 ops)
	for i := 10; i <= 14; i++ {
		if out, err := bd("update", ids[i], "--status", "closed"); err != nil {
			return fmt.Errorf("%s [op %d] close %s: %s: %w", clientTag, op, ids[i], out, err)
		}
	}

	// Add labels to tasks 3-6 (4 ops)
	labelNames := []string{"urgent", "backend", "frontend", "infra"}
	for j, i := range []int{3, 4, 5, 6} {
		if out, err := bd("update", ids[i], "--add-label", labelNames[j], "--add-label", clientTag); err != nil {
			return fmt.Errorf("%s [op %d] add-label %s: %s: %w", clientTag, op, ids[i], out, err)
		}
	}

	// Set priorities on features 15-19 (5 ops)
	priorities := []string{"P0", "P1", "P2", "P3", "P4"}
	for j, i := range []int{15, 16, 17, 18, 19} {
		if out, err := bd("update", ids[i], "--priority", priorities[j]); err != nil {
			return fmt.Errorf("%s [op %d] set priority %s: %s: %w", clientTag, op, ids[i], out, err)
		}
	}

	// Update descriptions on epics 20-22 (3 ops)
	for i := 20; i <= 22; i++ {
		if out, err := bd("update", ids[i], "-d", fmt.Sprintf("Epic %d plan for %s", i, clientTag)); err != nil {
			return fmt.Errorf("%s [op %d] update desc %s: %s: %w", clientTag, op, ids[i], out, err)
		}
	}

	// ── Phase 4: Show + verify ──────────────────────────────────────────

	// Verify updated titles on tasks 0-4 (5 ops)
	for i := range 5 {
		issue, err := show(ids[i])
		if err != nil {
			return fmt.Errorf("%s [op %d] show %s: %w", clientTag, op, ids[i], err)
		}
		want := fmt.Sprintf("%s task %d UPDATED", clientTag, i)
		if got, _ := issue["title"].(string); got != want {
			return fmt.Errorf("%s [op %d] show %s: title = %q, want %q", clientTag, op, ids[i], got, want)
		}
	}

	// Verify bugs 10-14 are closed (5 ops)
	for i := 10; i <= 14; i++ {
		issue, err := show(ids[i])
		if err != nil {
			return fmt.Errorf("%s [op %d] show %s: %w", clientTag, op, ids[i], err)
		}
		if got, _ := issue["status"].(string); got != "closed" {
			return fmt.Errorf("%s [op %d] show %s: status = %q, want closed", clientTag, op, ids[i], got)
		}
	}

	// Verify feature 15 has P0 priority (1 op)
	f15, err := show(ids[15])
	if err != nil {
		return fmt.Errorf("%s [op %d] show %s: %w", clientTag, op, ids[15], err)
	}
	if pri, _ := f15["priority"].(float64); int(pri) != 0 {
		return fmt.Errorf("%s [op %d] show %s: priority = %v, want 0 (P0)", clientTag, op, ids[15], f15["priority"])
	}

	// Verify feature 16 has dependency on feature 15 (1 op)
	f16, err := show(ids[16])
	if err != nil {
		return fmt.Errorf("%s [op %d] show %s: %w", clientTag, op, ids[16], err)
	}
	if deps, _ := f16["dependencies"].([]any); len(deps) == 0 {
		return fmt.Errorf("%s [op %d] show %s: expected dependencies on feature 15, got none", clientTag, op, ids[16])
	}

	// Verify chore 25 is child of epic 20 (1 op)
	c25, err := show(ids[25])
	if err != nil {
		return fmt.Errorf("%s [op %d] show %s: %w", clientTag, op, ids[25], err)
	}
	if parent, _ := c25["parent"].(string); parent != ids[20] {
		return fmt.Errorf("%s [op %d] show %s: parent = %q, want %q", clientTag, op, ids[25], parent, ids[20])
	}

	// Verify labels on task 3 (1 op)
	t3, err := show(ids[3])
	if err != nil {
		return fmt.Errorf("%s [op %d] show %s: %w", clientTag, op, ids[3], err)
	}
	labelArr, _ := t3["labels"].([]any)
	labelSet := make(map[string]bool)
	for _, l := range labelArr {
		if s, ok := l.(string); ok {
			labelSet[s] = true
		}
	}
	if !labelSet["urgent"] || !labelSet[clientTag] {
		return fmt.Errorf("%s [op %d] show %s: labels = %v, want urgent+%s", clientTag, op, ids[3], labelArr, clientTag)
	}

	// Verify epic 20 description (1 op)
	e20, err := show(ids[20])
	if err != nil {
		return fmt.Errorf("%s [op %d] show %s: %w", clientTag, op, ids[20], err)
	}
	if desc, _ := e20["description"].(string); !strings.Contains(desc, fmt.Sprintf("Epic 20 plan for %s", clientTag)) {
		return fmt.Errorf("%s [op %d] show %s: description = %q, missing expected content", clientTag, op, ids[20], desc)
	}

	// ── Phase 5: List queries ───────────────────────────────────────────
	//
	// Multiple clients share the same database, so use small --limit and
	// per-client label filters to keep list ops fast at scale.

	// Verify this client's labeled issues are findable (1 op)
	labeled, err := list("--label", clientTag)
	if err != nil {
		return fmt.Errorf("%s [op %d] list by label: %w", clientTag, op, err)
	}
	if len(labeled) < 4 {
		return fmt.Errorf("%s [op %d] list label=%s: got %d, want >= 4", clientTag, op, clientTag, len(labeled))
	}

	// Spot-check: list with type filter (1 op)
	bugs, err := list("--all", "--type", "bug", "--limit", "5")
	if err != nil {
		return fmt.Errorf("%s [op %d] list bugs: %w", clientTag, op, err)
	}
	if len(bugs) < 5 {
		return fmt.Errorf("%s [op %d] list bugs: got %d, want >= 5", clientTag, op, len(bugs))
	}

	// Spot-check: list with status filter (1 op)
	closed, err := list("--status", "closed", "--limit", "5")
	if err != nil {
		return fmt.Errorf("%s [op %d] list closed: %w", clientTag, op, err)
	}
	if len(closed) < 5 {
		return fmt.Errorf("%s [op %d] list closed: got %d, want >= 5", clientTag, op, len(closed))
	}

	// Spot-check: list with priority filter (1 op)
	hiPri, err := list("--type", "feature", "--priority-max", "1", "--limit", "2")
	if err != nil {
		return fmt.Errorf("%s [op %d] list hi-pri features: %w", clientTag, op, err)
	}
	if len(hiPri) < 2 {
		return fmt.Errorf("%s [op %d] list hi-pri features: got %d, want >= 2", clientTag, op, len(hiPri))
	}

	// Spot-check: list epics (1 op)
	epics, err := list("--type", "epic", "--limit", "5")
	if err != nil {
		return fmt.Errorf("%s [op %d] list epics: %w", clientTag, op, err)
	}
	if len(epics) < 5 {
		return fmt.Errorf("%s [op %d] list epics: got %d, want >= 5", clientTag, op, len(epics))
	}

	// Spot-check: list open (1 op)
	open, err := list("--status", "open,in_progress,blocked", "--limit", "10")
	if err != nil {
		return fmt.Errorf("%s [op %d] list open: %w", clientTag, op, err)
	}
	if len(open) < 10 {
		return fmt.Errorf("%s [op %d] list open: got %d, want >= 10", clientTag, op, len(open))
	}

	// Full list once (1 op) — capped to keep serialization cost bounded
	all, err := list("--all", "--limit", "50")
	if err != nil {
		return fmt.Errorf("%s [op %d] list all: %w", clientTag, op, err)
	}
	if len(all) < 30 {
		return fmt.Errorf("%s [op %d] list all: got %d, want >= 30", clientTag, op, len(all))
	}

	// ── Phase 6: Delete + verify ────────────────────────────────────────

	for i := 25; i <= 29; i++ {
		if out, err := bd("delete", ids[i], "--force", "--json"); err != nil {
			return fmt.Errorf("%s [op %d] delete %s: %s: %w", clientTag, op, ids[i], out, err)
		}
	}

	// Verify each deleted issue is gone via show (5 ops — fast O(1) lookups)
	for i := 25; i <= 29; i++ {
		if out, err := bd("show", ids[i], "--json"); err == nil {
			return fmt.Errorf("%s [op %d] show deleted %s should fail: %s", clientTag, op, ids[i], out)
		}
	}

	return nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// ssBdExec runs a bd command and returns combined output.
func ssBdExec(ctx context.Context, binary, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ssBdCreateJSON runs a bd create command with --json and extracts the issue ID.
func ssBdCreateJSON(ctx context.Context, binary, dir string, env []string, args ...string) (string, error) {
	out, err := ssBdExec(ctx, binary, dir, env, args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", out, err)
	}
	id, err := ssExtractJSONField(out, "id")
	if err != nil {
		return "", fmt.Errorf("extracting id from create output: %w\noutput: %s", err, out)
	}
	return id, nil
}

// ssBdShowJSON runs bd show <id> --json and returns the parsed JSON object.
// bd show --json wraps the result in an array; this extracts the first element.
func ssBdShowJSON(ctx context.Context, binary, dir string, env []string, id string) (map[string]any, error) {
	out, err := ssBdExec(ctx, binary, dir, env, "show", id, "--json")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", out, err)
	}
	jsonStr := ssExtractJSONString(out)
	var arr []map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &arr); err != nil {
		return ssParseJSON(out)
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("bd show returned empty array for %s", id)
	}
	return arr[0], nil
}

// ssBdListJSON runs bd list with --json and returns the parsed array.
func ssBdListJSON(ctx context.Context, binary, dir string, env []string, extraArgs ...string) ([]any, error) {
	args := append([]string{"list", "--json", "--flat"}, extraArgs...)
	out, err := ssBdExec(ctx, binary, dir, env, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", out, err)
	}
	jsonStr := ssExtractJSONString(out)
	var result []any
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("parse list JSON: %w\noutput: %s", err, out)
	}
	return result, nil
}

// ssExtractJSONField finds the first JSON object in output and returns a string field.
func ssExtractJSONField(output, field string) (string, error) {
	m, err := ssParseJSON(output)
	if err != nil {
		return "", err
	}
	v, ok := m[field].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("field %q not found or empty in JSON", field)
	}
	return v, nil
}

// ssParseJSON finds the first JSON object in output (skipping non-JSON preamble)
// and returns it as a map.
func ssParseJSON(output string) (map[string]any, error) {
	jsonStr := ssExtractJSONString(output)
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return nil, fmt.Errorf("parse JSON: %w\nraw: %s", err, output)
	}
	return m, nil
}

// ssExtractJSONString finds the first JSON value (object or array) in output.
func ssExtractJSONString(output string) string {
	for i, ch := range output {
		if ch == '{' || ch == '[' {
			return output[i:]
		}
	}
	return output
}

// gitInit initializes a git repo in dir with required config.
func gitInit(ctx context.Context, dir string) error {
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, c := range cmds {
		cmd := exec.CommandContext(ctx, c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %s: %w", strings.Join(c, " "), string(out), err)
		}
	}
	return nil
}

// buildSharedServerTestBinary compiles the bd binary for integration testing.
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
		binDir := t.TempDir()
		bdBin := filepath.Join(binDir, "bd")
		cmd := exec.Command("go", "build", "-o", bdBin, ".")
		cmd.Dir = pkgDir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
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
