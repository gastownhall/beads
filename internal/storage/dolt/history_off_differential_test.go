package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/historyoffequiv"
)

// TestVersionedHistoryOffMatchesPreStackGolden_Dolt is the server leg's
// versioned-history flag-off differential (internal/storage/historyoffequiv):
// on a store whose versioned history was never turned on, the scripted
// mutation set must leave every table as the code before the
// versioned-history stack left it, apart from that stack's new columns and
// table staying NULL and empty.
func TestVersionedHistoryOffMatchesPreStackGolden_Dolt(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	t.Cleanup(cleanup)
	if err := store.SetConfig(t.Context(), "issue_prefix", historyoffequiv.Prefix); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	historyoffequiv.Run(t, historyoffequiv.Leg{
		Name:  "server",
		Roles: store,
		Wake: func(ctx context.Context) error {
			store.WakeExpiredDefersAdvisory(ctx)
			return nil
		},
		Extra: historyoffequiv.DoltStorageSteps(store),
		Query: historyoffequiv.SQLQuery(func(context.Context) (*sql.DB, func(), error) {
			return store.DB(), func() {}, nil
		}),
	})
}
