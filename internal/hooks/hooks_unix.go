//go:build unix

package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/types"
)

// runHook executes the hook and enforces a timeout, killing the process group
// on expiration to ensure descendant processes are terminated.
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

	// Prepare JSON data for stdin
	issueJSON, err := json.Marshal(issue)
	if err != nil {
		return err
	}

	// Create command: hook_script <issue_id> <event_type>
	// #nosec G204 -- hookPath is from controlled .beads/hooks directory
	cmd := exec.CommandContext(ctx, hookPath, issue.ID, event)
	cmd.Stdin = bytes.NewReader(issueJSON)

	// Capture output for debugging (but don't block on it)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Start the hook so we can manage its process group and kill children on timeout.
	//
	// Rationale: scripts may spawn child processes (backgrounded or otherwise).
	// If we only kill the immediate process, descendants may survive and keep
	// the test (or caller) blocked — this was exposed by TestRunSync_Timeout and
	// validated by TestRunSync_KillsDescendants. Creating a process group (Setpgid)
	// and sending a negative PID to syscall.Kill ensures the entire group
	// (parent + children) are killed reliably on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("kill process group: %w", err)
			}
		}
		// Wait for process to exit after the kill attempt
		<-done
		addHookOutputEvents(span, &stdout, &stderr)
		return ctx.Err()
	case err := <-done:
		addHookOutputEvents(span, &stdout, &stderr)
		if err != nil {
			return err
		}
		return nil
	}
}
