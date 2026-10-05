package issueops

import (
	"errors"

	"github.com/steveyegge/beads/internal/types"
)

// UnversionableIssue names one issue that RecordVersionInTx would refuse to
// version, and the refusal it would return. Field is the top-level field of the
// issue the refusal was found in ("metadata", "timeout"), empty when the refusal
// does not name one.
type UnversionableIssue struct {
	ID    string
	Field string
	Err   error
}

// FindUnversionable runs CheckIssueVersionable over every issue in issues that
// the mint would version and returns the ones it refuses, in the order they were
// given. It is the core of the check made when versioned history is switched on:
// with history on, a write that introduces a value the mint refuses aborts in its
// own transaction, but a row that already holds one -- written while history was
// off, or by a path that does not mint -- would fail every later write to it. The
// check is over the whole issue, as the mint's is, and not over its metadata
// alone: a gate's timeout past 2^53-1 nanoseconds is refused too.
//
// It skips exactly the rows the mint skips, by the mint's own rule (IsWisp):
// ephemeral and no-history rows are never versioned, so a value they hold can
// never abort a write. It does no I/O; the caller loads the rows, which is what
// lets one function serve the direct store and the proxied route.
func FindUnversionable(issues []*types.Issue) []UnversionableIssue {
	var found []UnversionableIssue
	for _, issue := range issues {
		if issue == nil || IsWisp(issue) {
			continue
		}
		if err := CheckIssueVersionable(issue); err != nil {
			var named *refusedFieldError
			field := ""
			if errors.As(err, &named) {
				field = named.field
			}
			found = append(found, UnversionableIssue{ID: issue.ID, Field: field, Err: err})
		}
	}
	return found
}
