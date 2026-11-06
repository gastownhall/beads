package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// These tests run Flatten against a real dolt sql-server. The clone-local
// fixture shapes a database like a beads store's working set: tracked tables
// plus dolt_ignored tables carrying the eight clone-local foreign keys of
// fix.CloneLocalFKs, whose enforcement DOLT_RESET('--hard') silently drops
// (bd-7bpkd). The compact fixture (compact_softreset_dolt_test.go) covers
// the t/u/config shape Flatten's other guarantees are pinned on.

// countCloneLocalFKs counts the FK constraints on the fixture's clone-local
// tables, the same plane information_schema the doctor's scan reads.
const countCloneLocalFKs = `SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
	WHERE TABLE_SCHEMA = DATABASE() AND CONSTRAINT_TYPE = 'FOREIGN KEY'
	  AND (TABLE_NAME = 'events' OR TABLE_NAME LIKE 'wisp\_%')`

// flattenFKFixture is one database on a test server with the eight
// clone-local FKs enforced and two commits of history beyond the initial
// commit: 'schema' and 'issue 3'.
type flattenFKFixture struct {
	t   *testing.T
	ctx context.Context
	db  *sql.DB
}

func newFlattenFKFixture(t *testing.T, port string) *flattenFKFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	name := fmt.Sprintf("flatten_%d", time.Now().UnixNano())
	root, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/", port))
	if err != nil {
		t.Fatalf("open server: %v", err)
	}
	defer root.Close()
	if _, err := root.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%s)/%s?parseTime=true", port, name))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	f := &flattenFKFixture{t: t, ctx: ctx, db: db}
	f.exec(db,
		"CREATE TABLE issues (id INT PRIMARY KEY)",
		"CREATE TABLE wisps (id INT PRIMARY KEY)",
		"CREATE TABLE events (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"CONSTRAINT fk_events_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_dependencies (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"depends_on_wisp_id INT, depends_on_issue_id INT, "+
			"CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE, "+
			"CONSTRAINT fk_wisp_dep_wisp_target FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps(id) ON DELETE CASCADE, "+
			"CONSTRAINT fk_wisp_dep_issue_target FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_labels (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"CONSTRAINT fk_wisp_labels_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_comments (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"CONSTRAINT fk_wisp_comments_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_events (id INT PRIMARY KEY, issue_id INT NOT NULL, "+
			"CONSTRAINT fk_wisp_events_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE)",
		"CREATE TABLE wisp_child_counters (parent_id INT PRIMARY KEY, "+
			"CONSTRAINT fk_wisp_child_counters_parent FOREIGN KEY (parent_id) REFERENCES wisps(id) ON DELETE CASCADE)",
		// Registered before the first commit, so every FK-carrying table but
		// issues stays clone-local: rows and constraints live in the working
		// set only, exactly the plane a hard reset severs them from.
		"INSERT INTO dolt_ignore VALUES ('wisps', true), ('wisp_%', true), ('events', true)",
		"INSERT INTO issues VALUES (1), (2)",
		"INSERT INTO wisps VALUES (10)",
		"INSERT INTO events VALUES (100, 1)",
		"INSERT INTO wisp_labels VALUES (200, 10)",
		"CALL DOLT_COMMIT('-Am', 'schema')",
		"INSERT INTO issues VALUES (3)",
		"CALL DOLT_COMMIT('-Am', 'issue 3')",
	)
	return f
}

func (f *flattenFKFixture) exec(conn DBConn, queries ...string) {
	f.t.Helper()
	for _, q := range queries {
		if _, err := conn.ExecContext(f.ctx, q); err != nil {
			f.t.Fatalf("%s: %v", q, err)
		}
	}
}

// count runs a COUNT(*) query on the shared pool (main's working set).
func (f *flattenFKFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (f *flattenFKFixture) value(query string) string {
	f.t.Helper()
	var v string
	if err := f.db.QueryRowContext(f.ctx, query).Scan(&v); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
	return v
}

// conn pins a session of its own, as a second writer or as Flatten's
// connection.
func (f *flattenFKFixture) conn() *sql.Conn {
	f.t.Helper()
	c, err := f.db.Conn(f.ctx)
	if err != nil {
		f.t.Fatalf("pin connection: %v", err)
	}
	f.t.Cleanup(func() { _ = c.Close() })
	return c
}

// wantNoTempBranch fails while a flatten-tmp is still around.
func (f *flattenFKFixture) wantNoTempBranch() {
	f.t.Helper()
	if n := f.count("SELECT COUNT(*) FROM dolt_branches WHERE name = 'flatten-tmp'"); n != 0 {
		f.t.Fatalf("flatten-tmp still exists after Flatten")
	}
}

// TestFlattenKeepsCloneLocalFKs: the flatten ends in a reset of main onto
// the flattened branch; as a hard reset that drops every FK on the
// clone-local tables (bd-7bpkd), as a soft reset it keeps them enforcing
// and cascading, with the tracked snapshot unchanged and the history
// squashed to the initial commit plus one snapshot commit.
func TestFlattenKeepsCloneLocalFKs(t *testing.T) {
	port := startCompactTestServer(t)
	f := newFlattenFKFixture(t, port)

	before := f.count(countCloneLocalFKs)
	if before != 8 {
		f.t.Fatalf("clone-local FKs before flatten = %d, want 8", before)
	}
	preHead := f.value("SELECT HASHOF('HEAD')")

	if err := Flatten(f.ctx, f.conn()); err != nil {
		f.t.Fatalf("Flatten: %v", err)
	}

	if after := f.count(countCloneLocalFKs); after != before {
		f.t.Errorf("clone-local FKs after flatten = %d, want %d", after, before)
	}
	if n := f.count("SELECT COUNT(*) FROM dolt_log"); n != 2 {
		f.t.Errorf("commits after flatten = %d, want 2 (initial + flattened snapshot)", n)
	}
	if n := f.count("SELECT COUNT(*) FROM dolt_diff_summary(?, 'HEAD')", preHead); n != 0 {
		f.t.Errorf("flattened HEAD differs from the pre-flatten HEAD in %d tables, want the same tree", n)
	}
	f.wantNoTempBranch()

	// The kept FKs still enforce: an events row cannot name a missing issue…
	if _, err := f.db.ExecContext(f.ctx,
		"INSERT INTO events (id, issue_id) VALUES (999, 888)"); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		f.t.Errorf("insert of an events row for a missing issue = %v, want a foreign key violation", err)
	}
	// …still cascade over the tracked plane…
	f.exec(f.db, "DELETE FROM issues WHERE id = 1")
	if n := f.count("SELECT COUNT(*) FROM events WHERE issue_id = 1"); n != 0 {
		f.t.Errorf("events for deleted issue 1 = %d, want 0 (fk_events_issue must still cascade)", n)
	}
	// …and over the clone-local plane.
	f.exec(f.db, "DELETE FROM wisps WHERE id = 10")
	if n := f.count("SELECT COUNT(*) FROM wisp_labels WHERE issue_id = 10"); n != 0 {
		f.t.Errorf("wisp_labels for deleted wisp 10 = %d, want 0 (fk_wisp_labels_issue must still cascade)", n)
	}
}
