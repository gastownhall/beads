package issueops

import (
	"errors"
	"fmt"
)

// ErrAlreadyClaimed is returned when attempting to claim an issue that is already
// claimed by another user. The error message contains the current assignee.
var ErrAlreadyClaimed = errors.New("issue already claimed")

// ErrNotClaimable is returned when attempting to claim an issue that is not in a
// claimable state, such as closed, deferred, or already in progress without the
// same actor owning the claim.
var ErrNotClaimable = errors.New("issue not claimable")

// ErrAssigneeMismatch is returned by UnclaimIssueIfAssignee when the issue's
// current assignee does not match the expected assignee (including when the
// issue is no longer assigned at all). The caller's view of the claim was
// stale; the issue is left untouched.
var ErrAssigneeMismatch = errors.New("assignee mismatch")

// ErrNotFound is returned when a requested entity does not exist in the database.
var ErrNotFound = errors.New("not found")

// ErrValidation classifies deterministic request-validation failures.
var ErrValidation = errors.New("validation failed")

// ErrNotInitialized is returned when the database has not been initialized
// (e.g., issue_prefix config is missing).
var ErrNotInitialized = errors.New("database not initialized")

// ErrPrefixMismatch is returned when an issue ID does not match the configured prefix.
var ErrPrefixMismatch = errors.New("prefix mismatch")

// ErrCloseBlocked is returned by CloseIssueChecked when an issue cannot be
// closed because it is still blocked (is_blocked=1: an open blocking dependency
// or an open blocking gate). Bypass with CloseIssueOptions.Force.
var ErrCloseBlocked = errors.New("cannot close blocked issue")

// ErrCloseOpenChildren is returned when an unforced close finds open
// parent-child dependents.
var ErrCloseOpenChildren = errors.New("cannot close issue with open children")

// CloseOpenChildrenError reports the issue and open-child count that refused a
// guarded close.
type CloseOpenChildrenError struct {
	IssueID      string
	OpenChildren int
}

func (e *CloseOpenChildrenError) Error() string {
	return fmt.Sprintf("cannot close %s: %d open child issue(s); close children first or use --force to override", e.IssueID, e.OpenChildren)
}

// Unwrap makes CloseOpenChildrenError match ErrCloseOpenChildren.
func (e *CloseOpenChildrenError) Unwrap() error {
	return ErrCloseOpenChildren
}

// ErrAlreadyExists is returned when a create operation is given an ID that is
// already occupied. The issue and wisp tables share one ID space.
var ErrAlreadyExists = errors.New("issue already exists")

// ErrVersionMismatch is returned by a *Checked op given an ExpectedVersion that
// no longer matches the row's current version (row_lock) — an optimistic
// concurrency failure. Callers errors.Is it to distinguish a lost-update
// precondition from other errors.
var ErrVersionMismatch = errors.New("version mismatch")

// ErrStatusMismatch is returned by UpdateIssueChecked given an ExpectedStatus
// that no longer matches the issue's current status. The caller's view of the
// issue was stale; the issue is left untouched. The assignee analog is
// ErrAssigneeMismatch, shared with UnclaimIssueIfAssignee.
var ErrStatusMismatch = errors.New("status mismatch")

// ErrSelfDependency is returned when a dependency edge would point an issue at
// itself. It is the static prefix of the formatted message, wrapped so callers
// can errors.Is it while the human-readable text is preserved byte-for-byte.
var ErrSelfDependency = errors.New("cannot add self-dependency")

// ErrDependencyCycle is returned when adding a dependency edge would introduce a
// scheduling cycle. It is scoped to the dependency-add family — the single and
// bulk add paths (add/addBulk) and the dolt cross-tier check — so callers can
// errors.Is any dependency-add cycle rejection. The whole-graph construction
// paths (ApplyIssueGraph/ApplyWispGraph) are a separate family and deliberately
// do not carry this sentinel yet.
var ErrDependencyCycle = errors.New("adding dependency would create a cycle")

// DependencyTypeConflictError is returned when an edge already exists between
// the same pair with a DIFFERENT type. Its message is byte-identical to the
// embedded issueops path (internal/storage/issueops/dependencies.go) so
// `bd dep add` surfaces the same user-facing retype error on the domain/db seam
// as on the embedded store. It is a typed error so the use-case can pass it
// through unwrapped instead of burying it under an "add dep: insert:" prefix.
type DependencyTypeConflictError struct {
	IssueID       string
	DependsOnID   string
	ExistingType  string
	RequestedType string
}

func (e *DependencyTypeConflictError) Error() string {
	return fmt.Sprintf("dependency %s -> %s already exists with type %q (requested %q); remove it first with 'bd dep remove' then re-add",
		e.IssueID, e.DependsOnID, e.ExistingType, e.RequestedType)
}

// DependencyHierarchyConflictError is returned when a blocking dependency
// would gate an issue on one of its own ancestors or descendants. Either shape
// can never clear under the parent-child close/blocking semantics.
type DependencyHierarchyConflictError struct {
	IssueID           string
	BlockerID         string
	BlockerIsAncestor bool
}

func (e *DependencyHierarchyConflictError) Error() string {
	if e.BlockerIsAncestor {
		return fmt.Sprintf("%s cannot be blocked by its ancestor %s: %s cannot close until its descendants finish, so the gate would never clear",
			e.IssueID, e.BlockerID, e.BlockerID)
	}
	return fmt.Sprintf("%s cannot be blocked by its descendant %s: blocked status cascades to descendants, so %s would inherit the block and never close",
		e.IssueID, e.BlockerID, e.BlockerID)
}
