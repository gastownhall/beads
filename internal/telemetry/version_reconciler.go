package telemetry

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// VersionReconciler returns the inner store's version-marker surface wrapped in
// this layer's instrumentation. It recurses instead of delegating: a blind
// delegation would return the inner surface unspanned and untimed.
//
// This is the one role whose spans are emitted on EVERY command, which makes it
// the one place a startup regression would show up as a number rather than as a
// report that bd feels slow.
func (s *InstrumentedStorage) VersionReconciler() (issueops.VersionReconciler, error) {
	inner, err := s.Unwrap().VersionReconciler()
	if err != nil {
		return nil, err
	}
	return s.WrapVersionReconciler(inner), nil
}

// WrapVersionReconciler instruments guarded version-marker access with this
// storage layer's existing telemetry meter and tracer.
func (s *InstrumentedStorage) WrapVersionReconciler(inner issueops.VersionReconciler) issueops.VersionReconciler {
	return &instrumentedVersionReconciler{storage: s, inner: inner}
}

type instrumentedVersionReconciler struct {
	storage *InstrumentedStorage
	inner   issueops.VersionReconciler
}

func (r *instrumentedVersionReconciler) RecordedVersion(ctx context.Context, request issueops.RecordedVersionRequest) (result issueops.RecordedVersionResult, err error) {
	ctx, span, started := r.storage.op(ctx, "VersionReconciler.RecordedVersion")
	result, err = r.inner.RecordedVersion(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}

func (r *instrumentedVersionReconciler) ReconcileVersion(ctx context.Context, request issueops.VersionReconcileRequest) (result issueops.VersionReconcileResult, err error) {
	ctx, span, started := r.storage.op(ctx, "VersionReconciler.ReconcileVersion")
	result, err = r.inner.ReconcileVersion(ctx, request)
	r.storage.done(ctx, span, started, err)
	return result, err
}
