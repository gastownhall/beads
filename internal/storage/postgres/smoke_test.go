package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/pgdialect"
	"github.com/steveyegge/beads/internal/types"
)

// TestPGSmoke drives the full stack — dialect translation + issueops delegation
// + the synchronous readiness strategy — against a live Postgres. It proves the
// wedge actually runs, not just compiles. Gated on BEADS_PG_TEST_URL.
func TestPGSmoke(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping live Postgres smoke")
	}
	schema := fmt.Sprintf("smoke_%d", time.Now().UnixNano())
	ctx := context.Background()

	st, err := New(ctx, Config{DSN: url, Schema: schema})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()
	// Schema DDL must run over a raw (non-translating) DB; st.DB() is the
	// translating one used for the actual issueops workload below.
	raw, err := pgdialect.OpenRaw(url, schema)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	if err := InitSchema(ctx, raw, schema); err != nil {
		_ = raw.Close()
		t.Fatalf("InitSchema: %v", err)
	}
	_ = raw.Close()
	t.Cleanup(func() {
		_, _ = st.DB().ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
	})

	if err := st.SetConfig(ctx, "issue_prefix", "smoke"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	mk := func(id, title string) {
		iss := &types.Issue{
			ID: id, Title: title, IssueType: types.IssueType("task"),
			Status: types.Status("open"), Priority: 2, CreatedBy: "tester",
		}
		if err := st.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	mk("smoke-1", "first")
	mk("smoke-2", "second")

	got, err := st.GetIssue(ctx, "smoke-1")
	if err != nil || got == nil {
		t.Fatalf("GetIssue(smoke-1): %v (got=%v)", err, got)
	}
	if got.Title != "first" {
		t.Fatalf("GetIssue title = %q, want %q", got.Title, "first")
	}

	readyIDs := func() []string {
		rows, err := st.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		return ids
	}

	// Both open, no deps -> both ready.
	if ids := readyIDs(); len(ids) != 2 {
		t.Fatalf("before dep: ready=%v, want 2", ids)
	}

	// smoke-2 depends on (is blocked by) smoke-1.
	dep := &types.Dependency{IssueID: "smoke-2", DependsOnID: "smoke-1", Type: types.DependencyType("blocks"), CreatedBy: "tester"}
	if err := st.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	// Now only smoke-1 is ready (readiness projection ran in-tx).
	if ids := readyIDs(); len(ids) != 1 || ids[0] != "smoke-1" {
		t.Fatalf("after dep: ready=%v, want [smoke-1]", ids)
	}

	// Close smoke-1 -> smoke-2 unblocks.
	if err := st.CloseIssue(ctx, "smoke-1", "done", "tester", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if ids := readyIDs(); len(ids) != 1 || ids[0] != "smoke-2" {
		t.Fatalf("after close: ready=%v, want [smoke-2]", ids)
	}
}
