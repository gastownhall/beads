//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxiedServerShow(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("show_single_issue", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ssi")
		issue := bdProxiedCreate(t, bd, p.dir, "Show me", "--type", "task")

		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID)
		if !strings.Contains(out, "Show me") {
			t.Errorf("expected title in output: %s", out)
		}
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID in output: %s", out)
		}
	})

	t.Run("show_multiple_issues", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "smi")
		issue1 := bdProxiedCreate(t, bd, p.dir, "Multi 1", "--type", "task")
		issue2 := bdProxiedCreate(t, bd, p.dir, "Multi 2", "--type", "task")

		out := bdProxiedShowRaw(t, bd, p.dir, issue1.ID, issue2.ID)
		if !strings.Contains(out, "Multi 1") || !strings.Contains(out, "Multi 2") {
			t.Errorf("expected both titles: %s", out)
		}
		if !strings.Contains(out, issue1.ID) || !strings.Contains(out, issue2.ID) {
			t.Errorf("expected both IDs: %s", out)
		}
	})

	t.Run("show_nonexistent_id_exits_nonzero", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sne")
		stdout, stderr := bdProxiedShowFail(t, bd, p.dir, "sne-nonexistent999")
		_ = stdout
		_ = stderr
	})

	t.Run("show_no_args_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sna")
		stdout, stderr := bdProxiedShowFail(t, bd, p.dir)
		combined := stdout + stderr
		if !strings.Contains(combined, "at least one issue ID") {
			t.Errorf("expected 'at least one issue ID' error, got: %s", combined)
		}
	})

	t.Run("show_json_fields_round_trip", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sjf")
		issue := bdProxiedCreate(t, bd, p.dir, "JSON show", "--type", "task",
			"--description", "A description", "-p", "1")

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, issue.ID)
		if m["id"] != issue.ID {
			t.Errorf("id: got %v, want %v", m["id"], issue.ID)
		}
		if m["title"] != "JSON show" {
			t.Errorf("title: got %v", m["title"])
		}
		if m["description"] != "A description" {
			t.Errorf("description: got %v", m["description"])
		}
		if m["issue_type"] != "task" {
			t.Errorf("issue_type: got %v, want task", m["issue_type"])
		}
		if m["priority"] != float64(1) {
			t.Errorf("priority: got %v, want 1", m["priority"])
		}
		if _, ok := m["created_at"]; !ok {
			t.Errorf("missing created_at")
		}
	})

	t.Run("show_json_includes_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sjl")
		issue := bdProxiedCreate(t, bd, p.dir, "Labeled show", "--type", "task", "-l", "bug")

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, issue.ID)
		labels, ok := m["labels"].([]interface{})
		if !ok {
			t.Fatalf("expected labels array, got %T (%v)", m["labels"], m["labels"])
		}
		found := false
		for _, l := range labels {
			if l == "bug" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'bug' in labels: %v", labels)
		}
	})

	t.Run("show_json_includes_dependencies", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sjd")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker", "--type", "task")
		blocked := bdProxiedCreate(t, bd, p.dir, "Blocked",
			"--type", "task", "--deps", "blocked-by:"+blocker.ID)

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, blocked.ID)
		deps, _ := m["dependencies"].([]interface{})
		if len(deps) == 0 {
			t.Fatalf("expected dependencies in JSON output: %v", m)
		}
		first, _ := deps[0].(map[string]interface{})
		if first["id"] != blocker.ID {
			t.Errorf("dep target id: got %v, want %v", first["id"], blocker.ID)
		}
		if first["dependency_type"] != "blocks" {
			t.Errorf("dep type: got %v, want blocks", first["dependency_type"])
		}
	})

	t.Run("show_json_count_fields_default", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sjc")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker", "--type", "task")
		blocked := bdProxiedCreate(t, bd, p.dir, "Blocked",
			"--type", "task", "--deps", "blocked-by:"+blocker.ID)

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, blocked.ID)
		if _, ok := m["dependency_count"]; !ok {
			t.Errorf("expected dependency_count present in default JSON")
		}
		if _, ok := m["dependent_count"]; !ok {
			t.Errorf("expected dependent_count present in default JSON")
		}
		if _, ok := m["comment_count"]; !ok {
			t.Errorf("expected comment_count present in default JSON")
		}
		if _, ok := m["dependents"]; ok {
			t.Errorf("dependents slice should be absent by default (count-only)")
		}
		if _, ok := m["comments"]; ok {
			t.Errorf("comments slice should be absent by default (count-only)")
		}
		if m["dependency_count"] != float64(1) {
			t.Errorf("dependency_count: got %v, want 1", m["dependency_count"])
		}
	})

	t.Run("show_json_includes_comments_under_flag", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sjic")
		issue := bdProxiedCreate(t, bd, p.dir, "Commented", "--type", "task")

		db := openProxiedDB(t, p)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (?, ?, ?, ?, NOW())",
			fmt.Sprintf("cmt-%d", time.Now().UnixNano()), issue.ID, "tester", "Hello"); err != nil {
			t.Fatalf("insert comment: %v", err)
		}

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, issue.ID, "--include-comments")
		comments, _ := m["comments"].([]interface{})
		if len(comments) == 0 {
			t.Errorf("expected comments with --include-comments: %v", m)
		}
	})

	t.Run("show_json_includes_dependents_under_flag", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sjidep")
		hub := bdProxiedCreate(t, bd, p.dir, "Hub", "--type", "task")
		bdProxiedCreate(t, bd, p.dir, "Spoke 1", "--type", "task", "--deps", "blocked-by:"+hub.ID)
		bdProxiedCreate(t, bd, p.dir, "Spoke 2", "--type", "task", "--deps", "blocked-by:"+hub.ID)

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, hub.ID, "--include-dependents")
		deps, _ := m["dependents"].([]interface{})
		if len(deps) != 2 {
			t.Errorf("expected 2 dependents, got %d: %v", len(deps), m["dependents"])
		}
		if first, ok := deps[0].(map[string]interface{}); ok {
			if first["id"] == nil || first["title"] == nil {
				t.Errorf("dependent shallow row missing id/title: %v", first)
			}
		}
	})

	t.Run("show_json_epic_progress", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sjep")
		epic := bdProxiedCreate(t, bd, p.dir, "Epic progress", "--type", "epic")
		child1 := bdProxiedCreate(t, bd, p.dir, "Epic child 1", "--type", "task", "--parent", epic.ID)
		bdProxiedCreate(t, bd, p.dir, "Epic child 2", "--type", "task", "--parent", epic.ID)
		bdProxiedUpdateOne(t, bd, p.dir, child1.ID, "--status", "closed")

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, epic.ID, "--include-dependents")
		if total, ok := m["epic_total_children"]; ok {
			if total != float64(2) {
				t.Errorf("epic_total_children: got %v, want 2", total)
			}
		} else {
			t.Errorf("missing epic_total_children: %v", m)
		}
		if closed, ok := m["epic_closed_children"]; ok {
			if closed != float64(1) {
				t.Errorf("epic_closed_children: got %v, want 1", closed)
			}
		} else {
			t.Errorf("missing epic_closed_children: %v", m)
		}
	})

	t.Run("show_json_parent_derived", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sjp")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Child", "--type", "task", "--parent", parent.ID)

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, child.ID)
		if got, _ := m["parent"].(string); got != parent.ID {
			t.Errorf("parent field: got %v, want %v", m["parent"], parent.ID)
		}
	})

	t.Run("show_short", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ssh")
		issue := bdProxiedCreate(t, bd, p.dir, "Short show", "--type", "task")
		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--short")
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) > 3 {
			t.Errorf("expected compact output, got %d lines:\n%s", len(lines), out)
		}
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID in short output: %s", out)
		}
	})

	t.Run("show_long", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "slong")
		issue := bdProxiedCreate(t, bd, p.dir, "Long show", "--type", "task",
			"--description", "Desc", "--assignee", "alice")
		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--long")
		if !strings.Contains(out, "Long show") {
			t.Errorf("expected title in long output: %s", out)
		}
	})

	t.Run("show_id_flag", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sid")
		issue := bdProxiedCreate(t, bd, p.dir, "ID flag test", "--type", "task")

		out := bdProxiedShowRaw(t, bd, p.dir, "--id", issue.ID, "--short")
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID via --id flag: %s", out)
		}

		out2 := bdProxiedShowRaw(t, bd, p.dir, "--id="+issue.ID, "--id="+issue.ID, "--short")
		if strings.Count(out2, issue.ID) < 2 {
			t.Errorf("expected ID twice via duplicate --id: %s", out2)
		}

		out3 := bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--id="+issue.ID, "--short")
		if strings.Count(out3, issue.ID) < 2 {
			t.Errorf("expected ID twice via mixed positional+--id: %s", out3)
		}
	})

	t.Run("show_local_time_no_error", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "slt")
		issue := bdProxiedCreate(t, bd, p.dir, "Local time", "--type", "task")
		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--local-time")
		if !strings.Contains(out, "Local time") {
			t.Errorf("expected title in output: %s", out)
		}
	})

}

func TestProxiedServerShow2(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("show_external_ref_rendered", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sxr")
		issue := bdProxiedCreate(t, bd, p.dir, "External ref test",
			"--type", "task", "--external-ref", "https://example.com/spec.md")
		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID)
		if !strings.Contains(out, "External:") {
			t.Errorf("expected 'External:' line in output, got: %s", out)
		}
		if !strings.Contains(out, "https://example.com/spec.md") {
			t.Errorf("expected external ref URL, got: %s", out)
		}
	})

	t.Run("show_no_external_ref_omits_line", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "snx")
		issue := bdProxiedCreate(t, bd, p.dir, "No ref test", "--type", "task")
		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID)
		if strings.Contains(out, "External:") {
			t.Errorf("expected no 'External:' line, got: %s", out)
		}
	})

	t.Run("show_not_found_json_envelope", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "snj")
		stdout, _ := bdProxiedShowFail(t, bd, p.dir, "snj-bogus", "--json")
		if stdout == "" {
			t.Fatal("expected JSON error on stdout, got empty")
		}
		var envelope map[string]interface{}
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatalf("expected valid JSON envelope on stdout, got: %v\n%s", err, stdout)
		}
		if errField, _ := envelope["error"].(string); errField == "" {
			t.Errorf("expected non-empty 'error' field, got: %s", stdout)
		}
	})

	t.Run("view_alias_dispatches_to_proxied", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sva")
		issue := bdProxiedCreate(t, bd, p.dir, "View alias", "--type", "task")
		out, err := bdProxiedRun(t, bd, p.dir, "view", issue.ID, "--short")
		if err != nil {
			t.Fatalf("bd view failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), issue.ID) {
			t.Errorf("expected ID via view alias: %s", out)
		}
	})

	t.Run("show_concurrent_json_and_short", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "scn")
		const (
			numWorkers      = 8
			issuesPerWorker = 3
		)
		ids := make([]string, 0, numWorkers*issuesPerWorker)
		for i := 0; i < numWorkers*issuesPerWorker; i++ {
			issue := bdProxiedCreate(t, bd, p.dir,
				fmt.Sprintf("concurrent-show-%d", i), "--type", "task")
			ids = append(ids, issue.ID)
		}

		type result struct {
			worker int
			err    error
		}
		results := make([]result, numWorkers)
		var wg sync.WaitGroup
		wg.Add(numWorkers)
		for w := 0; w < numWorkers; w++ {
			go func(worker int) {
				defer wg.Done()
				r := result{worker: worker}
				for i := 0; i < issuesPerWorker; i++ {
					id := ids[worker*issuesPerWorker+i]
					if _, err := bdProxiedRun(t, bd, p.dir, "show", id, "--json"); err != nil {
						r.err = fmt.Errorf("show --json %s: %w", id, err)
						results[worker] = r
						return
					}
					if _, err := bdProxiedRun(t, bd, p.dir, "show", id, "--short"); err != nil {
						r.err = fmt.Errorf("show --short %s: %w", id, err)
						results[worker] = r
						return
					}
				}
				results[worker] = r
			}(w)
		}
		wg.Wait()
		for _, r := range results {
			if r.err != nil {
				t.Errorf("worker %d failed: %v", r.worker, r.err)
			}
		}
	})

	t.Run("show_wisp_id_routes_to_wisp_uc", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swr")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp default", "--type", "task", "--ephemeral")

		out := bdProxiedShowRaw(t, bd, p.dir, wisp.ID)
		if !strings.Contains(out, "Wisp default") {
			t.Errorf("expected wisp title in output: %s", out)
		}

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, wisp.ID)
		if _, ok := m["comment_count"]; !ok {
			t.Errorf("expected comment_count for wisp")
		}
		if _, ok := m["dependent_count"]; !ok {
			t.Errorf("expected dependent_count for wisp")
		}
	})

	t.Run("show_refs_lists_referrers", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "srl")
		parent := bdProxiedCreate(t, bd, p.dir, "Refs parent", "--type", "task")
		child := bdProxiedCreate(t, bd, p.dir, "Refs child",
			"--type", "task", "--deps", "blocked-by:"+parent.ID)

		out := bdProxiedShowRaw(t, bd, p.dir, parent.ID, "--refs")
		if !strings.Contains(out, child.ID) {
			t.Errorf("expected --refs output to contain referrer %s: %s", child.ID, out)
		}
	})

	t.Run("show_refs_groups_by_type", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "srg")
		hub := bdProxiedCreate(t, bd, p.dir, "Refs hub", "--type", "task")
		bdProxiedCreate(t, bd, p.dir, "Blocker A",
			"--type", "task", "--deps", "blocked-by:"+hub.ID)
		bdProxiedCreate(t, bd, p.dir, "Related A",
			"--type", "task", "--deps", "related:"+hub.ID)

		out := bdProxiedShowRaw(t, bd, p.dir, hub.ID, "--refs")
		if !strings.Contains(out, "blocks") {
			t.Errorf("expected 'blocks' group in --refs output: %s", out)
		}
		if !strings.Contains(out, "related") {
			t.Errorf("expected 'related' group in --refs output: %s", out)
		}
	})

	t.Run("show_refs_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sre")
		issue := bdProxiedCreate(t, bd, p.dir, "No refs", "--type", "task")
		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--refs")
		if !strings.Contains(out, "No references found") {
			t.Errorf("expected 'No references found' in --refs output: %s", out)
		}
	})

	t.Run("show_refs_json_map_shape", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "srj")
		parent := bdProxiedCreate(t, bd, p.dir, "Refs parent J", "--type", "task")
		child := bdProxiedCreate(t, bd, p.dir, "Refs child J",
			"--type", "task", "--deps", "blocked-by:"+parent.ID)

		out, err := bdProxiedRun(t, bd, p.dir, "show", parent.ID, "--refs", "--json")
		if err != nil {
			t.Fatalf("bd show --refs --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON object found: %s", s)
		}
		var envelope map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &envelope); err != nil {
			t.Fatalf("parse refs JSON: %v\n%s", err, s[start:])
		}
		entry, ok := envelope[parent.ID]
		if !ok {
			t.Fatalf("expected entry for %s in refs JSON: %v", parent.ID, envelope)
		}
		refs, _ := entry.([]interface{})
		if len(refs) == 0 {
			t.Fatalf("expected non-empty refs slice for %s: %v", parent.ID, entry)
		}
		first, _ := refs[0].(map[string]interface{})
		if first["id"] != child.ID {
			t.Errorf("expected child %s in refs JSON, got: %v", child.ID, first)
		}
	})

	t.Run("show_wisp_refs", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swrefs")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp ref target", "--type", "task", "--ephemeral")
		referrer := bdProxiedCreate(t, bd, p.dir, "Wisp referrer",
			"--type", "task", "--ephemeral", "--deps", "blocked-by:"+wisp.ID)

		out := bdProxiedShowRaw(t, bd, p.dir, wisp.ID, "--refs")
		if !strings.Contains(out, referrer.ID) {
			t.Errorf("expected wisp referrer %s in --refs output: %s", referrer.ID, out)
		}
	})

	t.Run("show_children", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sc")
		parent := bdProxiedCreate(t, bd, p.dir, "Children parent", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Children child",
			"--type", "task", "--parent", parent.ID)

		out := bdProxiedShowRaw(t, bd, p.dir, parent.ID, "--children")
		if !strings.Contains(out, child.ID) {
			t.Errorf("expected child %s in --children output: %s", child.ID, out)
		}
	})

	t.Run("show_children_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sce")
		issue := bdProxiedCreate(t, bd, p.dir, "No children", "--type", "task")
		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--children")
		if !strings.Contains(out, "No children found") {
			t.Errorf("expected 'No children found' in output: %s", out)
		}
	})

	t.Run("show_children_short", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "scs")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent S", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Child S",
			"--type", "task", "--parent", parent.ID)

		out := bdProxiedShowRaw(t, bd, p.dir, parent.ID, "--children", "--short")
		if !strings.Contains(out, child.ID) {
			t.Errorf("expected child %s in --children --short output: %s", child.ID, out)
		}
	})

	t.Run("show_children_json_map_shape", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "scj")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent J", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Child J",
			"--type", "task", "--parent", parent.ID)

		out, err := bdProxiedRun(t, bd, p.dir, "show", parent.ID, "--children", "--json")
		if err != nil {
			t.Fatalf("show --children --json: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON object: %s", s)
		}
		var envelope map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &envelope); err != nil {
			t.Fatalf("parse children JSON: %v\n%s", err, s[start:])
		}
		entry, ok := envelope[parent.ID]
		if !ok {
			t.Fatalf("expected entry for %s in children JSON: %v", parent.ID, envelope)
		}
		kids, _ := entry.([]interface{})
		if len(kids) == 0 {
			t.Fatalf("expected children slice for %s: %v", parent.ID, entry)
		}
		first, _ := kids[0].(map[string]interface{})
		if first["id"] != child.ID {
			t.Errorf("expected child %s in children JSON: %v", child.ID, first)
		}
		if first["dependency_type"] != "parent-child" {
			t.Errorf("expected dependency_type=parent-child, got %v", first["dependency_type"])
		}
	})

	t.Run("show_wisp_children", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swc")
		epicW := bdProxiedCreate(t, bd, p.dir, "Wisp epic", "--type", "epic", "--ephemeral")
		childW := bdProxiedCreate(t, bd, p.dir, "Wisp child",
			"--type", "task", "--ephemeral", "--parent", epicW.ID)

		out := bdProxiedShowRaw(t, bd, p.dir, epicW.ID, "--children")
		if !strings.Contains(out, childW.ID) {
			t.Errorf("expected wisp child %s in --children: %s", childW.ID, out)
		}
	})

}

func TestProxiedServerShow3(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("show_as_of_historical_title", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sao")
		issue := bdProxiedCreate(t, bd, p.dir, "AsOf original", "--type", "task")

		hash := proxiedCurrentCommit(t, p)

		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--title", "AsOf updated")

		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--as-of", hash)
		if !strings.Contains(out, "AsOf original") {
			t.Errorf("expected original title at old commit: %s", out)
		}
		if strings.Contains(out, "AsOf updated") {
			t.Errorf("should not see updated title at old commit: %s", out)
		}
	})

	t.Run("show_as_of_invalid_ref_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "saoi")
		issue := bdProxiedCreate(t, bd, p.dir, "AsOf invalid", "--type", "task")

		_, stderr, _ := bdProxiedRunBuffers(t, bd, p.dir, "show", issue.ID, "--as-of", "'; DROP TABLE issues; --")
		if !strings.Contains(stderr, "invalid ref") {
			t.Errorf("expected 'invalid ref' in stderr, got: %s", stderr)
		}
	})

	t.Run("show_as_of_short_mode", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "saos")
		issue := bdProxiedCreate(t, bd, p.dir, "AsOf short", "--type", "task")
		hash := proxiedCurrentCommit(t, p)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--title", "AsOf short updated")

		out := bdProxiedShowRaw(t, bd, p.dir, issue.ID, "--as-of", hash, "--short")
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID in --as-of --short output: %s", out)
		}
		if !strings.Contains(out, "AsOf short") {
			t.Errorf("expected historical title in short output: %s", out)
		}
	})

	t.Run("show_as_of_json_array", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "saoj")
		issue := bdProxiedCreate(t, bd, p.dir, "AsOf JSON", "--type", "task")
		hash := proxiedCurrentCommit(t, p)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--title", "AsOf JSON updated")

		arr := bdProxiedShowDetailsAll(t, bd, p.dir, issue.ID, "--as-of", hash)
		if len(arr) == 0 {
			t.Fatalf("expected non-empty array from --as-of --json")
		}
		if arr[0]["title"] != "AsOf JSON" {
			t.Errorf("expected historical title in JSON, got %v", arr[0]["title"])
		}
	})

	t.Run("show_thread_walks_to_root", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sthr")
		root := bdProxiedCreate(t, bd, p.dir, "Root msg", "--type", "task")
		mid := bdProxiedCreate(t, bd, p.dir, "Mid reply",
			"--type", "task", "--deps", "replies-to:"+root.ID)
		leaf := bdProxiedCreate(t, bd, p.dir, "Leaf reply",
			"--type", "task", "--deps", "replies-to:"+mid.ID)

		out := bdProxiedShowRaw(t, bd, p.dir, leaf.ID, "--thread")
		for _, want := range []string{root.ID, mid.ID, leaf.ID, "Total: 3"} {
			if !strings.Contains(out, want) {
				t.Errorf("expected %q in --thread output:\n%s", want, out)
			}
		}
	})

	t.Run("show_thread_collects_replies", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "stbr")
		root := bdProxiedCreate(t, bd, p.dir, "Branch root", "--type", "task")
		a := bdProxiedCreate(t, bd, p.dir, "Reply A",
			"--type", "task", "--deps", "replies-to:"+root.ID)
		b := bdProxiedCreate(t, bd, p.dir, "Reply B",
			"--type", "task", "--deps", "replies-to:"+root.ID)
		aReply := bdProxiedCreate(t, bd, p.dir, "Reply A1",
			"--type", "task", "--deps", "replies-to:"+a.ID)

		out := bdProxiedShowRaw(t, bd, p.dir, root.ID, "--thread")
		for _, want := range []string{root.ID, a.ID, b.ID, aReply.ID, "Total: 4"} {
			if !strings.Contains(out, want) {
				t.Errorf("expected %q in thread output:\n%s", want, out)
			}
		}
	})

	t.Run("show_thread_json_emits_array", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "stj")
		root := bdProxiedCreate(t, bd, p.dir, "Thread JSON root", "--type", "task")
		reply := bdProxiedCreate(t, bd, p.dir, "Thread JSON reply",
			"--type", "task", "--deps", "replies-to:"+root.ID)

		out, err := bdProxiedRun(t, bd, p.dir, "show", root.ID, "--thread", "--json")
		if err != nil {
			t.Fatalf("--thread --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array: %s", s)
		}
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &arr); err != nil {
			t.Fatalf("parse thread JSON: %v\n%s", err, s[start:])
		}
		if len(arr) != 2 {
			t.Errorf("expected 2 entries in thread JSON, got %d: %v", len(arr), arr)
		}
		if arr[0]["id"] != root.ID || arr[1]["id"] != reply.ID {
			t.Errorf("thread order: got [%v, %v], want [%v, %v]",
				arr[0]["id"], arr[1]["id"], root.ID, reply.ID)
		}
	})

	t.Run("show_thread_orphan_message_renders_alone", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sto")
		orphan := bdProxiedCreate(t, bd, p.dir, "Orphan msg", "--type", "task")
		out := bdProxiedShowRaw(t, bd, p.dir, orphan.ID, "--thread")
		if !strings.Contains(out, orphan.ID) {
			t.Errorf("expected orphan ID in thread output: %s", out)
		}
		if !strings.Contains(out, "Total: 1") {
			t.Errorf("expected 'Total: 1' for orphan thread: %s", out)
		}
	})

	t.Run("show_current_with_in_progress", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "scur")
		issue := bdProxiedCreate(t, bd, p.dir, "In progress", "--type", "task")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID,
			"--status", "in_progress", "--assignee", "alice", "--actor", "alice")

		out, err := bdProxiedRun(t, bd, p.dir, "--actor", "alice", "show", "--current")
		if err != nil {
			t.Fatalf("--current failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), issue.ID) {
			t.Errorf("expected --current to resolve to %s: %s", issue.ID, out)
		}
	})

	t.Run("show_current_prefers_in_progress_over_hooked", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "scp")
		hooked := bdProxiedCreate(t, bd, p.dir, "Hooked one", "--type", "task")
		inProg := bdProxiedCreate(t, bd, p.dir, "InProg one", "--type", "task")
		bdProxiedUpdateOne(t, bd, p.dir, hooked.ID,
			"--status", "hooked", "--assignee", "alice", "--actor", "alice")
		bdProxiedUpdateOne(t, bd, p.dir, inProg.ID,
			"--status", "in_progress", "--assignee", "alice", "--actor", "alice")

		out, err := bdProxiedRun(t, bd, p.dir, "--actor", "alice", "show", "--current")
		if err != nil {
			t.Fatalf("--current failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), inProg.ID) {
			t.Errorf("expected in-progress %s to win over hooked: %s", inProg.ID, out)
		}
		if strings.Contains(string(out), hooked.ID) {
			t.Errorf("hooked should not appear when in-progress exists: %s", out)
		}
	})

	t.Run("show_current_with_id_fails", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "scid")
		issue := bdProxiedCreate(t, bd, p.dir, "Conflict", "--type", "task")
		stdout, stderr := bdProxiedShowFail(t, bd, p.dir, "--current", issue.ID)
		combined := stdout + stderr
		if !strings.Contains(combined, "--current cannot be combined") {
			t.Errorf("expected conflict error, got: %s", combined)
		}
	})

	t.Run("show_current_no_match_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "scnm")
		stdout, stderr := bdProxiedShowFail(t, bd, p.dir, "--current")
		combined := stdout + stderr
		if !strings.Contains(combined, "no current issue found") {
			t.Errorf("expected 'no current issue found' error, got: %s", combined)
		}
	})

	t.Run("show_watch_rejected_in_proxied_mode", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swt")
		issue := bdProxiedCreate(t, bd, p.dir, "Watch test", "--type", "task")
		stdout, stderr := bdProxiedShowFail(t, bd, p.dir, issue.ID, "--watch")
		combined := stdout + stderr
		if !strings.Contains(combined, "watch mode not supported in proxied-server mode") {
			t.Errorf("expected proxied watch-rejection error, got: %s", combined)
		}
	})

	t.Run("show_wisp_comments_default_count_only", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swcc")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp w/comments", "--type", "task", "--ephemeral")

		db := openProxiedDB(t, p)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for i := 0; i < 3; i++ {
			if _, err := db.ExecContext(ctx,
				"INSERT INTO wisp_comments (id, issue_id, author, text, created_at) VALUES (?, ?, ?, ?, NOW())",
				fmt.Sprintf("wcmt-%d-%d", time.Now().UnixNano(), i), wisp.ID, "tester",
				fmt.Sprintf("wisp comment %d", i)); err != nil {
				t.Fatalf("insert wisp_comment: %v", err)
			}
		}

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, wisp.ID)
		if got, _ := m["comment_count"].(float64); got != 3 {
			t.Errorf("comment_count: got %v, want 3", m["comment_count"])
		}
		if _, ok := m["comments"]; ok {
			t.Errorf("comments slice should be absent by default")
		}
	})

	t.Run("show_wisp_comments_include_streams", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "swci")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp stream", "--type", "task", "--ephemeral")

		db := openProxiedDB(t, p)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO wisp_comments (id, issue_id, author, text, created_at) VALUES (?, ?, ?, ?, NOW())",
			fmt.Sprintf("wcmt-stream-%d", time.Now().UnixNano()), wisp.ID, "tester", "stream me"); err != nil {
			t.Fatalf("insert wisp_comment: %v", err)
		}

		m := bdProxiedShowDetailsFirst(t, bd, p.dir, wisp.ID, "--include-comments")
		comments, _ := m["comments"].([]interface{})
		if len(comments) == 0 {
			t.Fatalf("expected wisp comments with --include-comments: %v", m)
		}
	})
}

func proxiedCurrentCommit(t *testing.T, p proxiedProject) string {
	t.Helper()
	db := openProxiedDB(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hash string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&hash); err != nil {
		t.Fatalf("read HASHOF('HEAD'): %v", err)
	}
	return hash
}
