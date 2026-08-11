//go:build cgo

package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	storagedb "github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedRepositoriesPromoteDurableWispHermetically exercises the same
// repository/use-case composition used by the proxied unit of work, but on an
// in-process Dolt SQL engine. The sql-server transport remains covered by the
// container suite; this test keeps physical-plane routing conformance runnable
// when that pinned container is unavailable.
func TestProxiedRepositoriesPromoteDurableWispHermetically(t *testing.T) {
	ctx := context.Background()
	database := "proxied_promotion"
	dataDir := t.TempDir()
	bootstrap, bootstrapCleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("open embedded Dolt bootstrap SQL: %v", err)
	}
	if _, err := bootstrap.ExecContext(ctx, "CREATE DATABASE proxied_promotion"); err != nil {
		_ = bootstrapCleanup()
		t.Fatalf("create embedded Dolt database: %v", err)
	}
	if err := bootstrapCleanup(); err != nil {
		t.Fatalf("close embedded Dolt bootstrap SQL: %v", err)
	}

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "main")
	if err != nil {
		t.Fatalf("open embedded Dolt SQL: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("close embedded Dolt SQL: %v", err)
		}
	})
	if _, err := schema.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin proxied unit of work: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	issueRepo := storagedb.NewIssueSQLRepository(tx)
	labelRepo := storagedb.NewLabelSQLRepository(tx)
	dependencyRepo := storagedb.NewDependencySQLRepository(tx)
	labelUseCase := domain.NewLabelUseCase(labelRepo)
	dependencyUseCase := domain.NewDependencyUseCase(dependencyRepo)
	issueUseCase := domain.NewIssueUseCase(
		issueRepo,
		dependencyRepo,
		labelRepo,
		storagedb.NewChildCounterSQLRepository(tx),
		storagedb.NewCommentSQLRepository(tx),
		storagedb.NewConfigSQLRepository(tx),
		storagedb.NewEventsSQLRepository(tx),
		labelUseCase,
		dependencyUseCase,
	)

	const id = "bd-proxied-promote"
	seed := &types.Issue{
		ID:              id,
		Title:           "proxied durable promotion",
		Status:          types.StatusOpen,
		Priority:        2,
		IssueType:       types.TypeTask,
		NoHistory:       true,
		ClosedBySession: "session-proxied",
	}
	if err := issueRepo.Insert(ctx, seed, "seed", domain.InsertIssueOpts{UseWispsTable: true}); err != nil {
		t.Fatalf("insert physical no-history wisp: %v", err)
	}

	promoted, err := issueUseCase.ApplyUpdate(ctx, id, domain.UpdateSpec{Fields: map[string]any{
		"wisp":       false,
		"no_history": false,
		"metadata":   json.RawMessage(`{"phase":"promoted"}`),
	}}, "promoter")
	if err != nil {
		t.Fatalf("promote through proxied repositories: %v", err)
	}
	if promoted.Ephemeral || promoted.NoHistory {
		t.Fatalf("promoted markers = ephemeral:%t no_history:%t, want false/false", promoted.Ephemeral, promoted.NoHistory)
	}
	if promoted.ClosedBySession != seed.ClosedBySession {
		t.Fatalf("closed_by_session = %q, want %q", promoted.ClosedBySession, seed.ClosedBySession)
	}
	assertPlaneCount(t, ctx, tx, "issues", id, 1)
	assertPlaneCount(t, ctx, tx, "wisps", id, 0)

	updated, err := issueUseCase.ApplyUpdate(ctx, id, domain.UpdateSpec{Fields: map[string]any{
		"status":   string(types.StatusInProgress),
		"metadata": json.RawMessage(`{"phase":"after-promotion"}`),
	}}, "worker")
	if err != nil {
		t.Fatalf("update promoted issue through proxied repositories: %v", err)
	}
	if updated.Status != types.StatusInProgress {
		t.Fatalf("status = %q, want %q", updated.Status, types.StatusInProgress)
	}
	if updated.ClosedBySession != seed.ClosedBySession {
		t.Fatalf("closed_by_session after later update = %q, want %q", updated.ClosedBySession, seed.ClosedBySession)
	}
	var metadata map[string]string
	if err := json.Unmarshal(updated.Metadata, &metadata); err != nil {
		t.Fatalf("decode metadata after later update: %v", err)
	}
	if metadata["phase"] != "after-promotion" {
		t.Fatalf("metadata phase = %q, want after-promotion", metadata["phase"])
	}
	assertPlaneCount(t, ctx, tx, "issues", id, 1)
	assertPlaneCount(t, ctx, tx, "wisps", id, 0)

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit proxied unit of work: %v", err)
	}
	assertPlaneCount(t, ctx, db, "issues", id, 1)
	assertPlaneCount(t, ctx, db, "wisps", id, 0)
}

func assertPlaneCount(t *testing.T, ctx context.Context, runner interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, table, id string, want int) {
	t.Helper()
	var got int
	if err := runner.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("count %s rows for %s: %v", table, id, err)
	}
	if got != want {
		t.Fatalf("%s rows for %s = %d, want %d", table, id, got, want)
	}
}
