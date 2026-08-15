// Package worktreeremove contains the side-effect-free safety policy for
// worktree removal. Git and filesystem observations are deliberately kept at
// the command adapter boundary.
package worktreeremove

import "fmt"

// Mode controls which safety checks are required.
type Mode uint8

const (
	// ModeUnknown is an unvalidated caller request.
	ModeUnknown Mode = iota
	// Normal requires cleanliness and containment.
	Normal
	// Force bypasses cleanliness and containment, but never identity checks.
	Force
)

// TargetKind identifies the target relative to the command and registry.
type TargetKind uint8

const (
	// TargetUnknown is an incomplete target observation.
	TargetUnknown TargetKind = iota
	// RegisteredTarget is a removable, non-primary registered target.
	RegisteredTarget
	// PrimaryWorktree is the primary registry entry.
	PrimaryWorktree
	// CurrentWorktree contains the running command.
	CurrentWorktree
)

// Match records an explicit identity comparison result.
type Match uint8

const (
	// MatchUnknown is an incomplete identity comparison.
	MatchUnknown Match = iota
	// Matched means both identities resolved to the same object.
	Matched
	// Unmatched means both identities were present but distinct.
	Unmatched
)

// Presence records whether a required observed object was present.
type Presence uint8

const (
	// PresenceUnknown is an incomplete presence observation.
	PresenceUnknown Presence = iota
	// Present means the object was observed by the adapter.
	Present
	// Missing means the object was not present at the observation point.
	Missing
)

// Status records the inspected cleanliness result.
type Status uint8

const (
	// StatusUnknown is an incomplete cleanliness observation.
	StatusUnknown Status = iota
	// Clean has no modified, untracked, or ignored entries.
	Clean
	// Dirty has at least one modified, untracked, or ignored entry.
	Dirty
)

// ComparatorKind records comparator resolution without using absent strings.
type ComparatorKind uint8

const (
	// ComparatorUnknown is an incomplete comparator observation.
	ComparatorUnknown ComparatorKind = iota
	// ComparatorAvailable is a resolved independent comparison target.
	ComparatorAvailable
	// ComparatorMissing has no usable configured or explicit comparator.
	ComparatorMissing
	// ComparatorNotRequired was deliberately not resolved because force mode
	// bypasses containment.
	ComparatorNotRequired
)

// Containment records the merge-base safety result.
type Containment uint8

const (
	// ContainmentUnknown is an incomplete containment observation.
	ContainmentUnknown Containment = iota
	// Contained means target HEAD is an ancestor of the comparator.
	Contained
	// NotContained means the comparator does not contain target HEAD.
	NotContained
	// ContainmentNotRequired was deliberately not checked because force mode
	// bypasses containment.
	ContainmentNotRequired
)

// IgnoreKind records managed .gitignore ownership.
type IgnoreKind uint8

const (
	// IgnoreUnknown is an incomplete managed-ignore observation.
	IgnoreUnknown IgnoreKind = iota
	// IgnoreAbsent means no managed cleanup is required.
	IgnoreAbsent
	// IgnoreManaged means a prepared managed entry must be cleaned up.
	IgnoreManaged
)

// PrepareFacts are typed results gathered before the first safety decision.
// RegisteredPath and ManagedIgnoreEntry are exact adapter-observed identities;
// filesystem handles, bytes, and fingerprints stay at the adapter edge.
type PrepareFacts struct {
	Registration       Presence
	Target             TargetKind
	RegisteredPath     string
	TargetDir          Presence
	GitAdminDir        Presence
	GitMarker          Presence
	CommonDir          Match
	Head               Presence
	Status             Status
	Comparator         ComparatorKind
	Containment        Containment
	ManagedIgnore      IgnoreKind
	ManagedIgnoreEntry string
}

// Request is the caller-selected policy input.
type Request struct{ Mode Mode }

// Mutation is the only destructive operation a prepared plan authorizes.
type Mutation struct {
	TargetPath string
	Force      bool
}

// Cleanup is the only managed-ignore cleanup a prepared plan authorizes.
type Cleanup struct{ Entry string }

// Plan is an opaque approval from Prepare and must be revalidated immediately
// before the destructive mutation.
type Plan struct {
	approved           bool
	mode               Mode
	targetPath         string
	managedIgnoreEntry string
}

// Mutation returns the exact destructive operation approved by Prepare.
func (plan Plan) Mutation() Mutation {
	return Mutation{TargetPath: plan.targetPath, Force: plan.mode == Force}
}

// Cleanup returns the exact managed-ignore cleanup approved by Prepare.
func (plan Plan) Cleanup() (Cleanup, bool) {
	if plan.managedIgnoreEntry == "" {
		return Cleanup{}, false
	}
	return Cleanup{Entry: plan.managedIgnoreEntry}, true
}

func (plan Plan) valid() bool {
	return plan.approved && (plan.mode == Normal || plan.mode == Force) && plan.targetPath != ""
}

// Prepare validates the initial observations and returns an approved plan.
func Prepare(request Request, facts PrepareFacts) (Plan, error) {
	if request.Mode != Normal && request.Mode != Force {
		return Plan{}, fmt.Errorf("worktree removal mode is absent or invalid")
	}
	switch facts.Target {
	case PrimaryWorktree:
		return Plan{}, fmt.Errorf("cannot remove the primary worktree")
	case CurrentWorktree:
		return Plan{}, fmt.Errorf("cannot remove the worktree containing the running command")
	case RegisteredTarget:
	default:
		return Plan{}, fmt.Errorf("registered worktree target is absent or invalid")
	}
	if facts.Registration != Present || facts.RegisteredPath == "" ||
		facts.TargetDir != Present || facts.GitAdminDir != Present ||
		facts.GitMarker != Present || facts.Head != Present {
		return Plan{}, fmt.Errorf("registered worktree target is absent or invalid")
	}
	if facts.CommonDir != Matched {
		return Plan{}, fmt.Errorf("target common git directory does not match repository")
	}
	if facts.Status != Clean && facts.Status != Dirty {
		return Plan{}, fmt.Errorf("worktree cleanliness observation is absent or invalid")
	}
	if facts.ManagedIgnore != IgnoreAbsent && facts.ManagedIgnore != IgnoreManaged {
		return Plan{}, fmt.Errorf("managed ignore observation is absent or invalid")
	}
	if (facts.ManagedIgnore == IgnoreManaged) != (facts.ManagedIgnoreEntry != "") {
		return Plan{}, fmt.Errorf("managed ignore cleanup identity is absent or invalid")
	}
	if request.Mode != Force {
		if facts.Status != Clean {
			return Plan{}, fmt.Errorf("worktree contains modified, untracked, or ignored files")
		}
		if facts.Comparator != ComparatorAvailable && facts.Comparator != ComparatorMissing && facts.Comparator != ComparatorNotRequired {
			return Plan{}, fmt.Errorf("worktree comparison target observation is absent or invalid")
		}
		if facts.Comparator != ComparatorAvailable {
			return Plan{}, fmt.Errorf("cannot verify unpushed commits: no comparison target is available")
		}
		if facts.Containment != Contained && facts.Containment != NotContained && facts.Containment != ContainmentNotRequired {
			return Plan{}, fmt.Errorf("worktree containment observation is absent or invalid")
		}
		if facts.Containment != Contained {
			return Plan{}, fmt.Errorf("worktree HEAD is not contained in the comparison target")
		}
	} else if facts.Comparator != ComparatorNotRequired || facts.Containment != ContainmentNotRequired {
		return Plan{}, fmt.Errorf("worktree force-mode comparison observations are inconsistent")
	}
	plan := Plan{approved: true, mode: request.Mode, targetPath: facts.RegisteredPath}
	if facts.ManagedIgnore == IgnoreManaged {
		plan.managedIgnoreEntry = facts.ManagedIgnoreEntry
	}
	return plan, nil
}

// InvariantState is the adapter's raw observation of one independently
// revalidated invariant.
type InvariantState uint8

const (
	// InvariantUnknown means the adapter could not complete the observation.
	InvariantUnknown InvariantState = iota
	// InvariantStable means the current observation matches preparation.
	InvariantStable
	// InvariantChanged means the current observation differs from preparation.
	InvariantChanged
	// InvariantNotRequired means force mode deliberately bypassed this check.
	InvariantNotRequired
)

// RevalidationFacts records every safety invariant independently. No adapter
// outcome may stand in for these raw observations.
type RevalidationFacts struct {
	Registration           InvariantState
	LockPrune              InvariantState
	TargetPath             InvariantState
	TargetDirectory        InvariantState
	GitAdminDirectory      InvariantState
	GitAdminDirectoryBytes InvariantState
	GitMarker              InvariantState
	GitMarkerBytes         InvariantState
	CommonDirectory        InvariantState
	Head                   InvariantState
	Cleanliness            InvariantState
	StatusBytes            InvariantState
	DirtyFileFingerprint   InvariantState
	Comparator             InvariantState
	Containment            InvariantState
	ManagedIgnore          InvariantState
}

func revalidationValid(plan Plan, facts RevalidationFacts) bool {
	for _, state := range []InvariantState{
		facts.Registration, facts.LockPrune, facts.TargetPath, facts.TargetDirectory,
		facts.GitAdminDirectory, facts.GitAdminDirectoryBytes, facts.GitMarker,
		facts.GitMarkerBytes, facts.CommonDirectory, facts.Head, facts.Cleanliness,
		facts.StatusBytes, facts.DirtyFileFingerprint, facts.ManagedIgnore,
	} {
		if state != InvariantStable {
			return false
		}
	}
	if plan.mode == Normal {
		return facts.Comparator == InvariantStable && facts.Containment == InvariantStable
	}
	return (facts.Comparator == InvariantStable || facts.Comparator == InvariantNotRequired) &&
		(facts.Containment == InvariantStable || facts.Containment == InvariantNotRequired)
}

// Revalidate refuses every unknown or changed invariant observed after
// Prepare. A zero or otherwise unapproved plan is always refused.
func Revalidate(plan Plan, facts RevalidationFacts) error {
	if !plan.valid() {
		return fmt.Errorf("worktree removal approval is absent or invalid")
	}
	if !revalidationValid(plan, facts) {
		return fmt.Errorf("worktree changed before removal")
	}
	return nil
}

// RevalidationResult records whether reinspection completed without an
// adapter-side diagnostic after a failed removal.
type RevalidationResult uint8

const (
	// RevalidationResultUnknown means reinspection did not report a result.
	RevalidationResultUnknown RevalidationResult = iota
	// RevalidationPassed means reinspection completed without a diagnostic.
	RevalidationPassed
	// RevalidationFailed means reinspection produced an adapter diagnostic.
	RevalidationFailed
)

// FailureFacts are raw post-failure observations. Registry and target path
// presence are recorded separately from complete revalidation evidence.
type FailureFacts struct {
	Revalidation       RevalidationFacts
	RevalidationResult RevalidationResult
	Registration       Presence
	TargetPath         Presence
}

// FailureKind classifies the presentation branch for a remove error.
type FailureKind uint8

const (
	UnchangedFailure FailureKind = iota
	PartialFailure
)

// Failure is the pure classification returned after a mutation error.
type Failure struct {
	Kind      FailureKind
	RemoveErr error
}

// ClassifyFailure distinguishes a safely unchanged target from partial or
// indeterminate state. Presentation retains diagnostics at the command edge.
func ClassifyFailure(plan Plan, facts FailureFacts, removeErr error) (Failure, error) {
	if !plan.valid() {
		return Failure{}, fmt.Errorf("worktree removal approval is absent or invalid")
	}
	if removeErr == nil {
		return Failure{}, fmt.Errorf("worktree removal failure is absent or invalid")
	}
	if facts.RevalidationResult == RevalidationPassed && facts.Registration == Present && facts.TargetPath == Present && revalidationValid(plan, facts.Revalidation) {
		return Failure{Kind: UnchangedFailure, RemoveErr: removeErr}, nil
	}
	return Failure{Kind: PartialFailure, RemoveErr: removeErr}, nil
}
