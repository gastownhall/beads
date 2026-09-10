//go:build js && wasm

package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/steveyegge/beads/internal/types"
)

func testUnsupportedExecutionAsyncWarningAndSyncError(t *testing.T) {
	hookPath := filepath.Join(t.TempDir(), HookOnCreate)

	stderrPath := filepath.Join(t.TempDir(), "stderr")
	stderr, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	previousStderr := os.Stderr
	os.Stderr = stderr
	t.Cleanup(func() {
		os.Stderr = previousStderr
		_ = stderr.Close()
	})

	runner := NewRunner(filepath.Dir(hookPath))
	issue := &types.Issue{ID: "wasm-test"}
	// Run calls this boundary after its platform-independent existence and
	// executable-bit preflight. Calling it directly keeps the js/wasm contract
	// deterministic on Node hosts whose virtual filesystem drops Unix exec bits.
	runner.runAsync(hookPath, EventCreate, issue)
	if !runner.Wait(runner.Timeout()) {
		t.Fatal("asynchronous hook refusal did not finish")
	}

	// RunSync delegates to this platform primitive after the same preflight. It
	// retains the returned-error contract without printing a second warning.
	if err := runner.runHook(hookPath, EventCreate, issue); !errors.Is(err, errHookExecutionUnsupported) {
		t.Fatalf("RunSync error = %v, want %v", err, errHookExecutionUnsupported)
	}

	os.Stderr = previousStderr
	if err := stderr.Close(); err != nil {
		t.Fatalf("close stderr capture: %v", err)
	}
	got, err := os.ReadFile(stderrPath)
	if err != nil {
		t.Fatalf("read stderr capture: %v", err)
	}
	want := fmt.Sprintf("warning: hook %q was not run: %v\n", hookPath, errHookExecutionUnsupported)
	if string(got) != want {
		t.Fatalf("stderr = %q, want exactly one warning %q", got, want)
	}
}

func TestRunHookReportsUnsupportedExecution(t *testing.T) {
	t.Run("async warning and sync error", testUnsupportedExecutionAsyncWarningAndSyncError)

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	previousTracerProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tracerProvider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousTracerProvider)
		if err := tracerProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("shut down tracer provider: %v", err)
		}
	})

	runner := NewRunner(t.TempDir())
	hookPath := "not-executable-on-wasm"
	issue := &types.Issue{ID: "wasm-test"}
	err := runner.runHook(
		hookPath,
		EventCreate,
		issue,
	)
	if !errors.Is(err, errHookExecutionUnsupported) {
		t.Fatalf("runHook error = %v, want %v", err, errHookExecutionUnsupported)
	}

	spans := spanRecorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "hook.exec" {
		t.Errorf("span name = %q, want %q", span.Name(), "hook.exec")
	}
	if span.Parent().IsValid() {
		t.Errorf("span parent = %v, want invalid root parent", span.Parent())
	}

	gotAttributes := make(map[attribute.Key]attribute.Value, len(span.Attributes()))
	for _, attr := range span.Attributes() {
		gotAttributes[attr.Key] = attr.Value
	}
	wantAttributes := map[attribute.Key]string{
		"hook.event":  EventCreate,
		"hook.path":   hookPath,
		"bd.issue_id": issue.ID,
	}
	if len(gotAttributes) != len(wantAttributes) {
		t.Errorf("span attributes = %v, want exactly %v", span.Attributes(), wantAttributes)
	}
	for key, want := range wantAttributes {
		got, ok := gotAttributes[key]
		if !ok || got.AsString() != want {
			t.Errorf("span attribute %q = %v, want %q", key, got, want)
		}
	}

	if span.Status().Code != codes.Error {
		t.Errorf("span status code = %v, want %v", span.Status().Code, codes.Error)
	}
	if span.Status().Description != errHookExecutionUnsupported.Error() {
		t.Errorf("span status description = %q, want %q", span.Status().Description, errHookExecutionUnsupported)
	}

	events := span.Events()
	if len(events) != 1 {
		t.Fatalf("span events = %v, want one recorded error", events)
	}
	if events[0].Name != "exception" {
		t.Errorf("span event name = %q, want %q", events[0].Name, "exception")
	}
	var recordedError string
	for _, attr := range events[0].Attributes {
		if attr.Key == "exception.message" {
			recordedError = attr.Value.AsString()
			break
		}
	}
	if recordedError != errHookExecutionUnsupported.Error() {
		t.Errorf("recorded error = %q, want %q", recordedError, errHookExecutionUnsupported)
	}
}
