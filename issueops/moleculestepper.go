package issueops

import (
	"context"
	"errors"
	"fmt"
)

// AdvanceRequest describes moving a molecule on after one of its steps
// closed: the operation behind `bd close --continue`.
type AdvanceRequest struct {
	// Actor is who advances. With AutoClaim it becomes the claimed step's
	// assignee, exactly as ClaimRequest.Actor does. It must not be empty.
	Actor string
	// ClosedStepID names the step that closed. It must not be empty, and an
	// id that names nothing is ErrNotFound. Nothing requires the step to be
	// closed: advancing reads where the molecule stands now.
	ClosedStepID string
	// AutoClaim claims the next ready step for Actor. Without it the advance
	// writes nothing and only names the step.
	AutoClaim bool
}

// AdvanceResult reports where a molecule stands after the advance.
type AdvanceResult struct {
	// ClosedStep is the step the request named, as read before any claim.
	ClosedStep *Issue
	// MoleculeID is the step's molecule root, or "" when the step belongs to
	// no molecule — in which case nothing else below is set.
	MoleculeID string
	// Complete reports that every step of the molecule is closed. NextStep is
	// then nil.
	Complete bool
	// NextStep is the step the advance claimed or, without a claim, the first
	// ready step (see MoleculeView.ReadySteps). Nil when no step is ready.
	NextStep *Issue
	// Claimed reports that the advance claimed NextStep for Actor: NextStep is
	// then the post-claim row, in progress and assigned to Actor.
	Claimed bool
}

// MoleculeStepper describes advancing a molecule to its next ready step. Like
// Lifecycle and Claimer it is a role with its own accessor; a new capability
// gets a new role interface, so never append a method here.
//
// THE CLAIM IS A REAL CLAIM. A durable step is claimed through the Claimer
// role and a wisp step through Lifecycle.Update's Claim (the Claimer does not
// serve the wisp plane; both run the one claim compare-and-set). Either way
// the step's assignee becomes Actor and a step another actor holds is refused
// rather than taken. A ready step lost that way — held by someone else, or
// moved out of a claimable status since it was read — is skipped for the next
// ready step; when every ready step is lost the advance claims nothing and
// still reports the first one as NextStep. Any other claim failure is
// returned.
//
// Implementations never mutate the caller's request. Validation failures
// match ErrValidation and write nothing.
type MoleculeStepper interface {
	Advance(ctx context.Context, req AdvanceRequest) (AdvanceResult, error)
}

// MoleculeStepperRoles is the role set the shared stepper is composed over.
type MoleculeStepperRoles struct {
	Reader    MoleculeReader
	Claimer   Claimer
	Lifecycle Lifecycle
}

// moleculeStepperSource is the accessor set a store or unit-of-work provider
// offers for the roles NewMoleculeStepper composes.
type moleculeStepperSource interface {
	BatchGetter() (BatchGetter, error)
	IssueRelations() (Relations, error)
	EdgeReader() (EdgeReader, error)
	IssueClaimer() (Claimer, error)
	IssueLifecycle() (Lifecycle, error)
}

// MoleculeStepperRolesOf binds the stepper's roles to src's own accessors.
func MoleculeStepperRolesOf(src moleculeStepperSource) (MoleculeStepperRoles, error) {
	reader, err := NewMoleculeReader(src)
	if err != nil {
		return MoleculeStepperRoles{}, err
	}
	claimer, err := src.IssueClaimer()
	if err != nil {
		return MoleculeStepperRoles{}, err
	}
	lifecycle, err := src.IssueLifecycle()
	if err != nil {
		return MoleculeStepperRoles{}, err
	}
	return MoleculeStepperRoles{Reader: reader, Claimer: claimer, Lifecycle: lifecycle}, nil
}

// NewMoleculeStepper is THE MoleculeStepper implementation. Every backend's
// accessor answers it composed over that backend's own roles, and an http
// client whose server does not serve the operation answers it composed over
// the client's roles, so every route advances a molecule by one rule.
func NewMoleculeStepper(roles MoleculeStepperRoles) MoleculeStepper {
	return moleculeStepper{roles: roles}
}

type moleculeStepper struct{ roles MoleculeStepperRoles }

// ValidateAdvanceRequest applies the request rules every MoleculeStepper
// shares.
func ValidateAdvanceRequest(req AdvanceRequest) error {
	if req.Actor == "" {
		return fmt.Errorf("%w: advance requires an actor", ErrValidation)
	}
	if req.ClosedStepID == "" {
		return fmt.Errorf("%w: advance requires the closed step id", ErrValidation)
	}
	return nil
}

func (s moleculeStepper) Advance(ctx context.Context, req AdvanceRequest) (AdvanceResult, error) {
	if err := ValidateAdvanceRequest(req); err != nil {
		return AdvanceResult{}, err
	}
	r := s.roles.Reader
	if !r.usable() || s.roles.Claimer == nil || s.roles.Lifecycle == nil {
		return AdvanceResult{}, fmt.Errorf("%w: molecule stepper is missing a role", ErrValidation)
	}
	steps, err := getIssues(ctx, r, []string{req.ClosedStepID})
	if err != nil {
		return AdvanceResult{}, err
	}
	closed := steps[req.ClosedStepID]
	if closed == nil {
		return AdvanceResult{}, fmt.Errorf("%w: issue %s", ErrNotFound, req.ClosedStepID)
	}
	result := AdvanceResult{ClosedStep: closed}

	rootID, err := MoleculeRoot(ctx, r, req.ClosedStepID)
	if err != nil {
		return AdvanceResult{}, fmt.Errorf("finding the molecule of %s: %w", req.ClosedStepID, err)
	}
	if rootID == "" {
		return result, nil
	}
	result.MoleculeID = rootID

	view, err := ViewMolecule(ctx, r, rootID)
	if err != nil {
		return AdvanceResult{}, fmt.Errorf("loading molecule %s: %w", rootID, err)
	}
	if view.Complete() {
		result.Complete = true
		return result, nil
	}
	ready := view.ReadySteps()
	if len(ready) == 0 {
		return result, nil
	}
	result.NextStep = ready[0]
	if !req.AutoClaim {
		return result, nil
	}
	for _, candidate := range ready {
		claimed, err := s.claimStep(ctx, candidate, req.Actor)
		if isLostStepClaim(err) {
			continue
		}
		if err != nil {
			return AdvanceResult{}, fmt.Errorf("claiming step %s: %w", candidate.ID, err)
		}
		result.NextStep = claimed
		result.Claimed = true
		break
	}
	return result, nil
}

// claimStep claims one step for actor and answers its post-claim row, labels
// carried over from the row the view read (the claim answers the bare row).
func (s moleculeStepper) claimStep(ctx context.Context, step *Issue, actor string) (*Issue, error) {
	var post *Issue
	if step.Ephemeral || step.NoHistory {
		res, err := s.roles.Lifecycle.Update(ctx, UpdateRequest{Actor: actor, IssueID: step.ID, Claim: true})
		if err != nil {
			return nil, err
		}
		post = res.Issue
	} else {
		res, err := s.roles.Claimer.Claim(ctx, ClaimRequest{Actor: actor, IssueID: step.ID})
		if err != nil {
			return nil, err
		}
		post = res.Issue
	}
	if post == nil {
		return step, nil
	}
	if len(post.Labels) == 0 && len(step.Labels) > 0 {
		post.Labels = append([]string(nil), step.Labels...)
	}
	return post, nil
}

// isLostStepClaim reports whether a step claim failed because the step is no
// longer this actor's to take: held by another actor, or moved out of a
// claimable status since the view read it.
func isLostStepClaim(err error) bool {
	return err != nil && (errors.Is(err, ErrAlreadyClaimed) || errors.Is(err, ErrNotClaimable))
}
