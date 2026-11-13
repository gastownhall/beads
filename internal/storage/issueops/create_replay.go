package issueops

import "github.com/steveyegge/beads/internal/types"

// SnapshotUnsetCreateIDs records which create-time ID fields the caller
// left empty — Issue.ID, Dependency.IssueID, Comment.ID — the fields the
// create engine mints in place (create.go). reset clears exactly those
// fields again. A caller that runs the engine in a retry loop over
// caller-owned structs calls it ONCE before the loop and calls reset as
// the first statement of every attempt; a snapshot taken inside the
// attempt would capture the previous attempt's mints as "set".
// Caller-supplied values are never captured; the committed attempt's
// values stay on the structs for the caller to read.
func SnapshotUnsetCreateIDs(issues []*types.Issue) (reset func()) {
	var unset []*string
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if issue.ID == "" {
			unset = append(unset, &issue.ID)
		}
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			if dep.IssueID == "" {
				unset = append(unset, &dep.IssueID)
			}
		}
		for _, comment := range issue.Comments {
			if comment == nil {
				continue
			}
			if comment.ID == "" {
				unset = append(unset, &comment.ID)
			}
		}
	}
	return func() {
		for _, field := range unset {
			*field = ""
		}
	}
}
