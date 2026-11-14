package issueops

import (
	"errors"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// createIdentityReset remembers which identity fields of one create call were
// EMPTY when the caller handed its issues over — the fields the create itself
// assigns when it runs: the minted issue id, the dependency IssueIDs that
// default to their owning issue (PersistDependenciesWithOptionsResult), the
// content-derived comment ids (PersistComments). A replayed transaction
// attempt must derive every one of them again: the ids a rolled-back attempt
// assigned name rows that attempt never wrote, and the replay would either
// collide with a row a writer landed under the minted id in the meantime, or
// carry the ids as if the caller had supplied them (bd-t4h0n7, PR #6754).
type createIdentityReset struct {
	assigned []*string
}

// RecordCreateIdentities records, in the caller's issues, every identity
// field that is empty NOW, before the create's first attempt runs: those are
// the fields the create assigns, and the ones a replay must clear. A field
// the caller supplied is never recorded, so a replay never discards it.
func RecordCreateIdentities(issues []*types.Issue) *createIdentityReset {
	reset := &createIdentityReset{}
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if issue.ID == "" {
			reset.assigned = append(reset.assigned, &issue.ID)
		}
		for _, dep := range issue.Dependencies {
			if dep != nil && dep.IssueID == "" {
				reset.assigned = append(reset.assigned, &dep.IssueID)
			}
		}
		for _, comment := range issue.Comments {
			if comment != nil && comment.ID == "" {
				reset.assigned = append(reset.assigned, &comment.ID)
			}
		}
	}
	return reset
}

// Reset clears every recorded field, so the next attempt of the create
// re-derives its identities instead of trusting a rolled-back attempt's. A
// nil receiver is a no-op, so a caller that never recorded can still reset.
func (r *createIdentityReset) Reset() {
	if r == nil {
		return
	}
	for _, field := range r.assigned {
		*field = ""
	}
}

// ResetOnFailedTransaction resets after a failed create, unless the failure
// is indeterminate: a commit whose result was lost may have landed, and the
// ids the attempt assigned may name rows that exist. The caller keeps them,
// exactly as it keeps the error, undecided.
func (r *createIdentityReset) ResetOnFailedTransaction(err error) {
	if r == nil || errors.Is(err, storage.ErrCommitIndeterminate) {
		return
	}
	r.Reset()
}
