//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestEmbeddedApplyChildRenumbersAbortsWhole is bd-28sb25 AC-7: a rename set
// that goes wrong part way, because the plan went stale (its destination is
// now taken) or because a later rename fails inside the transaction, leaves
// none of its renames, counter moves or rename events behind, and the
// unrelated rows as they were.
func TestEmbeddedApplyChildRenumbersAbortsWhole(t *testing.T) {
	p := childCollisionParent
	cases := []struct {
		name    string
		renames []storage.ChildRenumber
		wantErr string
	}{
		{
			name:    "stale_plan",
			renames: []storage.ChildRenumber{{From: p + ".71", To: p + ".74"}, {From: p + ".72", To: p + ".73"}},
			wantErr: "already exists",
		},
		{
			name: "rename_fails_mid_transaction",
			renames: []storage.ChildRenumber{
				{From: p + ".71", To: p + ".74"},
				{From: p + ".72", To: p + "." + strings.Repeat("9", 300)},
			},
			wantErr: p + ".72",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestEnv(t, "rn")
			ctx := t.Context()
			withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
				seedChildCollision(t, ctx, conn)
				if _, err := conn.ExecContext(ctx, "INSERT INTO child_counters (parent_id, last_child) VALUES (?, 4)", p+".71"); err != nil {
					t.Fatalf("seed P.71 counter: %v", err)
				}
				if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local clone: P.71 counter')"); err != nil {
					t.Fatalf("commit P.71 counter: %v", err)
				}
				before := headHash(t, ctx, conn)

				err := versioncontrolops.ApplyChildRenumbers(ctx, conn, tc.renames, "tester")
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ApplyChildRenumbers = %v, want an error naming %q", err, tc.wantErr)
				}

				wantTitles := map[string]string{p + ".71": "local 71", p + ".72": "local 72", p + ".73": "local 73"}
				if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantTitles) {
					t.Errorf("children = %v, want unchanged %v", got, wantTitles)
				}
				for parent, want := range map[string]int{p: 73, p + ".71": 4} {
					if n, ok := counterValue(t, ctx, conn, parent); !ok || n != want {
						t.Errorf("counter %s = %d (present %v), want %d", parent, n, ok, want)
					}
				}
				if n, ok := counterValue(t, ctx, conn, p+".74"); ok {
					t.Errorf("counter %s.74 = %d persisted", p, n)
				}
				var renamed int
				if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE event_type = 'renamed'").Scan(&renamed); err != nil {
					t.Fatalf("read events: %v", err)
				}
				if renamed != 0 {
					t.Errorf("%d rename events persisted", renamed)
				}
				clean, err := versioncontrolops.WorkingSetClean(ctx, conn)
				if err != nil {
					t.Fatalf("WorkingSetClean: %v", err)
				}
				if !clean {
					t.Error("the aborted renames left the working set dirty")
				}
				if got := headHash(t, ctx, conn); got != before {
					t.Errorf("HEAD moved from %s to %s", before, got)
				}
			})
		})
	}
}
