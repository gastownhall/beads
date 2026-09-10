//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestProxiedServerHeartbeat journeys bd heartbeat through the proxied-server
// plane (bd-aq0ql). The worker contract is the exit code (workers call
// heartbeat every ~90s and only check rc), the --json success shape is
// {"id","status":"heartbeat","owner"}, and — the bd-lrgn1 invariant — a
// heartbeat mints exactly ZERO Dolt commits: the lease write lands in the
// dolt_ignored leases table via the SQL-only ephemeral commit, with the same
// timestamps/format classic `bd reclaim` staleness semantics consume (the
// SQL body is literally issueops.HeartbeatIssueInTx in both modes).
func TestProxiedServerHeartbeat(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "hbt")

	db := openProxiedDB(t, p)

	leaseRow := func(t *testing.T, id string) (holder string, expires, heartbeat time.Time, ok bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := db.QueryRowContext(ctx,
			"SELECT holder, lease_expires_at, heartbeat_at FROM leases WHERE issue_id = ?", id,
		).Scan(&holder, &expires, &heartbeat)
		if errors.Is(err, sql.ErrNoRows) {
			return "", time.Time{}, time.Time{}, false
		}
		if err != nil {
			t.Fatalf("read lease row for %s: %v", id, err)
		}
		return holder, expires, heartbeat, true
	}

	doltCommits := func(t *testing.T) int {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&n); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		return n
	}

	backdateLease := func(t *testing.T, id string, by time.Duration) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		past := time.Now().UTC().Add(-by)
		res, err := db.ExecContext(ctx,
			"UPDATE leases SET lease_expires_at = ?, heartbeat_at = ? WHERE issue_id = ?", past, past, id)
		if err != nil {
			t.Fatalf("backdate lease for %s: %v", id, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			t.Fatalf("no lease row to backdate for %s", id)
		}
	}

	t.Run("refreshes_lease_zero_dolt_commits", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "Heartbeat target", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, issue.ID, "--claim", "--actor", "worker-7")
		if holder, _, _, ok := leaseRow(t, issue.ID); !ok || holder != "worker-7" {
			t.Fatalf("claim did not arm a worker-7 lease (ok=%v holder=%q)", ok, holder)
		}

		// Make the lease stale so the refresh is unambiguous, then rescue it.
		backdateLease(t, issue.ID, time.Hour)
		_, preExpires, preHeartbeat, _ := leaseRow(t, issue.ID)

		commitsBefore := doltCommits(t)
		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "--actor", "worker-7", "heartbeat", issue.ID)
		if err != nil {
			t.Fatalf("heartbeat: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr)
		}
		if !strings.Contains(out, "Heartbeat "+issue.ID) || !strings.Contains(out, "(lease refreshed)") {
			t.Errorf("unexpected heartbeat output:\n%s", out)
		}

		// The bd-lrgn1 acceptance criterion, proxied edition: the lease
		// advanced but dolt_log is byte-for-byte the same length — the write
		// rode the SQL-only ephemeral commit, no Dolt commit, no history.
		if commitsAfter := doltCommits(t); commitsAfter != commitsBefore {
			t.Errorf("heartbeat minted Dolt commits: %d -> %d", commitsBefore, commitsAfter)
		}
		holder, expires, heartbeat, ok := leaseRow(t, issue.ID)
		if !ok {
			t.Fatal("lease row vanished after heartbeat")
		}
		if holder != "worker-7" {
			t.Errorf("holder = %q, want worker-7 (heartbeat must not move the lease)", holder)
		}
		if !expires.After(preExpires) || !expires.After(time.Now().UTC()) {
			t.Errorf("lease_expires_at not pushed into the future: pre=%v post=%v", preExpires, expires)
		}
		if !heartbeat.After(preHeartbeat) {
			t.Errorf("heartbeat_at not advanced: pre=%v post=%v", preHeartbeat, heartbeat)
		}

		// Classic-staleness parity: the freshly-heartbeaten lease reads as
		// live to the reclaim sweep (reclaim consumes the very timestamps
		// heartbeat just wrote — same clock, same format).
		rout, rerr := bdProxiedRun(t, bd, p.dir, "reclaim", "--older-than", "0s", "--json")
		if rerr != nil {
			t.Fatalf("reclaim: %v\n%s", rerr, rout)
		}
		if strings.Contains(string(rout), issue.ID) {
			t.Errorf("reclaim reverted a just-heartbeaten lease:\n%s", rout)
		}
	})

	t.Run("json_shape", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "JSON heartbeat", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, issue.ID, "--claim", "--actor", "worker-json")

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "--actor", "worker-json", "heartbeat", issue.ID, "--json")
		if err != nil {
			t.Fatalf("heartbeat --json: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr)
		}
		s := strings.TrimSpace(out)
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON object in heartbeat --json output:\n%s", out)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(s[start:]), &got); err != nil {
			t.Fatalf("parse heartbeat --json: %v\n%s", err, out)
		}
		// outputJSON envelopes every payload with schema_version (both modes);
		// the heartbeat contract on top of it is exactly id/status/owner.
		want := map[string]string{"id": issue.ID, "status": "heartbeat", "owner": "worker-json"}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("heartbeat --json .%s = %v, want %q", k, got[k], v)
			}
		}
		delete(got, "schema_version")
		if len(got) != len(want) {
			t.Errorf("heartbeat --json shape drifted: got %v, want exactly keys id/status/owner", got)
		}
	})

	t.Run("wrong_owner_refused", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "Not your lease", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, issue.ID, "--claim", "--actor", "worker-7")
		_, preExpires, _, _ := leaseRow(t, issue.ID)

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "--actor", "intruder", "heartbeat", issue.ID)
		if err == nil {
			t.Fatalf("expected nonzero exit for wrong-owner heartbeat, got success:\n%s", stdout)
		}
		if combined := stdout + stderr; !strings.Contains(combined, "already claimed") {
			t.Errorf("expected 'already claimed' classification, got:\n%s", combined)
		}
		if _, expires, _, ok := leaseRow(t, issue.ID); !ok || !expires.Equal(preExpires) {
			t.Errorf("wrong-owner heartbeat must not touch the lease (ok=%v pre=%v post=%v)", ok, preExpires, expires)
		}
	})

	t.Run("unclaimed_refused", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "Never claimed", "--type", "task")
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "--actor", "worker-7", "heartbeat", issue.ID)
		if err == nil {
			t.Fatalf("expected nonzero exit heartbeating an unclaimed issue, got success:\n%s", stdout)
		}
		if combined := stdout + stderr; !strings.Contains(combined, "not claimable") {
			t.Errorf("expected 'not claimable' classification, got:\n%s", combined)
		}
	})

	t.Run("closed_refused", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "Closed lease", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, issue.ID, "--claim", "--actor", "worker-7")
		if out, err := bdProxiedRun(t, bd, p.dir, "--actor", "worker-7", "close", issue.ID); err != nil {
			t.Fatalf("close: %v\n%s", err, out)
		}
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "--actor", "worker-7", "heartbeat", issue.ID)
		if err == nil {
			t.Fatalf("expected nonzero exit heartbeating a closed issue (worker must learn to stop), got:\n%s", stdout)
		}
		if combined := stdout + stderr; !strings.Contains(combined, "not claimable") {
			t.Errorf("expected 'not claimable' classification, got:\n%s", combined)
		}
	})

	t.Run("missing_refused", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "heartbeat", "hbt-doesnotexist")
		if err == nil {
			t.Fatalf("expected nonzero exit for a missing id, got success:\n%s", stdout)
		}
		if combined := stdout + stderr; !strings.Contains(combined, "not found") {
			t.Errorf("expected 'not found' error, got:\n%s", combined)
		}
	})

	t.Run("rearms_hand_doled_claim", func(t *testing.T) {
		// A claim hand-doled through a generic update never arms a lease
		// (bd-9hpgf); the holder's first heartbeat opts into lease semantics.
		// This is the INSERT path of the lease write, so it doubles as the
		// zero-Dolt-commit proof for UpsertLeaseInTx under the ephemeral
		// commit.
		issue := bdProxiedCreate(t, bd, p.dir, "Hand-doled claim", "--type", "task")
		bdProxiedUpdate(t, bd, p.dir, issue.ID, "--assignee", "hand-worker", "--status", "in_progress")
		if _, _, _, ok := leaseRow(t, issue.ID); ok {
			t.Fatal("generic update must not arm a lease")
		}

		commitsBefore := doltCommits(t)
		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "--actor", "hand-worker", "heartbeat", issue.ID)
		if err != nil {
			t.Fatalf("re-arm heartbeat: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr)
		}
		if commitsAfter := doltCommits(t); commitsAfter != commitsBefore {
			t.Errorf("re-arm heartbeat minted Dolt commits: %d -> %d", commitsBefore, commitsAfter)
		}
		holder, expires, _, ok := leaseRow(t, issue.ID)
		if !ok || holder != "hand-worker" {
			t.Fatalf("heartbeat should have armed a hand-worker lease (ok=%v holder=%q)", ok, holder)
		}
		if !expires.After(time.Now().UTC()) {
			t.Errorf("re-armed lease_expires_at should be in the future, got %v", expires)
		}
	})
}
