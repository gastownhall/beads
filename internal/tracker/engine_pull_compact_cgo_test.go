//go:build cgo

// AC-6, the real-store leg of GH#7294: a real DoltStore whose history was
// rewritten by bd compact must not turn the next tracker pull into an
// unconditional overwrite of unpushed local edits. The compact runs through
// DoltStore.Compact on the store's own database, whose working branch is the
// hardcoded 'main' the recipe checks out - the shared-test-server helpers
// work on a test branch of one shared database and cannot offer that - so
// each subtest gets a fresh database on an isolated Dolt server handle.
//
// The control subtest keeps the history intact and pins that the ordinary
// pull behavior on a real store is unchanged: an untouched linked issue
// stays untouched, while a known re-link and a post-sync link (a NULL
// external_ref at the cursor) still hydrate from the overlap window, with no
// warning.

package tracker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// newCompactPullStore opens a fresh DoltStore in server mode against the
// test's own Dolt server, mirroring the dolt package's test-store setup.
func newCompactPullStore(t *testing.T, ctx context.Context, port int, name string) *dolt.DoltStore {
	t.Helper()
	cfg := &dolt.Config{
		Path:            t.TempDir(),
		CommitterName:   "tester",
		CommitterEmail:  "tester@example.com",
		Database:        name,
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		CreateIfMissing: true,
	}
	store, err := dolt.New(ctx, cfg)
	if err != nil {
		t.Fatalf("open store %s: %v", name, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "ac6"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	return store
}

// compactPullIssue is a local issue with an explicit ID, stamped two hours
// back so it always predates the pull's cursor.
func compactPullIssue(id, title, ref string) *types.Issue {
	stamp := time.Now().UTC().Add(-2 * time.Hour)
	issue := &types.Issue{
		ID:          id,
		Title:       title,
		Description: "body " + id,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   stamp,
		UpdatedAt:   stamp,
	}
	if ref != "" {
		issue.ExternalRef = &ref
	}
	return issue
}

// compactPullLogRows returns main's commits, newest first, the order dolt_log
// reports and the versioncontrolops fixtures rely on.
func compactPullLogRows(t *testing.T, ctx context.Context, store *dolt.DoltStore) (hashes, messages []string) {
	t.Helper()
	rows, err := store.UnderlyingDB().QueryContext(ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h, m string
		if err := rows.Scan(&h, &m); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		hashes = append(hashes, h)
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_log: %v", err)
	}
	return hashes, messages
}

// compactPullIssueTitle reads an issue's current title from the store.
func compactPullIssueTitle(t *testing.T, ctx context.Context, store *dolt.DoltStore, id string) string {
	t.Helper()
	issue, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", id, err)
	}
	return issue.Title
}

func TestDoltStoreCompactPullPreservesLocalEdits(t *testing.T) {
	ctr := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(ctr.Port)
	if err != nil {
		t.Fatalf("parse server port %q: %v", ctr.Port, err)
	}

	t.Run("compacted store keeps unpushed local edits", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		store := newCompactPullStore(t, ctx, port, fmt.Sprintf("ac6compact_%d", time.Now().UnixNano()))

		// Two linked issues before the cursor, then two filler commits after
		// it: the compact squashes everything through the boundary commit
		// ("bd: create ac6-3") and replays only the head.
		for _, issue := range []*types.Issue{
			compactPullIssue("ac6-1", "local ac6-1", "https://test.test/EXT-1"),
			compactPullIssue("ac6-2", "local ac6-2", "https://test.test/EXT-2"),
		} {
			if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
			}
		}
		time.Sleep(1200 * time.Millisecond) // let the cursor's commits land strictly before lastSync
		lastSync := time.Now().UTC().Truncate(time.Second) // RFC3339 storage truncates to seconds
		time.Sleep(1200 * time.Millisecond) // and the boundary strictly after it
		for _, issue := range []*types.Issue{
			compactPullIssue("ac6-3", "filler 3", ""),
			compactPullIssue("ac6-4", "filler 4", ""),
		} {
			if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
			}
		}

		hashes, messages := compactPullLogRows(t, ctx, store)
		if len(messages) < 5 ||
			messages[0] != "bd: create ac6-4" || messages[1] != "bd: create ac6-3" ||
			messages[2] != "bd: create ac6-2" || messages[3] != "bd: create ac6-1" {
			t.Fatalf("fixture history = %q, want the four create commits newest first above the init commits", messages)
		}
		initial := hashes[len(hashes)-1] // the repository's root commit
		boundary := hashes[1]             // "bd: create ac6-3"
		head := hashes[0]                 // "bd: create ac6-4"
		oldCommits := len(hashes) - 2     // commits after the root, through the boundary

		// Before the rewrite the baseline row the pull compares against is
		// reachable from the cursor.
		ref, found, err := store.PreviousExternalRef(ctx, "ac6-1", lastSync)
		if err != nil || !found || ref != "https://test.test/EXT-1" {
			t.Fatalf("pre-compact PreviousExternalRef(ac6-1) = (%q, %v, %v), want the recorded EXT-1 ref", ref, found, err)
		}

		if err := store.Compact(ctx, initial, boundary, head, oldCommits, []string{head}); err != nil {
			t.Fatalf("Compact: %v", err)
		}

		// The squashed base takes the boundary's date, which is after
		// lastSync: the baseline row for ac6-1 is gone. That is #7294's
		// trigger, so the fixture must demonstrate it, not assume it.
		_, found, err = store.PreviousExternalRef(ctx, "ac6-1", lastSync)
		if err != nil {
			t.Fatalf("post-compact PreviousExternalRef(ac6-1): %v", err)
		}
		if found {
			t.Fatal("post-compact PreviousExternalRef(ac6-1) found a row at or before lastSync; the fixture does not reproduce #7294")
		}

		// An unpushed local edit, then a pull whose only remote rows sit in
		// the overlap window before the stored cursor.
		if err := store.UpdateIssue(ctx, "ac6-1", map[string]interface{}{"title": "unpushed local edit"}, "tester"); err != nil {
			t.Fatalf("UpdateIssue(ac6-1): %v", err)
		}
		if err := store.Commit(ctx, "bd: local edit"); err != nil {
			t.Fatalf("Commit(local edit): %v", err)
		}
		if err := store.SetLocalMetadata(ctx, "test.last_sync", lastSync.Format(time.RFC3339)); err != nil {
			t.Fatalf("SetLocalMetadata(test.last_sync): %v", err)
		}

		mock, since := newOverlapTracker(
			TrackerIssue{
				ID: "EXT-1", Identifier: "EXT-1",
				Title: "stale remote title", Description: "stale remote body",
				UpdatedAt: lastSync.Add(-30 * time.Second),
			},
			TrackerIssue{
				ID: "EXT-2", Identifier: "EXT-2",
				Title: "local ac6-2", Description: "body ac6-2",
				UpdatedAt: lastSync.Add(-30 * time.Second),
			},
		)
		engine := NewEngine(mock, store, "sync")
		engine.PushHooks = overlapTitleHooks

		result, err := engine.Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Created != 0 || result.PullStats.Updated != 0 {
			t.Fatalf("PullStats = %+v, want no creates and no updates", result.PullStats)
		}
		if mock.fetchCalls != 0 {
			t.Fatalf("FetchIssue calls = %d, want 0 (no per-issue hydration fetch)", mock.fetchCalls)
		}
		if *since == nil || !(*since).Equal(lastSync.Add(-pullFetchOverlap)) {
			t.Fatalf("FetchOptions.Since = %v, want the original cursor %s",
				*since, lastSync.Add(-pullFetchOverlap).Format(time.RFC3339))
		}
		warnings := 0
		for _, warning := range result.Warnings {
			if strings.Contains(warning, "cannot verify whether issues linked before the last sync") {
				warnings++
			}
		}
		if warnings != 1 {
			t.Fatalf("unverified-links warnings = %d (%v), want exactly one", warnings, result.Warnings)
		}
		warning, _ := missingHistoryWarning(result)
		if !strings.Contains(warning, lastSync.Format(time.RFC3339)) {
			t.Errorf("warning %q does not name the original sync time %s", warning, lastSync.Format(time.RFC3339))
		}
		if !strings.Contains(warning, "review") {
			t.Errorf("warning %q does not ask for review before pushing", warning)
		}
		if title := compactPullIssueTitle(t, ctx, store, "ac6-1"); title != "unpushed local edit" {
			t.Fatalf("ac6-1 title = %q, want the unpushed local edit kept", title)
		}
		if title := compactPullIssueTitle(t, ctx, store, "ac6-2"); title != "local ac6-2" {
			t.Fatalf("ac6-2 title = %q, want the unchanged issue left alone", title)
		}
	})

	// Control: with the history intact the same pull behaves exactly as it
	// did before the fix - the withheld cases of the compacted subtest are
	// the missing-history ones, and none of these issues has any.
	t.Run("retained history keeps the ordinary pull behavior", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		store := newCompactPullStore(t, ctx, port, fmt.Sprintf("ac6ctl_%d", time.Now().UnixNano()))

		// ctl1 stays linked and untouched; ctl2 is re-linked after the
		// cursor; ctl3 is linked after it (NULL external_ref at the cursor).
		for _, issue := range []*types.Issue{
			compactPullIssue("ac6-ctl1", "local ctl1", "https://test.test/EXT-CTL1"),
			compactPullIssue("ac6-ctl2", "local ctl2", "https://test.test/EXT-CTL2"),
			compactPullIssue("ac6-ctl3", "local ctl3", ""),
		} {
			if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
			}
		}
		time.Sleep(1200 * time.Millisecond)
		lastSync := time.Now().UTC().Truncate(time.Second) // RFC3339 storage truncates to seconds
		time.Sleep(1200 * time.Millisecond)
		if err := store.UpdateIssue(ctx, "ac6-ctl2", map[string]interface{}{"external_ref": "https://test.test/EXT-CTL2B"}, "tester"); err != nil {
			t.Fatalf("UpdateIssue(ac6-ctl2): %v", err)
		}
		if err := store.Commit(ctx, "bd: relink ctl2"); err != nil {
			t.Fatalf("Commit(relink ctl2): %v", err)
		}
		if err := store.UpdateIssue(ctx, "ac6-ctl3", map[string]interface{}{"external_ref": "https://test.test/EXT-CTL3"}, "tester"); err != nil {
			t.Fatalf("UpdateIssue(ac6-ctl3): %v", err)
		}
		if err := store.Commit(ctx, "bd: link ctl3"); err != nil {
			t.Fatalf("Commit(link ctl3): %v", err)
		}

		// The history answers for every issue at the cursor - ctl3 with a
		// NULL row, the known pre-existing unlinked shape.
		for _, want := range []struct {
			id  string
			ref string
		}{
			{"ac6-ctl1", "https://test.test/EXT-CTL1"},
			{"ac6-ctl2", "https://test.test/EXT-CTL2"},
			{"ac6-ctl3", ""},
		} {
			ref, found, err := store.PreviousExternalRef(ctx, want.id, lastSync)
			if err != nil || !found || ref != want.ref {
				t.Fatalf("PreviousExternalRef(%s) = (%q, %v, %v), want (%q, found)", want.id, ref, found, err, want.ref)
			}
		}

		if err := store.SetLocalMetadata(ctx, "test.last_sync", lastSync.Format(time.RFC3339)); err != nil {
			t.Fatalf("SetLocalMetadata(test.last_sync): %v", err)
		}
		mock, _ := newOverlapTracker(
			TrackerIssue{
				ID: "EXT-CTL1", Identifier: "EXT-CTL1",
				Title: "local ctl1", Description: "body ac6-ctl1",
				UpdatedAt: lastSync.Add(-30 * time.Second),
			},
			TrackerIssue{
				ID: "EXT-CTL2B", Identifier: "EXT-CTL2B",
				Title: "remote ctl2", Description: "body ac6-ctl2",
				UpdatedAt: lastSync.Add(-30 * time.Second),
			},
			TrackerIssue{
				ID: "EXT-CTL3", Identifier: "EXT-CTL3",
				Title: "remote ctl3", Description: "body ac6-ctl3",
				UpdatedAt: lastSync.Add(-30 * time.Second),
			},
		)
		engine := NewEngine(mock, store, "sync")
		engine.PushHooks = overlapTitleHooks

		result, err := engine.Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if result.PullStats.Updated != 2 || result.PullStats.Created != 0 {
			t.Fatalf("PullStats = %+v, want the re-link and the new stub updated, no creates", result.PullStats)
		}
		if mock.fetchCalls != 0 {
			t.Fatalf("FetchIssue calls = %d, want 0 (both hydrations reuse overlap rows)", mock.fetchCalls)
		}
		if _, found := missingHistoryWarning(result); found {
			t.Fatalf("warnings = %v, want none when the history answers", result.Warnings)
		}
		if title := compactPullIssueTitle(t, ctx, store, "ac6-ctl1"); title != "local ctl1" {
			t.Fatalf("ac6-ctl1 title = %q, want the untouched issue left alone", title)
		}
		if title := compactPullIssueTitle(t, ctx, store, "ac6-ctl2"); title != "remote ctl2" {
			t.Fatalf("ac6-ctl2 title = %q, want the known re-link hydrated", title)
		}
		if title := compactPullIssueTitle(t, ctx, store, "ac6-ctl3"); title != "remote ctl3" {
			t.Fatalf("ac6-ctl3 title = %q, want the post-sync link hydrated", title)
		}
	})
}
