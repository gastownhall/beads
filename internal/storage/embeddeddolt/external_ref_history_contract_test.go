//go:build cgo

package embeddeddolt_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/storagecontract"
	"github.com/steveyegge/beads/internal/types"
)

func TestExternalRefHistoryBatchContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "xr")
	storagecontract.RunExternalRefHistoryBatchContract(t, t.Context(), externalRefHistoryFixture(te.store, te.dataDir, te.database))
}

func BenchmarkExternalRefHistoryBatch(b *testing.B) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		b.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt benchmarks")
	}
	ctx := b.Context()
	beadsDir := filepath.Join(b.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "bench", "main")
	if err != nil {
		b.Fatalf("open embedded Dolt store: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "bench"); err != nil {
		b.Fatalf("set issue_prefix: %v", err)
	}
	storagecontract.RunExternalRefHistoryBatchBenchmark(b, ctx, externalRefHistoryFixture(store, filepath.Join(beadsDir, "embeddeddolt"), "bench"))
}

func externalRefHistoryFixture(store *embeddeddolt.EmbeddedDoltStore, dataDir, database string) storagecontract.ExternalRefHistoryFixture {
	exec := func(ctx context.Context, branch string, stmts ...string) error {
		// The engine admits one holder: close the raw handle before the next store call.
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "main")
		if err != nil {
			return err
		}
		defer cleanup()
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		for _, stmt := range append([]string{"CALL DOLT_CHECKOUT('" + branch + "')"}, stmts...) {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}
	return storagecontract.ExternalRefHistoryFixture{
		Store:  store,
		Branch: "main",
		CreateIssues: func(ctx context.Context, ids []string) error {
			issues := make([]*types.Issue, len(ids))
			for i, id := range ids {
				issues[i] = &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			}
			return store.CreateIssues(ctx, issues, "tester")
		},
		DeleteIssue: store.DeleteIssue,
		CreateBranch: func(ctx context.Context, name string) error {
			return exec(ctx, "main", "CALL DOLT_BRANCH('"+name+"')")
		},
		CommitOn: func(ctx context.Context, branch string, when time.Time, stmts ...string) error {
			return exec(ctx, branch, append(stmts, "CALL DOLT_COMMIT('-Am', 'step', '--date', '"+when.Format("2006-01-02T15:04:05.000Z")+"')")...)
		},
		Merge: func(ctx context.Context, branch string) error {
			_, err := store.Merge(ctx, branch)
			return err
		},
	}
}
