//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/historyoffequiv"
)

// TestVersionedHistoryOffMatchesPreStackGolden_Embedded is the embedded
// leg's versioned-history flag-off differential (internal/storage/historyoffequiv):
// on a store whose versioned history was never turned on, the scripted
// mutation set must leave every table as the code before the
// versioned-history stack left it, apart from that stack's new columns and
// table staying NULL and empty.
func TestVersionedHistoryOffMatchesPreStackGolden_Embedded(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, historyoffequiv.Prefix)
	historyoffequiv.Run(t, historyoffequiv.Leg{
		Name:  "embedded",
		Roles: te.store,
		Wake: func(ctx context.Context) error {
			te.store.WakeExpiredDefersAdvisory(ctx)
			return nil
		},
		Extra: historyoffequiv.DoltStorageSteps(te.store),
		Query: historyoffequiv.SQLQuery(func(ctx context.Context) (*sql.DB, func(), error) {
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				return nil, nil, err
			}
			return db, func() { _ = cleanup() }, nil
		}),
	})
}
