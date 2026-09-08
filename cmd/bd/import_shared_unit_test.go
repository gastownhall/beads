package main

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

type fakeImportIssueLookupStore struct {
	storage.DoltStorage
	issues     []*types.Issue
	created    []*types.Issue
	createOpts []storage.BatchCreateOptions
	// rejectAsStale simulates the in-txn guard rejecting these IDs (a local
	// update raced in between the pre-filter read and the batch write).
	rejectAsStale []string
}

func (f *fakeImportIssueLookupStore) GetIssuesByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return f.issues, nil
}

func (f *fakeImportIssueLookupStore) CreateIssuesWithFullOptions(_ context.Context, issues []*types.Issue, _ string, opts storage.BatchCreateOptions) error {
	f.created = append(f.created, issues...)
	f.createOpts = append(f.createOpts, opts)
	if opts.OnStaleRejected != nil {
		for _, id := range f.rejectAsStale {
			opts.OnStaleRejected(id)
		}
	}
	return nil
}

func TestFilterStaleImportIssuesSkipsOlderIncomingRecords(t *testing.T) {
	base := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	incoming := []*types.Issue{
		{ID: "bd-stale", Title: "stale snapshot", UpdatedAt: base},
		{ID: "bd-equal", Title: "same snapshot time", UpdatedAt: base},
		{ID: "bd-newer", Title: "newer snapshot", UpdatedAt: base.Add(2 * time.Hour)},
		{ID: "bd-new", Title: "new record", UpdatedAt: base},
	}
	store := &fakeImportIssueLookupStore{issues: []*types.Issue{
		{ID: "bd-stale", Title: "stale snapshot", UpdatedAt: base.Add(time.Hour)},
		{ID: "bd-equal", Title: "same snapshot time", UpdatedAt: base},
		{ID: "bd-newer", Title: "old title", UpdatedAt: base.Add(time.Hour)},
	}}

	filtered, skippedIDs, plan, err := filterStaleImportIssues(context.Background(), store, incoming)
	if err != nil {
		t.Fatalf("filterStaleImportIssues: %v", err)
	}
	if len(skippedIDs) != 1 || skippedIDs[0] != "bd-stale" {
		t.Fatalf("skippedIDs = %#v, want [bd-stale]", skippedIDs)
	}

	got := make(map[string]bool, len(filtered))
	for _, issue := range filtered {
		got[issue.ID] = true
	}
	for _, id := range []string{"bd-equal", "bd-newer", "bd-new"} {
		if !got[id] {
			t.Fatalf("filtered issues missing %s: %#v", id, got)
		}
	}
	if got["bd-stale"] {
		t.Fatalf("stale issue was not filtered: %#v", got)
	}
	// bd-newer differs from the local row and is strictly newer, so the
	// change plan must surface it; bd-equal is identical so no tie conflict.
	if len(plan.Updates) != 1 || plan.Updates[0].ID != "bd-newer" {
		t.Fatalf("plan.Updates = %#v, want [bd-newer]", plan.Updates)
	}
	if len(plan.TieKeptLocal) != 0 {
		t.Fatalf("plan.TieKeptLocal = %#v, want empty (identical tie row)", plan.TieKeptLocal)
	}
}

// bd-hj85c: equal-timestamp rows whose content differs from the local issue
// are second-granularity ties. The upsert keeps the local row for them, and
// the pre-filter must report them so the kept-local decision is visible —
// in particular an incoming row with empty notes must not look like a clean
// re-import of the local row.
func TestFilterStaleImportIssuesReportsTieConflicts(t *testing.T) {
	base := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	incoming := []*types.Issue{
		{ID: "bd-tie", Title: "title", UpdatedAt: base},                                // notes missing
		{ID: "bd-tie-same", Title: "title", Notes: "kept notes", UpdatedAt: base},      // identical
		{ID: "bd-subsec", Title: "title", UpdatedAt: base.Add(400 * time.Millisecond)}, // sub-second "newer"
	}
	store := &fakeImportIssueLookupStore{issues: []*types.Issue{
		{ID: "bd-tie", Title: "title", Notes: "local notes", UpdatedAt: base},
		{ID: "bd-tie-same", Title: "title", Notes: "kept notes", UpdatedAt: base},
		{ID: "bd-subsec", Title: "title", Notes: "local notes", UpdatedAt: base},
	}}

	filtered, skippedIDs, plan, err := filterStaleImportIssues(context.Background(), store, incoming)
	if err != nil {
		t.Fatalf("filterStaleImportIssues: %v", err)
	}
	if len(skippedIDs) != 0 {
		t.Fatalf("skippedIDs = %#v, want none (ties are not stale)", skippedIDs)
	}
	if len(filtered) != 3 {
		t.Fatalf("filtered = %d rows, want all 3 kept for aux merging", len(filtered))
	}
	// bd-tie differs (notes wiped) at the same second; bd-subsec's 400ms
	// must not promote it past the tie (updated_at is DATETIME(0)).
	want := map[string]bool{"bd-tie": true, "bd-subsec": true}
	if len(plan.TieKeptLocal) != 2 || !want[plan.TieKeptLocal[0]] || !want[plan.TieKeptLocal[1]] {
		t.Fatalf("plan.TieKeptLocal = %#v, want [bd-tie bd-subsec]", plan.TieKeptLocal)
	}
	if len(plan.Updates) != 0 {
		t.Fatalf("plan.Updates = %#v, want empty", plan.Updates)
	}
}

func TestImportRowChangeSummary(t *testing.T) {
	local := &types.Issue{
		Title: "t", Status: types.StatusClosed, Priority: 1,
		IssueType: types.TypeBug, Notes: "local notes",
	}
	incoming := &types.Issue{
		Title: "t", Status: types.StatusOpen, Priority: 2,
		IssueType: types.TypeBug,
	}
	got := importRowChangeSummary(local, incoming)
	want := "status closed → open, priority 1 → 2, notes cleared"
	if got != want {
		t.Fatalf("importRowChangeSummary = %q, want %q", got, want)
	}
	if s := importRowChangeSummary(local, local); s != "" {
		t.Fatalf("importRowChangeSummary(identical) = %q, want empty", s)
	}
}

func TestImportIssuesCoreReportsStaleSkippedIDs(t *testing.T) {
	base := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	store := &fakeImportIssueLookupStore{issues: []*types.Issue{
		{ID: "bd-stale", UpdatedAt: base.Add(time.Hour)},
	}}

	result, err := importIssuesCore(context.Background(), "", store, []*types.Issue{
		{ID: "bd-stale", Title: "stale snapshot", UpdatedAt: base},
	}, ImportOptions{})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if result.Created != 0 {
		t.Fatalf("Created = %d, want 0", result.Created)
	}
	if result.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", result.Skipped)
	}
	if len(result.ImportedIDs) != 0 {
		t.Fatalf("ImportedIDs = %#v, want empty", result.ImportedIDs)
	}
	if len(result.StaleSkippedIDs) != 1 || result.StaleSkippedIDs[0] != "bd-stale" {
		t.Fatalf("StaleSkippedIDs = %#v, want [bd-stale]", result.StaleSkippedIDs)
	}
}

// bd-6dnrw.9: --allow-stale must bypass the stale guard so deliberately
// restoring an older snapshot actually writes rows instead of silently
// no-oping per row.
func TestImportIssuesCoreAllowStaleImportsOlderRows(t *testing.T) {
	base := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	store := &fakeImportIssueLookupStore{issues: []*types.Issue{
		{ID: "bd-stale", UpdatedAt: base.Add(time.Hour)},
	}}

	result, err := importIssuesCore(context.Background(), "", store, []*types.Issue{
		{ID: "bd-stale", Title: "stale snapshot", UpdatedAt: base},
	}, ImportOptions{AllowStale: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if result.Created != 1 {
		t.Fatalf("Created = %d, want 1", result.Created)
	}
	if result.Skipped != 0 || len(result.StaleSkippedIDs) != 0 {
		t.Fatalf("Skipped = %d, StaleSkippedIDs = %#v, want none", result.Skipped, result.StaleSkippedIDs)
	}
	if len(store.created) != 1 || store.created[0].ID != "bd-stale" {
		t.Fatalf("store.created = %#v, want the stale row written", store.created)
	}
}

// bd-hj85c: the import must report which existing local issues it changed
// (field-level summary) and which same-timestamp conflicting rows kept local
// state, so reverts are visible instead of silent. Updates rejected by the
// in-txn guard must drop out of the report.
func TestImportIssuesCoreReportsUpdatedAndTieKeptIssues(t *testing.T) {
	base := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	store := &fakeImportIssueLookupStore{
		issues: []*types.Issue{
			{ID: "bd-upd", Title: "t", Status: types.StatusClosed, UpdatedAt: base},
			{ID: "bd-tie", Title: "t", Notes: "local notes", UpdatedAt: base},
			{ID: "bd-raced", Title: "t", Status: types.StatusClosed, UpdatedAt: base},
		},
		rejectAsStale: []string{"bd-raced"},
	}

	result, err := importIssuesCore(context.Background(), "", store, []*types.Issue{
		{ID: "bd-upd", Title: "t", Status: types.StatusOpen, UpdatedAt: base.Add(time.Hour)},
		{ID: "bd-tie", Title: "t", UpdatedAt: base},
		{ID: "bd-raced", Title: "t", Status: types.StatusOpen, UpdatedAt: base.Add(time.Hour)},
		{ID: "bd-new", Title: "brand new", UpdatedAt: base},
	}, ImportOptions{})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}

	if result.Updated != 1 || len(result.UpdatedIssues) != 1 || result.UpdatedIssues[0].ID != "bd-upd" {
		t.Fatalf("UpdatedIssues = %#v (Updated=%d), want exactly bd-upd", result.UpdatedIssues, result.Updated)
	}
	if want := "status closed → open"; result.UpdatedIssues[0].Changes != want {
		t.Fatalf("Changes = %q, want %q", result.UpdatedIssues[0].Changes, want)
	}
	if len(result.TieKeptLocalIDs) != 1 || result.TieKeptLocalIDs[0] != "bd-tie" {
		t.Fatalf("TieKeptLocalIDs = %#v, want [bd-tie]", result.TieKeptLocalIDs)
	}
	if len(result.StaleSkippedIDs) != 1 || result.StaleSkippedIDs[0] != "bd-raced" {
		t.Fatalf("StaleSkippedIDs = %#v, want [bd-raced]", result.StaleSkippedIDs)
	}
}

// bd-pkim8: the pre-filter alone is racy (read-then-upsert), so importIssuesCore
// must also arm the transactional guard inside the batch write — except under
// --allow-stale, where overwriting newer local rows is the requested behavior.
func TestImportIssuesCoreArmsTransactionalStaleGuard(t *testing.T) {
	base := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	issue := func() []*types.Issue {
		return []*types.Issue{{ID: "bd-race", Title: "snapshot", UpdatedAt: base}}
	}

	store := &fakeImportIssueLookupStore{}
	if _, err := importIssuesCore(context.Background(), "", store, issue(), ImportOptions{}); err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if len(store.createOpts) != 1 || !store.createOpts[0].RejectStaleUpserts {
		t.Fatalf("createOpts = %#v, want RejectStaleUpserts armed by default", store.createOpts)
	}

	store = &fakeImportIssueLookupStore{}
	if _, err := importIssuesCore(context.Background(), "", store, issue(), ImportOptions{AllowStale: true}); err != nil {
		t.Fatalf("importIssuesCore (allow-stale): %v", err)
	}
	if len(store.createOpts) != 1 || store.createOpts[0].RejectStaleUpserts {
		t.Fatalf("createOpts = %#v, want RejectStaleUpserts disarmed under --allow-stale", store.createOpts)
	}
}
