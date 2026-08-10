//go:build js && wasm

package hooks

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/types"
)

var errHookExecutionUnsupported = errors.New("hook execution is not supported on js/wasm")

func (*Runner) runHook(hookPath, event string, issue *types.Issue) (retErr error) {
	// Hooks are fire-and-forget so they have no parent span; create the same
	// root span as native runners so asynchronous refusals remain observable.
	tracer := otel.Tracer("github.com/steveyegge/beads/hooks")
	_, span := tracer.Start(context.Background(), "hook.exec",
		trace.WithAttributes(
			attribute.String("hook.event", event),
			attribute.String("hook.path", hookPath),
			attribute.String("bd.issue_id", issue.ID),
		),
	)
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	return errHookExecutionUnsupported
}
