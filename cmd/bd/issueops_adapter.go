package main

import (
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// newIssueOperations asks a store for its guarded issue-lifecycle surface.
// It is a variable purely as a test seam: the write-verb parity tests
// substitute a counting facade so "which operations did this command perform"
// stays observable. Production always calls the store's own accessor.
var newIssueOperations = func(st beads.Storage) (issueops.Lifecycle, error) {
	return st.IssueLifecycle()
}

// writeOps returns the issue-lifecycle surface for st, the store a write verb
// is actually writing to.
//
// Callers build it per write site — the global store, a RoutedResult's store,
// or a --repo target — rather than once per command, because each store hands
// back a lifecycle carrying its own decorator stack. Building it over the
// store that owns the issue is what keeps a routed write hookless and a local
// write hooked, exactly as the direct store calls did.
func writeOps(st storage.DoltStorage) (issueops.Lifecycle, error) {
	return newIssueOperations(st)
}
