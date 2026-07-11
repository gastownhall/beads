package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Temporary red-team repro: create with NO explicit ID (default hash-ID path)
// which exercises GetAdaptiveIDLengthTx's INSTR() query.
func TestPGReproHashIDCreate(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set")
	}
	schema := fmt.Sprintf("repro_%d", time.Now().UnixNano())
	ctx := context.Background()

	st, err := New(ctx, Config{DSN: url, Schema: schema})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()
	if err := InitSchema(ctx, st.DB(), schema); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.DB().ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
	})
	if err := st.SetConfig(ctx, "issue_prefix", "repro"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	iss := &types.Issue{
		Title: "no explicit id", IssueType: types.IssueType("task"),
		Status: types.Status("open"), Priority: 2, CreatedBy: "tester",
	}
	if err := st.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("CreateIssue(hash-id): %v", err)
	}
	t.Logf("created id=%s", iss.ID)
}
