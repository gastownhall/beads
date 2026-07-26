//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestProxiedServerDuplicates(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "dup")

	// Two issues with identical content form an exact-duplicate group.
	a := bdProxiedCreate(t, bd, p.dir, "Fix login redirect", "--type", "bug", "--priority", "1", "--description", "Users bounce to /home after login")
	b := bdProxiedCreate(t, bd, p.dir, "Fix login redirect", "--type", "bug", "--priority", "1", "--description", "Users bounce to /home after login")
	// A distinct issue that must not be grouped.
	bdProxiedCreate(t, bd, p.dir, "Unrelated task", "--type", "task", "--priority", "2")

	dupsJSON := func(t *testing.T, args ...string) map[string]interface{} {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"duplicates", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("duplicates --json %v: %v\n%s", args, err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return m
	}

	t.Run("finds_exact_group", func(t *testing.T) {
		var got struct {
			DuplicateGroups int `json:"duplicate_groups"`
			Groups          []struct {
				SuggestedTarget  string   `json:"suggested_target"`
				SuggestedSources []string `json:"suggested_sources"`
				Issues           []struct {
					ID string `json:"id"`
				} `json:"issues"`
			} `json:"groups"`
		}
		out, err := bdProxiedRun(t, bd, p.dir, "duplicates", "--json")
		if err != nil {
			t.Fatalf("duplicates --json: %v\n%s", err, out)
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.DuplicateGroups != 1 {
			t.Fatalf("duplicate_groups = %d, want 1", got.DuplicateGroups)
		}
		ids := map[string]bool{}
		for _, iss := range got.Groups[0].Issues {
			ids[iss.ID] = true
		}
		if !ids[a.ID] || !ids[b.ID] {
			t.Errorf("group should contain %s and %s, got %v", a.ID, b.ID, ids)
		}
		tgt := got.Groups[0].SuggestedTarget
		if tgt != a.ID && tgt != b.ID {
			t.Errorf("suggested_target %s not one of the duplicates", tgt)
		}
	})

	t.Run("json_output_structure", func(t *testing.T) {
		m := dupsJSON(t)
		if _, ok := m["duplicate_groups"]; !ok {
			t.Error("expected 'duplicate_groups' key")
		}
		if _, ok := m["groups"]; !ok {
			t.Error("expected 'groups' key")
		}
	})

	t.Run("human_readable", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "duplicates")
		if err != nil {
			t.Fatalf("duplicates: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "duplicate") && !strings.Contains(s, "Duplicate") && !strings.Contains(s, "No duplicates") {
			t.Errorf("expected duplicate info in output: %s", s)
		}
	})

	t.Run("no_duplicates_all_unique", func(t *testing.T) {
		// A fresh project with only distinct issues yields zero groups.
		p2 := newSharedProxiedProject(t, bd, "duq")
		bdProxiedCreate(t, bd, p2.dir, "Unique 1", "--type", "task", "--description", "A")
		bdProxiedCreate(t, bd, p2.dir, "Unique 2", "--type", "task", "--description", "B")
		out, err := bdProxiedRun(t, bd, p2.dir, "duplicates", "--json")
		if err != nil {
			t.Fatalf("duplicates --json: %v\n%s", err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if groups := int(m["duplicate_groups"].(float64)); groups != 0 {
			t.Errorf("expected 0 duplicate groups, got %d", groups)
		}
	})

	t.Run("excludes_closed", func(t *testing.T) {
		// When one of an exact-duplicate pair is closed, only the open one
		// remains and no group is reported (proxied groups open issues only).
		p3 := newSharedProxiedProject(t, bd, "duc")
		x := bdProxiedCreate(t, bd, p3.dir, "Closed dup", "--type", "task", "--description", "Same closed")
		bdProxiedCreate(t, bd, p3.dir, "Closed dup", "--type", "task", "--description", "Same closed")
		if out, err := bdProxiedRun(t, bd, p3.dir, "close", x.ID); err != nil {
			t.Fatalf("close: %v\n%s", err, out)
		}
		out, err := bdProxiedRun(t, bd, p3.dir, "duplicates", "--json")
		if err != nil {
			t.Fatalf("duplicates --json: %v\n%s", err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if groups := int(m["duplicate_groups"].(float64)); groups != 0 {
			t.Errorf("expected 0 groups after closing one duplicate, got %d", groups)
		}
	})

	t.Run("dry_run_lists_commands", func(t *testing.T) {
		// Plain --dry-run (no --auto-merge) is read-only and emits merge_commands.
		m := dupsJSON(t, "--dry-run")
		if _, ok := m["merge_commands"]; !ok {
			t.Errorf("expected merge_commands with --dry-run, got %v", m)
		}
		if _, ok := m["merge_results"]; ok {
			t.Errorf("dry run must not have merge_results, got %v", m)
		}
	})

	t.Run("auto_merge_rejected", func(t *testing.T) {
		// Proxied mode refuses the write path of --auto-merge (embedded's auto_merge
		// and auto_merge_reparents scenarios are intentionally not portable here).
		out, err := bdProxiedRun(t, bd, p.dir, "duplicates", "--auto-merge")
		if err == nil {
			t.Fatalf("expected --auto-merge to be rejected, got success: %s", out)
		}
		if !strings.Contains(string(out), "not supported in proxied-server mode") {
			t.Errorf("unexpected error: %s", out)
		}
	})

	t.Run("auto_merge_dry_run_allowed", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "duplicates", "--auto-merge", "--dry-run", "--json")
		if err != nil {
			t.Fatalf("--auto-merge --dry-run should be read-only allowed: %v\n%s", err, out)
		}
		var got map[string]interface{}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if _, ok := got["merge_commands"]; !ok {
			t.Errorf("expected merge_commands in dry-run output, got %v", got)
		}
		if _, ok := got["merge_results"]; ok {
			t.Errorf("dry-run must not perform merges (no merge_results), got %v", got)
		}
	})
}

func TestProxiedServerFindDuplicates(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "fdp")

	// Near-identical login issues (should be flagged as duplicates).
	bdProxiedCreate(t, bd, p.dir, "Fix login page timeout error", "--type", "bug",
		"--description", "The login page throws a timeout error after 30 seconds of inactivity")
	bdProxiedCreate(t, bd, p.dir, "Login page timeout error fix needed", "--type", "bug",
		"--description", "After 30 seconds the login page shows a timeout error to the user")
	// Distinct issues (should NOT be flagged).
	bdProxiedCreate(t, bd, p.dir, "Add dark mode to settings", "--type", "feature",
		"--description", "Users want a dark mode toggle in the settings page")
	bdProxiedCreate(t, bd, p.dir, "Upgrade database to PostgreSQL 16", "--type", "task",
		"--description", "Migrate from PostgreSQL 14 to PostgreSQL 16 for performance")
	bdProxiedCreate(t, bd, p.dir, "Write API documentation", "--type", "task",
		"--description", "Document all REST endpoints with OpenAPI spec")
	// A closed near-duplicate for status-filter testing.
	closed := bdProxiedCreate(t, bd, p.dir, "Fix login page timeout error (old)", "--type", "bug",
		"--description", "The login page throws a timeout error")
	if out, err := bdProxiedRun(t, bd, p.dir, "close", closed.ID); err != nil {
		t.Fatalf("close: %v\n%s", err, out)
	}

	fdJSON := func(t *testing.T, args ...string) map[string]interface{} {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"find-duplicates", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("find-duplicates --json %v: %v\n%s", args, err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		return m
	}

	t.Run("mechanical_finds_near_identical", func(t *testing.T) {
		m := fdJSON(t, "--threshold", "0.15")
		if m["method"] != "mechanical" {
			t.Errorf("method = %v, want mechanical", m["method"])
		}
		pairs, ok := m["pairs"].([]interface{})
		if !ok {
			t.Fatalf("expected pairs array, got %T", m["pairs"])
		}
		if len(pairs) == 0 {
			t.Fatal("expected at least 1 duplicate pair for near-identical issues")
		}
		var found bool
		for _, pr := range pairs {
			pm := pr.(map[string]interface{})
			ta := strings.ToLower(pm["issue_a_title"].(string))
			tb := strings.ToLower(pm["issue_b_title"].(string))
			if strings.Contains(ta, "login") && strings.Contains(tb, "login") {
				found = true
			}
		}
		if !found {
			t.Error("expected login timeout issues to be flagged as duplicates")
		}
	})

	t.Run("threshold_lower_finds_at_least_as_many", func(t *testing.T) {
		high := fdJSON(t, "--threshold", "0.95")["pairs"].([]interface{})
		low := fdJSON(t, "--threshold", "0.2")["pairs"].([]interface{})
		if len(low) < len(high) {
			t.Errorf("lower threshold should find >= as many pairs: low=%d high=%d", len(low), len(high))
		}
	})

	t.Run("threshold_zero_finds_pairs", func(t *testing.T) {
		pairs := fdJSON(t, "--threshold", "0.0")["pairs"].([]interface{})
		if len(pairs) == 0 {
			t.Error("expected at least some pairs at threshold 0")
		}
	})

	t.Run("status_filter_open_excludes_closed", func(t *testing.T) {
		pairs := fdJSON(t, "--status", "open", "--threshold", "0.2")["pairs"].([]interface{})
		for _, pr := range pairs {
			pm := pr.(map[string]interface{})
			if strings.Contains(pm["issue_a_title"].(string), "(old)") || strings.Contains(pm["issue_b_title"].(string), "(old)") {
				t.Error("closed issue should be excluded with --status open")
			}
		}
	})

	t.Run("status_filter_all", func(t *testing.T) {
		pairs := fdJSON(t, "--status", "all", "--threshold", "0.3")["pairs"].([]interface{})
		if len(pairs) == 0 {
			t.Error("expected at least 1 pair with --status all")
		}
	})

	t.Run("limit_caps_results", func(t *testing.T) {
		pairs := fdJSON(t, "--threshold", "0.0", "--limit", "1")["pairs"].([]interface{})
		if len(pairs) > 1 {
			t.Errorf("expected at most 1 pair with --limit 1, got %d", len(pairs))
		}
	})

	t.Run("no_duplicates_high_threshold", func(t *testing.T) {
		// A very high threshold should simply produce no pairs without crashing.
		if _, err := bdProxiedRun(t, bd, p.dir, "find-duplicates", "--threshold", "0.99"); err != nil {
			t.Fatalf("find-duplicates --threshold 0.99: %v", err)
		}
	})

	t.Run("json_output_structure", func(t *testing.T) {
		m := fdJSON(t, "--threshold", "0.3")
		for _, key := range []string{"pairs", "count", "method", "threshold"} {
			if _, ok := m[key]; !ok {
				t.Errorf("expected %q key in JSON output", key)
			}
		}
		if m["method"] != "mechanical" {
			t.Errorf("expected method='mechanical', got %v", m["method"])
		}
	})

	t.Run("json_pair_fields", func(t *testing.T) {
		pairs := fdJSON(t, "--threshold", "0.15")["pairs"].([]interface{})
		if len(pairs) == 0 {
			t.Skip("no pairs to check fields on")
		}
		pm := pairs[0].(map[string]interface{})
		for _, key := range []string{"issue_a_id", "issue_b_id", "issue_a_title", "issue_b_title", "similarity", "method"} {
			if _, ok := pm[key]; !ok {
				t.Errorf("expected %q key in pair JSON", key)
			}
		}
	})

	t.Run("invalid_method_error", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "find-duplicates", "--method", "bogus")
		if err == nil {
			t.Fatalf("expected invalid method error, got success: %s", out)
		}
		if !strings.Contains(string(out), "invalid method") {
			t.Errorf("expected 'invalid method' error, got: %s", out)
		}
	})

	t.Run("ai_method_missing_key_error", func(t *testing.T) {
		// The missing-key guard runs before any AI call. Skip when a key is
		// present in the environment so we never actually reach the API.
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			t.Skip("ANTHROPIC_API_KEY set; skipping missing-key assertion to avoid a live AI call")
		}
		out, err := bdProxiedRun(t, bd, p.dir, "find-duplicates", "--method", "ai")
		if err == nil {
			t.Fatalf("expected missing-key error for --method ai, got success: %s", out)
		}
		if !strings.Contains(string(out), "ANTHROPIC_API_KEY") {
			t.Errorf("expected ANTHROPIC_API_KEY hint in error, got: %s", out)
		}
	})

	t.Run("find_dups_alias", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "find-dups", "--json", "--threshold", "0.3")
		if err != nil {
			t.Fatalf("find-dups alias: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "pairs") {
			t.Errorf("expected JSON with 'pairs' from alias: %s", out)
		}
	})

	t.Run("human_readable_output", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "find-duplicates", "--threshold", "0.15")
		if err != nil {
			t.Fatalf("find-duplicates: %v\n%s", err, out)
		}
		s := string(out)
		if strings.Contains(s, "No similar issues") {
			t.Skip("no pairs found at this threshold")
		}
		if !strings.Contains(s, "similar") {
			t.Errorf("expected 'similar' in human-readable output: %s", s)
		}
		if !strings.Contains(s, "Pair") {
			t.Errorf("expected 'Pair' in human-readable output: %s", s)
		}
	})

	t.Run("fewer_than_2_issues", func(t *testing.T) {
		p2 := newSharedProxiedProject(t, bd, "fd1")
		bdProxiedCreate(t, bd, p2.dir, "Only one issue", "--type", "task")
		out, err := bdProxiedRun(t, bd, p2.dir, "find-duplicates")
		if err != nil {
			t.Fatalf("find-duplicates: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Not enough issues") {
			t.Errorf("expected 'Not enough issues' message, got: %s", out)
		}
	})

	t.Run("fewer_than_2_issues_json", func(t *testing.T) {
		p3 := newSharedProxiedProject(t, bd, "fd2")
		bdProxiedCreate(t, bd, p3.dir, "Only one issue json", "--type", "task")
		out, err := bdProxiedRun(t, bd, p3.dir, "find-duplicates", "--json")
		if err != nil {
			t.Fatalf("find-duplicates --json: %v\n%s", err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if count := int(m["count"].(float64)); count != 0 {
			t.Errorf("expected count=0 with fewer than 2 issues, got %d", count)
		}
	})
}
