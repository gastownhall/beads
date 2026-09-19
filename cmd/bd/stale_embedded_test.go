//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// bdStale runs "bd stale" with the given args and returns raw stdout.
func bdStale(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"stale"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd stale %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdStaleFail runs "bd stale" expecting failure.
func bdStaleFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"stale"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd stale %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdStaleJSON runs "bd stale --json" and parses the result as a slice.
func bdStaleJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"stale", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd stale --json %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array in stale output: %s", s)
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &entries); err != nil {
		t.Fatalf("parse stale JSON: %v\n%s", err, s)
	}
	return entries
}

// makeIssuesStale updates updated_at to 60 days ago via raw SQL.
func makeIssuesStale(t *testing.T, beadsDir, database string, issueIDs []string) {
	t.Helper()
	ctx := t.Context()
	dataDir := beadsDir + "/embeddeddolt"
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()

	for _, id := range issueIDs {
		_, err := db.ExecContext(ctx,
			"UPDATE issues SET updated_at = DATE_SUB(NOW(), INTERVAL 60 DAY) WHERE id = ?", id)
		if err != nil {
			t.Fatalf("update updated_at for %s: %v", id, err)
		}
	}
	// Commit the changes
	_, err = db.ExecContext(ctx, "CALL DOLT_ADD('-A')")
	if err != nil {
		t.Fatalf("dolt add: %v", err)
	}
	_, err = db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'make issues stale for testing')")
	if err != nil {
		t.Fatalf("dolt commit: %v", err)
	}
}

func TestEmbeddedStale(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "st")

	// Create issues: some will be made stale, some will stay fresh.
	stale1 := bdCreate(t, bd, dir, "Stale open issue", "--type", "task")
	stale2 := bdCreate(t, bd, dir, "Stale in_progress issue", "--type", "task")
	bdUpdate(t, bd, dir, stale2.ID, "--status", "in_progress")
	stale3 := bdCreate(t, bd, dir, "Stale bug", "--type", "bug", "--assignee", "alice")
	fresh1 := bdCreate(t, bd, dir, "Fresh issue", "--type", "task")
	closedIssue := bdCreate(t, bd, dir, "Closed issue", "--type", "task")
	bdClose(t, bd, dir, closedIssue.ID)

	// Make specific issues stale (60 days old).
	makeIssuesStale(t, beadsDir, "st", []string{stale1.ID, stale2.ID, stale3.ID})

	// ===== Basic stale detection =====

	t.Run("basic_stale_default_days", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir)
		if len(entries) < 3 {
			t.Errorf("expected at least 3 stale issues, got %d", len(entries))
		}
		// Fresh issue should not appear
		for _, e := range entries {
			if e["id"] == fresh1.ID {
				t.Errorf("fresh issue %s should not be stale", fresh1.ID)
			}
		}
	})

	// ===== Custom --days =====

	t.Run("custom_days", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--days", "90")
		// Issues are 60 days stale, so --days 90 should not find them
		if len(entries) != 0 {
			t.Errorf("expected 0 stale issues at 90 days, got %d", len(entries))
		}
	})

	t.Run("custom_days_lower", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--days", "1")
		// --days 1 should find all stale issues plus potentially fresh ones
		// At minimum the 3 stale ones
		if len(entries) < 3 {
			t.Errorf("expected at least 3 stale issues at 1 day, got %d", len(entries))
		}
	})

	// ===== --status filter =====

	t.Run("status_filter_in_progress", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--status", "in_progress")
		found := false
		for _, e := range entries {
			if e["id"] == stale2.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected stale in_progress issue %s in results", stale2.ID)
		}
	})

	t.Run("status_filter_open", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--status", "open")
		for _, e := range entries {
			if e["id"] == stale2.ID {
				t.Errorf("in_progress issue %s should not appear with --status open", stale2.ID)
			}
		}
	})

	// ===== --limit =====

	t.Run("limit_caps_results", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--limit", "1")
		if len(entries) > 1 {
			t.Errorf("expected at most 1 result with --limit 1, got %d", len(entries))
		}
	})

	// ===== --json output =====

	t.Run("json_output_is_array", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir)
		// Already parsed as array — just verify non-nil
		if entries == nil {
			t.Error("expected non-nil JSON array")
		}
	})

	t.Run("json_issue_has_fields", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir)
		if len(entries) == 0 {
			t.Skip("no stale issues to check")
		}
		e := entries[0]
		for _, key := range []string{"id", "title", "status"} {
			if _, ok := e[key]; !ok {
				t.Errorf("expected '%s' key in stale issue JSON", key)
			}
		}
	})

	// ===== No stale issues =====

	t.Run("no_stale_issues", func(t *testing.T) {
		out := bdStale(t, bd, dir, "--days", "90")
		if !strings.Contains(out, "No stale issues") {
			t.Errorf("expected 'No stale issues' message: %s", out)
		}
	})

	// ===== Error handling =====

	t.Run("invalid_days_zero", func(t *testing.T) {
		out := bdStaleFail(t, bd, dir, "--days", "0")
		if !strings.Contains(out, "at least 1") {
			t.Errorf("expected 'at least 1' error: %s", out)
		}
	})

	t.Run("invalid_status", func(t *testing.T) {
		out := bdStaleFail(t, bd, dir, "--status", "bogus")
		if !strings.Contains(out, "invalid status") {
			t.Errorf("expected 'invalid status' error: %s", out)
		}
	})

	// ===== Boundary test =====

	t.Run("boundary_exact_cutoff", func(t *testing.T) {
		// Issues made 60 days stale should show at --days 60 but not --days 61
		entries60 := bdStaleJSON(t, bd, dir, "--days", "60")
		entries61 := bdStaleJSON(t, bd, dir, "--days", "61")
		if len(entries60) < len(entries61) {
			t.Errorf("--days 60 should find >= issues than --days 61: got %d vs %d", len(entries60), len(entries61))
		}
	})

	// ===== Human-readable output =====

	t.Run("human_readable_format", func(t *testing.T) {
		out := bdStale(t, bd, dir)
		if !strings.Contains(out, "Stale issues") {
			t.Errorf("expected 'Stale issues' header: %s", out)
		}
		if !strings.Contains(out, "days ago") {
			t.Errorf("expected 'days ago' in output: %s", out)
		}
	})

	// ===== Closed issues excluded =====

	t.Run("closed_issues_excluded", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir)
		for _, e := range entries {
			if e["id"] == closedIssue.ID {
				t.Errorf("closed issue %s should not appear in stale results", closedIssue.ID)
			}
		}
	})
}

// TestEmbeddedStaleConcurrent exercises stale operations concurrently.
func TestEmbeddedStaleConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "slc")

	var ids []string
	for i := 0; i < 10; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-stale-%d", i), "--type", "task")
		ids = append(ids, issue.ID)
	}
	makeIssuesStale(t, beadsDir, "slc", ids[:5])

	const numWorkers = 8

	type workerResult struct {
		worker int
		err    error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			queries := [][]string{
				{"--json"},
				{"--json", "--days", "1"},
				{"--json", "--status", "open"},
				{"--json", "--limit", "3"},
				{"--json"},
				{"--json", "--days", "7"},
				{"--json", "--status", "open"},
				{"--json"},
			}
			q := queries[worker%len(queries)]

			args := append([]string{"stale"}, q...)
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d stale: %v\n%s", worker, err, out)
				results[worker] = r
				return
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}

// idSet reduces bd's JSON rows to the set of issue IDs they name, so a filter
// assertion can talk about membership rather than ordering or row counts.
func idSet(entries []map[string]interface{}) map[string]bool {
	ids := make(map[string]bool, len(entries))
	for _, e := range entries {
		if id, ok := e["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

// TestEmbeddedStaleLabelFilters covers --label, --label-any and
// --exclude-label on bd stale. Before these flags a theme-scoped stale sweep
// had to pull every stale issue and post-filter the JSON, which also silently
// broke --limit: the limit applied to the unfiltered query, so a caller asking
// for N issues in one theme could get none.
func TestEmbeddedStaleLabelFilters(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sl")

	alpha := bdCreate(t, bd, dir, "Stale alpha", "--type", "task", "--labels", "theme:alpha")
	beta := bdCreate(t, bd, dir, "Stale beta", "--type", "task", "--labels", "theme:beta")
	alphaUrgent := bdCreate(t, bd, dir, "Stale alpha urgent", "--type", "task", "--labels", "theme:alpha,urgent")
	bare := bdCreate(t, bd, dir, "Stale unlabelled", "--type", "task")

	makeIssuesStale(t, beadsDir, "sl", []string{alpha.ID, beta.ID, alphaUrgent.ID, bare.ID})

	t.Run("label_scopes_to_one_theme", func(t *testing.T) {
		ids := idSet(bdStaleJSON(t, bd, dir, "--label", "theme:alpha"))
		if !ids[alpha.ID] || !ids[alphaUrgent.ID] {
			t.Errorf("expected both theme:alpha issues, got %v", ids)
		}
		if ids[beta.ID] || ids[bare.ID] {
			t.Errorf("theme:alpha filter leaked other issues: %v", ids)
		}
	})

	t.Run("repeated_label_is_AND", func(t *testing.T) {
		ids := idSet(bdStaleJSON(t, bd, dir, "--label", "theme:alpha", "--label", "urgent"))
		if !ids[alphaUrgent.ID] {
			t.Errorf("expected %s carrying both labels, got %v", alphaUrgent.ID, ids)
		}
		if ids[alpha.ID] {
			t.Errorf("%s carries only theme:alpha and must not match an AND of both: %v", alpha.ID, ids)
		}
	})

	t.Run("label_any_is_OR", func(t *testing.T) {
		ids := idSet(bdStaleJSON(t, bd, dir, "--label-any", "theme:alpha,theme:beta"))
		if !ids[alpha.ID] || !ids[beta.ID] || !ids[alphaUrgent.ID] {
			t.Errorf("expected all three themed issues, got %v", ids)
		}
		if ids[bare.ID] {
			t.Errorf("unlabelled issue %s must not match --label-any: %v", bare.ID, ids)
		}
	})

	t.Run("exclude_label_drops_matches", func(t *testing.T) {
		ids := idSet(bdStaleJSON(t, bd, dir, "--exclude-label", "theme:alpha"))
		if ids[alpha.ID] || ids[alphaUrgent.ID] {
			t.Errorf("--exclude-label theme:alpha still returned alpha issues: %v", ids)
		}
		if !ids[beta.ID] || !ids[bare.ID] {
			t.Errorf("expected beta and unlabelled issues to survive exclusion, got %v", ids)
		}
	})

	t.Run("no_label_flags_returns_everything", func(t *testing.T) {
		ids := idSet(bdStaleJSON(t, bd, dir))
		for _, id := range []string{alpha.ID, beta.ID, alphaUrgent.ID, bare.ID} {
			if !ids[id] {
				t.Errorf("expected %s in unfiltered stale output, got %v", id, ids)
			}
		}
	})

	t.Run("limit_applies_after_the_label_filter", func(t *testing.T) {
		// The regression this pins: if labels were applied to the hydrated
		// results instead of in SQL, LIMIT 1 would take the stalest issue
		// overall and then drop it for not matching, returning nothing.
		entries := bdStaleJSON(t, bd, dir, "--label", "theme:alpha", "--limit", "1")
		if len(entries) != 1 {
			t.Fatalf("expected exactly 1 result for --label theme:alpha --limit 1, got %d: %v", len(entries), entries)
		}
		ids := idSet(entries)
		if !ids[alpha.ID] && !ids[alphaUrgent.ID] {
			t.Errorf("the single result should be a theme:alpha issue, got %v", ids)
		}
	})

	// These clauses match a label EXACTLY, so an untrimmed flag value returns
	// nothing and looks exactly like "no stale issues carry that label". Every
	// sibling filter (list, search, ready, orphans) normalizes its input; these
	// pin that --label means the same thing here.
	t.Run("leading_space_is_trimmed_like_every_other_label_filter", func(t *testing.T) {
		// The everyday form: pflag's CSV split leaves the space on the second
		// value, so `--label 'theme:beta, theme:alpha'` arrives as
		// {"theme:beta", " theme:alpha"}.
		ids := idSet(bdStaleJSON(t, bd, dir, "--label", " theme:alpha"))
		if !ids[alpha.ID] || !ids[alphaUrgent.ID] {
			t.Errorf("expected ' theme:alpha' to match theme:alpha, got %v", ids)
		}
		if ids[beta.ID] || ids[bare.ID] {
			t.Errorf("' theme:alpha' matched issues outside the theme: %v", ids)
		}
	})

	t.Run("empty_element_does_not_annihilate_the_filter", func(t *testing.T) {
		// A doubled comma used to AND in a `label = ''` clause, which no row
		// can satisfy, turning a valid filter into a silent empty result.
		ids := idSet(bdStaleJSON(t, bd, dir, "--label", "theme:alpha,,urgent"))
		if !ids[alphaUrgent.ID] {
			t.Errorf("expected %s to survive an empty label element, got %v", alphaUrgent.ID, ids)
		}
	})

	t.Run("normalization_applies_to_label_any_and_exclude_label", func(t *testing.T) {
		anyIDs := idSet(bdStaleJSON(t, bd, dir, "--label-any", " theme:beta"))
		if !anyIDs[beta.ID] {
			t.Errorf("expected ' theme:beta' to match via --label-any, got %v", anyIDs)
		}
		excludeIDs := idSet(bdStaleJSON(t, bd, dir, "--exclude-label", " theme:alpha"))
		if excludeIDs[alpha.ID] || excludeIDs[alphaUrgent.ID] {
			t.Errorf("' theme:alpha' failed to exclude the alpha issues: %v", excludeIDs)
		}
		if !excludeIDs[beta.ID] {
			t.Errorf("exclusion dropped issues it should have kept: %v", excludeIDs)
		}
	})
}
