package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	publicops "github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

// AC-7 of the offline child-id collision repair (GH#4796, bd-28sb25): when a
// repair plan is invalidated, or a rename fails anywhere inside the rename
// transaction, the repair aborts BEFORE its rename commit — none of its
// issue, reference, or counter renames persist, and unrelated user data
// survives untouched. These tests run against a real dolt sql-server (the
// same server-Dolt storage tier as the Compact tests) and drive
// applyAndMergeChildRenumbers directly: the window between planning and
// applying that invalidates a plan cannot be opened through
// PullWithChildRenumbers, which recomputes the plan and applies it in one
// call.

// renumberAbortFixture is one full beads database on a test server holding the
// report fixture: the local leg (children bd-rngr.71/.72/.73, counter 73, and
// an unrelated issue bd-unl from the common ancestor), while remotes/origin/
// main holds the other clone's leg (bd-rngr.71/.72 with distinct content,
// counter 72), common ancestor at counter 70. The single-database rewind dance
// is the embedded fixture's (buildOfflineChildCollision): push one leg, rewind
// the branch to build the other, push, rewind back.
type renumberAbortFixture struct {
	t         *testing.T
	ctx       context.Context
	db        *sql.DB
	conn      *sql.Conn // the pinned session plan and apply run on
	localHead string
}

const renumberAbortAuthor = "tester <tester@example.com>"

func newRenumberAbortFixture(t *testing.T) *renumberAbortFixture {
	t.Helper()
	port := testutil.StartIsolatedDoltContainerHandle(t).Port
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	name := fmt.Sprintf("renumber_abort_%d", time.Now().UnixNano())
	root, err := sql.Open("mysql", "root@tcp(127.0.0.1:"+port+")/")
	if err != nil {
		t.Fatalf("open server: %v", err)
	}
	if _, err := root.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	_ = root.Close()

	// parseTime + multiStatements, as every bd DSN sets them (doltutil:
	// MigrateUp executes whole up.sql files through one ExecContext).
	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/%s?parseTime=true&multiStatements=true", port, name))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin session: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	f := &renumberAbortFixture{t: t, ctx: ctx, db: db, conn: conn}
	// The full beads schema, the same open-time pass the stores run
	// (schema.MigrateUp on the pinned session, per its doc).
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	seed := func(id, title string) {
		f.mustExec("seed "+id, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, title)
	}
	head := func() string {
		var hash string
		if err := conn.QueryRowContext(ctx, "SELECT hash FROM dolt_branches WHERE name = 'main'").Scan(&hash); err != nil {
			t.Fatalf("read branch head: %v", err)
		}
		return hash
	}

	// C0: the shared ancestor — the parent with counter 70 and no children,
	// plus an unrelated issue from before the divergence neither repair path
	// is allowed to disturb.
	seed("bd-rngr", "parent epic")
	seed("bd-unl", "unrelated issue")
	f.mustExec("seed counter 70", "INSERT INTO child_counters (parent_id, last_child) VALUES ('bd-rngr', 70)")
	f.mustExec("commit C0", "CALL DOLT_COMMIT('-Am', 'test: seed ancestor counter 70 (C0)')")
	c0 := head()
	f.mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
	f.mustExec("push C0", "CALL DOLT_PUSH('origin', 'main')")

	// The local clone's leg: children 71-73, counter 73.
	seed("bd-rngr.71", "local child 71")
	seed("bd-rngr.72", "local child 72")
	seed("bd-rngr.73", "local child 73")
	f.mustExec("local counter 73", "UPDATE child_counters SET last_child = 73 WHERE parent_id = 'bd-rngr'")
	f.mustExec("commit local leg", "CALL DOLT_COMMIT('-Am', 'test: local children 71-73')")
	f.localHead = head()

	// The other clone's leg: children 71-72 with distinct content, counter
	// 72, published; then the branch rewinds to the local leg, leaving the
	// diverged shape a failed pull sees.
	f.mustExec("rewind to C0", "CALL DOLT_RESET('--hard', ?)", c0)
	seed("bd-rngr.71", "remote child 71")
	seed("bd-rngr.72", "remote child 72")
	f.mustExec("remote counter 72", "UPDATE child_counters SET last_child = 72 WHERE parent_id = 'bd-rngr'")
	f.mustExec("commit remote leg", "CALL DOLT_COMMIT('-Am', 'test: remote children 71-72')")
	f.mustExec("push remote leg", "CALL DOLT_PUSH('origin', 'main')")
	f.mustExec("rewind to local leg", "CALL DOLT_RESET('--hard', ?)", f.localHead)
	return f
}

func (f *renumberAbortFixture) mustExec(stage string, q string, args ...any) {
	f.t.Helper()
	if _, err := f.conn.ExecContext(f.ctx, q, args...); err != nil {
		f.t.Fatalf("%s: %v", stage, err)
	}
}

// plan computes the repair proposal on the pinned session and asserts it is
// exactly the report fixture's: bd-rngr.71 -> .74 and bd-rngr.72 -> .75,
// above both sides' high-water marks, with .73 untouched.
func (f *renumberAbortFixture) plan() *storage.ChildRenumberPlan {
	f.t.Helper()
	plan, err := PlanChildRenumbers(f.ctx, f.conn, "main", "origin")
	if err != nil {
		f.t.Fatalf("PlanChildRenumbers: %v", err)
	}
	if len(plan.Mappings) != 2 ||
		plan.Mappings[0].OldID != "bd-rngr.71" || plan.Mappings[0].NewID != "bd-rngr.74" || !plan.Mappings[0].Root ||
		plan.Mappings[1].OldID != "bd-rngr.72" || plan.Mappings[1].NewID != "bd-rngr.75" || !plan.Mappings[1].Root {
		f.t.Fatalf("plan mappings = %+v, want bd-rngr.71 -> bd-rngr.74 and bd-rngr.72 -> bd-rngr.75 (roots)", plan.Mappings)
	}
	return plan
}

// head reads main's current head hash.
func (f *renumberAbortFixture) head() string {
	f.t.Helper()
	var hash string
	if err := f.db.QueryRowContext(f.ctx, "SELECT hash FROM dolt_branches WHERE name = 'main'").Scan(&hash); err != nil {
		f.t.Fatalf("read branch head: %v", err)
	}
	return hash
}

// assertUntouched asserts the repair never renamed anything, the parent's
// counter is still the local leg's 73, and the unrelated issue survived.
func (f *renumberAbortFixture) assertUntouched() {
	f.t.Helper()
	titles := map[string]string{
		"bd-rngr":    "parent epic",
		"bd-unl":     "unrelated issue",
		"bd-rngr.71": "local child 71",
		"bd-rngr.72": "local child 72",
		"bd-rngr.73": "local child 73",
	}
	rows, err := f.db.QueryContext(f.ctx, "SELECT id, title FROM issues")
	if err != nil {
		f.t.Fatalf("read issues: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			f.t.Fatalf("scan issue: %v", err)
		}
		got[id] = title
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("read issues: %v", err)
	}
	for id, title := range titles {
		if got[id] != title {
			f.t.Errorf("issues[%q] = %q, want %q; a rename persisted that the repair must have rolled back entirely", id, got[id], title)
		}
	}
	if len(got) != len(titles) {
		f.t.Errorf("issues after the abort = %v, want exactly %v (no .74/.75, nothing dropped)", keysOf(got), keysOf(titles))
	}
	var last int
	if err := f.db.QueryRowContext(f.ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = 'bd-rngr'").Scan(&last); err != nil {
		f.t.Fatalf("read counter: %v", err)
	}
	if last != 73 {
		f.t.Errorf("counter after the abort = %d, want the untouched 73", last)
	}
	var renumbered int
	if err := f.db.QueryRowContext(f.ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'bd: renumber %'").Scan(&renumbered); err != nil {
		f.t.Fatalf("count rename commits: %v", err)
	}
	if renumbered != 0 {
		f.t.Errorf("dolt_log holds %d renumber commit(s); the repair must abort before its rename commit", renumbered)
	}
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestApplyChildRenumbersInvalidatedPlanDeclines (bd-28sb25 AC-7): a plan goes
// stale when the branch advances between planning and applying. The apply half
// must decline the stale plan before opening the rename transaction: no id,
// reference, or counter moves, and neither the rename commit nor any other
// write appears on the branch.
func TestApplyChildRenumbersInvalidatedPlanDeclines(t *testing.T) {
	f := newRenumberAbortFixture(t)
	plan := f.plan()

	// The branch advances: an unrelated edit committed after the plan was
	// computed invalidates every head the plan pinned.
	f.mustExec("unrelated edit", "UPDATE issues SET notes = 'touched after planning' WHERE id = 'bd-unl'")
	f.mustExec("commit unrelated", "CALL DOLT_COMMIT('-Am', 'test: unrelated change after planning')")
	movedHead := f.head()

	result, err := applyAndMergeChildRenumbers(f.ctx, f.conn, plan, "main", renumberAbortAuthor)
	if err == nil {
		t.Fatal("apply of a stale plan succeeded; want the moved-head decline")
	}
	if !strings.Contains(err.Error(), "moved since the repair plan was computed") {
		t.Errorf("decline error = %v, want the moved-head decline naming the re-run", err)
	}
	if result != nil {
		t.Errorf("result = %+v, want nil with the error", result)
	}
	if got := f.head(); got != movedHead {
		t.Errorf("branch head = %s, want %s; the decline must not touch the branch", got, movedHead)
	}
	f.assertUntouched()

	// The unrelated edit survived the declined repair.
	var notes string
	if err := f.db.QueryRowContext(f.ctx, "SELECT notes FROM issues WHERE id = 'bd-unl'").Scan(&notes); err != nil {
		t.Fatalf("read unrelated notes: %v", err)
	}
	if notes != "touched after planning" {
		t.Errorf("unrelated notes = %q, want the post-planning edit preserved", notes)
	}
}

// TestApplyChildRenumbersMidTxFailureRollsBack (bd-28sb25 AC-7): a rename that
// fails INSIDE the rename transaction — the second mapping's destination is
// retired by a tombstone that post-dates the plan — aborts the repair before
// its rename commit. The first mapping's already-executed renames die with the
// transaction: no issue, reference, counter, or even audit-plane write
// persists, and unrelated user data survives.
func TestApplyChildRenumbersMidTxFailureRollsBack(t *testing.T) {
	f := newRenumberAbortFixture(t)
	plan := f.plan()

	// Retire bd-rngr.75 AFTER the plan selected it as a destination: no live
	// issue holds it, so RefuseRetiredIDInTx refuses the rename mid-repair,
	// after bd-rngr.71 -> bd-rngr.74 has already run inside the transaction.
	// wisp_id_tombstones is clone-local and dolt_ignore'd: the insert does
	// not dirty the working set or move the branch.
	if _, err := f.conn.ExecContext(f.ctx,
		"INSERT INTO wisp_id_tombstones (id, closed_at) VALUES ('bd-rngr.75', '2026-01-01 00:00:00')"); err != nil {
		t.Fatalf("retire destination: %v", err)
	}

	result, err := applyAndMergeChildRenumbers(f.ctx, f.conn, plan, "main", renumberAbortAuthor)
	if err == nil {
		t.Fatal("apply with a retired destination succeeded; want the mid-rename refusal")
	}
	var expired *publicops.IdempotencyExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("refusal error = %v, want *IdempotencyExpiredError for the retired destination", err)
	}
	if result != nil {
		t.Errorf("result = %+v, want nil with the error", result)
	}
	if got := f.head(); got != f.localHead {
		t.Errorf("branch head = %s, want the fixture's %s; no rename commit may exist", got, f.localHead)
	}
	f.assertUntouched()

	// Even the rename's audit-plane write inside the transaction (the derived
	// event for the already-renamed bd-rngr.74) died with the rollback: its
	// clone-local table is not committed history, but the rollback still
	// covers it.
	var events int
	if err := f.db.QueryRowContext(f.ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = 'bd-rngr.74'").Scan(&events); err != nil {
		t.Fatalf("count rolled-back audit events: %v", err)
	}
	if events != 0 {
		t.Errorf("events holds %d row(s) for bd-rngr.74; the rename transaction's writes must all roll back", events)
	}
}
