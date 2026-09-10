//go:build cgo

package embeddeddolt

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi/storeversionreconciler"
	"github.com/steveyegge/beads/issueops"
)

// VersionReconciler returns the clone-local version markers for this store.
func (s *EmbeddedDoltStore) VersionReconciler() (issueops.VersionReconciler, error) {
	return newVersionReconciler(s)
}

// newVersionReconciler returns the version markers backed by store.
//
// The implementation is the shared one: the two Dolt-backed stores differ below
// storage.DoltStorage, not above it, so a second copy here would be a copy of
// nothing but the constructor.
func newVersionReconciler(store *EmbeddedDoltStore) (issueops.VersionReconciler, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newVersionReconciler", Backend: "nil"}
	}
	return storeversionreconciler.New(store)
}
