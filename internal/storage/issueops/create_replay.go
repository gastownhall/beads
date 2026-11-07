package issueops

import "github.com/steveyegge/beads/internal/types"

// SnapshotCreateInputs records what the create path writes back into a
// caller's issues and returns a func that restores it, so a create that a
// transaction retry replays starts every attempt from the request the caller
// made rather than from what a rolled-back attempt minted.
//
// The create path writes three things back:
//
//   - the Issue struct itself: the minted ID (assignCreateIssueIDInTx), the
//     defaulted timestamps and content hash (PrepareIssueForInsert);
//   - through each inline dependency the issue points at, the minted ID as the
//     edge's source when the edge named none (PersistDependenciesWithOptionsResult);
//   - through each inline comment the issue points at, the derived ID when the
//     comment carried none (PersistComments).
//
// A struct copy restores only the first. Left behind, the other two point a
// replay's edges and comments at the rolled-back attempt's ID: in hash mode an
// issue that no longer exists (the replay mints a fresh ID, and the edge fails
// its foreign key), in counter mode the concurrent writer that won the conflict
// and minted the same number (the edge lands silently on that writer's issue).
//
// The dependency and comment objects themselves are kept — callers hold them
// — and only the fields the create path writes are reset. Keep this in step
// with those writers: a new write into caller-owned state there needs its
// restore here.
func SnapshotCreateInputs(issues []*types.Issue) (restore func()) {
	type issueState struct {
		issue        *types.Issue
		fields       types.Issue
		depSourceIDs []string
		commentIDs   []string
	}
	saved := make([]issueState, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		state := issueState{issue: issue, fields: *issue}
		for _, dep := range issue.Dependencies {
			id := ""
			if dep != nil {
				id = dep.IssueID
			}
			state.depSourceIDs = append(state.depSourceIDs, id)
		}
		for _, comment := range issue.Comments {
			id := ""
			if comment != nil {
				id = comment.ID
			}
			state.commentIDs = append(state.commentIDs, id)
		}
		saved = append(saved, state)
	}
	return func() {
		for _, state := range saved {
			*state.issue = state.fields
			for i, dep := range state.issue.Dependencies {
				if dep != nil && i < len(state.depSourceIDs) {
					dep.IssueID = state.depSourceIDs[i]
				}
			}
			for i, comment := range state.issue.Comments {
				if comment != nil && i < len(state.commentIDs) {
					comment.ID = state.commentIDs[i]
				}
			}
		}
	}
}
