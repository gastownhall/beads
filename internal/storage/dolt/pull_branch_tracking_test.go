package dolt

import (
	"context"
	"testing"
	"time"
)

// TestPullWithoutUpstreamTrackingTakesTheFetchMergePath is the GH#3144 case
// after bd-e2q8zs: a remote added with DOLT_REMOTE('add') (as `bd dolt remote
// add` does) leaves the branch with no upstream tracking. The default-remote
// pull no longer tries DOLT_PULL and falls back on its branch-tracking error:
// its one path fetches and merges origin/main, so it succeeds without
// tracking and its merge message names the tracking ref.
func TestPullWithoutUpstreamTrackingTakesTheFetchMergePath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fx := newPullFixture(t, ctx, "origin", false, true)
	store := fx.store

	var upstream string
	if err := store.db.QueryRowContext(ctx,
		"SELECT COALESCE(remote, '') FROM dolt_branches WHERE name = 'main'").Scan(&upstream); err != nil {
		t.Fatalf("read main's upstream: %v", err)
	}
	if upstream != "" {
		t.Fatalf("precondition broken: main tracks %q; the GH#3144 case has no upstream tracking", upstream)
	}

	report, err := store.pullTransportReporting(ctx, "origin")
	if err != nil {
		t.Fatalf("pull without upstream tracking: %v", err)
	}
	if !report.Reported || !report.Merged {
		t.Errorf("pull report = %+v, want the merge's own report with Merged", report)
	}
	assertMergeMessageOmitsRemoteURL(t, ctx, store.db, "origin/main")
}
