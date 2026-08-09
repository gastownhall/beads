package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// IssueReader returns the inner store's query surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has
// to know which decorators a store is wearing. What it does NOT do is wrap the
// result — reads fire no completion hooks, because there is no completion to
// report. That is a statement about hooks, not an oversight, and it is why
// this file is three lines where hook_issue_operations.go is a hundred.
func (h *HookFiringStore) IssueReader() (issueops.Reader, error) {
	return h.inner.IssueReader()
}
