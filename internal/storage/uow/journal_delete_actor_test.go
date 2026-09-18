package uow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestDeleterAttributesTheRequesterInTheJournal is the #5985 regression on the
// SECOND write plumbing: a delete through the unit-of-work Deleter — the route
// proxied-server `bd delete` and the HTTP API's deleteIssues both take — must
// journal its delete row and the cascade dep_remove rows under the request's
// actor.
//
// It is a real second vote and not an engine check. This leg does not run
// issueops.DeleteInTx: it reaches domain.deleteMany and the repository seam,
// which is a DIFFERENT set of journal call sites from the store body's. That
// seam is exactly where #5908's review caught the actor going missing once
// already (IssueRepository.MovePersistence), and where it went missing again
// here.
//
// One provider, no t.Parallel: this backend has no per-test copy-on-write
// branch and the journal table is database-global (see
// newUOWRoleFixtureProvider).
func TestDeleterAttributesTheRequesterInTheJournal(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "dja")

	configurer, ok := provider.(storage.EventsJournalConfigurer)
	require.Truef(t, ok, "provider %T does not implement storage.EventsJournalConfigurer", provider)
	configurer.SetEventsJournalEnabled(true)
	t.Cleanup(func() { configurer.SetEventsJournalEnabled(false) })

	cursorSource, ok := provider.(EventsJournalCursorSource)
	require.Truef(t, ok, "provider %T does not offer the EventsJournalCursor accessor", provider)
	cursor, err := cursorSource.EventsJournalCursor()
	require.NoError(t, err)

	kit := newUOWRoleFixtureKit(provider, "dja")
	const target = "dja-target"
	const dependent = "dja-dependent"
	for _, id := range []string{target, dependent} {
		require.NoError(t, kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "creator-1"))
	}
	// An inbound edge, so the forced delete orphans a survivor and has an edge
	// to cascade off — the shape #5985 reported.
	require.NoError(t, kit.AddDependency(ctx, &types.Dependency{
		IssueID: dependent, DependsOnID: target, Type: types.DepBlocks,
	}, "linker-1"))

	// Baseline AFTER the seeds, so the assertions read only this delete's rows.
	base, err := cursor.ReadEventsJournalPage(ctx, 0, 0)
	require.NoError(t, err)

	source, ok := provider.(DeleterSource)
	require.Truef(t, ok, "provider %T does not offer the Deleter accessor", provider)
	deleter, err := source.Deleter()
	require.NoError(t, err)
	_, err = deleter.Delete(ctx, publicops.DeleteRequest{
		IDs: []string{target}, Force: true, Actor: "deleter-1",
	})
	require.NoError(t, err)

	page, err := cursor.ReadEventsJournalPage(ctx, base.Head, 0)
	require.NoError(t, err)

	var sawDelete, sawDepRemove bool
	for _, row := range page.Rows {
		switch row.Op {
		case "delete":
			require.Equalf(t, "deleter-1", row.Actor,
				"delete row for %s must carry the requesting actor", row.IssueID)
			sawDelete = true
		case "dep_remove":
			// The cascade edge removal belongs to the identity whose delete
			// removed it, not to whoever created the edge.
			require.Equalf(t, "deleter-1", row.Actor,
				"cascade dep_remove row for %s must carry the requesting actor", row.IssueID)
			sawDepRemove = true
		}
	}
	require.Truef(t, sawDelete, "no delete row journaled: %+v", page.Rows)
	require.Truef(t, sawDepRemove, "no cascade dep_remove row journaled: %+v", page.Rows)
}
