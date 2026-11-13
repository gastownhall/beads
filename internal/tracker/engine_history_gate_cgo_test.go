//go:build cgo

// Cgo-tagged regression tests for the externalRefChangedAfter fast-path gate
// (GH#4549), exercised against the real Dolt-backed stores. Pure-Go tests
// using fakes live in engine_external_ref_history_test.go.

package tracker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedDoltExternalRefChangedAfterUsesHistoryFastPath is a regression
// test for GH#4549: EmbeddedDoltStore supports the dolt_history_issues query
// (it implements storage.HistoryViewer) but does not expose a raw *sql.DB,
// so the old dbProvider{DB() *sql.DB}-gated fast path never engaged for it —
// it silently fell back to the coarse CreatedAt/UpdatedAt heuristic even
// though the precise history table was available. This test constructs a
// case where the two heuristics disagree (local's timestamps predate asOf,
// so the fallback would wrongly say "unchanged") and asserts the fast,
// correct answer is produced instead.
func TestEmbeddedDoltExternalRefChangedAfterUsesHistoryFastPath(t *testing.T) {
	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "gh4549", "main")
	if err != nil {
		t.Fatalf("Open embedded store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "gh4549"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	issue := &types.Issue{
		ID:        "gh4549-1",
		Title:     "regression for GH#4549",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"external_ref": "old-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=old-ref): %v", err)
	}
	if err := store.Commit(ctx, "set old external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// asOf must land strictly after the "old-ref" commit and strictly
	// before the next one. Sleep on both sides to clear commit_date
	// timestamp granularity, since the query filters on commit_date <= asOf.
	time.Sleep(1200 * time.Millisecond)
	asOf := time.Now().UTC()
	time.Sleep(1200 * time.Millisecond)

	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"external_ref": "new-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=new-ref): %v", err)
	}
	if err := store.Commit(ctx, "set new external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Sanity check: EmbeddedDoltStore must be recognized as history-capable
	// directly (no decorator involved here).
	if _, ok := externalRefHistoryQuerier(store); !ok {
		t.Fatal("expected EmbeddedDoltStore to satisfy storage.ExternalRefHistoryQuerier")
	}

	e := &Engine{Store: store}

	// local's own timestamps predate asOf, so the coarse fallback heuristic
	// would (wrongly) say "unchanged" here.
	local := &types.Issue{
		ID:        issue.ID,
		CreatedAt: asOf.Add(-time.Hour),
		UpdatedAt: asOf.Add(-time.Hour),
	}

	changed, _, err := e.externalRefChangedAfter(ctx, local, "new-ref", asOf)
	if err != nil {
		t.Fatalf("externalRefChangedAfter: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true: external_ref differs from its value as of asOf, even though local's timestamps predate asOf")
	}

	// Complementary check: currentRef matching the historical value as of
	// asOf should report unchanged.
	changed, _, err = e.externalRefChangedAfter(ctx, local, "old-ref", asOf)
	if err != nil {
		t.Fatalf("externalRefChangedAfter: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false: currentRef matches the historical value as of asOf")
	}
}

// TestDoltStoreExternalRefChangedAfterUsesHistoryFastPath confirms the
// GH#4549 fix leaves the Dolt server store's fast-path behavior unchanged:
// DoltStore already implements storage.ExternalRefHistoryQuerier (it always
// had DB() access), so this should behave exactly as the old dbProvider gate
// did for this backend.
func TestDoltStoreExternalRefChangedAfterUsesHistoryFastPath(t *testing.T) {
	store := newTestStore(t) // skips if the shared test Dolt server isn't available
	defer store.Close()

	ctx := context.Background()
	issue := &types.Issue{
		ID:        "history-gate-1",
		Title:     "regression for GH#4549",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"external_ref": "old-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=old-ref): %v", err)
	}
	if err := store.Commit(ctx, "set old external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	time.Sleep(1200 * time.Millisecond)
	asOf := time.Now().UTC()
	time.Sleep(1200 * time.Millisecond)

	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"external_ref": "new-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=new-ref): %v", err)
	}
	if err := store.Commit(ctx, "set new external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if _, ok := externalRefHistoryQuerier(store); !ok {
		t.Fatal("expected DoltStore to satisfy storage.ExternalRefHistoryQuerier")
	}

	e := &Engine{Store: store}
	local := &types.Issue{
		ID:        issue.ID,
		CreatedAt: asOf.Add(-time.Hour),
		UpdatedAt: asOf.Add(-time.Hour),
	}

	changed, _, err := e.externalRefChangedAfter(ctx, local, "new-ref", asOf)
	if err != nil {
		t.Fatalf("externalRefChangedAfter: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true via the (behaviorally unchanged) Dolt server fast path")
	}
}

// TestDoltStoreCompactPullPreservesLocalEdits pins AC-6 of #7294 on a real
// Dolt store. Linked issues are committed before a tracker's last_sync; one
// is edited after it. A compaction whose boundary lies after last_sync
// squashes the commits that held their state at last_sync into a base dated
// after it, so PreviousExternalRef finds no row for them. The next pull with
// the original last_sync must not hydrate them from stale remotes: the edit
// and the unchanged issue keep their local content, and one warning names
// the issues changed since last_sync but not the untouched one. A pre-linked
// stub created after last_sync is still hydrated, which needs the real
// store's created_at. The control compacts at a boundary before last_sync,
// so history keeps each issue's row there: the edited issue is unchanged,
// and the issue linked after last_sync (a historical NULL ref) and the new
// stub are hydrated. In both cases a linked ephemeral and a linked
// no-history bead, created before last_sync and edited after it, have no
// history row (the wisps table is not versioned): they keep their edits,
// and no warning names them.
//
// Commit dates are set through @@dolt_committer_date, which DOLT_COMMIT
// stamps on the store's single pooled session, so last_sync falls between
// commits without wall-clock sleeps.
func TestDoltStoreCompactPullPreservesLocalEdits(t *testing.T) {
	testutil.RequireDoltBinary(t)
	if testServerPort == 0 {
		t.Skip("shared test Dolt server not initialized, skipping test")
	}

	// T1 holds every issue's state at last_sync; T3 the edit, the link and
	// the new stub made after it; T4 a later, unrelated commit. All lie in
	// the past, so the local edit's wall-clock updated_at is after last_sync.
	t1 := time.Now().UTC().Add(-10 * 24 * time.Hour).Truncate(time.Second)
	lastSync := t1.Add(time.Hour)
	t3 := t1.Add(2 * time.Hour)
	t4 := t1.Add(3 * time.Hour)

	for _, test := range []struct {
		name string
		// boundaryDate picks the last commit Compact squashes.
		boundaryDate     time.Time
		baselineRemoved  bool
		wantFetches      int
		wantLinkedTitle  string
		wantMissingWarns int
	}{
		{name: "baseline compacted away", boundaryDate: t3, baselineRemoved: true, wantFetches: 1, wantLinkedTitle: "linked after sync", wantMissingWarns: 1},
		{name: "baseline retained", boundaryDate: t1, wantFetches: 2, wantLinkedTitle: "stale remote title EXT-2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newCompactableTrackerStore(t)
			db := store.DB()
			setDate := func(d time.Time) {
				t.Helper()
				value := ""
				if !d.IsZero() {
					value = d.UTC().Format("2006-01-02T15:04:05.000Z07:00")
				}
				if _, err := db.ExecContext(ctx, "SET @@dolt_committer_date = ?", value); err != nil {
					t.Fatalf("set committer date: %v", err)
				}
			}

			created := t1.Add(-time.Hour)
			edited := linkedIssue("bd-edited", "EXT-1", "synced title", created, created)
			unchanged := linkedIssue("bd-unchanged", "EXT-3", "synced unchanged", created, created)
			relinked := &types.Issue{ID: "bd-relinked", Title: "linked after sync", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask, CreatedAt: created, UpdatedAt: created}
			wisp := linkedIssue("bd-wisp", "EXT-5", "synced wisp", created, created)
			wisp.Ephemeral = true
			noHistory := linkedIssue("bd-nh", "EXT-6", "synced no-history bead", created, created)
			noHistory.NoHistory = true
			wispsPlane := []*types.Issue{wisp, noHistory}

			setDate(t1)
			for _, issue := range []*types.Issue{edited, unchanged, relinked, wisp, noHistory} {
				if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
					t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
				}
			}
			setDate(t3)
			if err := store.UpdateIssue(ctx, edited.ID, map[string]interface{}{"title": "local edit not yet pushed"}, "tester"); err != nil {
				t.Fatalf("UpdateIssue(edit): %v", err)
			}
			for _, issue := range wispsPlane {
				if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": "local edit of " + issue.ID}, "tester"); err != nil {
					t.Fatalf("UpdateIssue(%s): %v", issue.ID, err)
				}
			}
			if err := store.UpdateIssue(ctx, relinked.ID, map[string]interface{}{"external_ref": "https://test.test/EXT-2"}, "tester"); err != nil {
				t.Fatalf("UpdateIssue(link): %v", err)
			}
			stub := linkedIssue("bd-stub", "EXT-4", "new stub", t3, t3)
			if err := store.CreateIssue(ctx, stub, "tester"); err != nil {
				t.Fatalf("CreateIssue(stub): %v", err)
			}
			setDate(t4)
			later := &types.Issue{ID: "bd-later", Title: "later work", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := store.CreateIssue(ctx, later, "tester"); err != nil {
				t.Fatalf("CreateIssue(later): %v", err)
			}
			setDate(time.Time{})

			// The fixture must hold the baseline before compaction, or the
			// dates did not take.
			if ref, found, err := store.PreviousExternalRef(ctx, edited.ID, lastSync); err != nil || !found || ref != "https://test.test/EXT-1" {
				t.Fatalf("before compaction PreviousExternalRef(edited) = (%q, %v, %v), want the synced ref", ref, found, err)
			}

			compactAt(t, ctx, store, test.boundaryDate)

			for _, probe := range []struct {
				id      string
				wantRef string
			}{{edited.ID, "https://test.test/EXT-1"}, {unchanged.ID, "https://test.test/EXT-3"}, {relinked.ID, ""}} {
				ref, found, err := store.PreviousExternalRef(ctx, probe.id, lastSync)
				if err != nil {
					t.Fatalf("PreviousExternalRef(%s): %v", probe.id, err)
				}
				if test.baselineRemoved && found {
					t.Fatalf("after compaction PreviousExternalRef(%s) = (%q, found), want no row: the fixture must remove the baseline", probe.id, ref)
				}
				if !test.baselineRemoved && (!found || ref != probe.wantRef) {
					t.Fatalf("after compaction PreviousExternalRef(%s) = (%q, %v), want (%q, found)", probe.id, ref, found, probe.wantRef)
				}
			}
			if ref, found, err := store.PreviousExternalRef(ctx, stub.ID, lastSync); err != nil || found {
				t.Fatalf("after compaction PreviousExternalRef(%s) = (%q, %v, %v), want no row: it was created after last_sync", stub.ID, ref, found, err)
			}
			for _, issue := range wispsPlane {
				if ref, found, err := store.PreviousExternalRef(ctx, issue.ID, lastSync); err != nil || found {
					t.Fatalf("PreviousExternalRef(%s) = (%q, %v, %v), want no row: the wisps table has no history", issue.ID, ref, found, err)
				}
			}

			if err := store.SetLocalMetadata(ctx, "test.last_sync", lastSync.Format(time.RFC3339)); err != nil {
				t.Fatalf("SetLocalMetadata(last_sync): %v", err)
			}
			mock, _ := newOverlapTracker(
				staleRemote("EXT-1", t1.Add(-2*time.Hour)),
				staleRemote("EXT-2", t1.Add(-2*time.Hour)),
				staleRemote("EXT-3", t1.Add(-2*time.Hour)),
				staleRemote("EXT-4", t1.Add(-2*time.Hour)),
				staleRemote("EXT-5", t1.Add(-2*time.Hour)),
				staleRemote("EXT-6", t1.Add(-2*time.Hour)),
			)
			result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true})
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if mock.fetchCalls != test.wantFetches {
				t.Errorf("FetchIssue calls = %d, want %d", mock.fetchCalls, test.wantFetches)
			}
			missingWarns := missingHistoryWarnings(result.Warnings)
			if len(missingWarns) != test.wantMissingWarns {
				t.Errorf("missing-history warnings = %d (warnings %q), want %d", len(missingWarns), result.Warnings, test.wantMissingWarns)
			}
			for _, w := range missingWarns {
				// The edit and the link changed updated_at after last_sync;
				// the unchanged issue's row still carries its creation time.
				for _, want := range []string{"2 issue(s)", edited.ID, relinked.ID} {
					if !strings.Contains(w, want) {
						t.Errorf("warning %q does not mention %q", w, want)
					}
				}
				if strings.Contains(w, unchanged.ID) {
					t.Errorf("warning %q names %s, which is untouched since last_sync", w, unchanged.ID)
				}
				for _, issue := range wispsPlane {
					if strings.Contains(w, issue.ID) {
						t.Errorf("warning %q names %s, which never has history", w, issue.ID)
					}
				}
			}
			for id, want := range map[string]string{
				edited.ID:    "local edit not yet pushed",
				unchanged.ID: "synced unchanged",
				relinked.ID:  test.wantLinkedTitle,
				stub.ID:      "stale remote title EXT-4",
				wisp.ID:      "local edit of " + wisp.ID,
				noHistory.ID: "local edit of " + noHistory.ID,
			} {
				got, err := store.GetIssue(ctx, id)
				if err != nil {
					t.Fatalf("GetIssue(%s): %v", id, err)
				}
				if got.Title != want {
					t.Errorf("%s title = %q, want %q", id, got.Title, want)
				}
			}
		})
	}
}

// newCompactableTrackerStore opens a store on its own database of the
// shared test server: Compact rewrites main, which the branch-per-test
// database every other test forks from must keep.
func newCompactableTrackerStore(t *testing.T) *dolt.DoltStore {
	t.Helper()
	ctx := context.Background()
	dbName := fmt.Sprintf("tracker_compact_%d", time.Now().UnixNano())
	conn, err := testutil.SetupSharedTestDB(testServerPort, dbName)
	if err != nil {
		t.Fatalf("create database %s: %v", dbName, err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		_ = conn.Close()
	})
	store, err := dolt.New(ctx, &dolt.Config{
		Path:           t.TempDir(),
		ServerHost:     "127.0.0.1",
		ServerPort:     testServerPort,
		Database:       dbName,
		CommitterName:  "tester",
		CommitterEmail: "tester@example.com",
		MaxOpenConns:   1,
	})
	if err != nil {
		t.Fatalf("open store on %s: %v", dbName, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'test: init', '--allow-empty')"); err != nil {
		t.Fatalf("commit schema: %v", err)
	}
	return store
}

// compactAt compacts store's history the way bd compact does, squashing
// every commit up to the newest one dated at or before boundaryDate and
// replaying the rest.
func compactAt(t *testing.T, ctx context.Context, store *dolt.DoltStore, boundaryDate time.Time) {
	t.Helper()
	rows, err := store.DB().QueryContext(ctx, "SELECT commit_hash, date FROM dolt_log ORDER BY commit_order")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	type commit struct {
		hash string
		date time.Time
	}
	var log []commit
	for rows.Next() {
		var c commit
		if err := rows.Scan(&c.hash, &c.date); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		log = append(log, c)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close dolt_log: %v", err)
	}
	boundary := -1
	for i, c := range log {
		if i > 0 && c.date.Equal(boundaryDate) {
			boundary = i
		}
	}
	if boundary < 0 || boundary == len(log)-1 {
		t.Fatalf("no commit dated %s to compact at, with a commit after it, in %+v", boundaryDate, log)
	}
	var recent []string
	for _, c := range log[boundary+1:] {
		recent = append(recent, c.hash)
	}
	if err := store.Compact(ctx, log[0].hash, log[boundary].hash, log[len(log)-1].hash, boundary, recent); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}
