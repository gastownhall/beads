//go:build regression

// Package regression implements differential testing between a pinned baseline
// bd binary (v0.49.6) and the current worktree build. Each test scenario runs
// the same CLI commands against both binaries in isolated workspaces, exports
// JSONL, normalizes volatile fields and IDs, and diffs the results.
//
// Run: go test -tags=regression -timeout=10m ./tests/regression/...
// Or:  make test-regression
package regression

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// baselineBin is the path to the pinned baseline bd binary.
var baselineBin string

// candidateBin is the path to the bd binary built from the current worktree.
var candidateBin string

// testDoltServerPort is the port of the isolated Dolt server started by TestMain.
var testDoltServerPort int

func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "regression tests not yet supported on Windows (zip extraction needed)")
		os.Exit(0)
	}

	// Start an isolated Dolt server so regression tests don't pollute
	// the production database on port 3307.
	if _, err := exec.LookPath("dolt"); err != nil {
		fmt.Fprintln(os.Stderr, "SKIP: dolt not found in PATH; regression tests require dolt")
		os.Exit(0)
	}
	cleanupServer := startTestDoltServer()

	tmpDir, err := os.MkdirTemp("", "bd-regression-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir: %v\n", err)
		cleanupServer()
		os.Exit(1)
	}

	// Build candidate from current worktree
	candidateBin = filepath.Join(tmpDir, "bd-candidate")
	fmt.Fprintln(os.Stderr, "Building candidate binary...")
	if err := buildCandidate(candidateBin); err != nil {
		fmt.Fprintf(os.Stderr, "building candidate: %v\n", err)
		os.RemoveAll(tmpDir)
		cleanupServer()
		os.Exit(1)
	}

	// Get baseline (env override > cache > download)
	fmt.Fprintln(os.Stderr, "Getting baseline binary...")
	baselineBin, err = getBaseline()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getting baseline: %v\n", err)
		os.RemoveAll(tmpDir)
		cleanupServer()
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Baseline:  %s\nCandidate: %s\n\n", baselineBin, candidateBin)
	code := m.Run()
	os.RemoveAll(tmpDir)
	cleanupServer()
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Binary management
// ---------------------------------------------------------------------------

func findModuleRoot() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		panic("could not determine test file location")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("could not find go.mod")
		}
		dir = parent
	}
}

func buildCandidate(outPath string) error {
	modRoot := findModuleRoot()
	cmd := exec.Command("go", "build", "-o", outPath, "./cmd/bd")
	cmd.Dir = modRoot
	cmd.Env = buildEnv()

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build: %w\n%s", err, out)
	}
	return nil
}

func buildEnv() []string {
	env := os.Environ()
	if prefix := icuPrefixPath(); prefix != "" {
		env = append(env,
			"CGO_CFLAGS=-I"+prefix+"/include",
			"CGO_CPPFLAGS=-I"+prefix+"/include",
			"CGO_LDFLAGS=-L"+prefix+"/lib",
		)
	}
	return env
}

func icuPrefixPath() string {
	out, err := exec.Command("brew", "--prefix", "icu4c").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func getBaseline() (string, error) {
	if bin := os.Getenv("BD_REGRESSION_BASELINE_BIN"); bin != "" {
		if _, err := os.Stat(bin); err != nil {
			return "", fmt.Errorf("BD_REGRESSION_BASELINE_BIN=%q: %w", bin, err)
		}
		return bin, nil
	}

	versionFile := filepath.Join(findModuleRoot(), "tests", "regression", "BASELINE_VERSION")
	data, err := os.ReadFile(versionFile)
	if err != nil {
		return "", fmt.Errorf("reading BASELINE_VERSION: %w", err)
	}
	version := strings.TrimSpace(string(data))

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	cacheDir = filepath.Join(cacheDir, "beads-regression")
	cachedBin := filepath.Join(cacheDir, "bd-"+version)

	if info, err := os.Stat(cachedBin); err == nil && info.Size() > 0 {
		fmt.Fprintf(os.Stderr, "Using cached baseline: %s\n", cachedBin)
		return cachedBin, nil
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("creating cache dir: %w", err)
	}
	ver := strings.TrimPrefix(version, "v")
	asset := fmt.Sprintf("beads_%s_%s_%s.tar.gz", ver, runtime.GOOS, runtime.GOARCH)
	url := fmt.Sprintf("https://github.com/steveyegge/beads/releases/download/%s/%s", version, asset)

	fmt.Fprintf(os.Stderr, "Downloading baseline: %s\n", url)
	if err := downloadAndExtract(url, cachedBin); err != nil {
		return "", fmt.Errorf("downloading baseline: %w", err)
	}

	return cachedBin, nil
}

func downloadAndExtract(url, destPath string) error {
	resp, err := http.Get(url) //nolint:gosec // URL constructed from trusted constants
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}

	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if filepath.Base(hdr.Name) == "bd" {
			f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, tr)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
	}
	return fmt.Errorf("bd binary not found in archive from %s", url)
}

// ---------------------------------------------------------------------------
// Workspace: isolated temp dir with git repo + bd init
// ---------------------------------------------------------------------------

type workspace struct {
	dir        string
	bdPath     string
	t          *testing.T
	createdIDs []string // issue IDs in creation order
}

func newWorkspace(t *testing.T, bdPath string) *workspace {
	t.Helper()
	dir := t.TempDir()
	w := &workspace{dir: dir, bdPath: bdPath, t: t}

	w.git("init")
	w.git("config", "user.name", "regression-test")
	w.git("config", "user.email", "test@regression.test")

	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w.git("add", ".")
	w.git("commit", "-m", "initial")

	w.run("init", "--prefix", "test", "--quiet")

	return w
}

func (w *workspace) runEnv() []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + w.dir,
		"BEADS_NO_DAEMON=1",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	if testDoltServerPort != 0 {
		env = append(env, "BEADS_DOLT_PORT="+strconv.Itoa(testDoltServerPort))
	}
	if v := os.Getenv("TMPDIR"); v != "" {
		env = append(env, "TMPDIR="+v)
	}
	return env
}

func (w *workspace) git(args ...string) string {
	w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = w.dir
	cmd.Env = w.runEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// run executes a bd command (combined stdout+stderr), fataling on error.
func (w *workspace) run(args ...string) string {
	w.t.Helper()
	cmd := exec.Command(w.bdPath, args...)
	cmd.Dir = w.dir
	cmd.Env = w.runEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// create runs bd create --silent and returns the created issue ID.
// The ID is also appended to createdIDs for canonical mapping.
func (w *workspace) create(args ...string) string {
	w.t.Helper()
	allArgs := append([]string{"create", "--silent"}, args...)
	cmd := exec.Command(w.bdPath, allArgs...)
	cmd.Dir = w.dir
	cmd.Env = w.runEnv()
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
	w.createdIDs = append(w.createdIDs, id)
	return id
}

// export runs bd export and returns the JSONL output.
func (w *workspace) export(extraArgs ...string) string {
	w.t.Helper()
	args := append([]string{"export"}, extraArgs...)
	return w.run(args...)
}

// ---------------------------------------------------------------------------
// Normalization
// ---------------------------------------------------------------------------

var volatileFields = []string{
	"created_at", "updated_at", "closed_at",
	"compacted_at", "compacted_at_commit",
	"last_activity", "closed_by_session",
	"compaction_level", "original_size",
}

var versionSpecificFields = []string{
	"deleted_at", "deleted_by", "delete_reason", "original_type",
}

// normalizeJSONL parses JSONL, normalizes each issue, applies ID canonicalization,
// sorts by canonical ID, and returns deterministic JSONL for comparison.
func normalizeJSONL(data string, idMap map[string]string) (string, error) {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return "", nil
	}

	var issues []map[string]any
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return "", fmt.Errorf("line %d: %w", i+1, err)
		}
		// Skip tombstoned issues: v0.49.6 exports deleted issues as
		// tombstones (with deleted_at set); main omits them entirely.
		if _, hasTombstone := m["deleted_at"]; hasTombstone {
			continue
		}
		normalizeIssue(m)
		canonicalizeIDs(m, idMap)
		// Re-sort deps/comments after canonicalization: real IDs are random
		// so pre-canonicalization sort may produce different orderings.
		sortSubobjects(m)
		issues = append(issues, m)
	}

	sort.Slice(issues, func(i, j int) bool {
		a, _ := issues[i]["id"].(string)
		b, _ := issues[j]["id"].(string)
		return a < b
	})

	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, issue := range issues {
		if err := enc.Encode(issue); err != nil {
			return "", err
		}
	}
	return buf.String(), nil
}

func normalizeIssue(m map[string]any) {
	for _, f := range volatileFields {
		delete(m, f)
	}
	for _, f := range versionSpecificFields {
		delete(m, f)
	}

	// Normalize date-only fields (due_at, defer_until) to date-only format.
	// SQLite stores local-timezone midnight, Dolt stores UTC midnight for
	// the same date input (e.g. "2099-01-15"). Truncate to date to avoid
	// false positives from timezone representation differences.
	for _, df := range []string{"due_at", "defer_until"} {
		if s, ok := m[df].(string); ok && s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				m[df] = t.Format("2006-01-02")
			}
		}
	}

	// Sort labels lexicographically
	if labels, ok := m["labels"].([]any); ok && len(labels) > 0 {
		sort.Slice(labels, func(i, j int) bool {
			a, _ := labels[i].(string)
			b, _ := labels[j].(string)
			return a < b
		})
	}

	// Normalize comments: strip volatile fields, sort by (author, text)
	if comments, ok := m["comments"].([]any); ok {
		for _, c := range comments {
			if cm, ok := c.(map[string]any); ok {
				delete(cm, "created_at")
				delete(cm, "id")
			}
		}
		sort.Slice(comments, func(i, j int) bool {
			ci, _ := comments[i].(map[string]any)
			cj, _ := comments[j].(map[string]any)
			ai, _ := ci["author"].(string)
			aj, _ := cj["author"].(string)
			if ai != aj {
				return ai < aj
			}
			ti, _ := ci["text"].(string)
			tj, _ := cj["text"].(string)
			return ti < tj
		})
	}

	// Normalize dependencies: strip volatile fields and empty metadata,
	// sort by (depends_on_id, type)
	if deps, ok := m["dependencies"].([]any); ok {
		for _, d := range deps {
			if dm, ok := d.(map[string]any); ok {
				delete(dm, "created_at")
				// Dolt stores metadata="{}" where SQLite omits it entirely
				if md, ok := dm["metadata"]; ok {
					if mdStr, _ := md.(string); mdStr == "" || mdStr == "{}" {
						delete(dm, "metadata")
					}
				}
			}
		}
		sort.Slice(deps, func(i, j int) bool {
			di, _ := deps[i].(map[string]any)
			dj, _ := deps[j].(map[string]any)
			a, _ := di["depends_on_id"].(string)
			b, _ := dj["depends_on_id"].(string)
			if a != b {
				return a < b
			}
			ta, _ := di["type"].(string)
			tb, _ := dj["type"].(string)
			return ta < tb
		})
	}

	// Remove nil, empty strings, empty collections to handle omitempty differences.
	// Preserve "priority" (0 = P0/critical) and "id".
	for k, v := range m {
		switch val := v.(type) {
		case nil:
			delete(m, k)
		case string:
			if val == "" && k != "id" {
				delete(m, k)
			}
		case []any:
			if len(val) == 0 {
				delete(m, k)
			}
		case map[string]any:
			if len(val) == 0 {
				delete(m, k)
			}
		case bool:
			if !val {
				delete(m, k)
			}
		case float64:
			if val == 0 && k != "priority" {
				delete(m, k)
			}
		}
	}
}

// canonicalizeIDs replaces real issue IDs with canonical names (ISSUE-1, ISSUE-2, ...)
// based on creation order, so two runs with different ID schemes can be compared.
func canonicalizeIDs(m map[string]any, idMap map[string]string) {
	if len(idMap) == 0 {
		return
	}

	replaceID := func(field string, obj map[string]any) {
		if id, ok := obj[field].(string); ok {
			if canonical, ok := idMap[id]; ok {
				obj[field] = canonical
			}
		}
	}

	replaceID("id", m)

	if deps, ok := m["dependencies"].([]any); ok {
		for _, d := range deps {
			if dm, ok := d.(map[string]any); ok {
				replaceID("issue_id", dm)
				replaceID("depends_on_id", dm)
			}
		}
	}

	if comments, ok := m["comments"].([]any); ok {
		for _, c := range comments {
			if cm, ok := c.(map[string]any); ok {
				replaceID("issue_id", cm)
			}
		}
	}
}

// sortSubobjects re-sorts dependencies and comments by their canonical IDs.
// Must be called after canonicalizeIDs, since real IDs are random and
// pre-canonicalization sort order is non-deterministic across runs.
func sortSubobjects(m map[string]any) {
	if deps, ok := m["dependencies"].([]any); ok && len(deps) > 1 {
		sort.Slice(deps, func(i, j int) bool {
			di, _ := deps[i].(map[string]any)
			dj, _ := deps[j].(map[string]any)
			a, _ := di["depends_on_id"].(string)
			b, _ := dj["depends_on_id"].(string)
			if a != b {
				return a < b
			}
			ta, _ := di["type"].(string)
			tb, _ := dj["type"].(string)
			return ta < tb
		})
	}
	if comments, ok := m["comments"].([]any); ok && len(comments) > 1 {
		sort.Slice(comments, func(i, j int) bool {
			ci, _ := comments[i].(map[string]any)
			cj, _ := comments[j].(map[string]any)
			ai, _ := ci["author"].(string)
			aj, _ := cj["author"].(string)
			if ai != aj {
				return ai < aj
			}
			ti, _ := ci["text"].(string)
			tj, _ := cj["text"].(string)
			return ti < tj
		})
	}
}

// canonicalIDMap builds ISSUE-1, ISSUE-2, ... from a slice of IDs in creation order.
func canonicalIDMap(ids []string) map[string]string {
	m := make(map[string]string, len(ids))
	for i, id := range ids {
		m[id] = fmt.Sprintf("ISSUE-%d", i+1)
	}
	return m
}

// ---------------------------------------------------------------------------
// Comparison helpers
// ---------------------------------------------------------------------------

// compareExports runs a scenario against both binaries, exports JSONL,
// canonicalizes IDs based on creation order, and diffs.
func compareExports(t *testing.T, scenario func(w *workspace)) {
	t.Helper()

	baselineWS := newWorkspace(t, baselineBin)
	scenario(baselineWS)
	baselineRaw := baselineWS.export()

	candidateWS := newWorkspace(t, candidateBin)
	scenario(candidateWS)
	candidateRaw := candidateWS.export()

	diffNormalized(t,
		baselineRaw, candidateRaw,
		canonicalIDMap(baselineWS.createdIDs),
		canonicalIDMap(candidateWS.createdIDs),
	)
}

// diffNormalized normalizes two JSONL strings and reports differences.
func diffNormalized(t *testing.T, baselineRaw, candidateRaw string, baselineIDMap, candidateIDMap map[string]string) {
	t.Helper()

	baselineNorm, err := normalizeJSONL(baselineRaw, baselineIDMap)
	if err != nil {
		t.Fatalf("normalizing baseline: %v", err)
	}
	candidateNorm, err := normalizeJSONL(candidateRaw, candidateIDMap)
	if err != nil {
		t.Fatalf("normalizing candidate: %v", err)
	}

	if baselineNorm == candidateNorm {
		return
	}

	t.Error("JSONL mismatch between baseline and candidate")

	bMap := issuesByID(baselineNorm)
	cMap := issuesByID(candidateNorm)

	for id, bLine := range bMap {
		cLine, ok := cMap[id]
		if !ok {
			t.Errorf("  %s: in baseline, missing from candidate", id)
			continue
		}
		if bLine != cLine {
			t.Errorf("  %s differs:\n    baseline:  %s\n    candidate: %s", id, bLine, cLine)
		}
	}
	for id := range cMap {
		if _, ok := bMap[id]; !ok {
			t.Errorf("  %s: in candidate, missing from baseline", id)
		}
	}
}

func issuesByID(jsonl string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(jsonl), "\n") {
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		if id, ok := obj["id"].(string); ok {
			m[id] = line
		}
	}
	return m
}

// parseReadyIDs runs bd ready --json and returns the set of issue IDs.
func parseReadyIDs(t *testing.T, w *workspace) map[string]bool {
	t.Helper()
	out := w.run("ready", "--json")
	ids := make(map[string]bool)

	// Try JSON array first
	var issues []map[string]any
	if err := json.Unmarshal([]byte(out), &issues); err == nil {
		for _, m := range issues {
			if id, ok := m["id"].(string); ok {
				ids[id] = true
			}
		}
		return ids
	}

	// Fall back to JSONL
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if id, ok := m["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

// tryRun executes a bd command and returns (output, error) without fataling.
func (w *workspace) tryRun(args ...string) (string, error) {
	w.t.Helper()
	cmd := exec.Command(w.bdPath, args...)
	cmd.Dir = w.dir
	cmd.Env = w.runEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// tryCreate runs bd create --silent and returns (issueID, error) without fataling.
// On success the ID is appended to createdIDs for canonical mapping.
func (w *workspace) tryCreate(args ...string) (string, error) {
	w.t.Helper()
	allArgs := append([]string{"create", "--silent"}, args...)
	cmd := exec.Command(w.bdPath, allArgs...)
	cmd.Dir = w.dir
	cmd.Env = w.runEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("bd create returned empty ID")
	}
	w.createdIDs = append(w.createdIDs, id)
	return id, nil
}

// ---------------------------------------------------------------------------
// Test Dolt server (isolation from production)
// ---------------------------------------------------------------------------

// startTestDoltServer starts a dedicated Dolt SQL server in a temp directory
// on a dynamic port. This prevents regression tests from creating databases on
// the production Dolt server (port 3307).
// Returns a cleanup function that stops the server and removes the temp dir.
func startTestDoltServer() func() {
	if _, err := exec.LookPath("dolt"); err != nil {
		fmt.Fprintln(os.Stderr, "WARN: dolt not found in PATH; regression tests will be skipped")
		return func() {}
	}

	tmpDir, err := os.MkdirTemp("", "bd-regression-dolt-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to create test dolt dir: %v\n", err)
		return func() {}
	}

	dbDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to create test dolt data dir: %v\n", err)
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	// Configure dolt user identity (required by dolt init).
	doltEnv := append(os.Environ(), "DOLT_ROOT_PATH="+tmpDir)
	for _, args := range [][]string{
		{"dolt", "config", "--global", "--add", "user.name", "regression-test"},
		{"dolt", "config", "--global", "--add", "user.email", "test@regression.test"},
	} {
		cfgCmd := exec.Command(args[0], args[1:]...)
		cfgCmd.Env = doltEnv
		if out, err := cfgCmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: %s failed: %v\n%s\n", args[1], err, out)
			_ = os.RemoveAll(tmpDir)
			return func() {}
		}
	}

	initCmd := exec.Command("dolt", "init")
	initCmd.Dir = dbDir
	initCmd.Env = doltEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: dolt init failed for test server: %v\n%s\n", err, out)
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	port, err := findFreePort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to find free port for test dolt server: %v\n", err)
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	serverCmd := exec.Command("dolt", "sql-server",
		"-H", "127.0.0.1",
		"-P", fmt.Sprintf("%d", port),
		"--no-auto-commit",
	)
	serverCmd.Dir = dbDir
	serverCmd.Env = doltEnv
	if os.Getenv("BEADS_TEST_DOLT_VERBOSE") != "1" {
		serverCmd.Stderr = nil
		serverCmd.Stdout = nil
	}
	if err := serverCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: failed to start test dolt server: %v\n", err)
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	if !waitForServer(port, 10*time.Second) {
		fmt.Fprintf(os.Stderr, "WARN: test dolt server did not become ready on port %d\n", port)
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		_ = os.RemoveAll(tmpDir)
		return func() {}
	}

	testDoltServerPort = port
	fmt.Fprintf(os.Stderr, "Test Dolt server running on port %d\n", port)

	return func() {
		testDoltServerPort = 0
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		_ = os.RemoveAll(tmpDir)
	}
}

// findFreePort finds an available TCP port by binding to :0.
func findFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// waitForServer polls until the Dolt server accepts a MySQL connection.
func waitForServer(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	dsn := fmt.Sprintf("root@tcp(127.0.0.1:%d)/?timeout=1s", port)
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			if err := db.Ping(); err == nil {
				_ = db.Close()
				return true
			}
			_ = db.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
