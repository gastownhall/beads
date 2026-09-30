package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// refusingStepClaimer answers ClaimStepIfOpen from a per-step verdict.
type refusingStepClaimer struct {
	refuse map[string]error
	tried  []string
}

func (c *refusingStepClaimer) ClaimStepIfOpen(_ context.Context, id, _ string) error {
	c.tried = append(c.tried, id)
	return c.refuse[id]
}

func stepIDOf(step *types.Issue) string {
	if step == nil {
		return "<none>"
	}
	return step.ID
}

func externallyHeldStep(id string) error {
	return fmt.Errorf("%w: %s is blocked by [external:remote:payments]", storage.ErrClaimBlocked, id)
}

// TestContinueNeverSuggestsClaimingAnExternallyHeldStep pins the `bd close
// --continue` hint: when the auto-claim claims nothing, the step it names is
// the first one the external-dependency guard did NOT refuse, and when the
// guard refused every ready step it names none — it used to print "Start with:
// bd update <step> --claim" for readySteps[0], a claim `bd update --claim`
// refuses for the same reason.
func TestContinueNeverSuggestsClaimingAnExternallyHeldStep(t *testing.T) {
	steps := []*types.Issue{{ID: "mx-1", Title: "one"}, {ID: "mx-2", Title: "two"}, {ID: "mx-3", Title: "three"}}
	taken := errors.New("step mx-2 already claimed (status: in_progress)")

	t.Run("claims the first claimable step past the held ones", func(t *testing.T) {
		c := &refusingStepClaimer{refuse: map[string]error{"mx-1": externallyHeldStep("mx-1")}}
		next, claimed, held := claimNextReadyStep(t.Context(), c, steps, "w")
		if !claimed || next == nil || next.ID != "mx-2" || !slices.Equal(held, []string{"mx-1"}) {
			t.Fatalf("next=%s claimed=%v held=%v, want mx-2 claimed past held [mx-1]", stepIDOf(next), claimed, held)
		}
	})

	t.Run("a step another agent took is still the next step", func(t *testing.T) {
		c := &refusingStepClaimer{refuse: map[string]error{
			"mx-1": externallyHeldStep("mx-1"), "mx-2": taken, "mx-3": externallyHeldStep("mx-3"),
		}}
		next, claimed, _ := claimNextReadyStep(t.Context(), c, steps, "w")
		if claimed || next == nil || next.ID != "mx-2" {
			t.Fatalf("next=%s claimed=%v, want unclaimed mx-2 (not the held mx-1)", stepIDOf(next), claimed)
		}
	})

	t.Run("every step held: no step suggested, and the output says why", func(t *testing.T) {
		c := &refusingStepClaimer{refuse: map[string]error{
			"mx-1": externallyHeldStep("mx-1"), "mx-2": externallyHeldStep("mx-2"), "mx-3": externallyHeldStep("mx-3"),
		}}
		next, claimed, held := claimNextReadyStep(t.Context(), c, steps, "w")
		if claimed || next != nil {
			t.Fatalf("next=%s claimed=%v, want no next step when the guard refused every one", stepIDOf(next), claimed)
		}
		if !slices.Equal(c.tried, []string{"mx-1", "mx-2", "mx-3"}) {
			t.Errorf("tried %v, want every ready step", c.tried)
		}
		out := captureStdout(t, func() error {
			PrintContinueResult(&ContinueResult{MoleculeID: "mx", NextStep: next, HeldSteps: held})
			return nil
		})
		if strings.Contains(out, "--claim") {
			t.Errorf("output suggests a claim the guard refuses:\n%s", out)
		}
		if !strings.Contains(out, "external dependency") || !strings.Contains(out, "mx-1, mx-2, mx-3") {
			t.Errorf("output does not say the ready steps are held by external dependencies:\n%s", out)
		}
	})
}

// guardingStepClaimer answers GuardStepClaim from a per-step verdict and
// fails the test if anything is claimed.
type guardingStepClaimer struct {
	t       *testing.T
	refuse  map[string]error
	guarded []string
}

func (c *guardingStepClaimer) GuardStepClaim(_ context.Context, id string) error {
	c.guarded = append(c.guarded, id)
	return c.refuse[id]
}

func (c *guardingStepClaimer) ClaimStepIfOpen(_ context.Context, id, _ string) error {
	c.t.Errorf("--no-auto claimed %s", id)
	return nil
}

// TestContinueNoAutoNeverSuggestsAnExternallyHeldStep is the `--no-auto`
// counterpart: nothing is claimed, the step suggested is the first the guard
// does not refuse, and when it refuses every ready step none is suggested. It
// used to suggest readySteps[0] unconditionally.
func TestContinueNoAutoNeverSuggestsAnExternallyHeldStep(t *testing.T) {
	steps := []*types.Issue{{ID: "mx-1", Title: "one"}, {ID: "mx-2", Title: "two"}, {ID: "mx-3", Title: "three"}}

	t.Run("suggests the first step past the held ones", func(t *testing.T) {
		c := &guardingStepClaimer{t: t, refuse: map[string]error{"mx-1": externallyHeldStep("mx-1")}}
		next, held := suggestNextReadyStep(t.Context(), c, steps)
		if next == nil || next.ID != "mx-2" || !slices.Equal(held, []string{"mx-1"}) {
			t.Fatalf("next=%s held=%v, want mx-2 past held [mx-1]", stepIDOf(next), held)
		}
		if !slices.Equal(c.guarded, []string{"mx-1", "mx-2"}) {
			t.Errorf("guarded %v, want the steps up to the first unrefused one", c.guarded)
		}
	})

	t.Run("a guard error other than the refusal does not hold the step", func(t *testing.T) {
		c := &guardingStepClaimer{t: t, refuse: map[string]error{"mx-1": errors.New("read failed")}}
		if next, held := suggestNextReadyStep(t.Context(), c, steps); next == nil || next.ID != "mx-1" || len(held) != 0 {
			t.Fatalf("next=%s held=%v, want mx-1", stepIDOf(next), held)
		}
	})

	t.Run("every step held: no step suggested, and the output says why", func(t *testing.T) {
		c := &guardingStepClaimer{t: t, refuse: map[string]error{
			"mx-1": externallyHeldStep("mx-1"), "mx-2": externallyHeldStep("mx-2"), "mx-3": externallyHeldStep("mx-3"),
		}}
		next, held := suggestNextReadyStep(t.Context(), c, steps)
		if next != nil {
			t.Fatalf("next=%s, want none when the guard refuses every step", stepIDOf(next))
		}
		out := captureStdout(t, func() error {
			PrintContinueResult(&ContinueResult{MoleculeID: "mx", NextStep: next, HeldSteps: held})
			return nil
		})
		if strings.Contains(out, "--claim") || !strings.Contains(out, "No claimable steps") || !strings.Contains(out, "mx-1, mx-2, mx-3") {
			t.Errorf("output:\n%s\nwant the no-claimable-steps reason and no claim hint", out)
		}
	})

	t.Run("a writer without the guard suggests the first ready step", func(t *testing.T) {
		if next, held := suggestNextReadyStep(t.Context(), &refusingStepClaimer{}, steps); next == nil || next.ID != "mx-1" || held != nil {
			t.Fatalf("next=%s held=%v, want mx-1", stepIDOf(next), held)
		}
	})
}
