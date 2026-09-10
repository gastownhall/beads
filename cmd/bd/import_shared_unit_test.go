package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// The embedded nil storage.DoltStorage would satisfy importRelationLookup and
// panic on call; opt the plain fake out explicitly so tie rows are rewritten.
func (f *fakeImportIssueLookupStore) GetCommentsForIssues(_ context.Context, _ []string) (map[string][]*types.Comment, error) {
	return nil, errImportRelationsUnavailable
}

func (f *fakeImportIssueLookupStore) GetDependencyRecordsForIssues(_ context.Context, _ []string) (map[string][]*types.Dependency, error) {
	return nil, errImportRelationsUnavailable
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

// GH#4901 follow-up: title-only rows (no ID) and zero-UpdatedAt rows can't
// be stale-checked against a local timestamp, but they still write on
// execution. filterStaleImportIssues must classify them via an existence
// lookup — nonexistent -> New, existing -> Updated — instead of silently
// passing them through unclassified.
func TestFilterStaleImportIssuesClassifiesUntimestampedRows(t *testing.T) {
	base := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

	t.Run("mixed_with_a_local_match", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{issues: []*types.Issue{
			{ID: "bd-existing", Title: "old title", UpdatedAt: base},
		}}
		incoming := []*types.Issue{
			{Title: "title only, no id"},            // new: no ID to look up
			{ID: "bd-existing", Title: "new title"}, // zero UpdatedAt, matches local
			{ID: "bd-brand-new", Title: "zero UpdatedAt, no local match"},
		}

		_, _, plan, err := filterStaleImportIssues(context.Background(), store, incoming)
		if err != nil {
			t.Fatalf("filterStaleImportIssues: %v", err)
		}
		if plan.NewCount != 2 {
			t.Fatalf("plan.NewCount = %d, want 2 (title-only + bd-brand-new)", plan.NewCount)
		}
		if len(plan.NewIDs) != 1 || plan.NewIDs[0] != "bd-brand-new" {
			t.Fatalf("plan.NewIDs = %#v, want [bd-brand-new] (title-only row has no ID to report)", plan.NewIDs)
		}
		if len(plan.Updates) != 1 || plan.Updates[0].ID != "bd-existing" {
			t.Fatalf("plan.Updates = %#v, want [bd-existing]", plan.Updates)
		}
	})

	t.Run("no_local_matches_at_all", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{} // empty db: exercises the short-circuit path
		incoming := []*types.Issue{
			{Title: "title only, no id"},
			{ID: "bd-new", Title: "zero UpdatedAt"},
		}
		_, _, plan, err := filterStaleImportIssues(context.Background(), store, incoming)
		if err != nil {
			t.Fatalf("filterStaleImportIssues: %v", err)
		}
		if plan.NewCount != 2 {
			t.Fatalf("plan.NewCount = %d, want 2", plan.NewCount)
		}
		if len(plan.NewIDs) != 1 || plan.NewIDs[0] != "bd-new" {
			t.Fatalf("plan.NewIDs = %#v, want [bd-new]", plan.NewIDs)
		}
	})
}

// GH#4901: plan.NewIDs must cover rows with no local match, including the
// "first import into an empty db" case, which used to short-circuit before
// ever populating it.
func TestFilterStaleImportIssuesReportsNewIDs(t *testing.T) {
	base := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

	t.Run("mixed_new_and_existing", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{issues: []*types.Issue{
			{ID: "bd-existing", Title: "t", UpdatedAt: base},
		}}
		incoming := []*types.Issue{
			{ID: "bd-existing", Title: "t", UpdatedAt: base},
			{ID: "bd-new", Title: "brand new", UpdatedAt: base},
		}
		_, _, plan, err := filterStaleImportIssues(context.Background(), store, incoming)
		if err != nil {
			t.Fatalf("filterStaleImportIssues: %v", err)
		}
		if len(plan.NewIDs) != 1 || plan.NewIDs[0] != "bd-new" {
			t.Fatalf("plan.NewIDs = %#v, want [bd-new]", plan.NewIDs)
		}
	})

	t.Run("all_new_no_local_matches", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{} // empty db: nothing matches
		incoming := []*types.Issue{
			{ID: "bd-a", Title: "a", UpdatedAt: base},
			{ID: "bd-b", Title: "b", UpdatedAt: base},
		}
		_, _, plan, err := filterStaleImportIssues(context.Background(), store, incoming)
		if err != nil {
			t.Fatalf("filterStaleImportIssues: %v", err)
		}
		if len(plan.NewIDs) != 2 {
			t.Fatalf("plan.NewIDs = %#v, want both bd-a and bd-b", plan.NewIDs)
		}
	})
}

// GH#4901: re-importing a byte-identical snapshot must classify every row
// as unchanged, not as a create.
func TestClassifyDryRunImport(t *testing.T) {
	base := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

	t.Run("identical_snapshot_reports_zero_creates", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{issues: []*types.Issue{
			{ID: "bd-a", Title: "a", Status: types.StatusOpen, UpdatedAt: base},
			{ID: "bd-b", Title: "b", Status: types.StatusOpen, UpdatedAt: base},
		}}
		incoming := []*types.Issue{
			{ID: "bd-a", Title: "a", Status: types.StatusOpen, UpdatedAt: base},
			{ID: "bd-b", Title: "b", Status: types.StatusOpen, UpdatedAt: base},
		}
		result, err := classifyDryRunImport(context.Background(), store, incoming, false)
		if err != nil {
			t.Fatalf("classifyDryRunImport: %v", err)
		}
		if result.Created != 0 {
			t.Fatalf("Created = %d, want 0 (no row is actually new)", result.Created)
		}
		if result.Unchanged != 2 {
			t.Fatalf("Unchanged = %d, want 2", result.Unchanged)
		}
		if result.Updated != 0 || result.Skipped != 0 {
			t.Fatalf("Updated = %d, Skipped = %d, want both 0", result.Updated, result.Skipped)
		}
	})

	t.Run("distinguishes_create_update_stale_unchanged", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{issues: []*types.Issue{
			{ID: "bd-unchanged", Title: "same", Status: types.StatusOpen, UpdatedAt: base},
			{ID: "bd-updated", Title: "old title", Status: types.StatusOpen, UpdatedAt: base},
			{ID: "bd-stale", Title: "t", Status: types.StatusOpen, UpdatedAt: base.Add(time.Hour)},
		}}
		incoming := []*types.Issue{
			{ID: "bd-unchanged", Title: "same", Status: types.StatusOpen, UpdatedAt: base},
			{ID: "bd-updated", Title: "new title", Status: types.StatusOpen, UpdatedAt: base.Add(time.Hour)},
			{ID: "bd-stale", Title: "t", Status: types.StatusOpen, UpdatedAt: base},
			{ID: "bd-created", Title: "brand new", Status: types.StatusOpen, UpdatedAt: base},
		}
		result, err := classifyDryRunImport(context.Background(), store, incoming, false)
		if err != nil {
			t.Fatalf("classifyDryRunImport: %v", err)
		}
		if result.Created != 1 || len(result.ImportedIDs) != 1 || result.ImportedIDs[0] != "bd-created" {
			t.Fatalf("Created = %d, ImportedIDs = %#v, want [bd-created]", result.Created, result.ImportedIDs)
		}
		if result.Updated != 1 || len(result.UpdatedIssues) != 1 || result.UpdatedIssues[0].ID != "bd-updated" {
			t.Fatalf("Updated = %d, UpdatedIssues = %#v, want [bd-updated]", result.Updated, result.UpdatedIssues)
		}
		if result.Skipped != 1 || len(result.StaleSkippedIDs) != 1 || result.StaleSkippedIDs[0] != "bd-stale" {
			t.Fatalf("Skipped = %d, StaleSkippedIDs = %#v, want [bd-stale]", result.Skipped, result.StaleSkippedIDs)
		}
		if result.Unchanged != 1 {
			t.Fatalf("Unchanged = %d, want 1 (bd-unchanged)", result.Unchanged)
		}
	})

	// GH#4901 follow-up: --allow-stale still bypasses the stale guard (no
	// row is skipped or tie-kept), but a row matching an existing local
	// issue is an update, not a create — the old blanket "every row is
	// Created" report didn't match execution, which upserts (not inserts)
	// a row whose ID already exists.
	t.Run("allow_stale_classifies_existing_rows_as_updated", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{issues: []*types.Issue{
			{ID: "bd-stale", Title: "t", UpdatedAt: base.Add(time.Hour)},
		}}
		incoming := []*types.Issue{
			{ID: "bd-stale", Title: "restored older snapshot", UpdatedAt: base},
			{ID: "bd-new", Title: "brand new", UpdatedAt: base},
		}
		result, err := classifyDryRunImport(context.Background(), store, incoming, true)
		if err != nil {
			t.Fatalf("classifyDryRunImport: %v", err)
		}
		if result.Updated != 1 || len(result.UpdatedIssues) != 1 || result.UpdatedIssues[0].ID != "bd-stale" {
			t.Fatalf("Updated = %d, UpdatedIssues = %#v, want [bd-stale]", result.Updated, result.UpdatedIssues)
		}
		if result.Created != 1 || len(result.ImportedIDs) != 1 || result.ImportedIDs[0] != "bd-new" {
			t.Fatalf("Created = %d, ImportedIDs = %#v, want [bd-new]", result.Created, result.ImportedIDs)
		}
		if result.Skipped != 0 {
			t.Fatalf("Skipped = %d, want 0 under --allow-stale (never stale-skips)", result.Skipped)
		}
	})

	// GH#4901 follow-up: a title-only row (no ID) and a zero-UpdatedAt row
	// have nothing to stale-check, but they still write on execution — the
	// pre-filter must classify them via an existence lookup instead of
	// falling through as "unchanged".
	t.Run("title_only_and_untimestamped_rows_never_unchanged", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{issues: []*types.Issue{
			{ID: "bd-existing", Title: "old title", UpdatedAt: base},
		}}
		incoming := []*types.Issue{
			{Title: "title only, no id"},            // new: no ID to look up
			{ID: "bd-existing", Title: "new title"}, // zero UpdatedAt, matches local -> update
		}
		result, err := classifyDryRunImport(context.Background(), store, incoming, false)
		if err != nil {
			t.Fatalf("classifyDryRunImport: %v", err)
		}
		if result.Unchanged != 0 {
			t.Fatalf("Unchanged = %d, want 0 (neither row is a clean re-import)", result.Unchanged)
		}
		if result.Created != 1 {
			t.Fatalf("Created = %d, want 1 (title-only row)", result.Created)
		}
		if len(result.ImportedIDs) != 0 {
			t.Fatalf("ImportedIDs = %#v, want empty (title-only row has no ID to report)", result.ImportedIDs)
		}
		if result.Updated != 1 || len(result.UpdatedIssues) != 1 || result.UpdatedIssues[0].ID != "bd-existing" {
			t.Fatalf("Updated = %d, UpdatedIssues = %#v, want [bd-existing]", result.Updated, result.UpdatedIssues)
		}
	})

	// A batch with no IDs takes the filter's title-only short-circuit, which
	// must still classify every row as created for dry-run reporting.
	t.Run("all_title_only_rows_report_created", func(t *testing.T) {
		incoming := []*types.Issue{
			{Title: "first title-only row"},
			{Title: "second title-only row"},
		}
		result, err := classifyDryRunImport(context.Background(), &fakeImportIssueLookupStore{}, incoming, false)
		if err != nil {
			t.Fatalf("classifyDryRunImport: %v", err)
		}
		if result.Created != len(incoming) {
			t.Fatalf("Created = %d, want %d", result.Created, len(incoming))
		}
		if result.Updated != 0 || result.Unchanged != 0 {
			t.Fatalf("Updated = %d, Unchanged = %d, want both 0", result.Updated, result.Unchanged)
		}
		if sum := result.Created + result.Updated + result.Unchanged; sum != len(incoming) {
			t.Fatalf("Created + Updated + Unchanged = %d, want %d", sum, len(incoming))
		}
	})

	// bd-hj85c cleanup: a tie-kept row (same-second timestamp, differing
	// content) is not rewritten by the upsert, so it belongs in Unchanged,
	// not Updated — it's still surfaced separately via TieKeptLocalIDs, and
	// the three category counts must sum to the rows considered.
	t.Run("tie_kept_local_counts_as_unchanged_not_updated", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{issues: []*types.Issue{
			{ID: "bd-tie", Title: "t", Notes: "local notes", UpdatedAt: base},
		}}
		incoming := []*types.Issue{
			{ID: "bd-tie", Title: "t", UpdatedAt: base},
		}
		result, err := classifyDryRunImport(context.Background(), store, incoming, false)
		if err != nil {
			t.Fatalf("classifyDryRunImport: %v", err)
		}
		if result.Updated != 0 {
			t.Fatalf("Updated = %d, want 0 (tie-kept rows are not rewritten)", result.Updated)
		}
		if result.Unchanged != 1 {
			t.Fatalf("Unchanged = %d, want 1", result.Unchanged)
		}
		if len(result.TieKeptLocalIDs) != 1 || result.TieKeptLocalIDs[0] != "bd-tie" {
			t.Fatalf("TieKeptLocalIDs = %#v, want [bd-tie]", result.TieKeptLocalIDs)
		}
		if sum := result.Created + result.Updated + result.Unchanged; sum != 1 {
			t.Fatalf("counts do not sum to the row considered: %+v (sum=%d)", result, sum)
		}
	})

	// Cleanup: duplicate incoming rows for the same ID must not produce a
	// duplicate entry in the reported ID list, but each row still counts
	// toward Created so the totals reflect every row considered.
	t.Run("dedupes_new_ids_for_duplicate_incoming_rows", func(t *testing.T) {
		store := &fakeImportIssueLookupStore{} // empty db: nothing matches
		incoming := []*types.Issue{
			{ID: "bd-dup", Title: "first", UpdatedAt: base},
			{ID: "bd-dup", Title: "second copy of same row", UpdatedAt: base},
		}
		result, err := classifyDryRunImport(context.Background(), store, incoming, false)
		if err != nil {
			t.Fatalf("classifyDryRunImport: %v", err)
		}
		if len(result.ImportedIDs) != 1 || result.ImportedIDs[0] != "bd-dup" {
			t.Fatalf("ImportedIDs = %#v, want deduped [bd-dup]", result.ImportedIDs)
		}
		if result.Created != 2 {
			t.Fatalf("Created = %d, want 2 (both rows counted even though the ID is deduped for display)", result.Created)
		}
	})

	t.Run("empty_input", func(t *testing.T) {
		result, err := classifyDryRunImport(context.Background(), &fakeImportIssueLookupStore{}, nil, false)
		if err != nil {
			t.Fatalf("classifyDryRunImport: %v", err)
		}
		if result.Created != 0 || result.Updated != 0 || result.Unchanged != 0 || result.Skipped != 0 {
			t.Fatalf("classifyDryRunImport(empty) = %#v, want all zero", result)
		}
	})
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

// bd-axluy: redirected stdin without "-" (or a file argument) must be an
// error, not a silent import of the default JSONL. The guard fires before any
// store access, so these cases need no database; the pass-through cases are
// asserted by seeing a later error than the guard's.
func TestRunImportInnerRejectsRedirectedStdinWithoutSource(t *testing.T) {
	t.Chdir(t.TempDir())
	origStdin, origStore := os.Stdin, store
	store = nil
	t.Cleanup(func() {
		os.Stdin = origStdin
		store = origStore
		importInput = ""
	})

	pipeStdin := func(t *testing.T) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		w.Close() // immediate EOF: nothing should ever read this pipe anyway
		t.Cleanup(func() { r.Close() })
		os.Stdin = r
	}

	t.Run("piped stdin, no args", func(t *testing.T) {
		pipeStdin(t)
		err := runImportInner(nil)
		if err == nil || !strings.Contains(err.Error(), "stdin is redirected") {
			t.Fatalf("err = %v, want redirected-stdin guard error", err)
		}
	})

	t.Run("regular-file stdin, no args", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "in.jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { f.Close() })
		os.Stdin = f
		err = runImportInner(nil)
		if err == nil || !strings.Contains(err.Error(), "stdin is redirected") {
			t.Fatalf("err = %v, want redirected-stdin guard error", err)
		}
	})

	t.Run("piped stdin with explicit dash passes the guard", func(t *testing.T) {
		pipeStdin(t)
		err := runImportInner([]string{"-"})
		if err == nil || strings.Contains(err.Error(), "stdin is redirected") {
			t.Fatalf("err = %v, want a non-guard error (nil store)", err)
		}
	})

	t.Run("piped stdin with explicit file passes the guard", func(t *testing.T) {
		pipeStdin(t)
		path := filepath.Join(t.TempDir(), "explicit.jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		err := runImportInner([]string{path})
		if err == nil || strings.Contains(err.Error(), "stdin is redirected") {
			t.Fatalf("err = %v, want a non-guard error (nil store)", err)
		}
	})

	t.Run("character-device stdin, no args passes the guard", func(t *testing.T) {
		devNull, err := os.Open(os.DevNull)
		if err != nil {
			t.Skipf("open %s: %v", os.DevNull, err)
		}
		t.Cleanup(func() { devNull.Close() })
		os.Stdin = devNull
		err = runImportInner(nil)
		if err == nil || strings.Contains(err.Error(), "stdin is redirected") {
			t.Fatalf("err = %v, want a non-guard error (no workspace here)", err)
		}
	})
}

// fakeImportRelationStore adds the optional bulk aux loaders the resume fast
// path (wy-sbgucn) proves rows unchanged with.
type fakeImportRelationStore struct {
	fakeImportIssueLookupStore
	comments map[string][]*types.Comment
	deps     map[string][]*types.Dependency
	loads    int
}

func (f *fakeImportRelationStore) GetCommentsForIssues(_ context.Context, _ []string) (map[string][]*types.Comment, error) {
	f.loads++
	return f.comments, nil
}

func (f *fakeImportRelationStore) GetDependencyRecordsForIssues(_ context.Context, _ []string) (map[string][]*types.Dependency, error) {
	f.loads++
	return f.deps, nil
}

// wy-sbgucn: a re-run of an interrupted chunked import must not rewrite the
// committed prefix. A tie row (same updated_at, same columns) whose labels,
// comments and dependencies are all already stored is provably a no-op write
// and leaves the write set; any aux row not yet stored keeps the row in.
func TestFilterStaleImportIssuesSkipsProvenUnchangedRowsOnResume(t *testing.T) {
	base := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	commentAt := base.Add(-time.Hour)
	mk := func(id string) *types.Issue {
		return &types.Issue{
			ID: id, Title: "row " + id, UpdatedAt: base,
			Labels:       []string{"lane:beads", "tier:fable"},
			Comments:     []*types.Comment{{ID: "c-" + id, Author: "cat", Text: "drill", CreatedAt: commentAt}},
			Dependencies: []*types.Dependency{{IssueID: id, DependsOnID: "bd-root", Type: types.DepBlocks}},
		}
	}
	incoming := []*types.Issue{mk("bd-same"), mk("bd-nocomment"), mk("bd-nodep"), mk("bd-nolabel"), mk("bd-new")}
	local := func(id string, labels ...string) *types.Issue {
		return &types.Issue{ID: id, Title: "row " + id, UpdatedAt: base, Labels: labels}
	}
	storedComment := []*types.Comment{{ID: "other-id", IssueID: "x", Author: "cat", Text: "drill", CreatedAt: commentAt.Add(500 * time.Millisecond)}}
	storedDep := []*types.Dependency{{DependsOnID: "bd-root", Type: types.DepBlocks}}
	store := &fakeImportRelationStore{
		fakeImportIssueLookupStore: fakeImportIssueLookupStore{issues: []*types.Issue{
			local("bd-same", "tier:fable", "lane:beads"),
			local("bd-nocomment", "lane:beads", "tier:fable"),
			local("bd-nodep", "lane:beads", "tier:fable"),
			local("bd-nolabel", "lane:beads"),
		}},
		comments: map[string][]*types.Comment{"bd-same": storedComment, "bd-nodep": storedComment, "bd-nolabel": storedComment},
		deps:     map[string][]*types.Dependency{"bd-same": storedDep, "bd-nocomment": storedDep, "bd-nolabel": storedDep},
	}

	filtered, skippedIDs, plan, err := filterStaleImportIssues(context.Background(), store, incoming)
	if err != nil {
		t.Fatalf("filterStaleImportIssues: %v", err)
	}
	if len(skippedIDs) != 0 {
		t.Fatalf("skippedIDs = %#v, want none (nothing is stale)", skippedIDs)
	}
	var kept []string
	for _, issue := range filtered {
		kept = append(kept, issue.ID)
	}
	want := []string{"bd-nocomment", "bd-nodep", "bd-nolabel", "bd-new"}
	if strings.Join(kept, ",") != strings.Join(want, ",") {
		t.Fatalf("write set = %v, want %v (only the provably stored row leaves it, order preserved)", kept, want)
	}
	if len(plan.Unchanged) != 1 || plan.Unchanged[0] != "bd-same" {
		t.Fatalf("plan.Unchanged = %#v, want [bd-same]", plan.Unchanged)
	}
	if len(plan.TieKeptLocal) != 0 || len(plan.Updates) != 0 {
		t.Fatalf("plan = %+v, want no tie conflicts or updates", plan)
	}
	if store.loads != 2 {
		t.Fatalf("aux loads = %d, want exactly one comment load + one dependency load", store.loads)
	}
}

// A fresh import (no local match) must not pay for the aux loads at all, and
// a store without the bulk loaders keeps the pre-wy-sbgucn behavior: every
// tie row stays in the write set.
func TestFilterStaleImportIssuesResumeFastPathIsOptIn(t *testing.T) {
	base := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	incoming := []*types.Issue{{ID: "bd-tie", Title: "t", UpdatedAt: base}, {ID: "bd-new", Title: "n", UpdatedAt: base}}

	fresh := &fakeImportRelationStore{}
	filtered, _, plan, err := filterStaleImportIssues(context.Background(), fresh, incoming)
	if err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if len(filtered) != 2 || fresh.loads != 0 || len(plan.Unchanged) != 0 {
		t.Fatalf("fresh import: kept %d rows, %d aux loads, unchanged %v; want 2 rows, 0 loads, none", len(filtered), fresh.loads, plan.Unchanged)
	}

	plain := &fakeImportIssueLookupStore{issues: []*types.Issue{{ID: "bd-tie", Title: "t", UpdatedAt: base}}}
	filtered, _, plan, err = filterStaleImportIssues(context.Background(), plain, incoming)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	if len(filtered) != 2 || len(plan.Unchanged) != 0 {
		t.Fatalf("store without bulk loaders: kept %d rows, unchanged %v; want both rows rewritten", len(filtered), plan.Unchanged)
	}

	// A real read failure is not an opt-out: it stops the import.
	broken := &failingImportRelationStore{fakeImportIssueLookupStore: plain}
	if _, _, _, err := filterStaleImportIssues(context.Background(), broken, incoming); err == nil || !strings.Contains(err.Error(), "check existing comments before import") {
		t.Fatalf("read failure: err = %v, want the comment-load failure surfaced", err)
	}
}

type failingImportRelationStore struct{ *fakeImportIssueLookupStore }

func (f *failingImportRelationStore) GetCommentsForIssues(_ context.Context, _ []string) (map[string][]*types.Comment, error) {
	return nil, errors.New("connection lost")
}

// The real import's result carries the skipped-as-unchanged count so the
// resume reports what it did not have to rewrite.
func TestImportIssuesCoreReportsUnchangedRowsOnResume(t *testing.T) {
	base := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	incoming := []*types.Issue{{ID: "bd-same", Title: "t", UpdatedAt: base}, {ID: "bd-new", Title: "n", UpdatedAt: base}}
	store := &fakeImportRelationStore{fakeImportIssueLookupStore: fakeImportIssueLookupStore{issues: []*types.Issue{{ID: "bd-same", Title: "t", UpdatedAt: base}}}}
	result, err := importIssuesCore(context.Background(), "", store, incoming, ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if result.Created != 1 || result.Unchanged != 1 || len(result.ImportedIDs) != 1 || result.ImportedIDs[0] != "bd-new" {
		t.Fatalf("result = %+v, want Created 1 (bd-new), Unchanged 1", result)
	}
	if len(store.created) != 1 || store.created[0].ID != "bd-new" {
		t.Fatalf("written rows = %v, want only bd-new", store.created)
	}
}

func TestBulkLoadPoolReadTimeoutOnlyForImport(t *testing.T) {
	if got := bulkLoadPoolReadTimeout(importCmd); got != importPoolReadTimeout {
		t.Fatalf("import fallback = %v, want %v", got, importPoolReadTimeout)
	}
	if got := bulkLoadPoolReadTimeout(exportCmd); got != 0 {
		t.Fatalf("export fallback = %v, want 0 (pool default)", got)
	}
	if got := bulkLoadPoolReadTimeout(nil); got != 0 {
		t.Fatalf("nil cmd fallback = %v, want 0", got)
	}
}

// wy-sbgucn (Fable review wy-03ne4j BLOCKER): dropping a tie row from the
// write set also bypasses RestoreLeaseOnImportInTx, so the proof must cover
// the ephemeral lease row too. A row is provably unchanged on the lease axis
// only when the rewrite's restore would upsert nothing new (no incoming
// lease, no live claim, or the local lease already equals the snapshot's) and
// its reconcile would drop nothing (no local lease row, or a live claim that
// keeps it). Every other shape stays in the write set.
func TestFilterStaleImportIssuesResumeKeepsRowsNeedingLeaseReconciliation(t *testing.T) {
	base := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	expires := base.Add(30 * time.Minute)
	beat := base.Add(-time.Minute)
	otherExpires := expires.Add(time.Hour)
	otherBeat := beat.Add(-time.Minute)
	claimed := func(id string, leaseExpires, heartbeat *time.Time, node string) *types.Issue {
		return &types.Issue{
			ID: id, Title: "row " + id, UpdatedAt: base,
			Status: types.StatusInProgress, Assignee: "plato",
			LeaseExpiresAt: leaseExpires, HeartbeatAt: heartbeat, LeaseGrantedNode: node,
		}
	}
	unassigned := func(id string, leaseExpires, heartbeat *time.Time) *types.Issue {
		return &types.Issue{
			ID: id, Title: "row " + id, UpdatedAt: base, Status: types.StatusInProgress,
			LeaseExpiresAt: leaseExpires, HeartbeatAt: heartbeat,
		}
	}
	open := func(id string, leaseExpires, heartbeat *time.Time) *types.Issue {
		return &types.Issue{
			ID: id, Title: "row " + id, UpdatedAt: base, Status: types.StatusOpen,
			LeaseExpiresAt: leaseExpires, HeartbeatAt: heartbeat,
		}
	}
	incoming := []*types.Issue{
		claimed("bd-lease-missing", &expires, &beat, "studio"), // restore would INSERT the lease row
		claimed("bd-lease-equal", &expires, &beat, "studio"),   // lease row already identical
		claimed("bd-lease-differs", &expires, &beat, "studio"), // local lease differs (expiry)
		claimed("bd-lease-node", &expires, &beat, "studio"),    // local lease differs (granting node)
		claimed("bd-lease-beat", &expires, &beat, "studio"),    // local lease differs (heartbeat only)
		claimed("bd-lease-nobeat", &expires, nil, "studio"),    // snapshot heartbeat would be stamped live
		claimed("bd-lease-local-only", nil, nil, ""),           // live claim keeps its local lease
		open("bd-lease-orphan", nil, nil),                      // reconcile would DROP the orphaned lease row
		unassigned("bd-lease-unassigned", nil, nil),            // in_progress but unassigned: the lease row is an orphan too
		open("bd-lease-none", nil, nil),                        // nothing on either side
		open("bd-lease-snapshot-open", &expires, &beat),        // restore only fires for a live claim
	}
	store := &fakeImportRelationStore{
		fakeImportIssueLookupStore: fakeImportIssueLookupStore{issues: []*types.Issue{
			claimed("bd-lease-missing", nil, nil, ""),
			claimed("bd-lease-equal", &expires, &beat, "studio"),
			claimed("bd-lease-differs", &otherExpires, &beat, "studio"),
			claimed("bd-lease-node", &expires, &beat, "mini"),
			claimed("bd-lease-beat", &expires, &otherBeat, "studio"),
			claimed("bd-lease-nobeat", &expires, &beat, "studio"),
			claimed("bd-lease-local-only", &expires, &beat, "studio"),
			open("bd-lease-orphan", &expires, &beat),
			unassigned("bd-lease-unassigned", &expires, &beat),
			open("bd-lease-none", nil, nil),
			open("bd-lease-snapshot-open", nil, nil),
		}},
	}

	filtered, skippedIDs, plan, err := filterStaleImportIssues(context.Background(), store, incoming)
	if err != nil {
		t.Fatalf("filterStaleImportIssues: %v", err)
	}
	if len(skippedIDs) != 0 {
		t.Fatalf("skippedIDs = %#v, want none (nothing is stale)", skippedIDs)
	}
	var kept []string
	for _, issue := range filtered {
		kept = append(kept, issue.ID)
	}
	wantKept := []string{"bd-lease-missing", "bd-lease-differs", "bd-lease-node", "bd-lease-beat", "bd-lease-nobeat", "bd-lease-orphan", "bd-lease-unassigned"}
	if strings.Join(kept, ",") != strings.Join(wantKept, ",") {
		t.Fatalf("write set = %v, want %v (every row whose lease row the rewrite would touch)", kept, wantKept)
	}
	wantUnchanged := []string{"bd-lease-equal", "bd-lease-local-only", "bd-lease-none", "bd-lease-snapshot-open"}
	if strings.Join(plan.Unchanged, ",") != strings.Join(wantUnchanged, ",") {
		t.Fatalf("plan.Unchanged = %v, want %v", plan.Unchanged, wantUnchanged)
	}
	if len(plan.TieKeptLocal) != 0 || len(plan.Updates) != 0 {
		t.Fatalf("plan = %+v, want no tie conflicts or updates", plan)
	}
}

// Lease timestamps are DATETIME(0) in the store: a sub-second component on
// the snapshot side must not defeat the equality proof.
func TestImportLeaseAlreadyReconciledComparesAtSecondGranularity(t *testing.T) {
	base := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	expires, beat := base.Add(30*time.Minute), base.Add(-time.Minute)
	expiresSub, beatSub := expires.Add(400*time.Millisecond), beat.Add(999*time.Millisecond)
	local := &types.Issue{ID: "bd-x", Status: types.StatusInProgress, Assignee: "plato", LeaseExpiresAt: &expires, HeartbeatAt: &beat, LeaseGrantedNode: "studio"}
	incoming := &types.Issue{ID: "bd-x", Status: types.StatusInProgress, Assignee: "plato", LeaseExpiresAt: &expiresSub, HeartbeatAt: &beatSub, LeaseGrantedNode: "studio"}
	if !importLeaseAlreadyReconciled(incoming, local) {
		t.Fatalf("sub-second snapshot lease timestamps must still prove the lease row unchanged")
	}
	nextSecond := expires.Add(time.Second)
	incoming.LeaseExpiresAt = &nextSecond
	if importLeaseAlreadyReconciled(incoming, local) {
		t.Fatalf("a lease expiry one second apart is a different lease row")
	}
}
