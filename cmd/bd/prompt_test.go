package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

type notifyStub struct {
	ready   chan struct{}
	cancel  context.CancelFunc
	signals []os.Signal
}

func installNotifyStub(t *testing.T) *notifyStub {
	t.Helper()
	stub := &notifyStub{ready: make(chan struct{}, 1)}
	original := notifyContext
	notifyContext = func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		stub.signals = append([]os.Signal(nil), signals...)
		ctx, cancel := context.WithCancel(parent)
		stub.cancel = cancel
		select {
		case stub.ready <- struct{}{}:
		default:
		}
		return ctx, cancel
	}
	t.Cleanup(func() { notifyContext = original })
	return stub
}

func TestReadLineWithContextReadsLine(t *testing.T) {
	stub := installNotifyStub(t)

	reader := bufio.NewReader(strings.NewReader("yes\n"))
	line, err := readLineWithContext(context.Background(), reader, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "yes\n" {
		t.Fatalf("unexpected line: %q", line)
	}
	if len(stub.signals) != 2 || stub.signals[0] != os.Interrupt || stub.signals[1] != syscall.SIGTERM {
		t.Fatalf("unexpected signals: %v", stub.signals)
	}
}

func TestReadLineWithContextCanceled(t *testing.T) {
	stub := installNotifyStub(t)

	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pr.Close()
		_ = pw.Close()
	})

	reader := bufio.NewReader(pr)
	done := make(chan error, 1)
	go func() {
		_, err := readLineWithContext(context.Background(), reader, pr)
		done <- err
	}()

	select {
	case <-stub.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for notifyContext")
	}
	if stub.cancel == nil {
		t.Fatal("expected cancel function")
	}

	stub.cancel()
	_ = pw.Close()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for readLineWithContext")
	}
}

func TestPromptAutoExportDefaultsOff(t *testing.T) {
	got, output, err := runPromptAutoExport(t, "\n")
	if err != nil {
		t.Fatalf("promptAutoExport returned error: %v", err)
	}
	if got {
		t.Fatal("empty response should leave auto-export disabled")
	}
	if !strings.Contains(output, "Enable auto-export? [y/N]:") {
		t.Fatalf("prompt did not show opt-in default:\n%s", output)
	}
	if !strings.Contains(output, "optional JSONL export") {
		t.Fatalf("prompt should describe JSONL as optional export:\n%s", output)
	}
	if !strings.Contains(output, "Dolt remotes/backups handle cross-machine sync and backup") {
		t.Fatalf("prompt should direct sync to Dolt remotes:\n%s", output)
	}
}

func TestPromptAutoExportAcceptsYes(t *testing.T) {
	got, _, err := runPromptAutoExport(t, "yes\n")
	if err != nil {
		t.Fatalf("promptAutoExport returned error: %v", err)
	}
	if !got {
		t.Fatal("yes response should enable auto-export")
	}
}

func runPromptAutoExport(t *testing.T, input string) (bool, string, error) {
	t.Helper()

	stdinFile, err := os.CreateTemp(t.TempDir(), "stdin-*")
	if err != nil {
		t.Fatalf("create temp stdin: %v", err)
	}
	if _, err := stdinFile.WriteString(input); err != nil {
		t.Fatalf("write temp stdin: %v", err)
	}
	if _, err := stdinFile.Seek(0, 0); err != nil {
		t.Fatalf("rewind temp stdin: %v", err)
	}

	oldStdin := os.Stdin
	oldStdout := os.Stdout
	oldRootCtx := rootCtx
	oldRootCancel := rootCancel
	var oldCmdRootCtx context.Context
	var oldCmdRootCancel context.CancelFunc
	if cmdCtx != nil {
		oldCmdRootCtx = cmdCtx.RootCtx
		oldCmdRootCancel = cmdCtx.RootCancel
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	setRootContext(ctx, cancel)
	os.Stdin = stdinFile
	os.Stdout = w

	got, promptErr := promptAutoExport()

	_ = w.Close()
	var b strings.Builder
	_, _ = io.Copy(&b, r)
	_ = r.Close()
	os.Stdin = oldStdin
	os.Stdout = oldStdout
	cancel()
	rootCtx = oldRootCtx
	rootCancel = oldRootCancel
	if cmdCtx != nil {
		cmdCtx.RootCtx = oldCmdRootCtx
		cmdCtx.RootCancel = oldCmdRootCancel
	}
	_ = stdinFile.Close()

	return got, b.String(), promptErr
}
