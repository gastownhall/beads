package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// recordingDropConn records the statements dropStaleDatabases issues. Only
// ExecContext is reachable from that function; the other two methods exist to
// satisfy versioncontrolops.DBConn and fail loudly if that ever changes.
type recordingDropConn struct {
	t       *testing.T
	queries []string
}

var _ versioncontrolops.DBConn = (*recordingDropConn)(nil)

func (c *recordingDropConn) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	c.queries = append(c.queries, query)
	return nil, nil
}

func (c *recordingDropConn) QueryContext(_ context.Context, query string, _ ...any) (*sql.Rows, error) {
	c.t.Fatalf("unexpected QueryContext(%q)", query)
	return nil, nil
}

func (c *recordingDropConn) QueryRowContext(_ context.Context, query string, _ ...any) *sql.Row {
	c.t.Fatalf("unexpected QueryRowContext(%q)", query)
	return nil
}

// TestDropStaleDatabasesQuotesIdentifiers pins the one destructive statement in
// the SHOW DATABASES set to the escaping helper. `bd dolt clean-databases`
// enumerates server database names and DROPs the stale ones, so the name it
// interpolates is whatever created the database, never a bd-minted value. The
// PR this test ships with fixed a sibling probe that had been left on raw
// interpolation precisely because nothing tied its call site to a builder;
// DROP DATABASE is where that mistake would cost the most, and the seam is
// free — dropStaleDatabases already takes versioncontrolops.DBConn as a
// parameter, so no server and no sqlmock are needed. Expectations are exact
// strings, so reverting the call site to raw `%s` interpolation fails the
// backtick case here rather than silently regressing.
func TestDropStaleDatabasesQuotesIdentifiers(t *testing.T) {
	conn := &recordingDropConn{t: t}
	stale := []string{"beads_x", "evil`; DROP TABLE x"}

	dropped, err := dropStaleDatabases(t.Context(), conn, stale)
	if err != nil {
		t.Fatalf("dropStaleDatabases: %v", err)
	}
	if dropped != len(stale) {
		t.Fatalf("dropped = %d, want %d", dropped, len(stale))
	}

	want := []string{
		"DROP DATABASE `beads_x`",
		"DROP DATABASE `evil``; DROP TABLE x`",
	}
	if len(conn.queries) != len(want) {
		t.Fatalf("issued %d statements (%q), want %d", len(conn.queries), conn.queries, len(want))
	}
	for i, wantQuery := range want {
		if conn.queries[i] != wantQuery {
			t.Errorf("statement %d = %q, want %q", i, conn.queries[i], wantQuery)
		}
	}
}
