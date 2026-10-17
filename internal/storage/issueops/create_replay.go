package issueops

import "github.com/steveyegge/beads/internal/types"

// SnapshotCreateInputs records what the create path writes back into the
// caller's issues and returns a func that puts it back, so a create that a
// transaction retry replays starts every attempt from the caller's request
// instead of from what a rolled-back attempt minted.
//
// The create path writes back three things:
//
//   - the Issue struct itself: the minted ID (assignCreateIssueIDInTx) and the
//     defaulted timestamps and content hash (PrepareIssueForInsert);
//   - through each inline dependency, the owning issue's ID as the edge's
//     source when the edge named none (PersistDependenciesWithOptionsResult);
//   - through each inline comment, the content-derived ID when the comment
//     carried none (PersistComments).
//
// Restoring only the struct would leave the last two pointing at the
// rolled-back attempt's ID. The dependency and comment objects stay the
// caller's: only the fields the create path writes are reset. A new write into
// caller-owned state on the create path needs its restore here.
func SnapshotCreateInputs(issues []*types.Issue) (restore func()) {
	type depSource struct {
		dep     *types.Dependency
		issueID string
	}
	type commentID struct {
		comment *types.Comment
		id      string
	}
	type issueState struct {
		issue    *types.Issue
		fields   types.Issue
		deps     []depSource
		comments []commentID
	}
	saved := make([]issueState, 0, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		state := issueState{issue: issue, fields: *issue}
		for _, dep := range issue.Dependencies {
			if dep != nil {
				state.deps = append(state.deps, depSource{dep: dep, issueID: dep.IssueID})
			}
		}
		for _, comment := range issue.Comments {
			if comment != nil {
				state.comments = append(state.comments, commentID{comment: comment, id: comment.ID})
			}
		}
		saved = append(saved, state)
	}
	return func() {
		for _, state := range saved {
			*state.issue = state.fields
			for _, d := range state.deps {
				d.dep.IssueID = d.issueID
			}
			for _, c := range state.comments {
				c.comment.ID = c.id
			}
		}
	}
}
