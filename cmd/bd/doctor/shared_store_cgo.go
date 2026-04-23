//go:build cgo

package doctor

import (
	"context"
	"os"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// NewSharedStore opens a single read-only DoltStorage for the given repo path,
// routing to the embedded engine or an external Dolt server based on
// metadata.json (bd-ffe).
//
// In server mode, the returned SharedStore holds an open *dolt.DoltStore and
// a raw *sql.DB for diagnostic queries. In embedded mode, the SharedStore
// records the mode but does NOT open the embedded Dolt engine — doing so
// would hold the exclusive flock for the entire doctor run, deadlocking
// subprocesses that the doctor itself spawns (e.g. `bd prime` via
// VerifyPrimeOutput). DB-backed checks consult SharedStore.IsEmbedded() and
// emit a clear "skipped in embedded mode" diagnostic instead.
//
// Callers MUST call Close() when done (typically via defer).
func NewSharedStore(path string) *SharedStore {
	beadsDir := ResolveBeadsDirForRepo(path)
	ss := &SharedStore{beadsDir: beadsDir}

	if !sharedStoreIsEmbeddedConfig(beadsDir) {
		openSharedStoreServer(context.Background(), ss, beadsDir)
		return ss
	}

	ss.isEmbedded = true
	return ss
}

// openSharedStoreServer populates ss for server-mode configs. Failures leave
// ss.store as nil so downstream checks surface their own "Unable to open
// database" diagnostics.
func openSharedStoreServer(ctx context.Context, ss *SharedStore, beadsDir string) {
	if sharedStoreNeedsLocalDoltDir(beadsDir) {
		doltPath := getDatabasePath(beadsDir)
		if _, err := os.Stat(doltPath); os.IsNotExist(err) {
			return
		}
	}
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return
	}
	ss.store = store
	ss.rawDB = store.UnderlyingDB()
}
