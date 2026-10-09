package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// MoleculeStepper returns the inner store's advance surface, wrapped so a
// claimed step fires on_update — the event every claim fires. An advance that
// claimed nothing wrote nothing and fires nothing.
func (h *HookFiringStore) MoleculeStepper() (issueops.MoleculeStepper, error) {
	inner, err := h.inner.MoleculeStepper()
	if err != nil {
		return nil, err
	}
	return &hookMoleculeStepper{inner: inner, hooks: h}, nil
}

type hookMoleculeStepper struct {
	inner issueops.MoleculeStepper
	hooks *HookFiringStore
}

func (s *hookMoleculeStepper) Advance(ctx context.Context, req issueops.AdvanceRequest) (issueops.AdvanceResult, error) {
	result, err := s.inner.Advance(ctx, req)
	if err == nil && result.Claimed && result.NextStep != nil {
		s.hooks.CompleteIssueOperationUpdate(result.NextStep)
	}
	return result, err
}
