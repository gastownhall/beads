//go:build js && wasm

package hooks

import (
	"bytes"
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

	var stderr bytes.Buffer

	runner := NewRunner(filepath.Dir(hookPath))
	issue := &types.Issue{ID: "wasm-test"}
	// Run calls this boundary after its platform-independent existence and
	// executable-bit preflight. Calling it directly keeps the js/wasm contract
	// deterministic on Node hosts whose virtual filesystem drops Unix exec bits.
	runner.runAsync(hookPath, EventCreate, issue, &stderr)
	if !runner.Wait(runner.Timeout()) {
		t.Fatal("asynchronous hook refusal did not finish")
	}

	// RunSync delegates to this platform primitive after the same preflight. It
	// retains the returned-error contract without printing a second warning.
	if err := runner.runHook(hookPath, EventCreate, issue); !errors.Is(err, errHookExecutionUnsupported) {
		t.Fatalf("runHook error = %v, want %v", err, errHookExecutionUnsupported)
	}

	got := stderr.String()
	want := fmt.Sprintf("warning: hook %q was not run: %v\n", hookPath, errHookExecutionUnsupported)
	if got != want {
		t.Fatalf("stderr = %q, want exactly one warning %q", got, want)
	}
}

// testUnsupportedExecutionRunReachesTheBoundary covers the production route the
// subtest above deliberately bypasses: Run itself, with a hook file that really
// exists on disk. Run gates on existence, IsDir and the executable bit before it
// calls runAsync, so whether the refusal is reachable at all on a given host is
// decided by whether that host's filesystem keeps the exec bit — which is
// exactly what the bypass comment above asserts it may not.
//
// The host answers that question here rather than being assumed, and either
// answer is asserted instead of skipped: this test runs under the required
// js/wasm lane, whose count guard rejects a SKIP line as well as a FAIL.
func testUnsupportedExecutionRunReachesTheBoundary(t *testing.T) {
	hooksDir := t.TempDir()
	hookPath := filepath.Join(hooksDir, HookOnCreate)
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("create hook file: %v", err)
	}
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatalf("stat hook file: %v", err)
	}
	executable := info.Mode()&0111 != 0

	// Run writes to the process stderr, so capturing it is the only way to see
	// what a user would. A file rather than a pipe: this host is a wasm runtime.
	stderrPath := filepath.Join(hooksDir, "stderr.txt")
	capture, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create stderr capture: %v", err)
	}
	original := os.Stderr
	restore := func() { os.Stderr = original }
	t.Cleanup(restore)
	os.Stderr = capture

	runner := NewRunner(hooksDir)
	issue := &types.Issue{ID: "wasm-test"}
	runner.Run(EventCreate, issue)
	if !runner.Wait(runner.Timeout()) {
		t.Fatal("asynchronous hook refusal did not finish")
	}

	// Wait returned, so the goroutine's Fprintf happens-before this restore.
	restore()
	if err := capture.Close(); err != nil {
		t.Fatalf("close stderr capture: %v", err)
	}
	got, err := os.ReadFile(stderrPath)
	if err != nil {
		t.Fatalf("read stderr capture: %v", err)
	}

	want := ""
	reached := "does not reach"
	if executable {
		want = fmt.Sprintf("warning: hook %q was not run: %v\n", hookPath, errHookExecutionUnsupported)
		reached = "reaches"
	}
	if string(got) != want {
		t.Fatalf("Run stderr = %q, want %q (hook mode %v)", string(got), want, info.Mode())
	}
	t.Logf("hook file mode %v on this host, so Run %s the refusal warning", info.Mode(), reached)
}

func TestRunHookReportsUnsupportedExecution(t *testing.T) {
	t.Run("async warning and sync error", testUnsupportedExecutionAsyncWarningAndSyncError)
	t.Run("run drives the boundary", testUnsupportedExecutionRunReachesTheBoundary)

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
