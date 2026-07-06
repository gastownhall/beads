package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newCloseLikeCmd builds a minimal cobra command with the close reason flags
// registered, so resolveReasonFile can be exercised without spinning up the
// full CLI / database layer.
func newCloseLikeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "close",
		Run: func(cmd *cobra.Command, args []string) {},
	}
	registerCloseReasonFlag(cmd)
	cmd.Flags().String("resolution", "", "")
	cmd.Flags().StringP("message", "m", "", "")
	cmd.Flags().String("comment", "", "")
	cmd.Flags().String("reason-file", "", "Read close reason from file (use - for stdin)")
	return cmd
}

func TestCloseResolveReasonFile_FileWithContent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "reason.md")
	content := "## Shipped\n\nWired up the thing. See PR #123.\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cmd := newCloseLikeCmd()
	if err := cmd.ParseFlags([]string{"--reason-file", path}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	got, ok, err := resolveReasonFile(cmd, false)
	if err != nil {
		t.Fatalf("resolveReasonFile: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != content {
		t.Errorf("content mismatch:\nwant: %q\ngot:  %q", content, got)
	}
}

func TestCloseResolveReasonFile_StdinDash(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	content := "Closed via stdin\nMulti-line reason\n"
	go func() {
		_, _ = w.WriteString(content)
		_ = w.Close()
	}()

	cmd := newCloseLikeCmd()
	if err := cmd.ParseFlags([]string{"--reason-file", "-"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	got, ok, err := resolveReasonFile(cmd, false)
	if err != nil {
		t.Fatalf("resolveReasonFile: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != content {
		t.Errorf("content mismatch:\nwant: %q\ngot:  %q", content, got)
	}
}

func TestCloseResolveReasonFile_FileNotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.md")
	cmd := newCloseLikeCmd()
	if err := cmd.ParseFlags([]string{"--reason-file", missing}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	_, ok, err := resolveReasonFile(cmd, false)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if ok {
		t.Error("expected ok=false on error")
	}
	// Path should be quoted in the error so users can see what was attempted.
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("expected error to reference path %q, got: %v", missing, err)
	}
}

func TestCloseResolveReasonFile_ConflictWithReason(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "reason.md")
	if err := os.WriteFile(path, []byte("from file"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cmd := newCloseLikeCmd()
	if err := cmd.ParseFlags([]string{"--reason-file", path, "--reason", "from flag"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	// Caller has already collected the inline reason from the various aliases,
	// so we pass it in directly to mirror how close.go invokes the helper.
	_, ok, err := resolveReasonFile(cmd, true)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if ok {
		t.Error("expected ok=false on conflict")
	}
	if !strings.Contains(err.Error(), "--reason-file") {
		t.Errorf("expected conflict error to mention --reason-file, got: %v", err)
	}
}

func TestCloseResolveReasonFile_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "empty.md")
	if err := os.WriteFile(path, []byte("   \n\t\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cmd := newCloseLikeCmd()
	if err := cmd.ParseFlags([]string{"--reason-file", path}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	_, ok, err := resolveReasonFile(cmd, false)
	if err == nil {
		t.Fatal("expected error for whitespace-only file, got nil")
	}
	if ok {
		t.Error("expected ok=false on empty content")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected error to mention 'empty', got: %v", err)
	}
}

func TestCloseResolveReasonFile_FlagNotSet(t *testing.T) {
	cmd := newCloseLikeCmd()
	if err := cmd.ParseFlags([]string{"--reason", "inline"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	got, ok, err := resolveReasonFile(cmd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false when --reason-file not set")
	}
	if got != "" {
		t.Errorf("expected empty content, got %q", got)
	}
}

func TestCloseResolveReasons_PerIDReasons(t *testing.T) {
	cmd := newCloseLikeCmd()
	cmd.SetArgs([]string{"issue-a", "--reason", "reason A", "issue-b", "--reason", "reason B"})

	var gotReasons, gotArgs []string
	cmd.Run = func(cmd *cobra.Command, args []string) {
		var err error
		gotReasons, gotArgs, err = resolveCloseReasons(cmd, args)
		if err != nil {
			t.Fatalf("resolveCloseReasons: %v", err)
		}
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !slices.Equal(gotArgs, []string{"issue-a", "issue-b"}) {
		t.Fatalf("args = %v, want [issue-a issue-b]", gotArgs)
	}
	if !slices.Equal(gotReasons, []string{"reason A", "reason B"}) {
		t.Fatalf("reasons = %v, want per-ID reasons", gotReasons)
	}
}

func TestCloseResolveReasons_SharedReasonForMultipleIDs(t *testing.T) {
	cmd := newCloseLikeCmd()
	cmd.SetArgs([]string{"issue-a", "issue-b", "--reason", "same reason"})

	var gotReasons, gotArgs []string
	cmd.Run = func(cmd *cobra.Command, args []string) {
		var err error
		gotReasons, gotArgs, err = resolveCloseReasons(cmd, args)
		if err != nil {
			t.Fatalf("resolveCloseReasons: %v", err)
		}
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !slices.Equal(gotArgs, []string{"issue-a", "issue-b"}) {
		t.Fatalf("args = %v, want [issue-a issue-b]", gotArgs)
	}
	if !slices.Equal(gotReasons, []string{"same reason"}) {
		t.Fatalf("reasons = %v, want one shared reason", gotReasons)
	}
}

func TestCloseResolveReasons_EmptyReasonFallsBackToDefault(t *testing.T) {
	cmd := newCloseLikeCmd()
	cmd.SetArgs([]string{"issue-a", "--reason", ""})

	var gotReasons, gotArgs []string
	cmd.Run = func(cmd *cobra.Command, args []string) {
		var err error
		gotReasons, gotArgs, err = resolveCloseReasons(cmd, args)
		if err != nil {
			t.Fatalf("resolveCloseReasons: %v", err)
		}
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !slices.Equal(gotArgs, []string{"issue-a"}) {
		t.Fatalf("args = %v, want [issue-a]", gotArgs)
	}
	if !slices.Equal(gotReasons, []string{"Closed"}) {
		t.Fatalf("reasons = %v, want default reason", gotReasons)
	}
}

func TestCloseResolveReasons_EmptyReasonDoesNotConflictWithReasonFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "reason.md")
	if err := os.WriteFile(path, []byte("from file"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cmd := newCloseLikeCmd()
	cmd.SetArgs([]string{"issue-a", "--reason", "", "--reason-file", path})

	var gotReasons, gotArgs []string
	cmd.Run = func(cmd *cobra.Command, args []string) {
		var err error
		gotReasons, gotArgs, err = resolveCloseReasons(cmd, args)
		if err != nil {
			t.Fatalf("resolveCloseReasons: %v", err)
		}
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !slices.Equal(gotArgs, []string{"issue-a"}) {
		t.Fatalf("args = %v, want [issue-a]", gotArgs)
	}
	if !slices.Equal(gotReasons, []string{"from file"}) {
		t.Fatalf("reasons = %v, want file reason", gotReasons)
	}
}

func TestCloseResolveReasons_RejectsMismatchedPerIDReasons(t *testing.T) {
	cmd := newCloseLikeCmd()
	cmd.SetArgs([]string{"issue-a", "--reason", "reason A", "issue-b", "--reason", "reason B", "issue-c"})

	cmd.Run = func(cmd *cobra.Command, args []string) {
		_, _, err := resolveCloseReasons(cmd, args)
		if err == nil {
			t.Fatal("expected mismatch error, got nil")
		}
		if !strings.Contains(err.Error(), "2 close reasons for 3 issue IDs") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
}
