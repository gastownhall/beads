package versioncontrolops

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltinfoenv"
)

// infoBranchLookup is os.LookupEnv(DOLT_REMOTE_INFO_BRANCH) at one statement.
type infoBranchLookup struct {
	value   string
	present bool
}

// infoBranchRecordingConn records DOLT_REMOTE_INFO_BRANCH as each statement
// executes: Dolt reads it when the statement builds the git blobstore.
type infoBranchRecordingConn struct {
	envRecordingConn
	infoBranch []infoBranchLookup
}

func (c *infoBranchRecordingConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	v, ok := os.LookupEnv(doltinfoenv.EnvVar)
	c.infoBranch = append(c.infoBranch, infoBranchLookup{v, ok})
	return c.envRecordingConn.ExecContext(ctx, query, args...)
}

// TestRemoteCallsDisableDoltRemoteInfoBranch is the in-process half of
// GH#6484: every remote statement runs with DOLT_REMOTE_INFO_BRANCH empty, so
// Dolt does not force-push refs/heads/__dolt_remote_info__ into the git
// remote, and the variable is gone again afterwards. A value the user set is
// passed through untouched.
func TestRemoteCallsDisableDoltRemoteInfoBranch(t *testing.T) {
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
			t.Setenv(doltinfoenv.EnvVar, "")
			if err := os.Unsetenv(doltinfoenv.EnvVar); err != nil {
				t.Fatalf("unsetenv: %v", err)
			}
			conn := &infoBranchRecordingConn{}
			if err := tt.call(conn); err != nil {
				t.Fatalf("call: %v", err)
			}
			if len(conn.infoBranch) == 0 {
				t.Fatal("no statement executed; the test proves nothing")
			}
			for i, got := range conn.infoBranch {
				if got != (infoBranchLookup{"", true}) {
					t.Errorf("during %q, %s = %q (present %v), want present and empty",
						conn.queries[i], doltinfoenv.EnvVar, got.value, got.present)
				}
			}
			if v, ok := os.LookupEnv(doltinfoenv.EnvVar); ok {
				t.Errorf("%s left set (%q) after the call returned", doltinfoenv.EnvVar, v)
			}
		})
		t.Run(tt.name+" keeps user value", func(t *testing.T) {
			t.Setenv(doltinfoenv.EnvVar, "dolt-info")
			conn := &infoBranchRecordingConn{}
			if err := tt.call(conn); err != nil {
				t.Fatalf("call: %v", err)
			}
			for i, got := range conn.infoBranch {
				if got != (infoBranchLookup{"dolt-info", true}) {
					t.Errorf("during %q, %s = %q (present %v), want the user's %q",
						conn.queries[i], doltinfoenv.EnvVar, got.value, got.present, "dolt-info")
				}
			}
			if v := os.Getenv(doltinfoenv.EnvVar); v != "dolt-info" {
				t.Errorf("after the call, %s = %q, want the user's %q", doltinfoenv.EnvVar, v, "dolt-info")
			}
		})
	}
}

// TestPullFetchStepDisablesDoltRemoteInfoBranch covers Pull's DOLT_FETCH step,
// the only part of a pull that talks to the remote (see
// TestPullFetchStepDisablesGitHooks).
func TestPullFetchStepDisablesDoltRemoteInfoBranch(t *testing.T) {
	t.Setenv(doltinfoenv.EnvVar, "")
	if err := os.Unsetenv(doltinfoenv.EnvVar); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	conn := &infoBranchRecordingConn{}
	// The merge step fails against this stub conn, after the fetch statement
	// was recorded.
	_ = PullWithStrategy(context.Background(), conn, "origin", "main", "", "")

	if len(conn.infoBranch) == 0 {
		t.Fatal("no statement executed; the test proves nothing")
	}
	if !strings.HasPrefix(conn.queries[0], "CALL DOLT_FETCH") {
		t.Fatalf("first statement = %q, want the DOLT_FETCH step", conn.queries[0])
	}
	if got := conn.infoBranch[0]; got != (infoBranchLookup{"", true}) {
		t.Errorf("during the fetch step, %s = %q (present %v), want present and empty",
			doltinfoenv.EnvVar, got.value, got.present)
	}
	if v, ok := os.LookupEnv(doltinfoenv.EnvVar); ok {
		t.Errorf("%s left set (%q) after the pull returned", doltinfoenv.EnvVar, v)
	}
}
