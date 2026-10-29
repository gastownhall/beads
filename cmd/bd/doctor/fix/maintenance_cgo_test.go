//go:build cgo

package fix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestPatrolPollution_DeletesFromDoltWithoutJSONL(t *testing.T) {
	requireFixDoltContainer(t)
	port := fixTestServerPort()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = port
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	cfg.DoltDatabase = "doctest_" + hex.EncodeToString(h[:6])
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
	if err != nil {
		t.Fatalf("dolt.NewFromConfigWithOptions against running test container: %v", err)
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
		t.Fatalf("failed to reopen store for verification: %v", err)
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

// TestStaleClosedIssues_KeepsRetainedTypes drives the stale-closed-issues fix
// against a server-mode workspace whose types.retained names chore: the old
// closed chore stays, the old closed task goes. The fix deletes with no
// tombstone, so a retained bead must never be its to take. A types.retained
// key in config.yaml naming task is ignored, and the fix says so before it
// selects: the setting is read from the workspace database only.
func TestStaleClosedIssues_KeepsRetainedTypes(t *testing.T) {
	requireFixDoltContainer(t)
	port := fixTestServerPort()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = port
	cfg.StaleClosedIssuesDays = 1
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	cfg.DoltDatabase = "doctest_" + hex.EncodeToString(h[:6])
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	ctx := context.Background()
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
	if err != nil {
		t.Fatalf("dolt.NewFromConfigWithOptions against running test container: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		_ = store.Close()
		t.Fatalf("failed to set issue_prefix: %v", err)
	}
	if err := store.SetConfig(ctx, "types.retained", "chore"); err != nil {
		_ = store.Close()
		t.Fatalf("failed to set types.retained: %v", err)
	}
	old := time.Now().AddDate(0, 0, -30)
	seed := func(id string, issueType types.IssueType) {
		t.Helper()
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusClosed, ClosedAt: &old, Priority: 2, IssueType: issueType}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			_ = store.Close()
			t.Fatalf("failed to create %s: %v", id, err)
		}
	}
	seed("bd-keyed", types.TypeChore)
	seed("bd-stale", types.TypeTask)
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close setup store: %v", err)
	}

	t.Chdir(tmpDir)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("types.retained", "task")

	var fixErr error
	out := captureFixStdout(t, func() { fixErr = StaleClosedIssues(tmpDir) })
	if fixErr != nil {
		t.Fatalf("StaleClosedIssues returned error: %v\n%s", fixErr, out)
	}
	warning := strings.Index(out, "Warning: types.retained in config.yaml (task) is ignored")
	cleaned := strings.Index(out, "Cleaned up 1 stale closed issue(s)")
	if warning < 0 || cleaned < 0 || warning > cleaned {
		t.Errorf("StaleClosedIssues printed:\n%s\nwant the ignored config.yaml warning, then the one task cleaned up", out)
	}

	verifyStore, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("failed to reopen store for verification: %v", err)
	}
	defer func() { _ = verifyStore.Close() }()
	if _, err := verifyStore.GetIssue(ctx, "bd-keyed"); err != nil {
		t.Errorf("bd-keyed (a retained type) is gone after the fix: %v", err)
	}
	if _, err := verifyStore.GetIssue(ctx, "bd-stale"); err == nil {
		t.Errorf("bd-stale survived the fix; want it deleted")
	}
}

// captureFixStdout runs fn with os.Stdout redirected and returns what it
// printed there, which is where a fix handler reports.
func captureFixStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	printed := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		printed <- string(b)
	}()
	orig := os.Stdout
	os.Stdout = w
	func() {
		defer func() {
			os.Stdout = orig
			_ = w.Close()
		}()
		fn()
	}()
	out := <-printed
	_ = r.Close()
	return out
}
