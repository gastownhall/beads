//go:build cgo

package embeddeddolt

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi/storereadycounter"
	"github.com/steveyegge/beads/issueops"
)

// ReadyCounter returns the guarded ready-count surface for this store.
func (s *EmbeddedDoltStore) ReadyCounter() (issueops.ReadyCounter, error) {
	return newReadyCounter(s)
}

// newReadyCounter returns guarded ready counts backed by store.
//
// The implementation is the shared one: the two Dolt-backed stores differ
// below storage.DoltStorage, not above it, so a second copy here would be a
// copy of nothing but the constructor.
func newReadyCounter(store *EmbeddedDoltStore) (issueops.ReadyCounter, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newReadyCounter", Backend: "nil"}
	}
	return storereadycounter.New(store)
}
