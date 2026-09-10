//go:build cgo

package embeddeddolt

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi/storeworkspaceconfig"
	"github.com/steveyegge/beads/issueops"
)

// WorkspaceConfig returns the guarded workspace-settings surface for this
// store.
func (s *EmbeddedDoltStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return newWorkspaceConfig(s)
}

// newWorkspaceConfig returns guarded workspace settings backed by store.
//
// The implementation is the shared one: the two Dolt-backed stores differ
// below storage.DoltStorage, not above it, so a second copy here would be a
// copy of nothing but the constructor.
func newWorkspaceConfig(store *EmbeddedDoltStore) (issueops.WorkspaceConfig, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "newWorkspaceConfig", Backend: "nil"}
	}
	return storeworkspaceconfig.New(store)
}
