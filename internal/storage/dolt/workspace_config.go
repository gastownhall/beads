package dolt

import (
	"github.com/jonbaldie/beads/internal/storage"
	"github.com/jonbaldie/beads/internal/workapi/storeworkspaceconfig"
	"github.com/jonbaldie/beads/issueops"
)

// WorkspaceConfig returns the guarded workspace-settings surface for this
// store.
func (s *DoltStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return newWorkspaceConfig(s)
}

// newWorkspaceConfig returns guarded workspace settings backed by store.
//
// The implementation is the shared one, for the reason newIssueReader gives:
// the two Dolt-backed stores differ below storage.DoltStorage, not above it,
// so a second copy here would be a copy of nothing but the constructor.
func newWorkspaceConfig(store *DoltStore) (issueops.WorkspaceConfig, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newWorkspaceConfig", Backend: "nil"}
	}
	return storeworkspaceconfig.New(store)
}
