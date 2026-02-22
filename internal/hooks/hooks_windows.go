//go:build windows

package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/types"
)

// runHook executes the hook and enforces a timeout on Windows.
// Windows lacks Unix-style process groups; on timeout we best-effort kill
// the started process. Descendant processes may survive if they detach,
// but this preserves previous behavior while keeping tests green on Windows.
func (r *Runner) runHook(hookPath, event string, issue *types.Issue) (retErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()

	// Hooks are fire-and-forget so they have no parent span; we create a root span
	// to track execution time and errors for observability.
	tracer := otel.Tracer("github.com/steveyegge/beads/hooks")
	ctx, span := tracer.Start(ctx, "hook.exec",
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

	issueJSON, err := json.Marshal(issue)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, hookPath, issue.ID, event)
	cmd.Stdin = bytes.NewReader(issueJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		addHookOutputEvents(span, &stdout, &stderr)
		return ctx.Err()
	case err := <-done:
		addHookOutputEvents(span, &stdout, &stderr)
		return err
	}
}
