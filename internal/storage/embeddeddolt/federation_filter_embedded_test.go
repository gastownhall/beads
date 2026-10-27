//go:build cgo

package embeddeddolt_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

func TestFilteredFederationSyncExcludesWispsAndWarnsOnce(t *testing.T) {
	ctx := t.Context()
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	te := newTestEnv(t, "filter")
	peer := &storage.FederationPeer{
		Name: "filter-peer", RemoteURL: "file://" + filepath.Join(t.TempDir(), "remote"),
	}
	if err := te.store.AddFederationPeer(ctx, peer); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	const excludedID = "filter-excluded"
	kept := &types.Issue{
		ID: "filter-kept", Title: "kept", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, kept, "tester"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", kept.ID, err)
	}
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: "filter-live-wisp", Title: "live wisp", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue(live wisp): %v", err)
	}

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() {
		if cleanup != nil {
			_ = cleanup()
		}
	}()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin fixture connection: %v", err)
	}
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	if _, err := conn.ExecContext(ctx, "DELETE FROM wisps WHERE id = ?", "filter-live-wisp"); err != nil {
		t.Fatalf("remove live wisp fixture row: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS wisp_dependencies (
		id CHAR(36) NOT NULL DEFAULT (UUID()) PRIMARY KEY,
		issue_id VARCHAR(255) NOT NULL,
		depends_on_issue_id VARCHAR(255) NULL,
		depends_on_wisp_id VARCHAR(255) NULL,
		depends_on_external VARCHAR(255) NULL,
		type VARCHAR(32) NOT NULL DEFAULT 'blocks',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		created_by VARCHAR(255) DEFAULT '',
		metadata JSON DEFAULT (JSON_OBJECT()),
		thread_id VARCHAR(255) DEFAULT ''
	)`); err != nil {
		t.Fatalf("create wisp_dependencies fixture table: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "REPLACE INTO dolt_ignore VALUES ('wisps', false)"); err != nil {
		t.Fatalf("make wisps fixture table versionable: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "REPLACE INTO dolt_ignore VALUES ('wisp_dependencies', false)"); err != nil {
		t.Fatalf("make wisp_dependencies fixture table versionable: %v", err)
	}
	// Seed a legacy durable-plane wisp; current CreateIssue routes wisps to ignored tables.
	if _, err := conn.ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral)
		VALUES (?, 'excluded', '', '', '', '', 'open', 2, 'task', 1)`, excludedID); err != nil {
		t.Fatalf("seed legacy durable-plane wisp: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM dolt_ignore WHERE pattern IN ('wisps', 'wisp_%')"); err != nil {
		t.Fatalf("make wisp fixture tables versionable: %v", err)
	}
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("stage wisp fixture tables: %v", err)
	}
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', 'test: version wisp fixture tables')"); err != nil {
		t.Fatalf("commit wisp fixture tables: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close fixture connection: %v", err)
	}
	conn = nil
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup fixture connection: %v", err)
	}
	cleanup = nil
	if err := te.store.PushTo(ctx, peer.Name); err != nil {
		t.Fatalf("initial PushTo: %v", err)
	}
	var initialExcludedCount int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues AS OF 'remotes/filter-peer/main' WHERE id = ? AND ephemeral = 1",
		[]any{excludedID}, &initialExcludedCount)
	if initialExcludedCount != 1 {
		t.Fatalf("pre-sync fixture count: excluded=%d, want 1", initialExcludedCount)
	}

	config.Set("federation.exclude_types", []string{"wisp"})
	if got := config.GetFederationConfig().ExcludeTypes; len(got) != 1 || got[0] != "wisp" {
		t.Fatalf("federation.exclude_types = %v, want [wisp]", got)
	}
	previousWriter := versioncontrolops.FederationWarnWriter
	var warnings bytes.Buffer
	versioncontrolops.FederationWarnWriter = &warnings
	t.Cleanup(func() { versioncontrolops.FederationWarnWriter = previousWriter })

	result, err := te.store.Sync(ctx, peer.Name, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !result.Pushed || result.PushError != nil {
		t.Fatalf("Sync push result: pushed=%t push error=%v", result.Pushed, result.PushError)
	}

	var excludedCount, keptCount int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues AS OF 'remotes/filter-peer/main' WHERE id = ?",
		[]any{excludedID}, &excludedCount)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues AS OF 'remotes/filter-peer/main' WHERE id = ?",
		[]any{kept.ID}, &keptCount)
	if excludedCount != 0 || keptCount != 1 {
		t.Fatalf("pushed tip counts: excluded=%d kept=%d, want 0 and 1", excludedCount, keptCount)
	}
	wantWarning := "Warning: federation.exclude_types removed 1 issue(s) from this push, but earlier commits in the pushed history still contain them; a peer that reads history can recover them. See https://beads.gascity.com/multi-agent/federation\n"
	if got := warnings.String(); got != wantWarning {
		t.Fatalf("warning = %q, want %q", got, wantWarning)
	}
}
