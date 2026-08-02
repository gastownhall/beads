//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func bdProxiedLabel(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"label"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd label %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func bdProxiedLabelFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"label"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd label %s to fail, got:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func bdProxiedLabelListJSON(t *testing.T, bd, dir, issueID string) []string {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, "label", "list", issueID, "--json")
	if err != nil {
		t.Fatalf("bd label list %s --json failed: %v\nstdout:\n%s\nstderr:\n%s", issueID, err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("no JSON array in label list output:\n%s", stdout)
	}
	var labels []string
	if err := json.Unmarshal([]byte(stdout[start:]), &labels); err != nil {
		t.Fatalf("parse label list JSON: %v\nraw: %s", err, stdout[start:])
	}
	sort.Strings(labels)
	return labels
}

func TestProxiedServerLabel(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("add_list_remove", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "la")
		issue := bdProxiedCreate(t, bd, p.dir, "Label target")

		out := bdProxiedLabel(t, bd, p.dir, "add", issue.ID, "alpha,beta")
		if !strings.Contains(out, "Added") {
			t.Errorf("expected 'Added' confirmation, got:\n%s", out)
		}

		if got := bdProxiedLabelListJSON(t, bd, p.dir, issue.ID); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
			t.Fatalf("labels after add = %v, want [alpha beta]", got)
		}

		db := openProxiedDB(t, p)
		persisted := getProxiedLabels(t, db, issue.ID)
		sort.Strings(persisted)
		if len(persisted) != 2 || persisted[0] != "alpha" || persisted[1] != "beta" {
			t.Errorf("persisted labels = %v, want [alpha beta]", persisted)
		}

		bdProxiedLabel(t, bd, p.dir, "remove", issue.ID, "alpha")
		if got := bdProxiedLabelListJSON(t, bd, p.dir, issue.ID); len(got) != 1 || got[0] != "beta" {
			t.Fatalf("labels after remove = %v, want [beta]", got)
		}
	})

	t.Run("add_multiple_issues", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "lm")
		a := bdProxiedCreate(t, bd, p.dir, "Issue A")
		b := bdProxiedCreate(t, bd, p.dir, "Issue B")

		bdProxiedLabel(t, bd, p.dir, "add", a.ID, b.ID, "shared")

		if got := bdProxiedLabelListJSON(t, bd, p.dir, a.ID); len(got) != 1 || got[0] != "shared" {
			t.Errorf("issue A labels = %v, want [shared]", got)
		}
		if got := bdProxiedLabelListJSON(t, bd, p.dir, b.ID); len(got) != 1 || got[0] != "shared" {
			t.Errorf("issue B labels = %v, want [shared]", got)
		}
	})

	t.Run("list_empty", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "le")
		issue := bdProxiedCreate(t, bd, p.dir, "No labels")
		out := bdProxiedLabel(t, bd, p.dir, "list", issue.ID)
		if !strings.Contains(out, "no labels") {
			t.Errorf("expected empty-state message, got:\n%s", out)
		}
	})

	t.Run("list_all", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ll")
		a := bdProxiedCreate(t, bd, p.dir, "A")
		b := bdProxiedCreate(t, bd, p.dir, "B")
		bdProxiedLabel(t, bd, p.dir, "add", a.ID, "common")
		bdProxiedLabel(t, bd, p.dir, "add", b.ID, "common")
		bdProxiedLabel(t, bd, p.dir, "add", a.ID, "solo")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "label", "list-all", "--json")
		if err != nil {
			t.Fatalf("label list-all --json failed: %v\nstderr:\n%s", err, stderr)
		}
		start := strings.Index(stdout, "[")
		if start < 0 {
			t.Fatalf("no JSON array in list-all output:\n%s", stdout)
		}
		var infos []struct {
			Label string `json:"label"`
			Count int    `json:"count"`
		}
		if err := json.Unmarshal([]byte(stdout[start:]), &infos); err != nil {
			t.Fatalf("parse list-all JSON: %v\nraw: %s", err, stdout[start:])
		}
		counts := map[string]int{}
		for _, i := range infos {
			counts[i.Label] = i.Count
		}
		if counts["common"] != 2 {
			t.Errorf("common count = %d, want 2", counts["common"])
		}
		if counts["solo"] != 1 {
			t.Errorf("solo count = %d, want 1", counts["solo"])
		}
	})

	t.Run("propagate", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "lp")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent epic", "--type", "epic")
		c1 := bdProxiedCreate(t, bd, p.dir, "Child 1", "--parent", parent.ID)
		c2 := bdProxiedCreate(t, bd, p.dir, "Child 2", "--parent", parent.ID)

		out := bdProxiedLabel(t, bd, p.dir, "propagate", parent.ID, "branch:x")
		if !strings.Contains(out, "Propagated") {
			t.Errorf("expected 'Propagated' output, got:\n%s", out)
		}

		for _, child := range []string{c1.ID, c2.ID} {
			if got := bdProxiedLabelListJSON(t, bd, p.dir, child); len(got) != 1 || got[0] != "branch:x" {
				t.Errorf("child %s labels = %v, want [branch:x]", child, got)
			}
		}
	})

	t.Run("propagate_no_children", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pn")
		lonely := bdProxiedCreate(t, bd, p.dir, "Lonely")
		out := bdProxiedLabel(t, bd, p.dir, "propagate", lonely.ID, "nope")
		if !strings.Contains(out, "No children found") {
			t.Errorf("expected no-children message, got:\n%s", out)
		}
	})

	t.Run("add_json", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "aj")
		issue := bdProxiedCreate(t, bd, p.dir, "Add JSON")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "label", "add", issue.ID, "j1,j2", "--json")
		if err != nil {
			t.Fatalf("label add --json failed: %v\nstderr:\n%s", err, stderr)
		}
		start := strings.Index(stdout, "[")
		if start < 0 {
			t.Fatalf("no JSON array in add output:\n%s", stdout)
		}
		var rows []struct {
			Status  string `json:"status"`
			IssueID string `json:"issue_id"`
			Label   string `json:"label"`
		}
		if err := json.Unmarshal([]byte(stdout[start:]), &rows); err != nil {
			t.Fatalf("parse add JSON: %v\nraw: %s", err, stdout[start:])
		}
		if len(rows) != 2 {
			t.Fatalf("expected 2 result rows, got %d", len(rows))
		}
		for _, r := range rows {
			if r.Status != "added" || r.IssueID != issue.ID {
				t.Errorf("unexpected row: %+v", r)
			}
		}
	})

	t.Run("remove_json", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rj")
		issue := bdProxiedCreate(t, bd, p.dir, "Remove JSON")
		bdProxiedLabel(t, bd, p.dir, "add", issue.ID, "rmj")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "label", "remove", issue.ID, "rmj", "--json")
		if err != nil {
			t.Fatalf("label remove --json failed: %v\nstderr:\n%s", err, stderr)
		}
		start := strings.Index(stdout, "[")
		if start < 0 {
			t.Fatalf("no JSON array in remove output:\n%s", stdout)
		}
		var rows []struct {
			Status string `json:"status"`
			Label  string `json:"label"`
		}
		if err := json.Unmarshal([]byte(stdout[start:]), &rows); err != nil {
			t.Fatalf("parse remove JSON: %v\nraw: %s", err, stdout[start:])
		}
		if len(rows) != 1 || rows[0].Status != "removed" || rows[0].Label != "rmj" {
			t.Fatalf("unexpected remove rows: %+v", rows)
		}
	})

	t.Run("add_duplicate_idempotent", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "di")
		issue := bdProxiedCreate(t, bd, p.dir, "Dup label")
		bdProxiedLabel(t, bd, p.dir, "add", issue.ID, "dup")
		bdProxiedLabel(t, bd, p.dir, "add", issue.ID, "dup")

		if got := bdProxiedLabelListJSON(t, bd, p.dir, issue.ID); len(got) != 1 || got[0] != "dup" {
			t.Fatalf("labels after duplicate add = %v, want [dup]", got)
		}
	})

	t.Run("remove_comma_separated_multi", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rm")
		issue := bdProxiedCreate(t, bd, p.dir, "Multi remove")
		bdProxiedLabel(t, bd, p.dir, "add", issue.ID, "rm-a,rm-b,rm-keep")

		bdProxiedLabel(t, bd, p.dir, "remove", issue.ID, "rm-a,rm-b")
		if got := bdProxiedLabelListJSON(t, bd, p.dir, issue.ID); len(got) != 1 || got[0] != "rm-keep" {
			t.Fatalf("labels after multi remove = %v, want [rm-keep]", got)
		}
	})

	t.Run("remove_batch", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rb")
		a := bdProxiedCreate(t, bd, p.dir, "Batch rm A")
		b := bdProxiedCreate(t, bd, p.dir, "Batch rm B")
		bdProxiedLabel(t, bd, p.dir, "add", a.ID, b.ID, "batch-rm")

		bdProxiedLabel(t, bd, p.dir, "remove", a.ID, b.ID, "batch-rm")
		for _, id := range []string{a.ID, b.ID} {
			if got := bdProxiedLabelListJSON(t, bd, p.dir, id); len(got) != 0 {
				t.Errorf("labels on %s after batch remove = %v, want none", id, got)
			}
		}
	})

	t.Run("list_text", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "lx")
		issue := bdProxiedCreate(t, bd, p.dir, "Text list")
		bdProxiedLabel(t, bd, p.dir, "add", issue.ID, "alpha,beta")

		out := bdProxiedLabel(t, bd, p.dir, "list", issue.ID)
		if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
			t.Errorf("expected both labels in text list, got:\n%s", out)
		}
	})

	t.Run("propagate_json", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pj")
		parent := bdProxiedCreate(t, bd, p.dir, "JSON propagate", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "JSON prop child", "--parent", parent.ID)

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "label", "propagate", parent.ID, "prop-json", "--json")
		if err != nil {
			t.Fatalf("label propagate --json failed: %v\nstderr:\n%s", err, stderr)
		}
		start := strings.Index(stdout, "[")
		if start < 0 {
			t.Fatalf("no JSON array in propagate output:\n%s", stdout)
		}
		var rows []struct {
			Status  string `json:"status"`
			IssueID string `json:"issue_id"`
			Label   string `json:"label"`
		}
		if err := json.Unmarshal([]byte(stdout[start:]), &rows); err != nil {
			t.Fatalf("parse propagate JSON: %v\nraw: %s", err, stdout[start:])
		}
		if len(rows) != 1 || rows[0].Status != "propagated" || rows[0].IssueID != child.ID || rows[0].Label != "prop-json" {
			t.Fatalf("unexpected propagate rows: %+v", rows)
		}
	})

	t.Run("add_empty_label_rejected", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "el")
		issue := bdProxiedCreate(t, bd, p.dir, "Empty label")
		out := bdProxiedLabelFail(t, bd, p.dir, "add", issue.ID, "")
		if !strings.Contains(out, "empty") {
			t.Errorf("expected empty-label error, got:\n%s", out)
		}
	})

	t.Run("provides_label_rejected", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pv")
		issue := bdProxiedCreate(t, bd, p.dir, "Provides guard")
		out := bdProxiedLabelFail(t, bd, p.dir, "add", issue.ID, "provides:foo")
		if !strings.Contains(out, "provides:") {
			t.Errorf("expected provides guard error, got:\n%s", out)
		}
	})

	t.Run("provides_in_comma_list_rejected", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pc")
		issue := bdProxiedCreate(t, bd, p.dir, "Provides in list")
		out := bdProxiedLabelFail(t, bd, p.dir, "add", issue.ID, "ok-label,provides:auth")
		if !strings.Contains(out, "provides:") {
			t.Errorf("expected provides guard error, got:\n%s", out)
		}
		if got := bdProxiedLabelListJSON(t, bd, p.dir, issue.ID); len(got) != 0 {
			t.Errorf("no label should be applied when the batch is rejected, got %v", got)
		}
	})

	t.Run("wisp_label_routes_to_wisp_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "lw")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp target", "--ephemeral")

		bdProxiedLabel(t, bd, p.dir, "add", wisp.ID, "wlabel")

		if got := bdProxiedLabelListJSON(t, bd, p.dir, wisp.ID); len(got) != 1 || got[0] != "wlabel" {
			t.Fatalf("wisp labels via list = %v, want [wlabel]", got)
		}

		db := openProxiedDB(t, p)
		var wispCount, permCount int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", wisp.ID).Scan(&wispCount); err != nil {
			t.Fatalf("count wisp_labels: %v", err)
		}
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM labels WHERE issue_id = ?", wisp.ID).Scan(&permCount); err != nil {
			t.Fatalf("count labels: %v", err)
		}
		if wispCount != 1 {
			t.Errorf("wisp_labels count = %d, want 1", wispCount)
		}
		if permCount != 0 {
			t.Errorf("labels (permanent) count = %d, want 0 — wisp label must not leak into the permanent table", permCount)
		}
	})

	t.Run("wisp_label_remove_routes_to_wisp_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "wr")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp remove target", "--ephemeral")

		bdProxiedLabel(t, bd, p.dir, "add", wisp.ID, "keep,drop")
		bdProxiedLabel(t, bd, p.dir, "remove", wisp.ID, "drop")

		if got := bdProxiedLabelListJSON(t, bd, p.dir, wisp.ID); len(got) != 1 || got[0] != "keep" {
			t.Fatalf("wisp labels after remove = %v, want [keep]", got)
		}

		db := openProxiedDB(t, p)
		var wispCount int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", wisp.ID).Scan(&wispCount); err != nil {
			t.Fatalf("count wisp_labels: %v", err)
		}
		if wispCount != 1 {
			t.Errorf("wisp_labels count after remove = %d, want 1", wispCount)
		}
	})

	t.Run("list_all_includes_wisp_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "law")
		perm := bdProxiedCreate(t, bd, p.dir, "Perm issue")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp issue", "--ephemeral")
		bdProxiedLabel(t, bd, p.dir, "add", perm.ID, "shared")
		bdProxiedLabel(t, bd, p.dir, "add", wisp.ID, "shared")
		bdProxiedLabel(t, bd, p.dir, "add", wisp.ID, "wisp-only")

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "label", "list-all", "--json")
		if err != nil {
			t.Fatalf("label list-all --json failed: %v\nstderr:\n%s", err, stderr)
		}
		start := strings.Index(stdout, "[")
		if start < 0 {
			t.Fatalf("no JSON array in list-all output:\n%s", stdout)
		}
		var infos []struct {
			Label string `json:"label"`
			Count int    `json:"count"`
		}
		if err := json.Unmarshal([]byte(stdout[start:]), &infos); err != nil {
			t.Fatalf("parse list-all JSON: %v\nraw: %s", err, stdout[start:])
		}
		counts := map[string]int{}
		for _, i := range infos {
			counts[i.Label] = i.Count
		}
		if counts["shared"] != 2 {
			t.Errorf("shared count = %d, want 2 (one perm + one wisp)", counts["shared"])
		}
		if counts["wisp-only"] != 1 {
			t.Errorf("wisp-only count = %d, want 1", counts["wisp-only"])
		}
	})

	t.Run("propagate_to_wisp_children", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pw")
		parent := bdProxiedCreate(t, bd, p.dir, "Wisp parent", "--ephemeral", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Wisp child", "--ephemeral", "--parent", parent.ID)

		out := bdProxiedLabel(t, bd, p.dir, "propagate", parent.ID, "branch:w")
		if !strings.Contains(out, "Propagated") {
			t.Errorf("expected 'Propagated' output, got:\n%s", out)
		}

		if got := bdProxiedLabelListJSON(t, bd, p.dir, child.ID); len(got) != 1 || got[0] != "branch:w" {
			t.Fatalf("wisp child labels = %v, want [branch:w]", got)
		}

		db := openProxiedDB(t, p)
		var wispCount, permCount int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", child.ID).Scan(&wispCount); err != nil {
			t.Fatalf("count wisp_labels: %v", err)
		}
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM labels WHERE issue_id = ?", child.ID).Scan(&permCount); err != nil {
			t.Fatalf("count labels: %v", err)
		}
		if wispCount != 1 {
			t.Errorf("wisp_labels count = %d, want 1", wispCount)
		}
		if permCount != 0 {
			t.Errorf("labels (permanent) count = %d, want 0 — propagated wisp label must not leak into the permanent table", permCount)
		}
	})
}
