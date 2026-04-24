//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// The tests in this file cover the be-nu4.3 `useSummary` narrow-projection
// selector in cmd/bd/list.go (~line 898):
//
//     useSummary := !watchMode && !prettyFormat && formatStr == "" &&
//                   !jsonOutput && (ui.IsAgentMode() || !longFormat)
//
// There is no in-process way to observe which branch was taken without
// refactoring the predicate into a helper (explicitly out of scope for
// be-2kl), so we drive the real `bd` binary as a subprocess and assert on
// output shape. Each flag combination below exercises exactly one side of
// the selector for coverage purposes — the assertions verify the output
// matches the mode's contract (JSON, pretty, agent, long, compact).
//
// The fast-path / slow-path distinction for compact + agent mode is
// byte-identical by design (enforced by TestSummaryRenderParity in
// list_summary_parity_test.go), so those tests assert on content parity
// rather than attempting to distinguish code paths.

// summarySelectorHarness sets up a target dolt store with a known issue
// and builds the `bd` binary once for reuse across subtests.
type summarySelectorHarness struct {
	t         *testing.T
	binPath   string
	repoDir   string
	beadsDir  string
	issue     *types.Issue
	secondary *types.Issue
}

func newSummarySelectorHarness(t *testing.T) *summarySelectorHarness {
	t.Helper()
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping useSummary selector tests")
	}

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "dolt.auto-commit: off\nactor: selector-actor\n")
	database := uniqueTestDBName(t)
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   database,
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		dropTestDatabase(database, testDoltServerPort)
	})

	if err := store.SetConfig(ctx, "issue_prefix", "sel"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	now := time.Now()
	primary := &types.Issue{
		ID:          "sel-1",
		Title:       "Selector probe issue",
		Description: "Used by useSummary selector CLI integration tests",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.CreateIssue(ctx, primary, "selector-actor"); err != nil {
		t.Fatalf("create primary issue: %v", err)
	}
	secondary := &types.Issue{
		ID:          "sel-2",
		Title:       "Second selector probe",
		Description: "Second row so sort/limit logic has something to act on",
		Status:      types.StatusInProgress,
		Priority:    0,
		IssueType:   types.TypeBug,
		Assignee:    "selector-actor",
		CreatedAt:   now.Add(1 * time.Minute),
		UpdatedAt:   now.Add(1 * time.Minute),
	}
	if err := store.CreateIssue(ctx, secondary, "selector-actor"); err != nil {
		t.Fatalf("create secondary issue: %v", err)
	}

	binPath := filepath.Join(t.TempDir(), "bd-under-test")
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = packageDir
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	return &summarySelectorHarness{
		t:         t,
		binPath:   binPath,
		repoDir:   repoDir,
		beadsDir:  beadsDir,
		issue:     primary,
		secondary: secondary,
	}
}

// run invokes the `bd` binary with the given extra env vars and flags.
// It always points BEADS_DIR at the fixture's .beads directory so the
// command binds to the seeded store. The extra args are appended after
// the `list` subcommand.
func (h *summarySelectorHarness) run(ctx context.Context, extraEnv []string, args ...string) (stdout string, err error) {
	h.t.Helper()
	allArgs := append([]string{"list"}, args...)
	cmd := exec.CommandContext(ctx, h.binPath, allArgs...)
	cmd.Dir = h.repoDir
	// Start from a filtered environment so the test run does not inherit
	// BEADS_*/BD_* state from the parent test process (same pattern as
	// filteredEnvForContextBinding).
	base := filteredEnvForContextBinding("BEADS_DIR", "BEADS_DB", "BD_DB", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_DATABASE")
	base = append(base,
		"HOME="+h.t.TempDir(),
		"XDG_CONFIG_HOME="+h.t.TempDir(),
		"BEADS_TEST_MODE=1",
		"BEADS_DIR="+h.beadsDir,
		"BEADS_DB=",
		// Clear agent auto-detect so tests control agent mode explicitly.
		"BD_AGENT_MODE=",
		"CLAUDE_CODE=",
	)
	cmd.Env = append(base, extraEnv...)
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

func TestListSelector_DefaultHumanCompact(t *testing.T) {
	h := newSummarySelectorHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := h.run(ctx, nil)
	if err != nil {
		t.Fatalf("bd list: %v\n%s", err, out)
	}
	// Fast path: compact output is byte-identical to slow-path compact per
	// TestSummaryRenderParity, so we assert content — both seeded IDs must
	// appear, and the output must NOT be JSON (which would indicate we
	// accidentally fell through to the slow path with jsonOutput=true).
	if !strings.Contains(out, "sel-1") || !strings.Contains(out, "sel-2") {
		t.Fatalf("expected both issue IDs in compact output, got:\n%s", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "[") || strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("compact default output must not be JSON-shaped:\n%s", out)
	}
}

func TestListSelector_AgentMode_UsesSummary(t *testing.T) {
	h := newSummarySelectorHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// BD_AGENT_MODE=1 makes ui.IsAgentMode() return true. This exercises the
	// agent-mode side of the `(ui.IsAgentMode() || !longFormat)` sub-clause
	// — the fast path should still be taken.
	out, err := h.run(ctx, []string{"BD_AGENT_MODE=1"})
	if err != nil {
		t.Fatalf("bd list --agent-mode: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sel-1") || !strings.Contains(out, "sel-2") {
		t.Fatalf("expected both issue IDs in agent-mode output, got:\n%s", out)
	}
}

func TestListSelector_AgentMode_LongFlagStillFastPath(t *testing.T) {
	h := newSummarySelectorHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// With BD_AGENT_MODE=1, the `(ui.IsAgentMode() || !longFormat)` sub-clause
	// short-circuits on the first operand, so --long must NOT flip the
	// selector into the slow path. This is the "regardless of --long" case
	// called out in be-2kl's spec.
	out, err := h.run(ctx, []string{"BD_AGENT_MODE=1"}, "--long")
	if err != nil {
		t.Fatalf("bd list --long (agent mode): %v\n%s", err, out)
	}
	if !strings.Contains(out, "sel-1") || !strings.Contains(out, "sel-2") {
		t.Fatalf("expected both issue IDs in agent+long output, got:\n%s", out)
	}
}

func TestListSelector_LongWithoutAgent_UsesSlowPath(t *testing.T) {
	h := newSummarySelectorHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Without agent mode, `--long` forces the `!longFormat` operand to false,
	// so the whole selector evaluates to false and the slow path (full-issue
	// hydration) runs. The long-format renderer emits a separate indented
	// "  Assignee: <name>" line per issue, while the fast path inlines the
	// assignee as "@<name>"; asserting the multi-line form proves we took
	// the slow path.
	out, err := h.run(ctx, nil, "--long")
	if err != nil {
		t.Fatalf("bd list --long: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sel-1") {
		t.Fatalf("expected primary issue id in long output, got:\n%s", out)
	}
	if !strings.Contains(out, "Assignee: selector-actor") {
		t.Fatalf("expected long-format 'Assignee: selector-actor' line (proves slow path), got:\n%s", out)
	}
}

func TestListSelector_JSONOutput_UsesSlowPath(t *testing.T) {
	h := newSummarySelectorHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --json sets jsonOutput=true, zeroing out the selector.
	out, err := h.run(ctx, nil, "--json")
	if err != nil {
		t.Fatalf("bd list --json: %v\n%s", err, out)
	}
	trimmed := strings.TrimSpace(out)
	if !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		t.Fatalf("expected JSON output, got:\n%s", out)
	}
	if !strings.Contains(out, "sel-1") {
		t.Fatalf("expected sel-1 in JSON output, got:\n%s", out)
	}
}

func TestListSelector_Pretty_UsesSlowPath(t *testing.T) {
	h := newSummarySelectorHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --pretty sets prettyFormat=true, zeroing out the selector. Pretty
	// output includes tree connectors or a "Total:" footer from
	// displayPrettyList, neither of which the fast path emits.
	out, err := h.run(ctx, nil, "--pretty")
	if err != nil {
		t.Fatalf("bd list --pretty: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sel-1") {
		t.Fatalf("expected sel-1 in pretty output, got:\n%s", out)
	}
	if !strings.Contains(out, "Total:") {
		t.Fatalf("expected pretty-format 'Total:' footer (proves slow path), got:\n%s", out)
	}
}

func TestListSelector_ExplicitFormat_UsesSlowPath(t *testing.T) {
	h := newSummarySelectorHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// -f json sets formatStr != "" AND jsonOutput=true via PersistentPreRun.
	// Either condition independently zeros the selector; this test exercises
	// the combined case. If the formatStr check ever regresses, only this
	// combination and the explicit `--format=json` path would catch it.
	out, err := h.run(ctx, nil, "-f", "json")
	if err != nil {
		t.Fatalf("bd list -f json: %v\n%s", err, out)
	}
	trimmed := strings.TrimSpace(out)
	if !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		t.Fatalf("expected JSON output from -f json, got:\n%s", out)
	}
}

func TestListSelector_Watch_UsesSlowPath(t *testing.T) {
	h := newSummarySelectorHarness(t)
	// --watch blocks forever (2s poll loop). We give it a short timeout,
	// observe the initial snapshot, then context-cancel. The binary should
	// have emitted the watch banner and rendered at least one frame before
	// being killed.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, _ := h.run(ctx, nil, "--watch")
	// We intentionally ignore the exit error — SIGKILL from context timeout
	// yields a non-nil err, but the captured output is what we care about.
	if !strings.Contains(out, "sel-1") {
		t.Fatalf("expected sel-1 in watch output (first frame), got:\n%s", out)
	}
	if !strings.Contains(out, "Watching for changes") {
		t.Fatalf("expected watch banner (proves slow/watch path ran), got:\n%s", out)
	}
}
