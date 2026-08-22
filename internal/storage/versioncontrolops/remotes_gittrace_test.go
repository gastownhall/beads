package versioncontrolops

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

// traceRecordingConn records whether GIT_TRACE was visible at the moment each
// statement executed — the instant Dolt would spawn git plumbing for a
// git-protocol remote, and the only point where the scrub matters.
type traceRecordingConn struct {
	queries []string
	seen    []bool // GIT_TRACE set at exec time?
}

func (c *traceRecordingConn) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	c.queries = append(c.queries, query)
	_, ok := os.LookupEnv("GIT_TRACE")
	c.seen = append(c.seen, ok)
	return nil, nil
}

func (c *traceRecordingConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubConn
}

func (c *traceRecordingConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

// TestRemoteCallsScrubStderrGitTrace is the regression test for the
// GIT_TRACE=1 poisoning: Dolt's git blobstore parses object ids out of
// combined stdout+stderr, so stderr-directed tracing corrupts every captured
// value and an embedded push/pull/clone against a git-protocol remote dies
// with "failed to get remote db ... could not be accessed". The operator
// action that triggers it — exporting GIT_TRACE=1 to debug a failing sync —
// is exactly the one that must not break the sync.
//
// Embedded mode reaches every remote operation through this package, so
// asserting here covers the mode the bug was reported against; the guard is
// withRemoteEnvGuards, shared with the GH#4272 hook suppression.
func TestRemoteCallsScrubStderrGitTrace(t *testing.T) {
	tests := []struct {
		name string
		call func(DBConn) error
	}{
		{"Push", func(db DBConn) error { return Push(context.Background(), db, "origin", "main", "") }},
		{"ForcePush", func(db DBConn) error { return ForcePush(context.Background(), db, "origin", "main", "") }},
		{"Fetch", func(db DBConn) error { return Fetch(context.Background(), db, "peer", "") }},
		{"Clone", func(db DBConn) error {
			return DoltClone(context.Background(), db, "https://example.com/r.git", "beads", "")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GIT_TRACE", "1")

			conn := &traceRecordingConn{}
			if err := tt.call(conn); err != nil {
				t.Fatalf("call: %v", err)
			}
			if len(conn.seen) == 0 {
				t.Fatal("no statement executed; the test proves nothing")
			}
			for i, wasSet := range conn.seen {
				if wasSet {
					t.Errorf("during %q, GIT_TRACE was still set; stderr trace output corrupts Dolt's plumbing parsing", conn.queries[i])
				}
			}
			if got := os.Getenv("GIT_TRACE"); got != "1" {
				t.Errorf("after the call, GIT_TRACE = %q, want %q restored", got, "1")
			}
		})
	}
}

// TestPullFetchStepScrubsStderrGitTrace covers Pull's fetch step, the only
// part of Pull that talks to the remote (mirrors the githooks sibling test).
func TestPullFetchStepScrubsStderrGitTrace(t *testing.T) {
	t.Setenv("GIT_TRACE", "1")

	conn := &traceRecordingConn{}
	// The merge step fails against this stub conn; the fetch statement has
	// already been recorded by then, which is all this test asserts.
	_ = PullWithStrategy(context.Background(), conn, "origin", "main", "", "")

	if len(conn.seen) == 0 {
		t.Fatal("no statement executed; the test proves nothing")
	}
	if conn.seen[0] {
		t.Errorf("during %q, GIT_TRACE was still set", conn.queries[0])
	}
}
