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
	"github.com/steveyegge/beads/internal/storage/issueops"
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
// session's identity, and their original author and author date, including
// a commit whose author and committer differ.
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
	// "edit A" and the empty commit sit in the same second 333 ms apart, and
	// asOf falls strictly between the two non-empty edits.
	S := time.Now().UTC().Truncate(time.Second)
	editA := S.Add(20 * time.Minute).Add(123 * time.Millisecond)
	emptyMarker := S.Add(20 * time.Minute).Add(456 * time.Millisecond)
	editB := S.Add(40 * time.Minute)
	asOf := S.Add(30 * time.Minute)

	// What each kept commit is created with, and must still carry after the
	// compaction. Date is the committer date, the one dolt_log.date, AS OF
	// and dolt_history read.
	type commitMeta struct {
		Committer, Email    string
		Date                time.Time
		Author, AuthorEmail string
		AuthorDate          time.Time
	}
	byOriginalAuthor := func(at time.Time) commitMeta {
		return commitMeta{"Original Author", "orig@example.com", at, "Original Author", "orig@example.com", at}
	}
	wantMeta := map[string]commitMeta{
		"repro: edit A": byOriginalAuthor(editA),
		"repro: marker": byOriginalAuthor(emptyMarker),
		// Committed by someone other than its author, five minutes after it
		// was authored: the shape `dolt cherry-pick` and an earlier
		// `bd compact` leave behind.
		"repro: edit B": {"Replayer", "replayer@example.com", editB,
			"Original Author", "orig@example.com", editB.Add(-5 * time.Minute)},
	}
	sameMeta := func(a, b commitMeta) bool {
		return a.Committer == b.Committer && a.Email == b.Email && a.Date.Equal(b.Date) &&
			a.Author == b.Author && a.AuthorEmail == b.AuthorEmail && a.AuthorDate.Equal(b.AuthorDate)
	}

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

	metaOf := func(msg string) commitMeta {
		var m commitMeta
		if err := db.QueryRowContext(t.Context(),
			"SELECT committer, email, date, author, author_email, author_date FROM dolt_log WHERE message = ?", msg,
		).Scan(&m.Committer, &m.Email, &m.Date, &m.Author, &m.AuthorEmail, &m.AuthorDate); err != nil {
			t.Fatalf("read commit %q: %v", msg, err)
		}
		return m
	}

	// commitAt commits as wantMeta[msg] says: the author and author date
	// through --author and --date, the committer and commit date through
	// the session's committer variables, on one pinned connection that
	// clears them again before it goes back to the pool.
	commitAt := func(msg, title, externalRef string) {
		m := wantMeta[msg]
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatalf("db.Conn: %v", err)
		}
		defer conn.Close()
		exec := func(q string, args ...interface{}) {
			if _, err := conn.ExecContext(t.Context(), q, args...); err != nil {
				t.Fatalf("commit %q: %s: %v", msg, q, err)
			}
		}
		if title != "" {
			exec("UPDATE issues SET title = ?, external_ref = ? WHERE id = ?", title, externalRef, issue.ID)
		}
		exec("SET @@dolt_committer_name = ?, @@dolt_committer_email = ?, @@dolt_committer_date = ?",
			m.Committer, m.Email, m.Date.UTC().Format("2006-01-02T15:04:05.000Z07:00"))
		exec("CALL DOLT_COMMIT('-Am', ?, '--date', ?, '--allow-empty', '--author', ?)",
			msg, m.AuthorDate.UTC().Format("2006-01-02T15:04:05.000Z07:00"), m.Author+" <"+m.AuthorEmail+">")
		exec("SET @@dolt_committer_name = '', @@dolt_committer_email = '', @@dolt_committer_date = ''")
	}
	commitAt("repro: edit A", "edit A", "A")
	commitAt("repro: marker", "", "")
	commitAt("repro: edit B", "edit B", "B")

	// The setup above must actually produce commits with both identities and
	// millisecond dates as intended, or the assertions below would report on
	// a harness artifact, not on compaction.
	for msg, want := range wantMeta {
		if got := metaOf(msg); !sameMeta(got, want) {
			t.Fatalf("setup: commit %q stored %+v, want %+v (DOLT_COMMIT dropped precision or an identity)", msg, got, want)
		}
	}

	// Dolt parses an AS OF TIMESTAMP literal as a session-zone wall time;
	// confirm this session reads the UTC literal as the intended instant
	// before relying on it (resolveAsOf treats a bare string as a ref, so
	// the timestamp must be a literal, not a bind parameter).
	tLiteral := asOf.UTC().Format("2006-01-02 15:04:05")
	var parsed time.Time
	if err := db.QueryRowContext(t.Context(), "SELECT CAST(? AS DATETIME)", tLiteral).Scan(&parsed); err != nil {
		t.Fatalf("parse probe %q: %v", tLiteral, err)
	}
	if !parsed.Equal(asOf) {
		t.Fatalf("session reads TIMESTAMP literal %q as %s, want %s (session time zone is not UTC)", tLiteral, parsed, asOf)
	}

	// Precondition: before compaction, the time between the two edits already
	// resolves to edit A's state; the assertions below pin that compaction
	// does not change that.
	if got := oneString("SELECT title FROM issues AS OF TIMESTAMP('" + tLiteral + "') WHERE id = '" + issue.ID + "'"); got != "edit A" {
		t.Fatalf("precondition: issues as of %s = %q, want %q", tLiteral, got, "edit A")
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
		for msg, want := range wantMeta {
			if got := oneTime("SELECT date FROM dolt_log WHERE message = ?", msg); !got.Equal(want.Date) {
				t.Errorf("replayed commit %q dated %s after compact, want its original date %s", msg, got.UTC(), want.Date)
			}
		}
	})

	// AC-2: each replayed commit keeps its original committer and email
	// rather than the SQL session's identity, and its original author and
	// author date, which the replay must not overwrite with the committer's.
	t.Run("replayed committer and author", func(t *testing.T) {
		for msg, want := range wantMeta {
			if got := metaOf(msg); !sameMeta(got, want) {
				t.Errorf("replayed commit %q carries %+v after compact, want its original %+v", msg, got, want)
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

	// AC-4: a timestamp between the two kept edits resolves to edit A's
	// state, as it did before the compaction — both through dolt's AS OF
	// and through the dolt_history_issues lookup behind
	// issueops.PreviousExternalRefInTx.
	t.Run("as-of reads resolve to the same commit", func(t *testing.T) {
		if got := oneString("SELECT title FROM issues AS OF TIMESTAMP('" + tLiteral + "') WHERE id = '" + issue.ID + "'"); got != "edit A" {
			t.Errorf("issues as of %s = %q after compact, want %q", tLiteral, got, "edit A")
		}
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer tx.Rollback()
		ref, found, err := issueops.PreviousExternalRefInTx(t.Context(), tx, issue.ID, asOf)
		if err != nil {
			t.Fatalf("PreviousExternalRefInTx: %v", err)
		}
		if !found || ref != "A" {
			t.Errorf("previous external_ref as of %s = (%q, %v), want (%q, true)", asOf, ref, found, "A")
		}
	})
}
