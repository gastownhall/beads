package issueops

import (
	"fmt"

	publicops "github.com/steveyegge/beads/issueops"
)

// MissingDependencySource builds the refusal for an edge whose SOURCE names no
// row this database holds.
//
// Every write plumbing that can observe the absence mints its refusal here or
// in the target twin below: the in-transaction store body, the cross-tier
// target precheck, and the domain repository's classification of a foreign-key
// refusal. That is what makes the message identical across them as well as the
// type.
func MissingDependencySource(issueID, dependsOnID string) error {
	return &publicops.DependencyEndpointNotFoundError{
		IssueID:     issueID,
		DependsOnID: dependsOnID,
		MissingID:   issueID,
		Err:         publicops.ErrDependencySourceNotFound,
	}
}

// MissingDependencyTarget builds the refusal for an edge whose TARGET names no
// row this database holds and whose absence this database can see. Callers
// decide that second half — an "external:" reference and another repository's
// id never reach here.
func MissingDependencyTarget(issueID, dependsOnID string) error {
	return &publicops.DependencyEndpointNotFoundError{
		IssueID:     issueID,
		DependsOnID: dependsOnID,
		MissingID:   dependsOnID,
		Err:         publicops.ErrDependencyTargetNotFound,
	}
}

// ErrTooManyRows is returned by SearchIssuesInTx (and equivalent paths in
// other backends) when a search would yield more rows than the caller's
// MaxRows cap allows. Callers can match it with errors.As to surface a
// structured "result set too large" condition instead of an opaque error
// string.
//
// Found is the row count observed when the cap fired. The storage layer
// issues LIMIT MaxRows+1 to detect overage, so Found equals MaxRows+1 in
// practice; the true row count in the underlying data may be larger.
//
// Source attributes which knob set MaxRows. Expected values: "--max-rows",
// "BEADS_MAX_ROWS", or "" (library users with no source attribution).
type ErrTooManyRows struct {
	Found  int
	Cap    int
	Source string
}

func (e *ErrTooManyRows) Error() string {
	if e.Source != "" {
		return fmt.Sprintf("search returned %d rows, exceeding %s cap of %d", e.Found, e.Source, e.Cap)
	}
	return fmt.Sprintf("search returned %d rows, exceeding cap of %d", e.Found, e.Cap)
}
