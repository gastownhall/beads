//go:build cgo

package embeddeddolt

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi/storereadylister"
	"github.com/steveyegge/beads/issueops"
)

// ReadyLister returns the guarded ready-listing surface for this store.
func (s *EmbeddedDoltStore) ReadyLister() (issueops.ReadyLister, error) {
	return newReadyLister(s)
}

// newReadyLister returns guarded ready listings backed by store.
//
// The implementation is the shared one: the two Dolt-backed stores differ
// below storage.DoltStorage, not above it.
func newReadyLister(store *EmbeddedDoltStore) (issueops.ReadyLister, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newReadyLister", Backend: "nil"}
	}
	return storereadylister.New(store)
}
