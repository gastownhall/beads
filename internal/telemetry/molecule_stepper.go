package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// MoleculeStepper returns the inner store's advance surface wrapped in this
// layer's instrumentation.
func (s *InstrumentedStorage) MoleculeStepper() (issueops.MoleculeStepper, error) {
	inner, err := s.Unwrap().MoleculeStepper()
	if err != nil {
		return nil, err
	}
	return s.WrapMoleculeStepper(inner), nil
}

// WrapMoleculeStepper instruments molecule advances with this storage layer's
// existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapMoleculeStepper(inner issueops.MoleculeStepper) issueops.MoleculeStepper {
	return &instrumentedMoleculeStepper{storage: s, inner: inner}
}

type instrumentedMoleculeStepper struct {
	storage *InstrumentedStorage
	inner   issueops.MoleculeStepper
}

func (m *instrumentedMoleculeStepper) Advance(ctx context.Context, req issueops.AdvanceRequest) (result issueops.AdvanceResult, err error) {
	ctx, span, started := m.storage.op(ctx, "MoleculeStepper.Advance")
	result, err = m.inner.Advance(ctx, req)
	m.storage.done(ctx, span, started, err)
	return result, err
}
