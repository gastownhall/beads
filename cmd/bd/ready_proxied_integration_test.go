//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

func bdProxiedClaimCmd(bd, dir, actor string) *exec.Cmd {
	cmd := exec.Command(bd, "ready", "--claim", "--json", "--label", "atomic")
	cmd.Dir = dir
	env := bdProxiedEnv(dir)
	env = append(env, "BEADS_ACTOR="+actor)
	cmd.Env = env
	return cmd
}

func bdProxiedReadyJSON(t *testing.T, bd string, p proxiedProject, args ...string) []*types.IssueWithCounts {
	t.Helper()
	fullArgs := append([]string{"ready", "--json"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd ready --json %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	s := strings.TrimSpace(stdout)
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array in ready --json output:\n%s", stdout)
	}
	var issues []*types.IssueWithCounts
	if err := json.Unmarshal([]byte(s[start:]), &issues); err != nil {
		t.Fatalf("parse ready JSON: %v\n%s", err, s[start:])
	}
	return issues
}

func bdProxiedReadyCapture(t *testing.T, bd string, p proxiedProject, args ...string) (string, string) {
	t.Helper()
	fullArgs := append([]string{"ready"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd ready %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout, stderr
}

func bdProxiedReadyFail(t *testing.T, bd string, p proxiedProject, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"ready"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, fullArgs...)
	if err == nil {
		t.Fatalf("bd ready %s should have failed; stdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func TestProxiedServerReady(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("json_round_trips_issue_with_counts", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdj1")
		issue := bdProxiedCreate(t, bd, p.dir, "Zero deps", "--label", "rdj1-only")
		ready := bdProxiedReadyJSON(t, bd, p, "--label", "rdj1-only")
		if len(ready) != 1 {
			t.Fatalf("ready count = %d, want 1", len(ready))
		}
		if ready[0].ID != issue.ID {
			t.Fatalf("ready[0].ID = %s, want %s", ready[0].ID, issue.ID)
		}
		if ready[0].DependencyCount != 0 {
			t.Errorf("DependencyCount = %d, want 0", ready[0].DependencyCount)
		}
	})

	t.Run("default_text_output", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdt")
		bdProxiedCreate(t, bd, p.dir, "Ready title here")
		stdout, _ := bdProxiedReadyCapture(t, bd, p)
		if !strings.Contains(stdout, "Ready title here") {
			t.Errorf("expected issue title in output, got: %s", stdout)
		}
	})

	t.Run("json_validity", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdv")
		bdProxiedCreate(t, bd, p.dir, "JSON test")
		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--json")
		if err != nil {
			t.Fatalf("bd ready --json failed: %v\n%s", err, stdout)
		}
		s := strings.TrimSpace(stdout)
		start := strings.IndexAny(s, "[{")
		if start < 0 {
			t.Fatalf("no JSON in output: %s", s)
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("invalid JSON: %s", s[start:])
		}
	})

	t.Run("limit_zero_returns_full_set_and_suppresses_hint", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdl0")
		const n = 4
		want := make(map[string]bool, n)
		for i := 0; i < n; i++ {
			issue := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("L0 item %d", i), "--label", "l0")
			want[issue.ID] = true
		}
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir,
			"ready", "--json", "--limit", "0", "--label", "l0")
		if err != nil {
			t.Fatalf("bd ready --json --limit 0 failed: %v\nstderr: %s", err, stderr)
		}
		if strings.Contains(stderr, "Use --limit 0 for all") {
			t.Errorf("--limit 0 must not emit truncation hint, got stderr: %s", stderr)
		}
		s := strings.TrimSpace(stdout)
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array in stdout: %s", stdout)
		}
		var got []*types.IssueWithCounts
		if err := json.Unmarshal([]byte(s[start:]), &got); err != nil {
			t.Fatalf("parse JSON: %v\n%s", err, s[start:])
		}
		gotIDs := map[string]bool{}
		for _, r := range got {
			gotIDs[r.ID] = true
		}
		for id := range want {
			if !gotIDs[id] {
				t.Errorf("expected %s in --limit 0 result", id)
			}
		}
		if len(got) < n {
			t.Errorf("got %d issues under --limit 0, want >=%d", len(got), n)
		}
	})

	t.Run("limit_truncation_hint_on_stderr", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdl")
		for i := 0; i < 4; i++ {
			bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Ready item %d", i))
		}
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--json", "--limit", "2")
		if err != nil {
			t.Fatalf("bd ready --json --limit 2 failed: %v\nstderr: %s\nstdout: %s", err, stderr, stdout)
		}
		s := strings.TrimSpace(stdout)
		start := strings.Index(s, "[")
		if start < 0 || !json.Valid([]byte(s[start:])) {
			t.Fatalf("stdout JSON should remain parseable, got: %s", stdout)
		}
		if !strings.Contains(stderr, "Use --limit 0 for all") {
			t.Fatalf("expected truncation hint on stderr, got: %q", stderr)
		}
		if !strings.Contains(stderr, "more matched but were hidden by --limit") {
			t.Errorf("expected HasMore-based hint wording on stderr, got: %q", stderr)
		}
	})

	t.Run("offset_with_large_finite_limit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdofflz")
		for i := 0; i < 4; i++ {
			bdProxiedCreate(t, bd, p.dir,
				fmt.Sprintf("Lz item %d", i), "--label", "rdofflz")
		}
		got := bdProxiedReadyJSON(t, bd, p,
			"--offset", "1", "--limit", "1000", "--label", "rdofflz")
		if len(got) != 3 {
			t.Errorf("expected 3 items (4 created, offset 1, loose limit), got %d", len(got))
		}
	})

	t.Run("offset_zero_equals_no_offset", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdoff0")
		for i := 0; i < 3; i++ {
			bdProxiedCreate(t, bd, p.dir,
				fmt.Sprintf("Eq item %d", i), "--label", "rdoff0")
		}
		baseline := bdProxiedReadyJSON(t, bd, p,
			"--limit", "10", "--label", "rdoff0")
		withZero := bdProxiedReadyJSON(t, bd, p,
			"--offset", "0", "--limit", "10", "--label", "rdoff0")
		if len(baseline) != len(withZero) {
			t.Fatalf("--offset 0 must equal no --offset, lengths differ: %d vs %d", len(baseline), len(withZero))
		}
		for i := range baseline {
			if baseline[i].ID != withZero[i].ID {
				t.Errorf("position %d: baseline=%s, withZero=%s", i, baseline[i].ID, withZero[i].ID)
			}
		}
	})

	t.Run("offset_combo_guards", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdoffg")
		bdProxiedCreate(t, bd, p.dir, "Seed")
		cases := []struct {
			name string
			args []string
		}{
			{"offset_claim", []string{"--offset", "1", "--claim"}},
			{"offset_mol", []string{"--offset", "1", "--mol", "x"}},
			{"offset_gated", []string{"--offset", "1", "--gated"}},
			{"offset_explain", []string{"--offset", "1", "--explain"}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				out := bdProxiedReadyFail(t, bd, p, c.args...)
				if !strings.Contains(out, "--offset cannot be combined") {
					t.Errorf("expected '--offset cannot be combined' error, got: %s", out)
				}
			})
		}
	})

	t.Run("offset_skips_leading_results", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdoff")
		ids := make([]string, 4)
		for i := 0; i < 4; i++ {
			issue := bdProxiedCreate(t, bd, p.dir,
				fmt.Sprintf("Off item %d", i), "-p", "1", "--label", "rdoff")
			ids[i] = issue.ID
		}
		got := bdProxiedReadyJSON(t, bd, p,
			"--offset", "2", "--limit", "2", "--label", "rdoff")
		if len(got) != 2 {
			t.Fatalf("expected exactly 2 items after offset+limit, got %d: %+v", len(got), got)
		}
		gotIDs := []string{got[0].ID, got[1].ID}
		first := bdProxiedReadyJSON(t, bd, p, "--limit", "0", "--label", "rdoff")
		if len(first) < 4 {
			t.Fatalf("baseline returned %d, need 4 to assert ordering", len(first))
		}
		wantA, wantB := first[2].ID, first[3].ID
		if gotIDs[0] != wantA || gotIDs[1] != wantB {
			t.Errorf("offset=2 limit=2 returned %v, want [%s %s] (the 3rd+4th of the unpaginated set)",
				gotIDs, wantA, wantB)
		}
	})

	t.Run("claim_json_no_match_returns_empty_array", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rcn")
		bdProxiedCreate(t, bd, p.dir, "Has wrong label", "--label", "real")
		out, err := bdProxiedRun(t, bd, p.dir, "ready", "--claim", "--json", "--label", "missing-label")
		if err != nil {
			t.Fatalf("bd ready --claim --json with no match failed: %v\n%s", err, out)
		}
		var empty []types.IssueWithCounts
		if err := json.Unmarshal(bytes.TrimSpace(out), &empty); err != nil {
			t.Fatalf("parse empty claim JSON: %v\n%s", err, out)
		}
		if len(empty) != 0 {
			t.Fatalf("expected no claimed issues, got %d: %s", len(empty), out)
		}
	})

	t.Run("claim_json_marks_in_progress_and_assignee", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rcm")
		issue := bdProxiedCreate(t, bd, p.dir, "Claim me", "--label", "claim-me")
		out, err := bdProxiedRun(t, bd, p.dir, "ready", "--claim", "--json", "--label", "claim-me")
		if err != nil {
			t.Fatalf("bd ready --claim --json failed: %v\n%s", err, out)
		}
		var claimed []types.IssueWithCounts
		if err := json.Unmarshal(bytes.TrimSpace(out), &claimed); err != nil {
			t.Fatalf("parse claim JSON: %v\n%s", err, out)
		}
		if len(claimed) != 1 {
			t.Fatalf("expected one claimed issue, got %d: %s", len(claimed), out)
		}
		if claimed[0].ID != issue.ID {
			t.Fatalf("claimed ID = %s, want %s", claimed[0].ID, issue.ID)
		}
		if claimed[0].Status != types.StatusInProgress {
			t.Errorf("Status = %s, want %s", claimed[0].Status, types.StatusInProgress)
		}
		if claimed[0].Assignee == "" {
			t.Error("Assignee should be set after claim")
		}
		shown := bdProxiedShow(t, bd, p.dir, issue.ID)
		if shown.Status != types.StatusInProgress {
			t.Errorf("after re-fetch: Status = %s, want %s", shown.Status, types.StatusInProgress)
		}
		if shown.Assignee == "" {
			t.Error("after re-fetch: Assignee empty (commit may not have persisted)")
		}
	})

	t.Run("blocked_issues_excluded_from_default_output", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdb")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker issue")
		bdProxiedCreate(t, bd, p.dir, "Should be hidden because blocked", "--deps", "depends-on:"+blocker.ID)
		stdout, _ := bdProxiedReadyCapture(t, bd, p)
		if strings.Contains(stdout, "Should be hidden because blocked") {
			t.Errorf("blocked issue must not appear in ready output: %s", stdout)
		}
	})

	t.Run("exclude_label_filter", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdex")
		bdProxiedCreate(t, bd, p.dir, "Triage pending item", "--label", "triage:pending")
		bdProxiedCreate(t, bd, p.dir, "Normal ready item")
		stdout, _ := bdProxiedReadyCapture(t, bd, p, "--exclude-label", "triage:pending")
		if strings.Contains(stdout, "Triage pending item") {
			t.Errorf("triage:pending issue must be excluded: %s", stdout)
		}
		if !strings.Contains(stdout, "Normal ready item") {
			t.Errorf("normal issue must appear: %s", stdout)
		}
	})

	t.Run("explain_json_round_trip", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rde")
		bdProxiedCreate(t, bd, p.dir, "Plain ready")
		blocker := bdProxiedCreate(t, bd, p.dir, "Live blocker")
		bdProxiedCreate(t, bd, p.dir, "Blocked dependent", "--deps", "depends-on:"+blocker.ID)
		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--explain", "--json")
		if err != nil {
			t.Fatalf("bd ready --explain --json failed: %v\n%s", err, stdout)
		}
		s := strings.TrimSpace(stdout)
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON in --explain output: %s", stdout)
		}
		var exp types.ReadyExplanation
		if err := json.Unmarshal([]byte(s[start:]), &exp); err != nil {
			t.Fatalf("parse ReadyExplanation: %v\n%s", err, s[start:])
		}
		if exp.Summary.TotalReady < 1 {
			t.Errorf("Summary.TotalReady = %d, want >=1", exp.Summary.TotalReady)
		}
		if exp.Summary.TotalBlocked < 1 {
			t.Errorf("Summary.TotalBlocked = %d, want >=1", exp.Summary.TotalBlocked)
		}
	})

	t.Run("explain_text_header", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdet")
		bdProxiedCreate(t, bd, p.dir, "Smoke")
		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--explain")
		if err != nil {
			t.Fatalf("bd ready --explain failed: %v\n%s", err, stdout)
		}
		if !strings.Contains(stdout, "Ready Work Explanation") {
			t.Errorf("expected explain heading, got: %s", stdout)
		}
	})

	t.Run("explain_detects_cycles", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdc")
		a := bdProxiedCreate(t, bd, p.dir, "Cycle node A")
		b := bdProxiedCreate(t, bd, p.dir, "Cycle node B")
		db := openProxiedDB(t, p)
		ctx := context.Background()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (?, ?, ?, ?, NOW(), 'test')",
			depid.New(a.ID, b.ID), a.ID, b.ID, string(types.DepBlocks)); err != nil {
			t.Fatalf("plant a->b edge: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (?, ?, ?, ?, NOW(), 'test')",
			depid.New(b.ID, a.ID), b.ID, a.ID, string(types.DepBlocks)); err != nil {
			t.Fatalf("plant b->a edge: %v", err)
		}
		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--explain", "--json")
		if err != nil {
			t.Fatalf("bd ready --explain --json after cycle plant: %v\n%s", err, stdout)
		}
		s := strings.TrimSpace(stdout)
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON in --explain output: %s", stdout)
		}
		var exp types.ReadyExplanation
		if err := json.Unmarshal([]byte(s[start:]), &exp); err != nil {
			t.Fatalf("parse ReadyExplanation: %v\n%s", err, s[start:])
		}
		if len(exp.Cycles) == 0 {
			t.Errorf("expected non-empty Cycles after planting a->b->a, got: %+v", exp)
		}
	})

	t.Run("gated_empty_case", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdge")
		bdProxiedCreate(t, bd, p.dir, "No gates anywhere")
		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--gated", "--json")
		if err != nil {
			t.Fatalf("bd ready --gated --json failed: %v\n%s", err, stdout)
		}
		s := strings.TrimSpace(stdout)
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON in gated output: %s", stdout)
		}
		var out GatedReadyOutput
		if err := json.Unmarshal([]byte(s[start:]), &out); err != nil {
			t.Fatalf("parse GatedReadyOutput: %v\n%s", err, s[start:])
		}
		if len(out.Molecules) != 0 {
			t.Errorf("expected empty molecules array, got %d", len(out.Molecules))
		}

		text, _ := bdProxiedReadyCapture(t, bd, p, "--gated")
		if !strings.Contains(text, "No closed gates found") {
			t.Errorf("expected 'No closed gates found' text, got: %s", text)
		}
	})

}

func TestProxiedServerReady2(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("gated_json_shape_with_open_gate", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdgw")
		bdProxiedCreate(t, bd, p.dir, "Open gate", "--type", "gate")
		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--gated", "--json")
		if err != nil {
			t.Fatalf("bd ready --gated --json failed: %v\n%s", err, stdout)
		}
		s := strings.TrimSpace(stdout)
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON in gated output: %s", stdout)
		}
		var out GatedReadyOutput
		if err := json.Unmarshal([]byte(s[start:]), &out); err != nil {
			t.Fatalf("parse GatedReadyOutput: %v\n%s", err, s[start:])
		}
		if len(out.Molecules) != 0 {
			t.Errorf("open (non-closed) gate must not appear in gated output, got %d", len(out.Molecules))
		}
	})

	t.Run("mol_text_and_json", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdm")
		mol := bdProxiedCreate(t, bd, p.dir, "Mol parent", "--type", "molecule")
		s1 := bdProxiedCreate(t, bd, p.dir, "Step one", "--parent", mol.ID)
		bdProxiedCreate(t, bd, p.dir, "Step two", "--parent", mol.ID, "--deps", "depends-on:"+s1.ID)
		stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "ready", "--mol", mol.ID, "--json")
		if err != nil {
			t.Fatalf("bd ready --mol --json failed: %v\n%s", err, stdout)
		}
		sj := strings.TrimSpace(stdout)
		start := strings.Index(sj, "{")
		if start < 0 {
			t.Fatalf("no JSON in --mol output: %s", stdout)
		}
		var out MoleculeReadyOutput
		if err := json.Unmarshal([]byte(sj[start:]), &out); err != nil {
			t.Fatalf("parse MoleculeReadyOutput: %v\n%s", err, sj[start:])
		}
		if out.TotalSteps < 2 {
			t.Errorf("TotalSteps = %d, want >=2", out.TotalSteps)
		}
		if out.ReadySteps < 1 {
			t.Errorf("ReadySteps = %d, want >=1", out.ReadySteps)
		}

		text, _ := bdProxiedReadyCapture(t, bd, p, "--mol", mol.ID)
		if !strings.Contains(text, "Mol parent") {
			t.Errorf("expected molecule title in text output, got: %s", text)
		}
	})

	t.Run("empty_state_no_open_issues", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdei")
		stdout, _ := bdProxiedReadyCapture(t, bd, p)
		if !strings.Contains(stdout, "No open issues") {
			t.Errorf("expected 'No open issues', got: %s", stdout)
		}
	})

	t.Run("empty_state_no_ready_with_blockers", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdeb")
		blocker := bdProxiedCreate(t, bd, p.dir, "Self-blocker")
		bdProxiedCreate(t, bd, p.dir, "Only issue, blocked", "--deps", "blocks:"+blocker.ID)
		if _, err := bdProxiedRun(t, bd, p.dir, "ready", "--claim", "--json"); err == nil {
		}
		if _, err := bdProxiedRun(t, bd, p.dir, "update", blocker.ID, "--claim"); err != nil {
			t.Fatalf("update --claim blocker: %v", err)
		}
		stdout, _ := bdProxiedReadyCapture(t, bd, p, "--label", "no-such-label")
		if !strings.Contains(stdout, "No ready work found") && !strings.Contains(stdout, "No open issues") {
			t.Errorf("expected empty-state hint, got: %s", stdout)
		}
	})

	t.Run("filter_priority_pass_through", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rfp")
		hi := bdProxiedCreate(t, bd, p.dir, "Hi pri", "-p", "0")
		lo := bdProxiedCreate(t, bd, p.dir, "Lo pri", "-p", "3")
		ready := bdProxiedReadyJSON(t, bd, p, "-p", "0")
		ids := map[string]bool{}
		for _, r := range ready {
			ids[r.ID] = true
		}
		if !ids[hi.ID] {
			t.Errorf("expected %s in priority=0 filter result", hi.ID)
		}
		if ids[lo.ID] {
			t.Errorf("did not expect %s in priority=0 result", lo.ID)
		}
	})

	t.Run("filter_assignee_pass_through", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rfa")
		mine := bdProxiedCreate(t, bd, p.dir, "Alice work", "--assignee", "alice")
		bdProxiedCreate(t, bd, p.dir, "Bob work", "--assignee", "bob")
		ready := bdProxiedReadyJSON(t, bd, p, "--assignee", "alice")
		ids := map[string]bool{}
		for _, r := range ready {
			ids[r.ID] = true
		}
		if !ids[mine.ID] {
			t.Errorf("expected %s for assignee=alice", mine.ID)
		}
		for _, r := range ready {
			if r.Assignee != "alice" {
				t.Errorf("got non-alice assignee %q for %s", r.Assignee, r.ID)
			}
		}
	})

	t.Run("filter_unassigned_pass_through", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rfu")
		un := bdProxiedCreate(t, bd, p.dir, "Free agent")
		bdProxiedCreate(t, bd, p.dir, "Taken", "--assignee", "alice")
		ready := bdProxiedReadyJSON(t, bd, p, "--unassigned")
		ids := map[string]bool{}
		for _, r := range ready {
			ids[r.ID] = true
		}
		if !ids[un.ID] {
			t.Errorf("expected %s in --unassigned result", un.ID)
		}
		for _, r := range ready {
			if r.Assignee != "" {
				t.Errorf("got assignee %q for %s under --unassigned", r.Assignee, r.ID)
			}
		}
	})

	t.Run("filter_type_pass_through", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rft")
		bug := bdProxiedCreate(t, bd, p.dir, "A bug", "--type", "bug")
		bdProxiedCreate(t, bd, p.dir, "A task", "--type", "task")
		ready := bdProxiedReadyJSON(t, bd, p, "--type", "bug")
		ids := map[string]bool{}
		for _, r := range ready {
			ids[r.ID] = true
		}
		if !ids[bug.ID] {
			t.Errorf("expected %s for --type=bug", bug.ID)
		}
		for _, r := range ready {
			if r.IssueType != types.TypeBug {
				t.Errorf("got type %s for %s under --type=bug", r.IssueType, r.ID)
			}
		}
	})

	t.Run("filter_parent_pass_through", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rfpar")
		epic := bdProxiedCreate(t, bd, p.dir, "Epic", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Inside", "--parent", epic.ID)
		outside := bdProxiedCreate(t, bd, p.dir, "Outside")
		ready := bdProxiedReadyJSON(t, bd, p, "--parent", epic.ID)
		ids := map[string]bool{}
		for _, r := range ready {
			ids[r.ID] = true
		}
		if !ids[child.ID] {
			t.Errorf("expected %s under --parent=%s", child.ID, epic.ID)
		}
		if ids[outside.ID] {
			t.Errorf("outside-of-parent issue %s leaked into --parent result", outside.ID)
		}
	})

	t.Run("metadata_field_match", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rmd1")
		match := bdProxiedCreate(t, bd, p.dir, "Has team", "--metadata", `{"team":"platform"}`)
		bdProxiedCreate(t, bd, p.dir, "Other team", "--metadata", `{"team":"frontend"}`)
		ready := bdProxiedReadyJSON(t, bd, p, "--metadata-field", "team=platform")
		ids := map[string]bool{}
		for _, r := range ready {
			ids[r.ID] = true
		}
		if !ids[match.ID] {
			t.Errorf("expected %s for team=platform filter", match.ID)
		}
	})

	t.Run("metadata_has_key", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rmd2")
		withMeta := bdProxiedCreate(t, bd, p.dir, "With meta", "--metadata", `{"team":"x"}`)
		bdProxiedCreate(t, bd, p.dir, "No meta")
		ready := bdProxiedReadyJSON(t, bd, p, "--has-metadata-key", "team")
		ids := map[string]bool{}
		for _, r := range ready {
			ids[r.ID] = true
		}
		if !ids[withMeta.ID] {
			t.Errorf("expected %s in --has-metadata-key=team result", withMeta.ID)
		}
	})

	t.Run("metadata_field_invalid_key_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rmd3")
		out := bdProxiedReadyFail(t, bd, p, "--metadata-field", "bad$key=x")
		if !strings.Contains(out, "metadata-field") {
			t.Errorf("expected validation error about metadata-field, got: %s", out)
		}
	})

	t.Run("include_deferred_toggle", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdd")
		issue := bdProxiedCreate(t, bd, p.dir, "Will defer")
		db := openProxiedDB(t, p)
		if _, err := db.ExecContext(context.Background(),
			"UPDATE issues SET defer_until = DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY) WHERE id = ?", issue.ID); err != nil {
			t.Fatalf("plant defer_until: %v", err)
		}
		ready := bdProxiedReadyJSON(t, bd, p)
		for _, r := range ready {
			if r.ID == issue.ID {
				t.Errorf("future-deferred issue %s should be hidden by default", issue.ID)
			}
		}
		ready2 := bdProxiedReadyJSON(t, bd, p, "--include-deferred")
		found := false
		for _, r := range ready2 {
			if r.ID == issue.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected deferred %s under --include-deferred", issue.ID)
		}
	})

	t.Run("include_ephemeral_surfaces_wisps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rdie")
		reg := bdProxiedCreate(t, bd, p.dir, "Regular")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wispy", "--ephemeral")
		def := bdProxiedReadyJSON(t, bd, p)
		ids := map[string]bool{}
		for _, r := range def {
			ids[r.ID] = true
		}
		if !ids[reg.ID] {
			t.Errorf("expected regular %s in default result", reg.ID)
		}
		if ids[wisp.ID] {
			t.Errorf("wisp %s must not appear by default", wisp.ID)
		}
		eph := bdProxiedReadyJSON(t, bd, p, "--include-ephemeral")
		ids2 := map[string]bool{}
		for _, r := range eph {
			ids2[r.ID] = true
		}
		if !ids2[wisp.ID] {
			t.Errorf("expected wisp %s under --include-ephemeral", wisp.ID)
		}
	})

	t.Run("mol_type_validation_rejects_invalid", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rmtv")
		out := bdProxiedReadyFail(t, bd, p, "--mol-type", "garbage")
		if !strings.Contains(out, "invalid mol-type") {
			t.Errorf("expected 'invalid mol-type' error, got: %s", out)
		}
	})

	t.Run("claim_combo_guards", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rcc")
		bdProxiedCreate(t, bd, p.dir, "Seed")
		cases := []struct {
			name string
			args []string
		}{
			{"claim_gated", []string{"--claim", "--gated"}},
			{"claim_mol", []string{"--claim", "--mol", "x"}},
			{"claim_explain", []string{"--claim", "--explain"}},
			{"claim_assignee", []string{"--claim", "--assignee", "alice"}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				out := bdProxiedReadyFail(t, bd, p, c.args...)
				if !strings.Contains(out, "--claim cannot be combined") {
					t.Errorf("expected '--claim cannot be combined' error, got: %s", out)
				}
			})
		}
	})

	// be-x42v.4 round-3 follow-up: rejectMaxRowsUnderProxiedServer must not
	// block `bd ready --claim` under proxied mode. --claim always delivers
	// exactly one row (same reasoning as the direct-path fix in
	// issueops/claim.go), so a rig-wide cap sized for bulk reads must not
	// hard-fail it. Bulk (non-claim) `bd ready` under proxied mode with an
	// active cap must still reject, same as `bd list` (see
	// list_proxied_integration_test.go's reject_max_rows_flag/_env).
	t.Run("claim_succeeds_under_env_max_rows_cap", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rcmr")
		// Ready pool (3) exceeds the cap (1) — exactly the scenario a
		// rig-wide BEADS_MAX_ROWS is meant to guard bulk reads against,
		// while claim (which only ever returns one row) must still work.
		for i := 0; i < 3; i++ {
			bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Claim under cap %d", i), "--label", "rcmr-claim")
		}
		stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir,
			[]string{"BEADS_MAX_ROWS=1"},
			"ready", "--claim", "--json", "--label", "rcmr-claim")
		if err != nil {
			t.Fatalf("bd ready --claim --json under BEADS_MAX_ROWS=1 with a larger ready pool should succeed: %v\nstdout:\n%s\nstderr:\n%s",
				err, stdout, stderr)
		}
		var claimed []types.IssueWithCounts
		s := strings.TrimSpace(stdout)
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array in claim output:\n%s", stdout)
		}
		if err := json.Unmarshal([]byte(s[start:]), &claimed); err != nil {
			t.Fatalf("parse claim JSON: %v\n%s", err, s[start:])
		}
		if len(claimed) != 1 {
			t.Fatalf("expected exactly one claimed issue, got %d: %s", len(claimed), stdout)
		}
		if claimed[0].Status != types.StatusInProgress {
			t.Errorf("Status = %s, want %s", claimed[0].Status, types.StatusInProgress)
		}
	})

	// be-x42v.4 round-4 follow-up (codex P2): the claim-exempt branch above
	// must still validate --max-rows via resolveMaxRows even though it
	// ignores the resolved (positive) cap — otherwise a malformed value
	// like -1 is silently accepted under proxied `ready --claim`, unlike
	// every other command (direct or proxied).
	t.Run("claim_rejects_invalid_max_rows_flag", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rcim")
		bdProxiedCreate(t, bd, p.dir, "Claim invalid max-rows seed")
		out := bdProxiedReadyFail(t, bd, p, "--claim", "--max-rows", "-1")
		if !strings.Contains(out, "must be non-negative") {
			t.Errorf("expected --max-rows usage-error rejection, got: %s", out)
		}
	})

	t.Run("reject_bulk_ready_under_max_rows_flag", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rbmf")
		bdProxiedCreate(t, bd, p.dir, "Bulk ready seed")
		out := bdProxiedReadyFail(t, bd, p, "--max-rows", "1")
		if !strings.Contains(out, "not supported in proxied-server mode") {
			t.Errorf("expected --max-rows proxied-server rejection for bulk (non-claim) ready, got: %s", out)
		}
	})

	t.Run("reject_bulk_ready_under_max_rows_env", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rbme")
		bdProxiedCreate(t, bd, p.dir, "Bulk ready env seed")
		stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir,
			[]string{"BEADS_MAX_ROWS=1"}, "ready")
		if err == nil {
			t.Fatalf("expected BEADS_MAX_ROWS under proxied bulk ready to fail, but it succeeded:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		out := stdout + stderr
		if !strings.Contains(out, "not supported in proxied-server mode") {
			t.Errorf("expected BEADS_MAX_ROWS proxied-server rejection for bulk (non-claim) ready, got: %s", out)
		}
	})

	t.Run("exclude_type_csv_and_repeated", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "rxt")
		task := bdProxiedCreate(t, bd, p.dir, "Task work", "--type", "task")
		bdProxiedCreate(t, bd, p.dir, "Bug work", "--type", "bug")
		bdProxiedCreate(t, bd, p.dir, "Epic work", "--type", "epic")

		csv := bdProxiedReadyJSON(t, bd, p, "--exclude-type", "bug,epic")
		csvIDs := map[string]bool{}
		for _, r := range csv {
			csvIDs[r.ID] = true
		}
		if !csvIDs[task.ID] {
			t.Errorf("expected task %s in CSV result", task.ID)
		}
		for _, r := range csv {
			if r.IssueType == types.TypeBug || r.IssueType == types.TypeEpic {
				t.Errorf("excluded type %s leaked: %s", r.IssueType, r.ID)
			}
		}

		rep := bdProxiedReadyJSON(t, bd, p, "--exclude-type", "bug", "--exclude-type", "epic")
		repIDs := map[string]bool{}
		for _, r := range rep {
			repIDs[r.ID] = true
		}
		if !repIDs[task.ID] {
			t.Errorf("expected task %s in repeated-flag result", task.ID)
		}
		for _, r := range rep {
			if r.IssueType == types.TypeBug || r.IssueType == types.TypeEpic {
				t.Errorf("excluded type %s leaked under repeated flag: %s", r.IssueType, r.ID)
			}
		}
	})
}

func TestProxiedServerReadyConcurrent(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "rxc")
	bdProxiedCreate(t, bd, p.dir, "Concurrent read target")

	const numWorkers = 8
	errs := make([]error, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			_, err := bdProxiedRun(t, bd, p.dir, "ready")
			errs[worker] = err
		}(w)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d: %v", i, err)
		}
	}
}

func TestProxiedServerReadyClaimConcurrent(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "rcx")
	issue := bdProxiedCreate(t, bd, p.dir, "Atomic claim target", "--label", "atomic")

	const numWorkers = 8
	type result struct {
		stdout string
		stderr string
		err    error
	}
	results := make([]result, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			cmd := bdProxiedClaimCmd(bd, p.dir, fmt.Sprintf("worker-%d@test", worker))
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			results[worker] = result{stdout: stdout.String(), stderr: stderr.String(), err: err}
		}(w)
	}
	wg.Wait()

	winners := 0
	for i, r := range results {
		s := strings.TrimSpace(r.stdout)
		start := strings.Index(s, "[")
		if start < 0 {
			continue
		}
		var claimed []types.IssueWithCounts
		if err := json.Unmarshal([]byte(s[start:]), &claimed); err != nil {
			t.Errorf("worker %d: parse JSON: %v\nstdout: %s\nstderr: %s", i, err, r.stdout, r.stderr)
			continue
		}
		if len(claimed) > 1 {
			t.Errorf("worker %d claimed multiple: %s", i, r.stdout)
			continue
		}
		if len(claimed) == 1 {
			winners++
			if claimed[0].ID != issue.ID {
				t.Errorf("worker %d claimed %s, want %s", i, claimed[0].ID, issue.ID)
			}
		}
	}
	if winners != 1 {
		for i, r := range results {
			t.Logf("worker %d: err=%v\nstdout=%s\nstderr=%s", i, r.err, r.stdout, r.stderr)
		}
		t.Fatalf("expected exactly one winning claim, got %d", winners)
	}
	final := bdProxiedShow(t, bd, p.dir, issue.ID)
	if final.Status != types.StatusInProgress {
		t.Errorf("final Status = %s, want in_progress", final.Status)
	}
	if final.Assignee == "" {
		t.Error("final Assignee empty")
	}
}
