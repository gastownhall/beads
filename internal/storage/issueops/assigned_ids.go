package issueops

import "github.com/steveyegge/beads/internal/types"

// AssignedIDs records the identity fields a create left for the store to
// assign: an Issue.ID, a Dependency.IssueID (defaulted to its owning issue)
// and a Comment.ID (content-derived from the owning issue's id). The create
// fills them in as it runs, so a transaction that does not land leaves values
// naming rows that do not exist; a replay that reuses them takes the minted id
// as an explicit one and writes onto whatever row a concurrent writer put
// there, and a caller that retries gets the same.
//
// Record before the first attempt, Reset at the start of every attempt so each
// one mints from scratch, and Reset again when the create fails definitely.
// Fields the caller supplied are never touched.
type AssignedIDs struct {
	issues   []*types.Issue
	deps     []*types.Dependency
	comments []*types.Comment
}

// RecordAssignedIDs records which of issues' identity fields are empty now.
func RecordAssignedIDs(issues ...*types.Issue) AssignedIDs {
	var a AssignedIDs
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if issue.ID == "" {
			a.issues = append(a.issues, issue)
		}
		for _, dep := range issue.Dependencies {
			if dep != nil && dep.IssueID == "" {
				a.deps = append(a.deps, dep)
			}
		}
		for _, comment := range issue.Comments {
			if comment != nil && comment.ID == "" {
				a.comments = append(a.comments, comment)
			}
		}
	}
	return a
}

// Reset empties every field RecordAssignedIDs found empty.
func (a AssignedIDs) Reset() {
	for _, issue := range a.issues {
		issue.ID = ""
	}
	for _, dep := range a.deps {
		dep.IssueID = ""
	}
	for _, comment := range a.comments {
		comment.ID = ""
	}
}
