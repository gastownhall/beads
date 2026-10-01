package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	publicops "github.com/steveyegge/beads/issueops"
)

type detailBatchCountingDBTX struct {
	storageops.DBTX
	statements int
}

func (c *detailBatchCountingDBTX) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.statements++
	return c.DBTX.QueryContext(ctx, query, args...)
}
func (c *detailBatchCountingDBTX) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.statements++
	return c.DBTX.QueryRowContext(ctx, query, args...)
}
func (c *detailBatchCountingDBTX) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.statements++
	return c.DBTX.ExecContext(ctx, query, args...)
}

// Real SQL and fixed fan-out (one shared target per subject) isolate the
// subject chunk count from far-end hydration. 10, 199 and 200 use the same reads;
// 201 requires two chunks, not 201 per-subject statements.
func TestDetailBatchStatementCount(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	const target = "test-dbr-count-target"
	at := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	insert := "INSERT INTO issues (id,title,description,design,acceptance_criteria,notes,status,priority,issue_type,created_at,updated_at) VALUES (?,?,'','','','','open',2,'task',?,?)"
	if _, err := store.db.ExecContext(ctx, insert, target, target, at, at); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 201)
	for i := range ids {
		ids[i] = fmt.Sprintf("test-dbr-count-%03d", i)
		if _, err := store.db.ExecContext(ctx, insert, ids[i], ids[i], at, at); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, "INSERT INTO dependencies (id,issue_id,depends_on_issue_id,type,created_at,created_by) VALUES (?,?,?,'related',?,'seed')", ids[i]+"-edge", ids[i], target, at); err != nil {
			t.Fatal(err)
		}
	}
	var baseline int
	for _, n := range []int{10, 199, 200, 201} {
		var count int
		err := store.withReadTx(ctx, func(tx *sql.Tx) error {
			counted := &detailBatchCountingDBTX{DBTX: tx}
			result, err := storageops.ExecuteDetailBatch(ctx, counted, publicops.DetailBatchRequest{IDs: ids[:n], IncludeDependents: true, IncludeComments: true}, storageops.DetailBatchDirectServer)
			count = counted.statements
			if err == nil {
				if len(result.Items) != n {
					t.Fatalf("items=%d, want %d", len(result.Items), n)
				}
				for _, item := range result.Items {
					if !item.Found || item.Issue == nil || len(item.Issue.Dependencies) != 1 || item.Issue.Dependencies[0].ID != target || *item.Issue.DependencyCount != 1 {
						t.Fatalf("fan-out item=%+v", item)
					}
				}
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("N=%d chunks=%d statements=%d", n, (n+199)/200, count)
		if n == 10 {
			baseline = count
			if baseline == 0 {
				t.Fatal("no statements recorded")
			}
		}
		// Two fixed wisp probes plus one far-end row and label read.
		want := 4 + (baseline-4)*((n+199)/200)
		if count != want {
			t.Errorf("N=%d statements=%d, want %d by chunk count", n, count, want)
		}
	}
}
