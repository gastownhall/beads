package dolt

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi/storequerier"
	"github.com/steveyegge/beads/issueops"
)

// Querier returns the guarded boolean-query surface for this store.
func (s *DoltStore) Querier() (issueops.Querier, error) {
	return newQuerier(s)
}

// newQuerier returns guarded queries backed by store.
//
// The implementation is the shared one, for the reason newIssueReader gives:
// the two Dolt-backed stores differ below storage.DoltStorage, not above it,
// so a second copy here would be a copy of nothing but the constructor.
func newQuerier(store *DoltStore) (issueops.Querier, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newQuerier", Backend: "nil"}
	}
	return storequerier.New(store)
}
