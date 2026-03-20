package notion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	name   string
	args   []string
	stdin  []byte
	stdout []byte
	stderr []byte
	err    error
}

func (f *fakeRunner) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, []byte, error) {
	f.name = name
	f.args = append([]string(nil), args...)
	f.stdin = append([]byte(nil), stdin...)
	return append([]byte(nil), f.stdout...), append([]byte(nil), f.stderr...), f.err
}

func TestNewClientUsesDefaults(t *testing.T) {
	t.Parallel()

	client := NewClient()
	if client.BinaryPath() != DefaultBinaryPath {
		t.Fatalf("binary path = %q, want %q", client.BinaryPath(), DefaultBinaryPath)
	}
}

func TestStatusInvalidBinaryPath(t *testing.T) {
	t.Parallel()

	client := NewClient(WithBinaryPath(filepath.Join(t.TempDir(), "missing-ncli")))
	_, err := client.Status(context.Background(), StatusRequest{})

	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected CommandError, got %T", err)
	}
	if cmdErr.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", cmdErr.ExitCode)
	}
}

func TestStatusNonZeroExit(t *testing.T) {
	t.Parallel()

	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "fake-ncli.sh")
	script := "#!/bin/sh\necho boom 1>&2\nexit 7\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	client := NewClient(WithBinaryPath(scriptPath))
	_, err := client.Status(context.Background(), StatusRequest{})

	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected CommandError, got %T", err)
	}
	if cmdErr.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", cmdErr.ExitCode)
	}
	if !strings.Contains(cmdErr.Stderr, "boom") {
		t.Fatalf("stderr = %q, want boom", cmdErr.Stderr)
	}
}

func TestStatusInvalidJSON(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{stdout: []byte("{")}
	client := NewClient(WithRunner(runner))
	_, err := client.Status(context.Background(), StatusRequest{})

	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected CommandError, got %T", err)
	}
	if !strings.Contains(cmdErr.Error(), "failed to decode JSON response") {
		t.Fatalf("error = %q, want decode failure", cmdErr.Error())
	}
	if runner.name != DefaultBinaryPath {
		t.Fatalf("binary path = %q, want %q", runner.name, DefaultBinaryPath)
	}
}

func TestStatusValidJSON(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		stdout: []byte(`{"ready":true,"data_source_id":"ds_123","archive":{"supported":false,"reason":"mcp missing"}}`),
	}
	client := NewClient(WithRunner(runner))
	resp, err := client.Status(context.Background(), StatusRequest{
		DatabaseID: "db_123",
		ViewURL:    "https://example.com/view",
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !resp.Ready {
		t.Fatalf("ready = false, want true")
	}
	if resp.DataSourceID != "ds_123" {
		t.Fatalf("data source id = %q, want ds_123", resp.DataSourceID)
	}
	wantArgs := []string{"beads", "status", "--json", "--database-id", "db_123", "--view-url", "https://example.com/view"}
	if strings.Join(runner.args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", runner.args, wantArgs)
	}
}

func TestPullValidJSON(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		stdout: []byte(`{"issues":[{"id":"bd-1","title":"One","external_ref":"notion:page_1"}]}`),
	}
	client := NewClient(WithRunner(runner))
	resp, err := client.Pull(context.Background(), PullRequest{})
	if err != nil {
		t.Fatalf("Pull returned error: %v", err)
	}
	if len(resp.Issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(resp.Issues))
	}
	wantArgs := []string{"beads", "pull", "--json"}
	if strings.Join(runner.args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", runner.args, wantArgs)
	}
}

func TestPushValidJSONAndStdin(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		stdout: []byte(`{"dry_run":false,"archive_requested":false,"archive_supported":false,"archive_reason":"unsupported","input_count":1,"created_count":1,"updated_count":0,"skipped_count":0}`),
	}
	client := NewClient(WithRunner(runner))
	payload := []byte(`{"issues":[{"id":"bd-1","title":"One"}]}`)

	resp, err := client.Push(context.Background(), PushRequest{
		DatabaseID: "db_123",
		ViewURL:    "https://example.com/view",
		Payload:    payload,
	})
	if err != nil {
		t.Fatalf("Push returned error: %v", err)
	}
	if resp.CreatedCount != 1 {
		t.Fatalf("created count = %d, want 1", resp.CreatedCount)
	}
	if resp.ArchiveRequested {
		t.Fatal("archive requested = true, want false")
	}
	if resp.ArchiveSupported {
		t.Fatal("archive supported = true, want false")
	}
	if resp.ArchiveReason != "unsupported" {
		t.Fatalf("archive reason = %q, want unsupported", resp.ArchiveReason)
	}
	wantArgs := []string{"beads", "push", "--json", "--input", "-", "--database-id", "db_123", "--view-url", "https://example.com/view"}
	if strings.Join(runner.args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", runner.args, wantArgs)
	}
	if string(runner.stdin) != string(payload) {
		t.Fatalf("stdin = %q, want %q", string(runner.stdin), string(payload))
	}
}

func TestPushRequiresPayload(t *testing.T) {
	t.Parallel()

	client := NewClient(WithRunner(&fakeRunner{}))
	_, err := client.Push(context.Background(), PushRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "payload is required") {
		t.Fatalf("error = %q, want payload is required", err.Error())
	}
}
