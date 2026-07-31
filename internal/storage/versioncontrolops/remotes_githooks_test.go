package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/githooksenv"
)

// envRecordingConn records the GIT_CONFIG_PARAMETERS value visible at the
// moment each statement executes. That is the instant Dolt would spawn git for
// a git-protocol remote, so it is the only point where the override matters.
type envRecordingConn struct {
	queries []string
	seen    []string
}

func (c *envRecordingConn) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	c.queries = append(c.queries, query)
	c.seen = append(c.seen, os.Getenv(githooksenv.ParametersEnv))
	return nil, nil
}

// QueryContext always errors: nothing here needs to read rows, and returning a
// nil *sql.Rows would panic in the caller instead of failing cleanly.
func (c *envRecordingConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errStubConn
}

var errStubConn = errors.New("envRecordingConn: queries are not supported")

func (c *envRecordingConn) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

// TestRemoteCallsDisableGitHooks is the regression test for GH#4272. PR #3740
// disabled hooks only on the CLI shell-out fallback, but the primary path is
// these in-process CALL DOLT_PUSH/DOLT_FETCH statements: Dolt shells out to git
// against .dolt/git-remote-cache/<hash>/repo.git, and a templated pre-push hook
// there kills the push with "fatal: this operation must be run in a work tree".
//
// Embedded mode reaches every remote operation through this package, so
// asserting here covers the mode the bug was reported against.
func TestRemoteCallsDisableGitHooks(t *testing.T) {
	tests := []struct {
		name string
		call func(DBConn) error
	}{
		{"Push", func(db DBConn) error { return Push(context.Background(), db, "origin", "main", "") }},
		{"Push with user", func(db DBConn) error { return Push(context.Background(), db, "origin", "main", "u") }},
		{"ForcePush", func(db DBConn) error { return ForcePush(context.Background(), db, "origin", "main", "") }},
		{"Fetch", func(db DBConn) error { return Fetch(context.Background(), db, "peer", "") }},
		{"Clone", func(db DBConn) error {
			return DoltClone(context.Background(), db, "https://example.com/r.git", "beads", "")
		}},
		{"Clone with user", func(db DBConn) error {
			return DoltClone(context.Background(), db, "https://example.com/r.git", "beads", "u")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.Unsetenv(githooksenv.ParametersEnv); err != nil {
				t.Fatalf("unsetenv: %v", err)
			}
			t.Cleanup(func() { _ = os.Unsetenv(githooksenv.ParametersEnv) })

			conn := &envRecordingConn{}
			if err := tt.call(conn); err != nil {
				t.Fatalf("call: %v", err)
			}
			if len(conn.seen) == 0 {
				t.Fatal("no statement executed; the test proves nothing")
			}
			for i, got := range conn.seen {
				if !strings.Contains(got, githooksenv.NoHooksParam) {
					t.Errorf("during %q, %s = %q, want it to contain %q",
						conn.queries[i], githooksenv.ParametersEnv, got, githooksenv.NoHooksParam)
				}
			}
			if _, ok := os.LookupEnv(githooksenv.ParametersEnv); ok {
				t.Errorf("%s left set after the call returned", githooksenv.ParametersEnv)
			}
		})
	}
}

// TestPullFetchStepDisablesGitHooks covers Pull separately: only its fetch step
// talks to the remote, and the merge that follows is local, so the assertion is
// scoped to the DOLT_FETCH statement. Pull is not in the table above because
// its merge step needs a real engine.
func TestPullFetchStepDisablesGitHooks(t *testing.T) {
	if err := os.Unsetenv(githooksenv.ParametersEnv); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(githooksenv.ParametersEnv) })

	conn := &envRecordingConn{}
	// The merge step will fail against this stub conn; the fetch statement has
	// already been recorded by then, which is all this test asserts.
	_ = PullWithStrategy(context.Background(), conn, "origin", "main", "", "")

	if len(conn.seen) == 0 {
		t.Fatal("no statement executed; the test proves nothing")
	}
	if !strings.HasPrefix(conn.queries[0], "CALL DOLT_FETCH") {
		t.Fatalf("first statement = %q, want the DOLT_FETCH step", conn.queries[0])
	}
	if !strings.Contains(conn.seen[0], githooksenv.NoHooksParam) {
		t.Errorf("during the fetch step, %s = %q, want it to contain %q",
			githooksenv.ParametersEnv, conn.seen[0], githooksenv.NoHooksParam)
	}
}
