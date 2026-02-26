//go:build cgo

package fix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestPatrolPollution_DeletesFromDoltWithoutJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	cfg.DoltDatabase = "doctest_" + hex.EncodeToString(h[:6])
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		_ = store.Close()
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	createIssue := func(title string) {
		t.Helper()
		issue := &types.Issue{
			Title:     title,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %q: %v", title, err)
		}
	}

	createEphemeralIssue := func(title string) {
		t.Helper()
		issue := &types.Issue{
			Title:     title,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create ephemeral issue %q: %v", title, err)
		}
	}

	createIssue("Digest: mol-abc-patrol")
	createIssue("Session ended: cleanup check")
	createIssue("Normal issue should remain")
	createEphemeralIssue("Session ended: ephemeral wisp should remain")

	if err := store.Close(); err != nil {
		t.Fatalf("failed to close setup store: %v", err)
	}

	if _, err := os.Stat(filepath.Join(beadsDir, "issues.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected no JSONL file before fix, stat err=%v", err)
	}

	if err := PatrolPollution(tmpDir); err != nil {
		t.Fatalf("PatrolPollution returned error: %v", err)
	}

	verifyStore, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer func() { _ = verifyStore.Close() }()

	ephemeralFalse := false
	remaining, err := verifyStore.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &ephemeralFalse})
	if err != nil {
		t.Fatalf("failed to query remaining issues: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("len(remaining) = %d, want 1 persistent issue", len(remaining))
	}
	if remaining[0].Title != "Normal issue should remain" {
		t.Fatalf("remaining issue title = %q, want normal issue", remaining[0].Title)
	}

	ephemeralTrue := true
	wisps, err := verifyStore.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &ephemeralTrue})
	if err != nil {
		t.Fatalf("failed to query remaining wisps: %v", err)
	}
	if len(wisps) != 1 {
		t.Fatalf("len(wisps) = %d, want 1", len(wisps))
	}
	if wisps[0].Title != "Session ended: ephemeral wisp should remain" {
		t.Fatalf("wisp title = %q, want ephemeral session wisp", wisps[0].Title)
	}
}
