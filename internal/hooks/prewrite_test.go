package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBeforeWriteMissingHookIsNoOp(t *testing.T) {
	runner := NewRunnerForBeadsDir(filepath.Join(t.TempDir(), ".beads"))
	if err := runner.BeforeWrite(t.Context(), "issue.create"); err != nil {
		t.Fatalf("BeforeWrite without hook: %v", err)
	}
}

func TestBeforeWritePassesVersionedRepositoryRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	hooksDir := filepath.Join(beadsDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "request.json")
	writePreWriteHook(t, hooksDir, "#!/bin/sh\ncat > "+shellQuote(output)+"\nprintf '%s\\n' '{\"allow\":true}'\n")

	runner := NewRunnerForBeadsDir(beadsDir)
	if err := runner.BeforeWrite(t.Context(), "issue.create"); err != nil {
		t.Fatalf("BeforeWrite: %v", err)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var request PreWriteRequest
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if request.Version != PreWriteProtocolVersion || request.Operation != "issue.create" {
		t.Fatalf("request = %#v", request)
	}
	expectedRoot, err := canonicalPath(root)
	if err != nil {
		t.Fatal(err)
	}
	expectedBeadsDir, err := canonicalPath(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	if request.Repository.Root != expectedRoot || request.Repository.BeadsDir != expectedBeadsDir {
		t.Fatalf("repository = %#v, want root=%q beads_dir=%q", request.Repository, expectedRoot, expectedBeadsDir)
	}
}

func TestBeforeWriteDenialAndMalformedResponseFailClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}
	for name, response := range map[string]string{
		"denial":    `{"allow":false,"reason":"maintenance"}`,
		"malformed": `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			hooksDir := filepath.Join(root, ".beads", "hooks")
			if err := os.MkdirAll(hooksDir, 0755); err != nil {
				t.Fatal(err)
			}
			writePreWriteHook(t, hooksDir, "#!/bin/sh\nprintf '%s\\n' "+shellQuote(response)+"\n")

			err := NewRunnerForBeadsDir(filepath.Join(root, ".beads")).BeforeWrite(context.Background(), "issue.update")
			if !errors.Is(err, ErrPreWriteRejected) {
				t.Fatalf("BeforeWrite error = %v, want ErrPreWriteRejected", err)
			}
			var rejection *PreWriteError
			if !errors.As(err, &rejection) {
				t.Fatalf("BeforeWrite error type = %T, want *PreWriteError", err)
			}
			if rejection.Operation != "issue.update" {
				t.Fatalf("operation = %q, want issue.update", rejection.Operation)
			}
		})
	}
}

func TestBeforeWriteConfiguredNonExecutableHookFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable-bit assertion")
	}
	root := t.TempDir()
	hooksDir := filepath.Join(root, ".beads", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, HookPreWrite), []byte("#!/bin/sh\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := NewRunnerForBeadsDir(filepath.Join(root, ".beads")).BeforeWrite(t.Context(), "issue.close")
	if !errors.Is(err, ErrPreWriteRejected) {
		t.Fatalf("BeforeWrite error = %v, want ErrPreWriteRejected", err)
	}
}

func TestBeforeWriteTimeoutFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is Unix-only")
	}
	root := t.TempDir()
	hooksDir := filepath.Join(root, ".beads", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	writePreWriteHook(t, hooksDir, "#!/bin/sh\nsleep 5\n")
	runner := NewRunnerForBeadsDir(filepath.Join(root, ".beads"))
	runner.timeout = 50 * time.Millisecond

	err := runner.BeforeWrite(t.Context(), "issue.close")
	if !errors.Is(err, ErrPreWriteRejected) {
		t.Fatalf("BeforeWrite error = %v, want ErrPreWriteRejected", err)
	}
	var rejection *PreWriteError
	if !errors.As(err, &rejection) || rejection.Kind != "timeout" {
		t.Fatalf("rejection = %#v, want timeout", rejection)
	}
}

func writePreWriteHook(t *testing.T, hooksDir, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(hooksDir, HookPreWrite), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
