package issueops

import "github.com/steveyegge/beads/internal/types"

// SnapshotUnsetCreateIDs records which create-time ID fields the caller left
// empty — Issue.ID, Dependency.IssueID, Comment.ID — the fields the create
// engine mints in place (create.go: the issue mint, the dependency's
// IssueID default, the content-derived comment id). reset clears exactly
// those fields again.
//
// A caller that runs the engine in a retry loop over caller-owned structs
// calls it ONCE before the loop and calls reset as the first statement of
// every attempt: a replayed attempt must see the structs as the first one
// did, or it reads the rolled-back attempt's mints as caller-supplied ids and
// upserts under them instead of minting again. A snapshot taken inside the
// attempt would capture the previous attempt's mints as "set".
//
// Caller-supplied values are never captured, so reset never touches them; and
// reset is not run after the committed attempt, so its mints stay on the
// structs for the caller to read back. Nil issues, dependencies and comments
// are skipped.
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
			if dep != nil && dep.IssueID == "" {
				unset = append(unset, &dep.IssueID)
			}
		}
		for _, comment := range issue.Comments {
			if comment != nil && comment.ID == "" {
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
