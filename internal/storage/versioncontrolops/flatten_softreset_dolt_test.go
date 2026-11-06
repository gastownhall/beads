package versioncontrolops

import (
	"testing"
)

// These tests run Flatten against a real dolt sql-server on compact's
// fixture and hook connection. Flatten's final step, like Compact's, must
// keep what main holds beyond the snapshot it built: writes in main's working
// set and a commit that reaches main after flatten-tmp was built. Its old
// DOLT_RESET('--hard', 'flatten-tmp') discarded both.

const (
	flattenSnapshotMsg = "flatten: squash all history into single commit"
	flattenFollowupMsg = "flatten: commit changes that reached main during flatten"
)

// flattenedLog is main's history after a flatten that added no follow-up
// commit, newest first.
var flattenedLog = []string{flattenSnapshotMsg, "Initialize data repository"}

// flatten runs Flatten on hook, pinning a connection of its own if unset.
func (f *compactFixture) flatten(hook *compactHookConn) error {
	f.t.Helper()
	if hook.Conn == nil {
		hook.Conn = f.conn()
	}
	return Flatten(f.ctx, hook)
}

func (f *compactFixture) wantNoFlattenBranch() {
	f.t.Helper()
	if n := f.count("SELECT COUNT(*) FROM dolt_branches WHERE name = 'flatten-tmp'"); n != 0 {
		f.t.Fatalf("flatten-tmp still exists after Flatten")
	}
}

// wantLandedKept checks landRow's row is in main's HEAD through Flatten's
// follow-up commit, and that the landed commit's own hash and message are not
// in main's history.
func (f *compactFixture) wantLandedKept(landed string) {
	f.t.Helper()
	if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
		f.t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
	}
	f.wantHeadTree(landed)
	if n := f.count("SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ? OR message = 'landed 600'", landed); n != 0 {
		f.t.Fatalf("main's history still holds the landed commit, want only its rows kept")
	}
}

// AC-2: changes in main's working set that no Dolt commit holds survive
// Flatten, stay out of HEAD and stay pending.
func TestFlattenKeepsUncommittedChanges(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("written before flatten", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)

		if err := f.flatten(&compactHookConn{}); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantPendingKept()
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		f.wantStatus("config:false", "u:false")
		f.wantNoFlattenBranch()
	})

	t.Run("written just before the final reset", func(t *testing.T) {
		f := newCompactFixture(t, port)
		writer := f.conn()

		hook := &compactHookConn{beforeExec: onceBeforeFinalReset(func() { f.writePending(writer) })}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantPendingKept()
		f.wantLog(flattenedLog)
		f.wantHeadTree(f.head)
		f.wantStatus("config:false", "u:false")
	})
}

// AC-2: a commit that reaches main after flatten-tmp was built is in main's
// HEAD through one preservation commit, without its own hash or message,
// while working-only changes stay pending.
func TestFlattenKeepsCommitLandingBeforeMainMoves(t *testing.T) {
	port := startCompactTestServer(t)

	t.Run("landing commit", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		var landed string
		hook := &compactHookConn{beforeExec: onceBeforeFinalReset(func() { landed = f.landRow(writer) })}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		if hook.followUps != 1 {
			t.Fatalf("Flatten issued %d preservation commits, want 1", hook.followUps)
		}
		f.wantLog(append([]string{flattenFollowupMsg}, flattenedLog...))
		f.wantLandedKept(landed)
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoFlattenBranch()
	})

	t.Run("another writer commits the staged rows first", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.writePending(f.db)
		writer := f.conn()

		hook := &compactHookConn{
			beforeExec: onceBeforeFinalReset(func() { f.landRow(writer) }),
			beforeFollowUp: func() {
				f.exec(writer, "CALL DOLT_COMMIT('-m', 'writer took staging')")
			},
		}
		if err := f.flatten(hook); err != nil {
			t.Fatalf("Flatten: %v (a nothing-to-commit preservation commit must be benign)", err)
		}
		if hook.followUps != 1 {
			t.Fatalf("Flatten issued %d preservation commits, want 1", hook.followUps)
		}
		f.wantLog(append([]string{"writer took staging"}, flattenedLog...))
		if n := f.count("SELECT COUNT(*) FROM t AS OF 'HEAD' WHERE id = 600"); n != 1 {
			t.Fatalf("landed t row 600 in main's HEAD: present=false, want present=true")
		}
		f.wantPendingKept()
		f.wantStatus("config:false", "u:false")
		f.wantNoFlattenBranch()
	})

	t.Run("pre-staged row joins the preservation commit", func(t *testing.T) {
		f := newCompactFixture(t, port)
		f.exec(f.db,
			"INSERT INTO u VALUES (800, 'staged')",
			"CALL DOLT_ADD('u')",
			"UPDATE u SET v = 'edited' WHERE id = 1",
		)

		if err := f.flatten(&compactHookConn{}); err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		f.wantLog(append([]string{flattenFollowupMsg}, flattenedLog...))
		if n := f.count("SELECT COUNT(*) FROM u AS OF 'HEAD' WHERE id = 800"); n != 1 {
			t.Fatalf("pre-staged u row 800 in main's HEAD: present=false, want present=true")
		}
		if v := f.value("SELECT v FROM u AS OF 'HEAD' WHERE id = 1"); v != "recent" {
			t.Fatalf("u row 1 in main's HEAD = %q, want the unstaged edit left out", v)
		}
		if v := f.value("SELECT v FROM u WHERE id = 1"); v != "edited" {
			t.Fatalf("u row 1 on main = %q, want the unstaged edit kept", v)
		}
		f.wantStatus("u:false")
	})
}
