//go:build cgo

package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedCompactKeepsReplayedCommitDates reproduces #7294: Compact
// replays the kept window with DOLT_CHERRY_PICK, which stamps each replayed
// commit with the time of the replay. The preserved commits must keep their
// original dates, or every timestamp recorded before the compaction (a
// tracker's last_sync) predates the rewritten history.
func TestEmbeddedCompactKeepsReplayedCommitDates(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "kd")
	issue := bdCreate(t, bd, dir, "Dated bead", "--type", "task")
	bdCreate(t, bd, dir, "Filler bead", "--type", "task")

	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	openConn := func() (func(), func(query string, args ...interface{}) string) {
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		closeFn := func() {
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
		}
		query := func(q string, args ...interface{}) string {
			var s string
			if err := db.QueryRowContext(t.Context(), q, args...).Scan(&s); err != nil {
				t.Fatalf("query %q: %v", q, err)
			}
			return s
		}
		return closeFn, query
	}

	// Dated in the future so --days 0 keeps both commits in the replay window.
	now := time.Now().UTC().Truncate(time.Second)
	edits := []struct {
		title string
		date  time.Time
	}{
		{"edit A", now.Add(20 * time.Minute)},
		{"edit B", now.Add(40 * time.Minute)},
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	for _, e := range edits {
		if _, err := conn.ExecContext(t.Context(), "UPDATE issues SET title = ? WHERE id = ?", e.title, issue.ID); err != nil {
			t.Fatalf("update title %q: %v", e.title, err)
		}
		if _, err := conn.ExecContext(t.Context(), "CALL DOLT_COMMIT('-Am', ?, '--date', ?)",
			"repro: "+e.title, e.date.Format(time.RFC3339)); err != nil {
			t.Fatalf("dolt commit %q: %v", e.title, err)
		}
	}
	conn.Close()
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	bdCompact(t, bd, dir, "--force", "--days", "0")

	closeFn, query := openConn()
	defer closeFn()
	for _, e := range edits {
		raw := query("SELECT DATE_FORMAT(date, '%Y-%m-%d %H:%i:%s') FROM dolt_log WHERE message = ?", "repro: "+e.title)
		got, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.UTC)
		if err != nil {
			t.Fatalf("parse date %q: %v", raw, err)
		}
		if d := got.Sub(e.date); d < -time.Second || d > time.Second {
			t.Errorf("replayed commit %q dated %s after compact, want its original date %s", e.title, got, e.date)
		}
	}
}

// TestEmbeddedCompactKeepsCommitDates is the strengthened #7294 repro: a
// store whose kept window holds commits with original dates — two of them in
// the same second 333 ms apart, one of those empty — survives
// `bd compact --force --days 0` with every replayed commit's date intact, to
// the millisecond, and the kept window in its original order. The replayed
// commits also keep their original committer and email, not the SQL
// session's identity.
func TestEmbeddedCompactKeepsCommitDates(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "kx")
	issue := bdCreate(t, bd, dir, "Dated bead", "--type", "task")
	bdCreate(t, bd, dir, "Filler bead", "--type", "task")

	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	openDB := func() (*sql.DB, func()) {
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		closeFn := func() {
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
		}
		return db, closeFn
	}

	// All three kept commits are dated in the future so --days 0 keeps them;
	// "edit A" and the empty commit sit in the same second 333 ms apart.
	S := time.Now().UTC().Truncate(time.Second)
	editA := S.Add(20 * time.Minute).Add(123 * time.Millisecond)
	emptyMarker := S.Add(20 * time.Minute).Add(456 * time.Millisecond)
	editB := S.Add(40 * time.Minute)

	// No defer on the first connection: bdCompact's subprocess needs the
	// store's lock, so this connection closes before it runs and reopens
	// after; leaking it on a setup failure only wastes the temp dir.
	db, closeDB := openDB()
	oneTime := func(q string, args ...interface{}) time.Time {
		var v time.Time
		if err := db.QueryRowContext(t.Context(), q, args...).Scan(&v); err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		return v
	}
	oneString := func(q string, args ...interface{}) string {
		var s string
		if err := db.QueryRowContext(t.Context(), q, args...).Scan(&s); err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		return s
	}
	firstColumn := func(q string) []string {
		rows, err := db.QueryContext(t.Context(), q)
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatalf("scan %q: %v", q, err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows %q: %v", q, err)
		}
		return out
	}

	// The boundary commit is the HEAD at this point: every commit made so far
	// is dated at or before real-now, so --days 0 keeps the three
	// future-dated commits below and squashes everything else.
	boundaryDate := oneTime("SELECT date FROM dolt_log ORDER BY date DESC LIMIT 1")

	commitAt := func(date time.Time, msg string, title, externalRef string) {
		if title != "" {
			if _, err := db.ExecContext(t.Context(), "UPDATE issues SET title = ?, external_ref = ? WHERE id = ?",
				title, externalRef, issue.ID); err != nil {
				t.Fatalf("update for %q: %v", msg, err)
			}
		}
		if _, err := db.ExecContext(t.Context(),
			"CALL DOLT_COMMIT('-Am', ?, '--date', ?, '--allow-empty', '--author', 'Original Author <orig@example.com>')",
			msg, date.UTC().Format("2006-01-02T15:04:05.000Z07:00")); err != nil {
			t.Fatalf("dolt commit %q: %v", msg, err)
		}
	}
	commitAt(editA, "repro: edit A", "edit A", "A")
	commitAt(emptyMarker, "repro: marker", "", "")
	commitAt(editB, "repro: edit B", "edit B", "B")

	// The setup above must actually produce millisecond-dated commits, or the
	// assertions below would report on a harness artifact, not on compaction.
	for _, e := range []struct {
		msg     string
		orgDate time.Time
	}{
		{"repro: edit A", editA},
		{"repro: marker", emptyMarker},
		{"repro: edit B", editB},
	} {
		got := oneTime("SELECT date FROM dolt_log WHERE message = ?", e.msg)
		if !got.Equal(e.orgDate) {
			t.Fatalf("setup: commit %q stored date %s, want %s (DOLT_COMMIT --date dropped precision)", e.msg, got.UTC(), e.orgDate)
		}
	}

	closeDB()

	bdCompact(t, bd, dir, "--force", "--days", "0")

	db, closeDB = openDB()
	defer closeDB()

	// AC-1: every replayed commit keeps its original date, to the
	// millisecond, and the kept window keeps its order (dolt_log is newest
	// date first).
	t.Run("replayed dates and order", func(t *testing.T) {
		msgs := firstColumn("SELECT message FROM dolt_log ORDER BY date DESC LIMIT 3")
		want := []string{"repro: edit B", "repro: marker", "repro: edit A"}
		if len(msgs) != len(want) {
			t.Fatalf("dolt_log has %d recent messages, want %d: %v", len(msgs), len(want), msgs)
		}
		for i, w := range want {
			if msgs[i] != w {
				t.Errorf("recent dolt_log row %d = %q, want %q", i, msgs[i], w)
			}
		}
		for _, e := range []struct {
			msg     string
			orgDate time.Time
		}{
			{"repro: edit A", editA},
			{"repro: marker", emptyMarker},
			{"repro: edit B", editB},
		} {
			got := oneTime("SELECT date FROM dolt_log WHERE message = ?", e.msg)
			if !got.Equal(e.orgDate) {
				t.Errorf("replayed commit %q dated %s after compact, want its original date %s", e.msg, got.UTC(), e.orgDate)
			}
		}
	})

	// AC-2: each replayed commit keeps its original committer, email and
	// message rather than the SQL session's identity.
	t.Run("replayed committer and message", func(t *testing.T) {
		for _, msg := range []string{"repro: edit A", "repro: marker", "repro: edit B"} {
			committer, email := oneString("SELECT committer FROM dolt_log WHERE message = ?", msg),
				oneString("SELECT email FROM dolt_log WHERE message = ?", msg)
			if committer != "Original Author" || email != "orig@example.com" {
				t.Errorf("replayed commit %q has committer %q <%s>, want %q <%s>",
					msg, committer, email, "Original Author", "orig@example.com")
			}
		}
	})

	// AC-3: the squashed base is dated as the boundary commit, so AS OF times
	// between the boundary and the first kept commit still resolve to the
	// pre-compaction state.
	t.Run("squashed base dated as boundary", func(t *testing.T) {
		got := oneTime("SELECT date FROM dolt_log WHERE message LIKE 'compact: squash%'")
		if !got.Equal(boundaryDate) {
			t.Errorf("squashed base dated %s, want the boundary commit's date %s", got.UTC(), boundaryDate.UTC())
		}
	})
}
