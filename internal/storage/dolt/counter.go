package dolt

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi/storecounter"
	"github.com/steveyegge/beads/issueops"
)

// Counter returns the guarded issue-count surface for this store.
func (s *DoltStore) Counter() (issueops.Counter, error) {
	return newCounter(s)
}

// newCounter returns guarded counts backed by store.
//
// The implementation is the shared one, for the reason newIssueReader gives:
// the two Dolt-backed stores differ below storage.DoltStorage, not above it,
// so a second copy here would be a copy of nothing but the constructor.
func newCounter(store *DoltStore) (issueops.Counter, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newCounter", Backend: "nil"}
	}
	return storecounter.New(store)
}
