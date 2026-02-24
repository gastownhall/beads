// Package protocol contains invariant tests that pin down expected CLI behavior.
//
// Each test asserts a specific rule that the bd CLI must satisfy. When a test
// is skipped, the skip message references the issue tracking the violation.
// Un-skip when the underlying bug is fixed — the test becomes a permanent
// guardrail against re-regression.
//
// These tests are independent of the differential regression suite
// (tests/regression/) and can be merged and run without it.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// ---------------------------------------------------------------------------
// Binary build (once per test run)
// ---------------------------------------------------------------------------

var (
	bdPath string
	bdDir  string
	bdOnce sync.Once
	bdErr  error
)

// testDoltPort is set by TestMain when a test Dolt server is available.
// Passed to bd subprocesses via BEADS_DOLT_PORT so they never hit prod.
var testDoltPort int

func TestMain(m *testing.M) {
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	srv, cleanup := testutil.StartTestDoltServer("protocol-test-dolt-*")
	defer cleanup()

	if srv != nil {
		testDoltPort = srv.Port
		os.Setenv("BEADS_DOLT_PORT", fmt.Sprintf("%d", srv.Port))
		os.Setenv("BEADS_TEST_MODE", "1")
	}

	code := m.Run()

	os.Unsetenv("BEADS_DOLT_PORT")
	os.Unsetenv("BEADS_TEST_MODE")

	if bdDir != "" {
		os.RemoveAll(bdDir)
	}
	return code
}

func buildBD(t *testing.T) string {
	t.Helper()
	bdOnce.Do(func() {
		bin := "bd-protocol"
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		dir, err := os.MkdirTemp("", "bd-protocol-*")
		if err != nil {
			bdErr = err
			return
		}
		bdDir = dir
		bdPath = filepath.Join(dir, bin)

		modRoot := findModuleRoot(t)
		cmd := exec.Command("go", "build", "-o", bdPath, "./cmd/bd")
		cmd.Dir = modRoot
		cmd.Env = buildEnv()

		out, err := cmd.CombinedOutput()
		if err != nil {
			bdErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if bdErr != nil {
		t.Skipf("skipping: failed to build bd: %v", bdErr)
	}
	return bdPath
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

func buildEnv() []string {
	env := os.Environ()
	if prefix := icuPrefix(); prefix != "" {
		env = append(env,
			"CGO_CFLAGS=-I"+prefix+"/include",
			"CGO_CPPFLAGS=-I"+prefix+"/include",
			"CGO_LDFLAGS=-L"+prefix+"/lib",
		)
	}
	return env
}

func icuPrefix() string {
	out, err := exec.Command("brew", "--prefix", "icu4c").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// Workspace: isolated temp dir with git repo + bd init
// ---------------------------------------------------------------------------

type workspace struct {
	dir string
	bd  string
	t   *testing.T
}

// testPrefix returns a unique prefix with a random suffix to ensure each test
// invocation gets its own Dolt database (<prefix>), avoiding cross-test
// pollution and stale data from prior runs.
func testPrefix(t *testing.T) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "t" + hex.EncodeToString(b[:]) // e.g. "t1a2b3c4d" — 9 chars, valid SQL identifier
}

func newWorkspace(t *testing.T) *workspace {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("skipping: dolt not installed")
	}
	bd := buildBD(t)
	dir := t.TempDir()
	w := &workspace{dir: dir, bd: bd, t: t}

	w.git("init")
	w.git("config", "user.name", "protocol-test")
	w.git("config", "user.email", "test@protocol.test")

	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w.git("add", ".")
	w.git("commit", "-m", "initial")

	prefix := testPrefix(t)
	w.run("init", "--prefix", prefix, "--quiet")
	return w
}

func (w *workspace) env() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + w.dir,
		"BEADS_NO_DAEMON=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"BEADS_TEST_MODE=1",
	}
	if testDoltPort > 0 {
		env = append(env, "BEADS_DOLT_PORT="+strconv.Itoa(testDoltPort))
	}
	if v := os.Getenv("TMPDIR"); v != "" {
		env = append(env, "TMPDIR="+v)
	}
	return env
}

func (w *workspace) git(args ...string) {
	w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (w *workspace) run(args ...string) string {
	w.t.Helper()
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// tryRun runs a bd command and returns output + error (does not fatal on failure).
func (w *workspace) tryRun(args ...string) (string, error) {
	w.t.Helper()
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runExpectError runs bd and expects a non-zero exit code.
// Returns the combined output and exit code.
func (w *workspace) runExpectError(args ...string) (string, int) {
	w.t.Helper()
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	out, err := cmd.CombinedOutput()
	if err == nil {
		w.t.Fatalf("bd %s: expected non-zero exit, got success\nOutput: %s",
			strings.Join(args, " "), out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		w.t.Fatalf("bd %s: unexpected error type: %v", strings.Join(args, " "), err)
	}
	return string(out), exitErr.ExitCode()
}

// create runs bd create --silent and returns the issue ID.
func (w *workspace) create(args ...string) string {
	w.t.Helper()
	allArgs := append([]string{"create", "--silent"}, args...)
	cmd := exec.Command(w.bd, allArgs...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		w.t.Fatalf("bd create %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		w.t.Fatal("bd create returned empty ID")
	}
	return id
}

// showJSON runs bd show <id> --json and returns the first issue object.
func (w *workspace) showJSON(id string) map[string]any {
	w.t.Helper()
	out := w.run("show", id, "--json")
	items := parseJSONOutput(w.t, out)
	if len(items) == 0 {
		w.t.Fatalf("bd show %s --json returned no items", id)
	}
	return items[0]
}

// ---------------------------------------------------------------------------
// Assertion and comparison helpers
// ---------------------------------------------------------------------------

// findByID finds an issue in a JSON array by its ID.
func findByID(items []map[string]any, id string) map[string]any {
	for _, item := range items {
		if item["id"] == id {
			return item
		}
	}
	return nil
}

// parseReadyIDs runs bd ready --json and returns the set of issue IDs.
func parseReadyIDs(t *testing.T, w *workspace) map[string]bool {
	t.Helper()
	out := w.run("ready", "--json")
	ids := make(map[string]bool)

	items := parseJSONOutput(t, out)
	for _, m := range items {
		if id, ok := m["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

// requireStringSetEqual asserts that got and want contain exactly the same
// strings (order-independent). On failure it prints missing and unexpected items.
func requireStringSetEqual(t *testing.T, got, want []string, context string) {
	t.Helper()
	sortedGot := append([]string(nil), got...)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedGot)
	sort.Strings(sortedWant)

	if slices.Equal(sortedGot, sortedWant) {
		return
	}

	missing, unexpected := setDiff(sortedWant, sortedGot)
	t.Errorf("%s: string set mismatch (got %d, want %d)\n  missing:    %v\n  unexpected: %v",
		context, len(got), len(want), missing, unexpected)
}

// depEdge represents a dependency edge for set comparison.
type depEdge struct {
	issueID     string
	dependsOnID string
}

// requireDepEdgesEqual asserts that the dependency objects contain exactly
// the expected depends-on targets (order-independent).
//
// Handles two JSON formats:
//   - list --json:  objects with "issue_id" and "depends_on_id" fields
//   - show --json:  embedded Issue objects where "id" = the depends-on target
//
// NOTE: This compares targets only, not dependency type (blocks vs
// parent-child etc.). Current protocol tests create one dep type per
// scenario so this is sufficient. If a test needs to distinguish types,
// extend depEdge to include type and compare (target, type) tuples.
func requireDepEdgesEqual(t *testing.T, gotObjs []map[string]any, want []depEdge, context string) {
	t.Helper()

	got := make([]depEdge, 0, len(gotObjs))
	for _, obj := range gotObjs {
		issueID, _ := obj["issue_id"].(string)
		dependsOn, _ := obj["depends_on_id"].(string)
		// show --json embeds the depended-on issue directly; its "id" is the target.
		if dependsOn == "" {
			dependsOn, _ = obj["id"].(string)
		}
		got = append(got, depEdge{issueID: issueID, dependsOnID: dependsOn})
	}

	// Compare only the depends_on_id targets. The issue_id may be empty in
	// the show --json format (it's implicit from the parent), so we compare
	// just the target set to stay format-agnostic.
	gotTargets := make([]string, len(got))
	for i, e := range got {
		gotTargets[i] = e.dependsOnID
	}
	wantTargets := make([]string, len(want))
	for i, e := range want {
		wantTargets[i] = e.dependsOnID
	}
	sort.Strings(gotTargets)
	sort.Strings(wantTargets)

	if slices.Equal(gotTargets, wantTargets) {
		return
	}

	missing, unexpected := setDiff(wantTargets, gotTargets)
	t.Errorf("%s: dep target set mismatch (got %d, want %d)\n  missing:    %v\n  unexpected: %v",
		context, len(got), len(want), missing, unexpected)
}

// requireCommentTextsEqual asserts that the comment objects contain exactly
// the expected text values (order-independent).
//
// NOTE: Uses text as identity, which works when all comment texts in a
// scenario are distinct. If a test creates duplicate-text comments, this
// will undercount — switch to multiset (count occurrences) or compare
// author+text pairs instead.
func requireCommentTextsEqual(t *testing.T, gotObjs []map[string]any, want []string, context string) {
	t.Helper()

	got := make([]string, 0, len(gotObjs))
	for _, obj := range gotObjs {
		if text, ok := obj["text"].(string); ok {
			got = append(got, text)
		}
	}

	sortedGot := append([]string(nil), got...)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedGot)
	sort.Strings(sortedWant)

	if slices.Equal(sortedGot, sortedWant) {
		return
	}

	missing, unexpected := setDiff(sortedWant, sortedGot)
	t.Errorf("%s: comment text set mismatch (got %d, want %d)\n  missing:    %v\n  unexpected: %v",
		context, len(got), len(want), missing, unexpected)
}

// setDiff returns items in want but not got (missing) and items in got but
// not want (unexpected). Both inputs must be sorted.
func setDiff(want, got []string) (missing, unexpected []string) {
	wantSet := make(map[string]bool, len(want))
	for _, s := range want {
		wantSet[s] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, s := range got {
		gotSet[s] = true
	}
	for _, s := range want {
		if !gotSet[s] {
			missing = append(missing, s)
		}
	}
	for _, s := range got {
		if !wantSet[s] {
			unexpected = append(unexpected, s)
		}
	}
	return missing, unexpected
}

func getStringSlice(m map[string]any, key string) []string {
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func getObjectSlice(m map[string]any, key string) []map[string]any {
	arr, ok := m[key].([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, v := range arr {
		if obj, ok := v.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func assertField(t *testing.T, issue map[string]any, key, want string) {
	t.Helper()
	got, ok := issue[key].(string)
	if !ok || got == "" {
		t.Errorf("field %q missing or empty in show --json, want %q", key, want)
		return
	}
	if got != want {
		t.Errorf("field %q = %q, want %q", key, got, want)
	}
}

func assertFieldFloat(t *testing.T, issue map[string]any, key string, want float64) {
	t.Helper()
	got, ok := issue[key].(float64)
	if !ok {
		t.Errorf("field %q missing or not a number in show --json, want %v", key, want)
		return
	}
	if got != want {
		t.Errorf("field %q = %v, want %v", key, got, want)
	}
}

func assertFieldPrefix(t *testing.T, issue map[string]any, key, prefix string) {
	t.Helper()
	got, ok := issue[key].(string)
	if !ok || got == "" {
		t.Errorf("field %q missing or empty in show --json, want prefix %q", key, prefix)
		return
	}
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("field %q = %q, want prefix %q", key, got, prefix)
	}
}

// parseJSONOutput handles both JSON array and JSONL formats.
func parseJSONOutput(t *testing.T, output string) []map[string]any {
	t.Helper()

	// Try JSON array first
	var arr []map[string]any
	if err := json.Unmarshal([]byte(output), &arr); err == nil {
		return arr
	}

	// Fall back to JSONL
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		arr = append(arr, m)
	}
	return arr
}
