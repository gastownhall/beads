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
)

// bdLabel runs "bd label" with the given args and returns stdout.
func bdLabel(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"label"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd label %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func bdLabelJSONOutput(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"label"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd label %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdLabelEditJSON runs "bd label <op> ... --json" and returns its result rows.
func bdLabelEditJSON(t *testing.T, bd, dir, op string, args ...string) []map[string]interface{} {
	t.Helper()
	s := strings.TrimSpace(bdLabelJSONOutput(t, bd, dir, append(append([]string{op}, args...), "--json")...))
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array in label %s output: %s", op, s)
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &rows); err != nil {
		t.Fatalf("parse label %s JSON: %v\nstdout: %s", op, err, s)
	}
	return rows
}

// bdLabelFail runs "bd label" expecting failure.
func bdLabelFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"label"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd label %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdLabelListJSON runs "bd label list --json" and returns parsed labels.
func bdLabelListJSON(t *testing.T, bd, dir, issueID string) []string {
	t.Helper()
	s := strings.TrimSpace(bdLabelJSONOutput(t, bd, dir, "list", issueID, "--json"))
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var labels []string
	if err := json.Unmarshal([]byte(s[start:]), &labels); err != nil {
		t.Fatalf("parse label list JSON: %v\nstdout: %s", err, s)
	}
	return labels
}

// bdLabelListAllJSON runs "bd label list-all --json" and returns parsed results.
func bdLabelListAllJSON(t *testing.T, bd, dir string) []map[string]interface{} {
	t.Helper()
	s := strings.TrimSpace(bdLabelJSONOutput(t, bd, dir, "list-all", "--json"))
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var results []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &results); err != nil {
		t.Fatalf("parse label list-all JSON: %v\nstdout: %s", err, s)
	}
	return results
}

// TestEmbeddedLabelAddRemove was split from TestEmbeddedLabel (originally
// ~360s, measured under --config=embedded) into 3 top-level tests over
// disjoint subtest groups, for CI shard balance (see
// scripts/ci/embedded_cmd_test_durations.json and engdocs/TESTING.md). Every original
// subtest is preserved exactly once.
func TestEmbeddedLabelAddRemove(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tl")

	// ===== Label Add =====

	t.Run("label_add_single", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Label add test", "--type", "task")
		out := bdLabel(t, bd, dir, "add", issue.ID, "urgent")
		if !strings.Contains(out, "Added") {
			t.Errorf("expected 'Added' in output: %s", out)
		}
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		found := false
		for _, l := range labels {
			if l == "urgent" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'urgent' in labels: %v", labels)
		}
	})

	t.Run("label_add_batch", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Batch label 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Batch label 2", "--type", "task")
		bdLabel(t, bd, dir, "add", issue1.ID, issue2.ID, "batch-label")

		for _, id := range []string{issue1.ID, issue2.ID} {
			labels := bdLabelListJSON(t, bd, dir, id)
			found := false
			for _, l := range labels {
				if l == "batch-label" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected 'batch-label' on %s: %v", id, labels)
			}
		}
	})

	t.Run("label_add_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Label JSON add", "--type", "task")
		s := strings.TrimSpace(bdLabelJSONOutput(t, bd, dir, "add", issue.ID, "json-label", "--json"))
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array in output: %s", s)
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON: %s", s)
		}
	})

	t.Run("label_add_comma_separated_multi", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Multi label add", "--type", "task")
		out := bdLabel(t, bd, dir, "add", issue.ID, "multi-a,multi-b,multi-c")
		if !strings.Contains(out, "Added") {
			t.Errorf("expected 'Added' in output: %s", out)
		}
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		labelSet := map[string]bool{}
		for _, l := range labels {
			labelSet[l] = true
		}
		for _, want := range []string{"multi-a", "multi-b", "multi-c"} {
			if !labelSet[want] {
				t.Errorf("expected %q in labels: %v", want, labels)
			}
		}
	})

	t.Run("label_remove_comma_separated_multi", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Multi label remove", "--type", "task",
			"--label", "rm-a", "--label", "rm-b", "--label", "rm-keep")
		bdLabel(t, bd, dir, "remove", issue.ID, "rm-a,rm-b")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		for _, l := range labels {
			if l == "rm-a" || l == "rm-b" {
				t.Errorf("label %q should have been removed: %v", l, labels)
			}
		}
		found := false
		for _, l := range labels {
			if l == "rm-keep" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'rm-keep' to survive: %v", labels)
		}
	})

	t.Run("label_add_duplicate_idempotent", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Dup label", "--type", "task")
		bdLabel(t, bd, dir, "add", issue.ID, "dup")
		bdLabel(t, bd, dir, "add", issue.ID, "dup")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		count := 0
		for _, l := range labels {
			if l == "dup" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected exactly 1 'dup' label, got %d in %v", count, labels)
		}
	})

	// ===== Label Remove =====

	t.Run("label_remove", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Remove label", "--type", "task", "--label", "removeme")
		bdLabel(t, bd, dir, "remove", issue.ID, "removeme")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		for _, l := range labels {
			if l == "removeme" {
				t.Error("label should have been removed")
			}
		}
	})

	t.Run("label_remove_batch", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Batch rm 1", "--type", "task", "--label", "batch-rm")
		issue2 := bdCreate(t, bd, dir, "Batch rm 2", "--type", "task", "--label", "batch-rm")
		bdLabel(t, bd, dir, "remove", issue1.ID, issue2.ID, "batch-rm")
		for _, id := range []string{issue1.ID, issue2.ID} {
			labels := bdLabelListJSON(t, bd, dir, id)
			for _, l := range labels {
				if l == "batch-rm" {
					t.Errorf("label should have been removed from %s", id)
				}
			}
		}
	})

	t.Run("label_remove_prefix", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Prefix remove", "--type", "task",
			"--label", "pool:refused:reason-a", "--label", "pool:refused:reason-b", "--label", "needs-human")
		bdLabel(t, bd, dir, "remove", issue.ID, "--prefix", "pool:refused:")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		for _, l := range labels {
			if strings.HasPrefix(l, "pool:refused:") {
				t.Errorf("label %q should have been removed by prefix: %v", l, labels)
			}
		}
		found := false
		for _, l := range labels {
			if l == "needs-human" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'needs-human' to survive: %v", labels)
		}
	})

	t.Run("label_remove_prefix_no_match_is_noop", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Prefix remove no match", "--type", "task", "--label", "keep-me")
		bdLabel(t, bd, dir, "remove", issue.ID, "--prefix", "does-not-exist:")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		if len(labels) != 1 || labels[0] != "keep-me" {
			t.Errorf("expected only 'keep-me' to survive a no-match prefix removal: %v", labels)
		}
	})

	// --prefix takes no trailing label argument (every positional is an issue
	// ID), so a caller who reflexively keeps the plain-remove habit of ending
	// with a label — `bd label remove <id> stale-label --prefix x` — must get
	// an explicit, unambiguous rejection rather than a confusing "could not
	// resolve issue ID" error that then recommends the very label-argument
	// syntax --prefix does not accept.
	t.Run("label_remove_prefix_rejects_label_shaped_positional", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Prefix remove mixed args", "--type", "task",
			"--label", "pool:refused:reason-a")
		out := bdLabelFail(t, bd, dir, "remove", issue.ID, "definitely-not-an-issue-id", "--prefix", "pool:refused:")
		if !strings.Contains(out, "cannot combine --prefix with label arguments") {
			t.Errorf("expected the --prefix + label-argument rejection message, got: %s", out)
		}
		// Resolution must fail before any mutation — the valid issue's labels
		// must be untouched by the rejected call, not partially applied.
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		found := false
		for _, l := range labels {
			if l == "pool:refused:reason-a" {
				found = true
			}
		}
		if !found {
			t.Errorf("rejected --prefix call must not remove labels from the valid issue id first: %v", labels)
		}
	})

	// A LONE positional that does not resolve is just a bad issue ID, so it
	// must NOT be blamed on the label-argument habit the subtest above covers:
	// the speculative hint is gated on there being more than one positional,
	// the same way resolveLabelIssueIDs gates its own.
	t.Run("label_remove_prefix_single_bad_id_is_not_blamed_on_arguments", func(t *testing.T) {
		out := bdLabelFail(t, bd, dir, "remove", "bd-definitely-absent", "--prefix", "pool:refused:")
		if strings.Contains(out, "cannot combine --prefix with label arguments") {
			t.Errorf("a lone unresolvable id must not be reported as an argument mistake: %s", out)
		}
		if !strings.Contains(out, `resolving issue ID "bd-definitely-absent"`) {
			t.Errorf("expected the plain resolution error, got: %s", out)
		}
	})

	// The prefix path's OUTPUT surfaces, which is where the GH#5988
	// false-confirmation class lives: it reports removals it did not make if
	// the report is driven from the pre-read snapshot instead of from the
	// UpdateResult. Asserting the stored labels alone cannot see that.
	t.Run("label_remove_prefix_reports_json_rows", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Prefix JSON rows", "--type", "task",
			"--label", "pool:refused:reason-a", "--label", "pool:refused:reason-b", "--label", "needs-human")
		rows := bdLabelEditJSON(t, bd, dir, "remove", issue.ID, "--prefix", "pool:refused:")
		if len(rows) != 2 {
			t.Fatalf("prefix remove JSON = %v, want one row per matching label", rows)
		}
		got := map[interface{}]interface{}{}
		for _, r := range rows {
			if r["issue_id"] != issue.ID {
				t.Errorf("row %v has issue_id %v, want %s", r, r["issue_id"], issue.ID)
			}
			got[r["label"]] = r["status"]
		}
		if got["pool:refused:reason-a"] != "removed" || got["pool:refused:reason-b"] != "removed" {
			t.Errorf("prefix remove JSON = %v, want both matching labels status=removed", rows)
		}
	})

	t.Run("label_remove_prefix_no_match_reports_empty_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Prefix JSON no match", "--type", "task", "--label", "keep-me")
		rows := bdLabelEditJSON(t, bd, dir, "remove", issue.ID, "--prefix", "does-not-exist:")
		if len(rows) != 0 {
			t.Errorf("no-match prefix remove JSON = %v, want an empty array", rows)
		}
	})
}

// TestEmbeddedLabelEditReports was split from TestEmbeddedLabel (originally
// ~360s, measured under --config=embedded) into 3 top-level tests over
// disjoint subtest groups, for CI shard balance (see
// scripts/ci/embedded_cmd_test_durations.json and engdocs/TESTING.md). Every original
// subtest is preserved exactly once.
func TestEmbeddedLabelEditReports(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tl")

	// Two issues carrying DIFFERENT matching sets: the report is per issue and
	// each issue's set is resolved from its own labels, so a refactor that
	// shared one label set across the ids — or reused one issue's outcome for
	// the other — would show up here and nowhere else.
	t.Run("label_remove_prefix_two_issues", func(t *testing.T) {
		both := bdCreate(t, bd, dir, "Prefix two-issue both", "--type", "task",
			"--label", "pool:refused:reason-a", "--label", "pool:refused:reason-b")
		one := bdCreate(t, bd, dir, "Prefix two-issue one", "--type", "task",
			"--label", "pool:refused:reason-c", "--label", "needs-human")
		rows := bdLabelEditJSON(t, bd, dir, "remove", both.ID, one.ID, "--prefix", "pool:refused:")
		byIssue := map[interface{}][]string{}
		for _, r := range rows {
			if r["status"] != "removed" {
				t.Errorf("row %v: want status=removed", r)
			}
			byIssue[r["issue_id"]] = append(byIssue[r["issue_id"]], r["label"].(string))
		}
		if len(byIssue[both.ID]) != 2 || len(byIssue[one.ID]) != 1 {
			t.Errorf("prefix remove over two issues = %v, want 2 rows for %s and 1 for %s", rows, both.ID, one.ID)
		}
		if labels := bdLabelListJSON(t, bd, dir, one.ID); len(labels) != 1 || labels[0] != "needs-human" {
			t.Errorf("non-matching label on the second issue should survive: %v", labels)
		}
	})

	// THE pin for the false-confirmation defect, and it needs no concurrency:
	// naming the same issue twice used to produce one Update and TWO "removed"
	// rows per label, because the report iterated the pre-read (issue, label)
	// pairs rather than what the write returned. The ids collapse to one, so
	// one edit reports one row per label — exactly the single-id result.
	t.Run("label_remove_prefix_duplicate_id_reports_each_label_once", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Prefix duplicate id", "--type", "task",
			"--label", "pool:refused:reason-a", "--label", "pool:refused:reason-b", "--label", "needs-human")
		rows := bdLabelEditJSON(t, bd, dir, "remove", issue.ID, issue.ID, "--prefix", "pool:refused:")
		if len(rows) != 2 {
			t.Errorf("duplicated issue id = %v rows, want one per matching label (2), not one per (id,label) pair", rows)
		}
		for _, r := range rows {
			if r["status"] != "removed" {
				t.Errorf("row %v: want status=removed", r)
			}
		}
		if labels := bdLabelListJSON(t, bd, dir, issue.ID); len(labels) != 1 || labels[0] != "needs-human" {
			t.Errorf("labels after duplicated-id prefix remove = %v, want [needs-human]", labels)
		}
	})

	// Re-running a prefix removal must not claim a second removal. This pins
	// the no-match branch only: the "unchanged" arm of the shared
	// reportLabelEdit needs the matched set to vanish between the Get and the
	// Update, which a single caller cannot arrange, and
	// label_remove_absent_reports_noop covers that arm of the handler through
	// the plain path.
	t.Run("label_remove_prefix_rerun_takes_no_match_branch", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Prefix rerun", "--type", "task",
			"--label", "pool:refused:reason-a", "--label", "needs-human")
		bdLabel(t, bd, dir, "remove", issue.ID, "--prefix", "pool:refused:")
		// The prefix no longer matches anything, so the second call resolves an
		// empty set and takes the no-match branch rather than reporting a
		// removal.
		out := bdLabel(t, bd, dir, "remove", issue.ID, "--prefix", "pool:refused:")
		if strings.Contains(out, "Removed") {
			t.Errorf("re-running a prefix removal claimed a second removal: %s", out)
		}
		if !strings.Contains(out, "No labels matching prefix 'pool:refused:' found") {
			t.Errorf("expected the no-match line on re-run, got: %s", out)
		}
	})

	t.Run("label_remove_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON rm label", "--type", "task", "--label", "jsonrm")
		cmd := exec.Command(bd, "label", "remove", issue.ID, "jsonrm", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd label remove --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array in output:\nstdout: %s\nstderr: %s", s, stderr.String())
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON: %s", s)
		}
	})

	// ===== No-op edits (GH#5988) =====
	//
	// A label edit that leaves the set as it was used to print the same
	// "✓ Removed"/"✓ Added" line and JSON status as a real one. It still exits
	// 0 (bdLabel fails the test otherwise), but it now says so.

	t.Run("label_remove_absent_reports_noop", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Remove absent", "--type", "task", "--label", "present")
		out := bdLabel(t, bd, dir, "remove", issue.ID, "never-applied")
		if strings.Contains(out, "Removed") {
			t.Errorf("remove of an absent label claimed a removal: %s", out)
		}
		if want := "Label 'never-applied' was not on " + issue.ID; !strings.Contains(out, want) {
			t.Errorf("expected %q in output: %s", want, out)
		}
		rows := bdLabelEditJSON(t, bd, dir, "remove", issue.ID, "never-applied")
		if len(rows) != 1 || rows[0]["status"] != "unchanged" || rows[0]["label"] != "never-applied" || rows[0]["issue_id"] != issue.ID {
			t.Errorf("remove of an absent label JSON = %v, want one unchanged row", rows)
		}
	})

	t.Run("label_add_present_reports_noop", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Add present", "--type", "task", "--label", "present")
		out := bdLabel(t, bd, dir, "add", issue.ID, "present")
		if strings.Contains(out, "Added") {
			t.Errorf("add of a present label claimed an addition: %s", out)
		}
		if want := issue.ID + " already has label 'present'"; !strings.Contains(out, want) {
			t.Errorf("expected %q in output: %s", want, out)
		}
		rows := bdLabelEditJSON(t, bd, dir, "add", issue.ID, "present")
		if len(rows) != 1 || rows[0]["status"] != "unchanged" || rows[0]["label"] != "present" || rows[0]["issue_id"] != issue.ID {
			t.Errorf("add of a present label JSON = %v, want one unchanged row", rows)
		}
	})

	t.Run("label_edit_real_change_still_reported", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Real edit", "--type", "task")
		if rows := bdLabelEditJSON(t, bd, dir, "add", issue.ID, "real"); len(rows) != 1 || rows[0]["status"] != "added" {
			t.Errorf("real add JSON = %v, want one added row", rows)
		}
		if rows := bdLabelEditJSON(t, bd, dir, "remove", issue.ID, "real"); len(rows) != 1 || rows[0]["status"] != "removed" {
			t.Errorf("real remove JSON = %v, want one removed row", rows)
		}
		if out := bdLabel(t, bd, dir, "add", issue.ID, "real"); !strings.Contains(out, "Added label 'real' to "+issue.ID) {
			t.Errorf("real add text = %s", out)
		}
		if out := bdLabel(t, bd, dir, "remove", issue.ID, "real"); !strings.Contains(out, "Removed label 'real' from "+issue.ID) {
			t.Errorf("real remove text = %s", out)
		}
	})

	t.Run("label_edit_mixed_reports_each_label", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Mixed edit", "--type", "task", "--label", "have")
		out := bdLabel(t, bd, dir, "add", issue.ID, "have,fresh")
		if !strings.Contains(out, "Added label 'fresh' to "+issue.ID) || !strings.Contains(out, issue.ID+" already has label 'have'") {
			t.Errorf("mixed add text = %s", out)
		}
		rows := bdLabelEditJSON(t, bd, dir, "remove", issue.ID, "have,ghost")
		got := map[interface{}]interface{}{}
		for _, r := range rows {
			got[r["label"]] = r["status"]
		}
		if len(rows) != 2 || got["have"] != "removed" || got["ghost"] != "unchanged" {
			t.Errorf("mixed remove JSON = %v, want have=removed ghost=unchanged", rows)
		}
	})

	// One label, two issues, only one of which has it: the divergence is
	// BETWEEN the issues, so this pins the per-issue derivation the way
	// label_edit_mixed_reports_each_label pins the per-label one. The outcome
	// is built inside the id loop from that id's own UpdateResult, and a
	// refactor that hoisted it out — or reused the last result for every id —
	// would print one identical line per issue again, which is what the
	// pre-GH#5988 code did. Every other subtest here edits a single issue and
	// so would pass such a refactor.
	t.Run("label_edit_divergent_multi_issue", func(t *testing.T) {
		fresh := bdCreate(t, bd, dir, "Divergent JSON fresh", "--type", "task")
		holder := bdCreate(t, bd, dir, "Divergent JSON holder", "--type", "task", "--label", "shared")
		rows := bdLabelEditJSON(t, bd, dir, "add", fresh.ID, holder.ID, "shared")
		got := map[interface{}]interface{}{}
		for _, r := range rows {
			if r["label"] != "shared" {
				t.Errorf("unexpected label in row %v", r)
			}
			got[r["issue_id"]] = r["status"]
		}
		if len(rows) != 2 || got[fresh.ID] != "added" || got[holder.ID] != "unchanged" {
			t.Errorf("divergent multi-issue add JSON = %v, want %s=added %s=unchanged",
				rows, fresh.ID, holder.ID)
		}

		// Same shape in the text report: one line per issue, each naming its
		// own id, and the no-op issue must not be claimed as an edit.
		freshText := bdCreate(t, bd, dir, "Divergent text fresh", "--type", "task")
		holderText := bdCreate(t, bd, dir, "Divergent text holder", "--type", "task", "--label", "shared")
		out := bdLabel(t, bd, dir, "add", freshText.ID, holderText.ID, "shared")
		if want := "Added label 'shared' to " + freshText.ID; !strings.Contains(out, want) {
			t.Errorf("expected %q in output: %s", want, out)
		}
		if want := holderText.ID + " already has label 'shared'"; !strings.Contains(out, want) {
			t.Errorf("expected %q in output: %s", want, out)
		}
		if claim := "Added label 'shared' to " + holderText.ID; strings.Contains(out, claim) {
			t.Errorf("add claimed an edit on the issue that already had the label: %s", out)
		}
	})

	// ===== Label List =====

	t.Run("label_list", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "List labels", "--type", "task", "--label", "alpha", "--label", "beta")
		out := bdLabel(t, bd, dir, "list", issue.ID)
		if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
			t.Errorf("expected both labels in list output: %s", out)
		}
	})

	t.Run("label_list_empty", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "No labels", "--type", "task")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		if len(labels) != 0 {
			t.Errorf("expected empty labels, got %v", labels)
		}
	})

	t.Run("label_list_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON list", "--type", "task", "--label", "x", "--label", "y")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		labelSet := map[string]bool{}
		for _, l := range labels {
			labelSet[l] = true
		}
		if !labelSet["x"] || !labelSet["y"] {
			t.Errorf("expected labels x and y, got %v", labels)
		}
	})

	// ===== Label List-All =====

	t.Run("label_list_all", func(t *testing.T) {
		out := bdLabel(t, bd, dir, "list-all")
		// Should show some labels from earlier tests
		if !strings.Contains(out, "urgent") {
			t.Logf("list-all output may not contain 'urgent': %s", out)
		}
	})

	t.Run("label_list_all_json", func(t *testing.T) {
		results := bdLabelListAllJSON(t, bd, dir)
		if len(results) == 0 {
			t.Error("expected labels in list-all")
		}
		// Each result should have label and count
		for _, r := range results {
			if _, ok := r["label"]; !ok {
				t.Error("expected 'label' key in list-all result")
			}
			if _, ok := r["count"]; !ok {
				t.Error("expected 'count' key in list-all result")
			}
		}
	})
}

// TestEmbeddedLabelPropagateRename was split from TestEmbeddedLabel
// (originally ~360s, measured under --config=embedded) into 3 top-level
// tests over disjoint subtest groups, for CI shard balance (see
// scripts/ci/embedded_cmd_test_durations.json and engdocs/TESTING.md). Every original
// subtest is preserved exactly once.
func TestEmbeddedLabelPropagateRename(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tl")

	// ===== Label Propagate =====

	t.Run("label_propagate", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Propagate parent", "--type", "epic")
		child1 := bdCreate(t, bd, dir, "Propagate child 1", "--type", "task")
		child2 := bdCreate(t, bd, dir, "Propagate child 2", "--type", "task")
		bdDepAdd(t, bd, dir, child1.ID, parent.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, child2.ID, parent.ID, "--type", "parent-child")

		out := bdLabel(t, bd, dir, "propagate", parent.ID, "team:platform")
		if !strings.Contains(out, "Propagated") {
			t.Errorf("expected 'Propagated' in output: %s", out)
		}

		// Verify children got the label
		for _, id := range []string{child1.ID, child2.ID} {
			labels := bdLabelListJSON(t, bd, dir, id)
			found := false
			for _, l := range labels {
				if l == "team:platform" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected 'team:platform' on child %s: %v", id, labels)
			}
		}
	})

	t.Run("label_propagate_json", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "JSON propagate", "--type", "epic")
		child := bdCreate(t, bd, dir, "JSON prop child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")

		s := strings.TrimSpace(bdLabelJSONOutput(t, bd, dir, "propagate", parent.ID, "prop-json", "--json"))
		start := strings.Index(s, "[")
		if start >= 0 && !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON: %s", s)
		}
	})

	t.Run("label_propagate_no_children", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "No children parent", "--type", "task")
		out := bdLabel(t, bd, dir, "propagate", parent.ID, "orphan-label")
		if !strings.Contains(out, "No children") {
			t.Logf("propagate with no children: %s", out)
		}
	})

	// ===== Label Rename =====

	t.Run("label_rename_basic", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Rename basic", "--type", "task", "--label", "backend")
		out := bdLabel(t, bd, dir, "rename", "backend", "server")
		if !strings.Contains(out, "Renamed") {
			t.Errorf("expected 'Renamed' in output: %s", out)
		}
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		if len(labels) != 1 || labels[0] != "server" {
			t.Errorf("expected labels [server], got %v", labels)
		}
	})

	t.Run("label_rename_merge", func(t *testing.T) {
		onlyOld := bdCreate(t, bd, dir, "Rename merge only-old", "--type", "task", "--label", "wip-r")
		both := bdCreate(t, bd, dir, "Rename merge both", "--type", "task",
			"--label", "wip-r", "--label", "in-progress-r")

		out := bdLabel(t, bd, dir, "rename", "wip-r", "in-progress-r")
		if !strings.Contains(out, "Renamed") {
			t.Errorf("expected 'Renamed' in output: %s", out)
		}

		labels := bdLabelListJSON(t, bd, dir, onlyOld.ID)
		if len(labels) != 1 || labels[0] != "in-progress-r" {
			t.Errorf("only-old: expected [in-progress-r], got %v", labels)
		}
		labels = bdLabelListJSON(t, bd, dir, both.ID)
		if len(labels) != 1 || labels[0] != "in-progress-r" {
			t.Errorf("both: expected exactly [in-progress-r] (no duplicate), got %v", labels)
		}
	})

	t.Run("label_rename_dry_run_writes_nothing", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Rename dry run", "--type", "task", "--label", "dry-old")
		out := bdLabel(t, bd, dir, "rename", "dry-old", "dry-new", "--dry-run")
		if !strings.Contains(out, "Would rename") {
			t.Errorf("expected 'Would rename' in dry-run output: %s", out)
		}
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		if len(labels) != 1 || labels[0] != "dry-old" {
			t.Errorf("dry-run must not write: expected [dry-old] unchanged, got %v", labels)
		}
	})

	t.Run("label_rename_zero_carrier_is_honest_noop", func(t *testing.T) {
		out := bdLabel(t, bd, dir, "rename", "no-such-label-anywhere", "irrelevant")
		if !strings.Contains(out, "No issues found") {
			t.Errorf("expected 'No issues found' for a zero-carrier rename: %s", out)
		}
	})

	t.Run("label_rename_identical_fails", func(t *testing.T) {
		bdLabelFail(t, bd, dir, "rename", "same-label", "same-label")
	})

	t.Run("label_rename_empty_fails", func(t *testing.T) {
		bdLabelFail(t, bd, dir, "rename", "", "new-name")
	})

	t.Run("label_rename_reserved_provides_fails", func(t *testing.T) {
		bdLabelFail(t, bd, dir, "rename", "plain-label", "provides:auth")
	})

	// ===== Error Cases =====

	t.Run("label_add_empty_label", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Empty label", "--type", "task")
		bdLabelFail(t, bd, dir, "add", issue.ID, "")
	})

	t.Run("label_add_reserved_provides", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reserved label", "--type", "task")
		bdLabelFail(t, bd, dir, "add", issue.ID, "provides:auth")
	})

	t.Run("label_add_reserved_provides_in_comma_list", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reserved label in list", "--type", "task")
		bdLabelFail(t, bd, dir, "add", issue.ID, "ok-label,provides:auth")
	})

	t.Run("label_add_unresolvable_id_fails", func(t *testing.T) {
		bdLabelFail(t, bd, dir, "add", "tl-doesnotexist", "some-label")
	})

	t.Run("label_add_space_separated_labels_fails_loudly", func(t *testing.T) {
		// Regression test for bd-vu5kv: "bd label add <id> a b c" used to
		// treat a/b as unresolvable issue IDs, skip them with a stderr
		// warning, and exit 0 having applied only "c". It must now fail
		// hard, apply nothing, and hint at the comma-separated form.
		issue := bdCreate(t, bd, dir, "Space separated labels", "--type", "task")
		out := bdLabelFail(t, bd, dir, "add", issue.ID, "space-a", "space-b", "space-c")
		if !strings.Contains(out, "comma-separated") {
			t.Errorf("expected comma-separated hint in error output: %s", out)
		}
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		for _, l := range labels {
			if strings.HasPrefix(l, "space-") {
				t.Errorf("no label should have been applied, found %q: %v", l, labels)
			}
		}
	})
}

// TestEmbeddedLabelConcurrent exercises label operations concurrently.
func TestEmbeddedLabelConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "lx")

	const numWorkers = 8

	// Pre-create issues
	var issueIDs []string
	for i := 0; i < numWorkers; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("label-concurrent-%d", i), "--type", "task")
		issueIDs = append(issueIDs, issue.ID)
	}

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
			id := issueIDs[worker]

			// Add labels
			for i := 0; i < 3; i++ {
				label := fmt.Sprintf("w%d-label-%d", worker, i)
				cmd := exec.Command(bd, "label", "add", id, label)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("add label %s to %s: %v\n%s", label, id, err, out)
					results[worker] = r
					return
				}
			}

			// List labels
			cmd := exec.Command(bd, "label", "list", id, "--json")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("list labels for %s: %v\n%s", id, err, out)
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
