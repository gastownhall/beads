//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerUnclaim(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "unc")

	statusAssignee := func(t *testing.T, id string) (string, string) {
		t.Helper()
		iss := bdProxiedShow(t, bd, p.dir, id)
		return string(iss.Status), iss.Assignee
	}

	t.Run("unclaim_own_claim", func(t *testing.T) {
		a := bdProxiedCreate(t, bd, p.dir, "Unclaim target", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, a.ID, "--claim")
		if st, as := statusAssignee(t, a.ID); st != string(types.StatusInProgress) || as == "" {
			t.Fatalf("expected in_progress+assignee after claim, got status=%s assignee=%q", st, as)
		}

		if out, err := bdProxiedRun(t, bd, p.dir, "unclaim", a.ID, "--reason", "abandoning"); err != nil {
			t.Fatalf("unclaim: %v\n%s", err, out)
		}
		st, as := statusAssignee(t, a.ID)
		if st != string(types.StatusOpen) {
			t.Errorf("status = %s, want open after unclaim", st)
		}
		if as != "" {
			t.Errorf("assignee = %q, want cleared after unclaim", as)
		}

		// The --reason should have been added as a comment.
		out, err := bdProxiedRun(t, bd, p.dir, "comments", a.ID, "--json")
		if err != nil {
			t.Fatalf("comments: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "abandoning") {
			t.Errorf("expected reason comment 'abandoning' on %s, got %s", a.ID, out)
		}
	})

	t.Run("unclaim_unassigned_errors", func(t *testing.T) {
		b := bdProxiedCreate(t, bd, p.dir, "Never claimed", "--type", "task")
		stdout, stderr, _ := bdProxiedRunBuffers(t, bd, p.dir, "unclaim", b.ID)
		if !strings.Contains(stdout+stderr, "not assigned") {
			t.Errorf("expected 'not assigned' error, got stdout=%q stderr=%q", stdout, stderr)
		}
	})

	t.Run("unclaim_reason_in_output", func(t *testing.T) {
		c := bdProxiedCreate(t, bd, p.dir, "Reason output", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, c.ID, "--claim")
		out, err := bdProxiedRun(t, bd, p.dir, "unclaim", c.ID, "--reason", "Agent crashed")
		if err != nil {
			t.Fatalf("unclaim --reason: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Agent crashed") {
			t.Errorf("expected reason in output, got: %s", out)
		}
		if st, as := statusAssignee(t, c.ID); st != string(types.StatusOpen) || as != "" {
			t.Errorf("expected open+cleared after unclaim, got status=%s assignee=%q", st, as)
		}
	})

	t.Run("unclaim_json_output", func(t *testing.T) {
		d := bdProxiedCreate(t, bd, p.dir, "JSON output", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, d.ID, "--claim")
		out, err := bdProxiedRun(t, bd, p.dir, "unclaim", d.ID, "--json")
		if err != nil {
			t.Fatalf("unclaim --json: %v\n%s", err, out)
		}
		var unclaimed []struct {
			Assignee string `json:"assignee"`
			Status   string `json:"status"`
		}
		if err := json.Unmarshal(out, &unclaimed); err != nil {
			t.Fatalf("parse unclaim --json: %v\n%s", err, out)
		}
		if len(unclaimed) != 1 {
			t.Fatalf("expected 1 issue in unclaim --json, got %d:\n%s", len(unclaimed), out)
		}
		if unclaimed[0].Assignee != "" {
			t.Errorf("expected empty assignee, got %q", unclaimed[0].Assignee)
		}
		if unclaimed[0].Status != string(types.StatusOpen) {
			t.Errorf("expected status open, got %q", unclaimed[0].Status)
		}
	})

	t.Run("unclaim_multiple_ids", func(t *testing.T) {
		e := bdProxiedCreate(t, bd, p.dir, "Multi 1", "--type", "task")
		f := bdProxiedCreate(t, bd, p.dir, "Multi 2", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, e.ID, "--claim")
		bdProxiedUpdate(t, bd, p.dir, f.ID, "--claim")
		if out, err := bdProxiedRun(t, bd, p.dir, "unclaim", e.ID, f.ID); err != nil {
			t.Fatalf("unclaim multiple: %v\n%s", err, out)
		}
		for _, id := range []string{e.ID, f.ID} {
			if st, as := statusAssignee(t, id); st != string(types.StatusOpen) || as != "" {
				t.Errorf("%s: expected open+cleared, got status=%s assignee=%q", id, st, as)
			}
		}
	})

	t.Run("unclaim_nonexistent_errors", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "unclaim", "nonexistent")
		if err == nil {
			t.Fatalf("expected failure for nonexistent id, got success:\n%s", stdout)
		}
		if !strings.Contains(stdout+stderr, "not found") {
			t.Errorf("expected 'not found' error, got stdout=%q stderr=%q", stdout, stderr)
		}
	})

	t.Run("unclaim_closed_errors", func(t *testing.T) {
		g := bdProxiedCreate(t, bd, p.dir, "Closed target", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, g.ID, "--claim")
		if out, err := bdProxiedRun(t, bd, p.dir, "close", g.ID); err != nil {
			t.Fatalf("close: %v\n%s", err, out)
		}
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "unclaim", g.ID)
		if err == nil {
			t.Fatalf("expected failure unclaiming closed issue, got success:\n%s", stdout)
		}
		if !strings.Contains(stdout+stderr, "cannot unclaim closed issue") {
			t.Errorf("expected closed-issue error, got stdout=%q stderr=%q", stdout, stderr)
		}
	})
}

func TestProxiedServerReclaim(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "rcl")

	issue := bdProxiedCreate(t, bd, p.dir, "Reclaim target", "--type", "task")
	bdProxiedUpdate(t, bd, p.dir, issue.ID, "--claim")

	// Backdate the lease so it is expired well in the past.
	db := openProxiedDB(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := db.ExecContext(ctx,
		"UPDATE leases SET lease_expires_at = ? WHERE issue_id = ?",
		time.Now().UTC().Add(-1*time.Hour), issue.ID)
	if err != nil {
		t.Fatalf("backdate lease: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		t.Fatalf("claim did not create a lease row for %s; cannot test reclaim", issue.ID)
	}

	t.Run("reclaims_stale_lease", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "reclaim", "--older-than", "0s", "--json")
		if err != nil {
			t.Fatalf("reclaim: %v\n%s", err, out)
		}
		var got struct {
			Count     int `json:"count"`
			Reclaimed []struct {
				ID            string `json:"id"`
				PreviousOwner string `json:"previous_owner"`
			} `json:"reclaimed"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		var found bool
		for _, r := range got.Reclaimed {
			if r.ID == issue.ID {
				found = true
				if r.PreviousOwner == "" {
					t.Errorf("expected a previous owner for reclaimed %s", issue.ID)
				}
			}
		}
		if !found {
			t.Fatalf("expected %s in reclaimed set, got %+v", issue.ID, got.Reclaimed)
		}

		// Issue must now be open with the assignee cleared.
		iss := bdProxiedShow(t, bd, p.dir, issue.ID)
		if iss.Status != types.StatusOpen {
			t.Errorf("status = %s, want open after reclaim", iss.Status)
		}
		if iss.Assignee != "" {
			t.Errorf("assignee = %q, want cleared after reclaim", iss.Assignee)
		}
	})

	t.Run("nothing_to_reclaim", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "reclaim", "--older-than", "0s", "--json")
		if err != nil {
			t.Fatalf("reclaim: %v\n%s", err, out)
		}
		var got struct {
			Count int `json:"count"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.Count != 0 {
			t.Errorf("expected 0 reclaimed on second run, got %d", got.Count)
		}
	})
}
