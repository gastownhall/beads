package issueops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/beadserrors"
	"github.com/steveyegge/beads/internal/types"
)

// ErrAlreadyClaimed is returned when attempting to claim an issue that is already
// claimed by another user. The error message contains the current assignee.
var ErrAlreadyClaimed = errors.New("issue already claimed")

// ErrNotClaimable is returned when attempting to claim an issue that is not in a
// claimable state, such as closed, deferred, or already in progress without the
// same actor owning the claim.
var ErrNotClaimable = errors.New("issue not claimable")

// ClaimConflictError reports the state that refused a claim — the current
// assignee and status, read inside the same transaction that lost the
// compare-and-set. It wraps the refusal rather than replacing it, so the
// sentinel still matches, the refusal's carefully-worded prose survives
// byte-for-byte, and a caller can classify the conflict from typed fields
// instead of parsing that prose.
//
// Err is set by every implementation that returns this type; a Claimer whose
// same-transaction re-read fails returns the bare refusal instead.
type ClaimConflictError struct {
	// IssueID names the issue that refused the claim.
	IssueID string
	// Assignee is the holder observed by the losing transaction. It is empty
	// when the refusal was about the status rather than a foreign holder.
	Assignee string
	// Status is the status observed by the losing transaction.
	Status Status
	// Err is the wrapped refusal. It matches ErrAlreadyClaimed or
	// ErrNotClaimable.
	Err error
}

func (e *ClaimConflictError) Error() string { return e.Err.Error() }

// Unwrap makes ClaimConflictError match the refusal it carries.
func (e *ClaimConflictError) Unwrap() error { return e.Err }

// ErrUnsupported reports a capability this backend does not serve. It is an
// alias of beadserrors.ErrUnsupported — the same type, so one errors.As arm
// matches it under either name — and it is re-exported here because a caller
// holding an issueops role should not have to discover a second package to
// classify the refusal.
//
// It is declared there rather than here because the capability shell is not an
// issue concept: a memory role can go unimplemented by a backend exactly as a
// Reader can.
type ErrUnsupported = beadserrors.ErrUnsupported

// ErrAssigneeMismatch is returned by a conditional release or update whose
// ExpectedAssignee does not match the issue's current assignee (including when
// the issue is no longer assigned at all). The caller's view of the claim was
// stale; the issue is left untouched. See Releaser.Release and
// UpdateRequest.ExpectedAssignee.
var ErrAssigneeMismatch = errors.New("assignee mismatch")

// ErrNotesOverwrite is returned when an update's Patch.Notes would replace
// existing non-empty notes with different non-empty content (see
// NotesReplacement) and UpdateRequest.ForceNotesOverwrite is false. It is
// independent of the assignee fence —
// unlike ErrAlreadyClaimed it applies regardless of ExpectedAssignee — because
// there is no compare-and-set that authorizes a notes overwrite the way a
// matching ExpectedAssignee authorizes an assignee transfer. Bypass with
// ForceNotesOverwrite (`bd update --force`); preserve history instead with
// --append-notes.
var ErrNotesOverwrite = errors.New("update would replace existing notes")

// NotesReplacement reports whether proposed would replace existing non-empty
// notes with different non-empty content — the one condition the
// notes-overwrite fence refuses without ForceNotesOverwrite. An explicit
// clear (proposed "") is not a replacement: every sibling text field clears
// the same way, the blind-clobber pattern the fence exists to stop is an
// agent writing its own non-empty content over someone else's, and the
// refusal's advice (force, or append) is meaningless for a clear. The clear
// itself is guarded separately at the CLI flag layer (`--notes ""` is
// refused; `bd update --clear-notes` is the deliberate verb, GH#6021), where
// an accidental empty — a dead command substitution — cannot be told from a
// deliberate one by its bytes; that guard is not this predicate's concern.
func NotesReplacement(existing, proposed string) bool {
	return existing != "" && proposed != "" && proposed != existing
}

// ErrNotOwner is returned when an actor tries to release a claim that a
// different actor holds. Releasing another actor's claim requires the force
// escape hatch (ReleaseRequest.Force, `bd unclaim --force`), reserved for
// abandoned claims — or ReleaseRequest.ExpectedAssignee, which authorizes the
// same thing by naming the holder instead of ignoring it.
//
// It is DECLARED here and re-exported by internal/storage rather than the other
// way round, which is the direction the memoryops slice settled: a Go alias
// preserves identity in both directions, so every existing storage.ErrNotOwner
// reference keeps matching this identical value, and a caller holding only the
// public role can now classify the refusal without importing an internal
// package it cannot reach. It is the ownership half of the same vocabulary
// ErrAssigneeMismatch above belongs to, which is why it is here rather than
// beside Releaser: both refusals answer "whose claim is this".
var ErrNotOwner = errors.New("issue claimed by a different actor")

// The namespace-neutral part of this vocabulary is declared by beadserrors and
// re-exported here. These are ALIASES, so they are the same values: every
// existing issueops.ErrX reference and every errors.Is site keeps matching the
// identical error, and a leaf that never imports issueops still matches it too.
//
// They live down there because none of them names an issue: a request can be
// invalid, a row can be missing and a database can be uninitialized on any
// plane. The refusals BELOW that name issue concepts stay here.
var (
	// ErrNotFound is returned when a requested entity does not exist in the database.
	ErrNotFound = beadserrors.ErrNotFound
	// ErrValidation classifies deterministic request-validation failures.
	ErrValidation = beadserrors.ErrValidation
	// ErrNotInitialized is returned when the database has not been initialized
	// (e.g., issue_prefix config is missing).
	ErrNotInitialized = beadserrors.ErrNotInitialized
)

// ErrPrefixMismatch is returned when an issue ID does not match the configured prefix.
var ErrPrefixMismatch = errors.New("prefix mismatch")

// ErrCloseBlocked is returned by CloseIssueChecked when an issue cannot be
// closed because it is still blocked (is_blocked=1: an open blocking dependency
// or an open blocking gate). Bypass with CloseIssueOptions.Force.
var ErrCloseBlocked = errors.New("cannot close blocked issue")

// Blocker is one live blocker named by a BlockedError: a local issue id, or an
// `external:<project>:<capability>` reference, with the blocking edge's type
// when the refusal knew it.
type Blocker struct {
	// ID is the blocker's issue id or its full `external:` reference.
	ID string
	// Type is the blocking edge's dependency type. It is empty when the
	// refusal did not report it, which is the case for an `external:`
	// reference named by the external-dependency guard.
	Type types.DependencyType
}

// externalBlockerPrefix marks a blocker that lives in another project and is
// satisfied by a shipped capability rather than by a local close.
const externalBlockerPrefix = "external:"

// External reports whether the blocker is an `external:` reference rather than
// an issue this database holds.
func (b Blocker) External() bool { return strings.HasPrefix(b.ID, externalBlockerPrefix) }

// String spells the blocker the way a blocked-issue refusal always has: the
// bare id for a `blocks` edge or an unreported type, and "id (type)" for the
// other blocking edges. ParseBlocker is its inverse.
func (b Blocker) String() string {
	if b.Type == "" || b.Type == types.DepBlocks {
		return b.ID
	}
	return b.ID + " (" + string(b.Type) + ")"
}

// ParseBlocker reads one entry of the []string blocker list a store's IsBlocked
// reports — the spelling Blocker.String writes — back into its typed form. It
// exists for producers that hold only that string contract; a producer with the
// edges in hand builds Blocker values directly. A bare local id is a `blocks`
// edge, and an `external:` reference keeps an unreported type.
//
// The `external:` test comes first because a capability may itself end in a
// parenthesized suffix: `external:remote:pay (beta)` is a legal reference, and
// splitting it would report a truncated ID and a type the external guard never
// names.
func ParseBlocker(s string) Blocker {
	if strings.HasPrefix(s, externalBlockerPrefix) {
		return Blocker{ID: s}
	}
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		return Blocker{ID: s[:i], Type: types.DependencyType(s[i+2 : len(s)-1])}
	}
	return Blocker{ID: s, Type: types.DepBlocks}
}

// BlockedError reports the live blockers that refused an operation on a blocked
// issue, read by the refusing check itself. It wraps the refusal rather than
// replacing it — the ClaimConflictError arrangement — so the sentinel still
// matches and its message is byte-identical to the historical
// "<sentinel>: <id> is blocked by [<blockers>]", while a caller that reports the
// refusal elsewhere (the HTTP surface's `blockers` member) reads the typed list
// instead of parsing that prose.
//
// Err is ErrCloseBlocked for a close-policy refusal. The type is not
// close-specific: any refusal whose subject is "this issue is blocked by these"
// can carry its own sentinel.
type BlockedError struct {
	// IssueID names the issue that was refused.
	IssueID string
	// Blockers are the live blockers, in the order the check reported them.
	Blockers []Blocker
	// Err is the wrapped refusal sentinel, e.g. ErrCloseBlocked.
	Err error
}

// NewCloseBlockedError builds the close-policy refusal for issueID from the
// []string blocker list a store's IsBlocked reports (see ParseBlocker).
func NewCloseBlockedError(issueID string, blockers []string) *BlockedError {
	parsed := make([]Blocker, 0, len(blockers))
	for _, blocker := range blockers {
		parsed = append(parsed, ParseBlocker(blocker))
	}
	return &BlockedError{IssueID: issueID, Blockers: parsed, Err: ErrCloseBlocked}
}

func (e *BlockedError) Error() string {
	names := make([]string, 0, len(e.Blockers))
	for _, blocker := range e.Blockers {
		names = append(names, blocker.String())
	}
	return fmt.Sprintf("%v: %s is blocked by %v", e.Err, e.IssueID, names)
}

// Unwrap makes BlockedError match the refusal it carries.
func (e *BlockedError) Unwrap() error { return e.Err }

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

// ErrAlreadyIdentified is returned by Bootstrapper.Bootstrap when the substrate
// already carries a workspace identity. It is its own sentinel rather than
// ErrAlreadyExists because that one is about an occupied ISSUE ID in a shared
// id space, and a caller that classifies the two together would answer a
// re-init with advice about `bd update`.
var ErrAlreadyIdentified = errors.New("workspace already identified")

// AlreadyIdentifiedError reports the identity a substrate was found carrying
// when a bootstrap was refused, read inside the same transaction that would
// have written over it.
//
// It carries the pair rather than formatting it away because the two things a
// caller does next both need the values: adopting the identity needs them, and
// telling a COMPLETE identity apart from a half-written one — the state a
// bootstrap that failed partway leaves on a substrate with no transactions —
// means looking at which of the two is empty.
type AlreadyIdentifiedError struct {
	// Prefix is the issue prefix found, or "" when the substrate carried none.
	Prefix string
	// ProjectID is the project identity found, or "" when the substrate
	// carried none.
	ProjectID string
}

func (e *AlreadyIdentifiedError) Error() string {
	switch {
	case e.Prefix != "" && e.ProjectID != "":
		return fmt.Sprintf("workspace already identified as prefix %q, project %s", e.Prefix, e.ProjectID)
	case e.Prefix != "":
		return fmt.Sprintf("workspace already identified as prefix %q with no project id", e.Prefix)
	default:
		return fmt.Sprintf("workspace already identified as project %s with no issue prefix", e.ProjectID)
	}
}

// Unwrap makes AlreadyIdentifiedError match ErrAlreadyIdentified.
func (e *AlreadyIdentifiedError) Unwrap() error { return ErrAlreadyIdentified }

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
// bulk add paths (add/AddDependencies) and the dolt cross-tier check — so
// callers can errors.Is any dependency-add cycle rejection. The whole-graph
// construction paths (ApplyIssueGraph/ApplyWispGraph) are a separate family and
// deliberately do not carry this sentinel yet.
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

// ErrDependencySourceNotFound is returned when an edge's SOURCE names no row
// this database holds. An edge follows its source, so there is no plane for it
// to land in and no event stream to record it on.
var ErrDependencySourceNotFound = errors.New("dependency source not found")

// ErrDependencyTargetNotFound is returned when an edge's TARGET names no row
// this database holds AND is one whose absence this database can SEE: an id in
// its own namespace, carrying no "external:" marker. An "external:" reference
// and an id belonging to another repository are accepted as external targets,
// so neither raises this.
//
// It is a separate sentinel from the source's because the two are separate
// answers. A ghost source is always a bad id; a target is only refused when
// this database is the one that would have held it, which is a narrower claim
// and the one a caller has to reason about before retrying.
var ErrDependencyTargetNotFound = errors.New("dependency target not found")

// DependencyEndpointNotFoundError reports which endpoint of a refused edge was
// absent, read inside the transaction that refused it. It wraps the refusal
// rather than replacing it, so the sentinel still matches, the message stays
// what it was, and a caller classifies the refusal from typed fields instead of
// parsing prose — the ClaimConflictError arrangement, applied to the graph.
//
// It carries the whole edge rather than only the missing id because the request
// is all-or-nothing: the refusal is the REQUEST's, so a caller reporting which
// of its own edges was rejected has to find it by both endpoints.
type DependencyEndpointNotFoundError struct {
	// IssueID and DependsOnID name the refused edge.
	IssueID     string
	DependsOnID string
	// MissingID is the endpoint that named no row: IssueID for a ghost source,
	// DependsOnID for a missing target.
	MissingID string
	// Err is the wrapped refusal. It matches ErrDependencySourceNotFound or
	// ErrDependencyTargetNotFound.
	Err error
}

func (e *DependencyEndpointNotFoundError) Error() string {
	return fmt.Sprintf("issue %s not found", e.MissingID)
}

// Unwrap makes DependencyEndpointNotFoundError match the refusal it carries.
func (e *DependencyEndpointNotFoundError) Unwrap() error { return e.Err }

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
