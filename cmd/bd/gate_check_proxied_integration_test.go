//go:build cgo

package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func seedGateAwait(t *testing.T, db *sql.DB, id, awaitType, awaitID string, createdAt time.Time, timeout time.Duration) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		"UPDATE issues SET await_type=?, await_id=?, timeout_ns=?, created_at=? WHERE id=?",
		awaitType, awaitID, int64(timeout), createdAt.UTC(), id); err != nil {
		t.Fatalf("seed await columns for %s: %v", id, err)
	}
}

func TestProxiedServerGateCheck(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	const expiredTimeout = time.Hour
	longAgo := func() time.Time { return time.Now().UTC().Add(-48 * time.Hour) }
	longFuture := time.Hour * 1000

	t.Run("no_gates", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gcn")
		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "check")
		if err != nil {
			t.Fatalf("gate check failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "No open gates") {
			t.Errorf("expected 'No open gates' message, got:\n%s", out)
		}
	})

	t.Run("timer_expired_resolves", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gce")
		gate := bdProxiedCreate(t, bd, p.dir, "Expired timer gate", "--type", "gate")
		db := openProxiedDB(t, p)
		seedGateAwait(t, db, gate.ID, "timer", "", longAgo(), expiredTimeout)

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "check", "--type", "timer")
		if err != nil {
			t.Fatalf("gate check failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got != types.StatusClosed {
			t.Errorf("expired timer gate should be closed, got %q", got)
		}
		if !strings.Contains(out, "Checked 1 gates: 1 resolved") {
			t.Errorf("expected summary with 1 resolved, got:\n%s", out)
		}
	})

	t.Run("timer_pending_stays_open", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gcp")
		gate := bdProxiedCreate(t, bd, p.dir, "Pending timer gate", "--type", "gate")
		db := openProxiedDB(t, p)
		seedGateAwait(t, db, gate.ID, "timer", "", time.Now().UTC(), longFuture)

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "check", "--type", "timer")
		if err != nil {
			t.Fatalf("gate check failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got == types.StatusClosed {
			t.Error("pending timer gate should stay open")
		}
		if !strings.Contains(out, "0 resolved") {
			t.Errorf("expected 0 resolved for pending timer, got:\n%s", out)
		}
	})

	t.Run("dry_run_does_not_close", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gcd")
		gate := bdProxiedCreate(t, bd, p.dir, "Dry run timer gate", "--type", "gate")
		db := openProxiedDB(t, p)
		seedGateAwait(t, db, gate.ID, "timer", "", longAgo(), expiredTimeout)

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "check", "--type", "timer", "--dry-run")
		if err != nil {
			t.Fatalf("gate check --dry-run failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got == types.StatusClosed {
			t.Error("dry-run must not close the gate")
		}
		if !strings.Contains(out, "would resolve") {
			t.Errorf("expected 'would resolve' in dry-run output, got:\n%s", out)
		}
	})

	t.Run("bead_gate_resolves_when_target_closes", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gcb")
		target := bdProxiedCreate(t, bd, p.dir, "Bead gate target")
		gate := bdProxiedCreate(t, bd, p.dir, "Bead gate", "--type", "gate")
		db := openProxiedDB(t, p)
		seedGateAwait(t, db, gate.ID, "bead", target.ID, time.Now().UTC(), 0)

		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "check", "--type", "bead"); err != nil {
			t.Fatalf("gate check (target open) failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got == types.StatusClosed {
			t.Error("bead gate should stay pending while target is open")
		}

		bdProxiedClose(t, bd, p.dir, target.ID)
		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "check", "--type", "bead")
		if err != nil {
			t.Fatalf("gate check (target closed) failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got != types.StatusClosed {
			t.Errorf("bead gate should resolve after target closes, got %q", got)
		}
		if !strings.Contains(out, "1 resolved") {
			t.Errorf("expected 1 resolved after target close, got:\n%s", out)
		}
	})

	t.Run("type_filter_scopes_evaluation", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gcf")
		timerGate := bdProxiedCreate(t, bd, p.dir, "Timer gate", "--type", "gate")
		prGate := bdProxiedCreate(t, bd, p.dir, "PR gate", "--type", "gate")
		db := openProxiedDB(t, p)
		seedGateAwait(t, db, timerGate.ID, "timer", "", longAgo(), expiredTimeout)
		seedGateAwait(t, db, prGate.ID, "gh:pr", "1", time.Now().UTC(), 0)

		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "check", "--type", "timer"); err != nil {
			t.Fatalf("gate check --type timer failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, timerGate.ID); got != types.StatusClosed {
			t.Errorf("timer gate should be closed under --type timer, got %q", got)
		}
		if got := readStatus(t, db, prGate.ID); got == types.StatusClosed {
			t.Error("gh:pr gate must be untouched under --type timer")
		}
	})

	t.Run("limit_bounds_gates_checked", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gcl")
		db := openProxiedDB(t, p)
		var ids []string
		for i := 0; i < 3; i++ {
			g := bdProxiedCreate(t, bd, p.dir, "Limit timer gate", "--type", "gate")
			seedGateAwait(t, db, g.ID, "timer", "", longAgo(), expiredTimeout)
			ids = append(ids, g.ID)
		}

		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "check", "--type", "timer", "--limit", "1"); err != nil {
			t.Fatalf("gate check --limit 1 failed: %v\nstderr:\n%s", err, stderr)
		}
		closed := 0
		for _, id := range ids {
			if readStatus(t, db, id) == types.StatusClosed {
				closed++
			}
		}
		if closed > 1 {
			t.Errorf("--limit 1 should close at most 1 gate, closed %d", closed)
		}
	})

	t.Run("timer_escalate_is_noop_and_resolves", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gcx")
		gate := bdProxiedCreate(t, bd, p.dir, "Escalate timer gate", "--type", "gate")
		db := openProxiedDB(t, p)
		seedGateAwait(t, db, gate.ID, "timer", "", longAgo(), expiredTimeout)

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "gate", "check", "--type", "timer", "--escalate")
		if err != nil {
			t.Fatalf("gate check --escalate failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got != types.StatusClosed {
			t.Errorf("expired timer gate should resolve with --escalate, got %q", got)
		}
		if strings.Contains(out, "ESCALATE") {
			t.Errorf("timer gates never escalate; --escalate should be a no-op, got:\n%s", out)
		}
	})
}
