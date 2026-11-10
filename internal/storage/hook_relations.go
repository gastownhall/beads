package storage

import (
	"github.com/steveyegge/beads/issueops"
)

// IssueRelations returns the inner store's neighbor-query surface.
//
// It recurses, like every other accessor on this decorator, rather than being
// absent: the accessor set is uniform across the chain, so a caller never has
// to know which decorators a store is wearing. What it does NOT do is wrap the
// result — reads fire no completion hooks, because there is no completion to
// report. That is the same statement hook_issue_reader.go makes, and it is why
// both files are three lines.
func (h *HookFiringStore) IssueRelations() (issueops.Relations, error) {
	return h.inner.IssueRelations()
}
