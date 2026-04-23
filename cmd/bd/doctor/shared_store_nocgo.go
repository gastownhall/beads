//go:build !cgo

package doctor

import (
	"context"
	"os"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// NewSharedStore opens a single read-only DoltStorage for the given repo path.
// Embedded-mode routing (bd-ffe) requires CGO; non-CGO builds only support
// server-mode Dolt.
func NewSharedStore(path string) *SharedStore {
	beadsDir := ResolveBeadsDirForRepo(path)
	ss := &SharedStore{beadsDir: beadsDir}

	if sharedStoreNeedsLocalDoltDir(beadsDir) {
		doltPath := getDatabasePath(beadsDir)
		if _, err := os.Stat(doltPath); os.IsNotExist(err) {
			return ss
		}
	}

	store, err := dolt.NewFromConfigWithOptions(context.Background(), beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return ss
	}
	ss.store = store
	ss.rawDB = store.UnderlyingDB()
	return ss
}
