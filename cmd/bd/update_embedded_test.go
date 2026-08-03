//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// ===== Shared test helpers (used by both update and close tests) =====

// bdUpdate runs "bd update" with the given args and returns stdout.
// Retries on flock contention.
func bdUpdate(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd update %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdUpdateFail runs "bd update" expecting failure.
func bdUpdateFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd update %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdUpdateCapture runs "bd update" expecting success, returning stdout and
// stderr separately (stdout may be JSON; warnings must not pollute it).
func bdUpdateCapture(t *testing.T, bd, dir string, args ...string) (stdout, stderr string) {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	outBuf, errBuf, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd update %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, outBuf.String(), errBuf.String())
	}
	return outBuf.String(), errBuf.String()
}

func embeddedCurrentCommit(t *testing.T, beadsDir, database string) string {
	t.Helper()
	store, err := embeddeddolt.Open(t.Context(), beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open embedded store: %v", err)
	}
	defer func() { _ = store.Close() }()

	head, err := store.GetCurrentCommit(t.Context())
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if head == "" {
		t.Fatal("GetCurrentCommit returned empty hash")
	}
	return head
}

// bdShowJSON runs "bd show <id> --json" and returns the raw JSON output.
func bdShowJSON(t *testing.T, bd, dir, id string) string {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\nstdout:\n%s\nstderr:\n%s", id, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// hasLabel checks if a label is present in the issue's labels.
func hasLabel(issue *types.Issue, label string) bool {
	for _, l := range issue.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// parseShowJSON parses the first JSON object from bd show --json output,
// which may be wrapped in an array or have non-JSON lines before it.
func parseShowJSON(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	start := strings.Index(raw, "{")
	if start < 0 {
		t.Fatalf("no JSON object in output: %s", raw)
	}
	dec := json.NewDecoder(strings.NewReader(raw[start:]))
	var obj json.RawMessage
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("parse JSON object: %v\nraw: %s", err, raw[start:])
	}
	return obj
}

// showLabels returns labels from bd show --json output (uses IssueDetails which includes labels).
func showLabels(t *testing.T, bd, dir, id string) []string {
	t.Helper()
	raw := bdShowJSON(t, bd, dir, id)
	obj := parseShowJSON(t, raw)
	var details struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(obj, &details); err != nil {
		t.Fatalf("parse labels: %v", err)
	}
	return details.Labels
}

// showDeps returns dependency IDs from bd show --json output.
func showDeps(t *testing.T, bd, dir, id string) []struct {
	ID   string `json:"id"`
	Type string `json:"dependency_type"`
} {
	t.Helper()
	raw := bdShowJSON(t, bd, dir, id)
	obj := parseShowJSON(t, raw)
	var details struct {
		Dependencies []struct {
			ID   string `json:"id"`
			Type string `json:"dependency_type"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(obj, &details); err != nil {
		t.Fatalf("parse deps: %v", err)
	}
	return details.Dependencies
}

// ===== Update tests =====

func TestEmbeddedUpdateBatchAutoCommitDoesNotAdvanceHead(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt update tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ub")
	issue := bdCreate(t, bd, dir, "Batch update")
	before := embeddedCurrentCommit(t, beadsDir, "ub")

	cmd := exec.Command(bd, "--dolt-auto-commit", "batch", "update", issue.ID, "--title", "Deferred batch update")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd --dolt-auto-commit batch update failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	after := embeddedCurrentCommit(t, beadsDir, "ub")
	if after != before {
		t.Fatalf("batch-mode update advanced HEAD; before=%s after=%s", before, after)
	}
}

func TestEmbeddedUpdateRoutedStoreCommitsTargetHead(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt update tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "src")

	targetDir := filepath.Join(dir, "target-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", "tgt")

	issue := bdCreate(t, bd, targetDir, "Routed target issue")
	route := `{"prefix":"tgt-","path":"target-repo"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".beads", "routes.jsonl"), []byte(route), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	targetBeadsDir := filepath.Join(targetDir, ".beads")
	before := embeddedCurrentCommit(t, targetBeadsDir, "tgt")
	bdUpdate(t, bd, dir, issue.ID, "--title", "Updated through route")
	after := embeddedCurrentCommit(t, targetBeadsDir, "tgt")
	if after == before {
		t.Fatalf("routed update did not advance target HEAD; before=%s after=%s", before, after)
	}

	targetStore := openStore(t, targetBeadsDir, "tgt")
	got, err := targetStore.GetIssue(t.Context(), issue.ID)
	if err != nil {
		t.Fatalf("GetIssue in target: %v", err)
	}
	if got.Title != "Updated through route" {
		t.Fatalf("target title = %q, want routed update title", got.Title)
	}
}

func TestEmbeddedUpdate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tu")

	t.Run("update_direct_flag_mapping", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Multi update", "--type", "task", "--metadata", `{"remove":"me"}`)
		out := bdUpdate(t, bd, dir,
			"--json", issue.ID,
			"--status", "in_progress",
			"--assignee", "bob",
			"--priority", "1",
			"--description", "Updated description",
			"--design", "Design notes here",
			"--acceptance", "AC text",
			"--external-ref", "gh-42",
			"--estimate", "60",
			"--await-id", "run-123",
			"--due", "2099-01-15",
			"--set-metadata", "team=platform",
			"--unset-metadata", "remove",
			"--no-history",
		)
		var updated []*types.Issue
		if err := json.Unmarshal([]byte(out), &updated); err != nil {
			t.Fatalf("parse direct update response: %v\n%s", err, out)
		}
		if len(updated) != 1 {
			t.Fatalf("direct update returned %d issues, want 1: %s", len(updated), out)
		}
		got := updated[0]
		if got.Status != types.StatusInProgress {
			t.Errorf("expected status in_progress, got %s", got.Status)
		}
		if got.Assignee != "bob" {
			t.Errorf("expected assignee bob, got %q", got.Assignee)
		}
		if got.Priority != 1 {
			t.Errorf("expected priority 1, got %d", got.Priority)
		}
		if got.Description != "Updated description" {
			t.Errorf("expected updated description, got %q", got.Description)
		}
		if got.Design != "Design notes here" {
			t.Errorf("expected design notes, got %q", got.Design)
		}
		if got.AcceptanceCriteria != "AC text" {
			t.Errorf("expected acceptance criteria, got %q", got.AcceptanceCriteria)
		}
		if got.ExternalRef == nil || *got.ExternalRef != "gh-42" {
			t.Errorf("expected external_ref gh-42, got %v", got.ExternalRef)
		}
		if got.EstimatedMinutes == nil || *got.EstimatedMinutes != 60 {
			t.Errorf("expected estimated_minutes 60, got %v", got.EstimatedMinutes)
		}
		if got.AwaitID != "run-123" {
			t.Errorf("expected await_id run-123, got %q", got.AwaitID)
		}
		if got.DueAt == nil || got.DueAt.Format("2006-01-02") != "2099-01-15" {
			t.Errorf("expected due_at 2099-01-15, got %v", got.DueAt)
		}
		if !got.NoHistory {
			t.Error("expected no_history true")
		}
		var metadata map[string]any
		if err := json.Unmarshal(got.Metadata, &metadata); err != nil {
			t.Fatalf("parse updated metadata %q: %v", got.Metadata, err)
		}
		if metadata["team"] != "platform" {
			t.Errorf("metadata team = %v, want platform", metadata["team"])
		}
		if _, ok := metadata["remove"]; ok {
			t.Errorf("metadata still contains removed key: %v", metadata)
		}

		conflict := bdUpdateFail(t, bd, dir, issue.ID,
			"--metadata", `{"other":"value"}`,
			"--set-metadata", "team=other",
		)
		if !strings.Contains(conflict, "cannot combine --metadata with --set-metadata or --unset-metadata") {
			t.Errorf("expected exact metadata conflict error, got: %s", conflict)
		}

		out = bdUpdate(t, bd, dir, "--json", issue.ID, "--due", "", "--history")
		updated = nil
		if err := json.Unmarshal([]byte(out), &updated); err != nil {
			t.Fatalf("parse clearing update response: %v\n%s", err, out)
		}
		if len(updated) != 1 {
			t.Fatalf("clearing update returned %d issues, want 1: %s", len(updated), out)
		}
		if updated[0].DueAt != nil {
			t.Errorf("expected due_at nil after clear, got %v", updated[0].DueAt)
		}
		if updated[0].NoHistory {
			t.Error("expected no_history false after --history")
		}
	})

	t.Run("update_type_custom", func(t *testing.T) {
		// Register "agent" as a custom type via bd config (GH#3030).
		// This writes to Dolt only, NOT to .beads/config.yaml.
		cfgCmd := exec.Command(bd, "config", "set", "types.custom", "agent,spike")
		cfgCmd.Dir = dir
		cfgCmd.Env = bdEnv(dir)
		if out, err := cfgCmd.CombinedOutput(); err != nil {
			t.Fatalf("bd config set types.custom failed: %v\n%s", err, out)
		}

		issue := bdCreate(t, bd, dir, "Custom type update", "--type", "task")
		// Before the fix (GH#3030), this would fail with "invalid issue type"
		// because the CLI-level validation could not read custom types from Dolt.
		bdUpdate(t, bd, dir, issue.ID, "--type", "agent")
		got := bdShow(t, bd, dir, issue.ID)
		if string(got.IssueType) != "agent" {
			t.Errorf("expected type agent, got %s", got.IssueType)
		}
	})

	// GH#3902: --external-ref "" must clear to SQL NULL (matching buildCreateIssue's
	// pointer semantics), not write an empty string. Otherwise sync/tracker code
	// that checks ExternalRef == nil silently misclassifies cleared refs as still
	// tracked, and two cleared issues round-trip with different JSON shapes
	// (cleared via CLI emits "external_ref":"" while never-set issues omit the field).
	t.Run("update_external_ref_clear", func(t *testing.T) {
		a := bdCreate(t, bd, dir, "ExtRef clear A", "--type", "task", "--external-ref", "ref-a")
		b := bdCreate(t, bd, dir, "ExtRef clear B", "--type", "task", "--external-ref", "ref-b")

		bdUpdate(t, bd, dir, a.ID, "--external-ref", "")
		// Repeat clear must succeed for a second issue — historical UNIQUE
		// constraint repro from the issue report.
		bdUpdate(t, bd, dir, b.ID, "--external-ref", "")

		gotA := bdShow(t, bd, dir, a.ID)
		gotB := bdShow(t, bd, dir, b.ID)
		if gotA.ExternalRef != nil {
			t.Errorf("expected A.external_ref to be nil after clear, got %q", *gotA.ExternalRef)
		}
		if gotB.ExternalRef != nil {
			t.Errorf("expected B.external_ref to be nil after clear, got %q", *gotB.ExternalRef)
		}

		// JSON output: cleared ref should be omitted via omitempty, not emitted as "".
		rawA := bdShowJSON(t, bd, dir, a.ID)
		if strings.Contains(rawA, `"external_ref"`) {
			t.Errorf("expected external_ref field to be omitted from JSON after clear, got: %s", rawA)
		}
	})

	t.Run("update_notes_overwrite_warns", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Notes warning test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--notes", "original notes")

		stdout, stderr := bdUpdateCapture(t, bd, dir, issue.ID, "--notes", "replacement notes")
		warning := fmt.Sprintf("warning: %s: --notes replaced existing notes (use --append-notes to preserve history)", issue.ID)
		if !strings.Contains(stderr, warning) {
			t.Errorf("expected stderr to contain %q, got: %s", warning, stderr)
		}
		if strings.Contains(stdout, "warning:") {
			t.Errorf("warning must not appear on stdout, got: %s", stdout)
		}
		if got := bdShow(t, bd, dir, issue.ID); got.Notes != "replacement notes" {
			t.Errorf("expected notes %q, got %q", "replacement notes", got.Notes)
		}
	})

	t.Run("update_defer", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Defer test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--defer", "2099-01-15")
		got := bdShow(t, bd, dir, issue.ID)
		if got.DeferUntil == nil {
			t.Error("expected defer_until to be set")
		}
		// GH#3233: --defer should also set status=deferred for consistency with `bd defer`
		if string(got.Status) != "deferred" {
			t.Errorf("expected status=deferred, got %q", got.Status)
		}
	})

	t.Run("update_defer_respects_explicit_status", func(t *testing.T) {
		// GH#3233: explicit --status should win over the implicit deferred set by --defer
		issue := bdCreate(t, bd, dir, "Defer+status test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--defer", "2099-01-15", "--status", "in_progress")
		got := bdShow(t, bd, dir, issue.ID)
		if string(got.Status) != "in_progress" {
			t.Errorf("expected explicit status=in_progress to win, got %q", got.Status)
		}
	})

	t.Run("update_defer_clear", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Defer clear test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--defer", "2099-01-15")
		bdUpdate(t, bd, dir, issue.ID, "--defer", "")
		got := bdShow(t, bd, dir, issue.ID)
		if got.DeferUntil != nil {
			t.Error("expected defer_until to be cleared")
		}
		// GH#3233: clearing defer on a deferred issue must restore ready visibility
		if string(got.Status) != "open" {
			t.Errorf("expected status=open after clearing defer, got %q", got.Status)
		}
	})

	t.Run("update_defer_past_date_keeps_status_open", func(t *testing.T) {
		// GH#3233: past-date --defer shouldn't flip status to deferred, because
		// the warning promises the issue "will appear in bd ready immediately".
		issue := bdCreate(t, bd, dir, "Past defer test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--defer", "2000-01-01")
		got := bdShow(t, bd, dir, issue.ID)
		if string(got.Status) == "deferred" {
			t.Errorf("past --defer should not set status=deferred, got %q", got.Status)
		}
	})

	t.Run("update_defer_clear_preserves_non_deferred_status", func(t *testing.T) {
		// GH#3233: clearing defer_until shouldn't clobber a non-deferred status
		// that was set independently (e.g. in_progress).
		issue := bdCreate(t, bd, dir, "Defer clear keep status test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress")
		bdUpdate(t, bd, dir, issue.ID, "--defer", "")
		got := bdShow(t, bd, dir, issue.ID)
		if string(got.Status) != "in_progress" {
			t.Errorf("expected status=in_progress to be preserved, got %q", got.Status)
		}
	})

	t.Run("update_claim_already_claimed", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Claim fail test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice")
		out := bdUpdateFail(t, bd, dir, issue.ID, "--claim")
		if !strings.Contains(out, "already assigned to") {
			t.Errorf("expected 'already assigned to' error, got: %s", out)
		}
		// The refusal must steer toward the holder, not teach an
		// unclaim-then-claim eviction of a live claim (bd-at6rc / wy-zs5s2).
		if !strings.Contains(out, "coordinate with the holder") {
			t.Errorf("refusal should say coordinate-with-holder, got: %s", out)
		}
		if strings.Contains(out, "to release it before re-claiming") {
			t.Errorf("refusal must not suggest plain unclaim of a foreign claim, got: %s", out)
		}
		// Nor may it name unclaim or --force at all: copy that names an
		// eviction command gets pattern-matched by batch agents into
		// `unclaim --force; claim` — a stronger steamroller than the one
		// this fix removed (wy-yuclk).
		if strings.Contains(out, "unclaim") || strings.Contains(out, "--force") {
			t.Errorf("claim refusal must not name an eviction command, got: %s", out)
		}
	})

	// A batch where one claim is lost and another is won must exit non-zero, so
	// the lost claim is not hidden from exit-code automation (beads audit
	// finding #10). The winner is still committed.
	t.Run("update_claim_batch_partial_loss_exits_nonzero", func(t *testing.T) {
		lost := bdCreate(t, bd, dir, "Batch lost", "--type", "task")
		won := bdCreate(t, bd, dir, "Batch won", "--type", "task")
		// Pre-assign `lost` to someone else so the default actor's claim loses.
		bdUpdate(t, bd, dir, lost.ID, "--assignee", "alice")

		out := bdUpdateFail(t, bd, dir, lost.ID, won.ID, "--claim")
		if !strings.Contains(out, "already assigned to") {
			t.Errorf("expected 'already assigned to' error in batch output, got: %s", out)
		}
		// The winning claim still lands despite the batch exiting non-zero.
		gotWon := bdShow(t, bd, dir, won.ID)
		if gotWon.Status != types.StatusInProgress {
			t.Errorf("winning claim %s: status = %s, want in_progress", won.ID, gotWon.Status)
		}
		// The lost issue is untouched.
		gotLost := bdShow(t, bd, dir, lost.ID)
		if gotLost.Assignee != "alice" {
			t.Errorf("lost issue %s assignee = %q, want alice (unchanged)", lost.ID, gotLost.Assignee)
		}
	})

	t.Run("update_parent_change", func(t *testing.T) {
		epic1 := bdCreate(t, bd, dir, "Old parent", "--type", "epic")
		epic2 := bdCreate(t, bd, dir, "New parent", "--type", "epic")
		child := bdCreate(t, bd, dir, "Reparent child", "--type", "task")
		bdUpdate(t, bd, dir, child.ID, "--parent", epic1.ID)
		bdUpdate(t, bd, dir, child.ID, "--parent", epic2.ID)
		deps := showDeps(t, bd, dir, child.ID)
		hasOld, hasNew := false, false
		for _, d := range deps {
			if d.Type == "parent-child" {
				if d.ID == epic1.ID {
					hasOld = true
				}
				if d.ID == epic2.ID {
					hasNew = true
				}
			}
		}
		if hasOld {
			t.Error("expected old parent dep to be removed")
		}
		if !hasNew {
			t.Error("expected new parent dep to exist")
		}
	})

	t.Run("update_persistent", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Persistent test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--ephemeral")
		bdUpdate(t, bd, dir, issue.ID, "--persistent")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Ephemeral {
			t.Error("expected ephemeral to be false after --persistent")
		}
	})

	t.Run("update_dolt_commit", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Dolt commit test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress")

		// Verify a Dolt commit exists by querying dolt_log.
		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		cfg, _ := configfile.Load(beadsDir)
		database := ""
		if cfg != nil {
			database = cfg.GetDoltDatabase()
		}
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var commitCount int
		err = db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&commitCount)
		if err != nil {
			t.Fatalf("query dolt_log: %v", err)
		}
		// At minimum: init schema commit + create commit + update commit
		if commitCount < 3 {
			t.Errorf("expected at least 3 dolt commits, got %d", commitCount)
		}
	})
}

// TestEmbeddedUpdateConcurrent exercises create, update, and list operations
// concurrently to verify EmbeddedDoltStore handles concurrent CLI invocations
// without panics, data corruption, or deadlocks.
func TestEmbeddedUpdateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cu")

	const (
		numWorkers      = 10
		issuesPerWorker = 5
	)

	type workerResult struct {
		worker     int
		ids        []string
		listCounts []int
		err        error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			for i := 0; i < issuesPerWorker; i++ {
				// Create an issue.
				title := fmt.Sprintf("w%d-issue-%d", worker, i)
				out, err := bdRunWithFlockRetry(t, bd, dir, "create", "--silent", title)
				if err != nil {
					r.err = fmt.Errorf("create %d: %v\n%s", i, err, out)
					results[worker] = r
					return
				}
				id := strings.TrimSpace(string(out))
				if id == "" {
					r.err = fmt.Errorf("create %d: empty ID", i)
					results[worker] = r
					return
				}
				r.ids = append(r.ids, id)

				// Update: change status to in_progress.
				uCmd := exec.Command(bd, "update", id, "--status", "in_progress")
				uCmd.Dir = dir
				uCmd.Env = bdEnv(dir)
				uOut, err := uCmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("update status %d: %v\n%s", i, err, uOut)
					results[worker] = r
					return
				}

				// Update: set priority and assignee.
				uCmd2 := exec.Command(bd, "update", id, "--priority", fmt.Sprintf("%d", worker%4), "--assignee", fmt.Sprintf("agent-%d", worker))
				uCmd2.Dir = dir
				uCmd2.Env = bdEnv(dir)
				uOut2, err := uCmd2.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("update fields %d: %v\n%s", i, err, uOut2)
					results[worker] = r
					return
				}

				// Update: add a label.
				uCmd3 := exec.Command(bd, "update", id, "--add-label", fmt.Sprintf("team-%d", worker%3))
				uCmd3.Dir = dir
				uCmd3.Env = bdEnv(dir)
				uOut3, err := uCmd3.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("update label %d: %v\n%s", i, err, uOut3)
					results[worker] = r
					return
				}

				// List to verify consistency (interleaved with writes).
				listCmd := exec.Command(bd, "list", "--json", "--limit", "0")
				listCmd.Dir = dir
				listCmd.Env = bdEnv(dir)
				listStdout, listStderr, err := runCommandBuffers(t, listCmd)
				if err != nil {
					r.err = fmt.Errorf("list after update %d: %v\nstdout:\n%s\nstderr:\n%s", i, err, listStdout.String(), listStderr.String())
					results[worker] = r
					return
				}
				s := listStdout.String()
				start := strings.Index(s, "[")
				if start < 0 {
					r.listCounts = append(r.listCounts, 0)
					continue
				}
				var issues []json.RawMessage
				if jsonErr := json.Unmarshal([]byte(s[start:]), &issues); jsonErr != nil {
					r.err = fmt.Errorf("list parse %d: %v\nstdout:\n%s\nstderr:\n%s", i, jsonErr, s, listStderr.String())
					results[worker] = r
					return
				}
				r.listCounts = append(r.listCounts, len(issues))
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	// Check for errors and collect IDs.
	allIDs := make(map[string]bool)
	var successes int
	for _, r := range results {
		if r.err != nil {
			if !strings.Contains(r.err.Error(), "one writer at a time") {
				t.Errorf("worker %d failed: %v", r.worker, r.err)
			}
			continue
		}
		successes++
		for _, id := range r.ids {
			if allIDs[id] {
				t.Errorf("duplicate ID %q from worker %d", id, r.worker)
			}
			allIDs[id] = true
		}
	}

	if successes == 0 {
		t.Fatal("all workers failed — expected at least 1 success")
	}

	expectedIDs := successes * issuesPerWorker
	if len(allIDs) != expectedIDs {
		t.Errorf("expected %d unique IDs from %d successful workers, got %d", expectedIDs, successes, len(allIDs))
	}

	// Verify all successfully created issues exist and were updated correctly.
	store := openStore(t, beadsDir, "cu")
	stats, err := store.GetStatistics(t.Context())
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if stats.TotalIssues < len(allIDs) {
		t.Errorf("expected at least %d issues in DB, got %d", len(allIDs), stats.TotalIssues)
	}

	// Spot-check: every issue should be in_progress with an assignee.
	for id := range allIDs {
		issue, err := store.GetIssue(t.Context(), id)
		if err != nil {
			t.Errorf("GetIssue(%s): %v", id, err)
			continue
		}
		if issue.Status != types.StatusInProgress {
			t.Errorf("issue %s: expected status in_progress, got %s", id, issue.Status)
		}
		if issue.Assignee == "" {
			t.Errorf("issue %s: expected assignee to be set", id)
		}
	}

	// Verify list counts were monotonically non-decreasing per worker.
	for _, r := range results {
		if r.err != nil {
			continue
		}
		for i := 1; i < len(r.listCounts); i++ {
			if r.listCounts[i] < r.listCounts[i-1] {
				t.Errorf("worker %d: list count decreased from %d to %d at step %d",
					r.worker, r.listCounts[i-1], r.listCounts[i], i)
			}
		}
	}

	t.Logf("created and updated %d issues across %d/%d successful workers, %d in DB",
		len(allIDs), successes, numWorkers, stats.TotalIssues)
}
