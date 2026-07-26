//go:build cgo

package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestProxiedServerOrphans(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "orp")

	gitCommit := func(t *testing.T, message string) {
		t.Helper()
		cmd := exec.Command("git", "commit", "--allow-empty", "-m", message)
		cmd.Dir = p.dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}

	orphanIDs := func(t *testing.T, args ...string) map[string]bool {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"orphans", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("orphans %v: %v\n%s", args, err, out)
		}
		if !json.Valid(out) {
			t.Fatalf("invalid JSON: %s", out)
		}
		var orphans []orphanIssueOutput
		if err := json.Unmarshal(out, &orphans); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		set := make(map[string]bool, len(orphans))
		for _, o := range orphans {
			set[o.IssueID] = true
		}
		return set
	}

	// An open issue referenced by a commit message is an orphan.
	referenced := bdProxiedCreate(t, bd, p.dir, "Referenced but never closed", "--type", "task")
	unreferenced := bdProxiedCreate(t, bd, p.dir, "Never mentioned", "--type", "task")
	// Give the referenced issue a label so --label can select it.
	bdProxiedLabel(t, bd, p.dir, "add", referenced.ID, "theme:personal")
	gitCommit(t, "feat: implemented ("+referenced.ID+")")

	t.Run("detects_referenced_open_issue", func(t *testing.T) {
		got := orphanIDs(t)
		if !got[referenced.ID] {
			t.Errorf("expected orphan %s (referenced in a commit, still open), got %v", referenced.ID, got)
		}
		if got[unreferenced.ID] {
			t.Errorf("did not expect unreferenced issue %s as orphan", unreferenced.ID)
		}
	})

	// Plain (non-JSON) default output must succeed and mention the orphan.
	t.Run("default_plain_output", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "orphans")
		if err != nil {
			t.Fatalf("orphans: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), referenced.ID) {
			t.Errorf("expected default output to mention %s, got:\n%s", referenced.ID, out)
		}
	})

	// --json on a db with orphans must still produce valid JSON.
	t.Run("json_valid", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "orphans", "--json")
		if err != nil {
			t.Fatalf("orphans --json: %v\n%s", err, out)
		}
		if !json.Valid(out) {
			t.Errorf("invalid JSON in orphans --json output: %s", out)
		}
	})

	// --details should succeed and include the orphan.
	t.Run("details", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "orphans", "--details")
		if err != nil {
			t.Fatalf("orphans --details: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), referenced.ID) {
			t.Errorf("expected --details output to mention %s, got:\n%s", referenced.ID, out)
		}
	})

	t.Run("label_filter_excludes", func(t *testing.T) {
		// referenced lacks this label, so a label filter should drop it.
		got := orphanIDs(t, "--label", "nonexistent-label-xyz")
		if got[referenced.ID] {
			t.Errorf("label filter should have excluded %s, got %v", referenced.ID, got)
		}
	})

	t.Run("label_filter_includes", func(t *testing.T) {
		// referenced carries theme:personal, so a matching filter keeps it.
		got := orphanIDs(t, "--label", "theme:personal")
		if !got[referenced.ID] {
			t.Errorf("label filter should have included %s, got %v", referenced.ID, got)
		}
	})

	t.Run("label_any_filter", func(t *testing.T) {
		// --label-any is OR semantics; theme:personal matches referenced.
		got := orphanIDs(t, "--label-any", "theme:personal,theme:ventures")
		if !got[referenced.ID] {
			t.Errorf("--label-any should have included %s, got %v", referenced.ID, got)
		}
		// A label-any set with no matches must produce valid JSON and no referenced orphan.
		none := orphanIDs(t, "--label-any", "nonexistent-label-xyz")
		if none[referenced.ID] {
			t.Errorf("--label-any with no matching label should exclude %s, got %v", referenced.ID, none)
		}
	})
}
