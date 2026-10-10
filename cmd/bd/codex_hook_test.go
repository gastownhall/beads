package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func stubCodexHookPrime(t *testing.T, fn func(cwd string, memoriesOnly bool) (string, error)) {
	t.Helper()
	orig := codexHookExecPrime
	codexHookExecPrime = func(_ context.Context, cwd string, memoriesOnly bool) (string, error) {
		return fn(cwd, memoriesOnly)
	}
	t.Cleanup(func() { codexHookExecPrime = orig })
}

func TestCodexHookSessionStartInjectsPrimeContext(t *testing.T) {
	stubCodexHookPrime(t, func(cwd string, memoriesOnly bool) (string, error) {
		if cwd != "/repo" {
			t.Fatalf("prime cwd = %q, want hook payload cwd /repo", cwd)
		}
		if memoriesOnly {
			t.Fatal("SessionStart should request full prime output")
		}
		return "BEADS PRIME\nbd ready --json\n", nil
	})

	var out bytes.Buffer
	input := `{"session_id":"s1","cwd":"/repo","hook_event_name":"SessionStart","source":"startup"}`
	if err := runCodexHook(context.Background(), codexHookSessionStart, strings.NewReader(input), &out); err != nil {
		t.Fatalf("runCodexHook: %v", err)
	}

	var got codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if got.HookSpecificOutput.HookEventName != codexHookSessionStart {
		t.Fatalf("hook event = %q", got.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(got.HookSpecificOutput.AdditionalContext, "bd ready --json") {
		t.Fatalf("expected prime output in additionalContext: %#v", got)
	}
}

// TestCodexHookSessionStartRunsPrimeInPayloadCWD is a real hook-process
// regression test for GH#7095: it runs the compiled bd binary as the
// codex-hook subprocess would, with the process CWD in a no-remote
// workspace and the hook payload's cwd pointing at a *different* repository
// that has a remote. Before the fix, codexHookExecPrime ignored the payload
// cwd entirely and primed the process's own (no-remote) workspace; the
// additionalContext would report "No git remote configured for this
// workspace" instead of scoping to the payload repository. Run this test
// against the unpatched base to reproduce that failure, and against the
// patched tree to verify it passes.
func TestCodexHookSessionStartRunsPrimeInPayloadCWD(t *testing.T) {
	binPath := buildBDForInitTests(t)
	processDir := t.TempDir()
	initGitRepoAt(t, processDir)

	initCmd := exec.Command(binPath, "init", "--quiet", "--prefix", "hooktest", "--skip-hooks", "--skip-agents")
	initCmd.Dir = processDir
	initCmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
	)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("initialize process workspace: %v\n%s", err, output)
	}

	workspace := filepath.Join(processDir, "rig")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("create payload workspace: %v", err)
	}
	initGitRepoAt(t, workspace)
	runGitForBootstrapTest(t, workspace, "remote", "add", "origin", "https://example.invalid/acme/rig.git")

	payload, err := json.Marshal(codexHookInput{
		SessionID:     "s1",
		CWD:           workspace,
		HookEventName: codexHookSessionStart,
	})
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}

	hookCmd := exec.Command(binPath, "codex-hook", codexHookSessionStart)
	hookCmd.Dir = processDir
	hookCmd.Stdin = bytes.NewReader(payload)
	hookCmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
	)
	// Stdout and stderr must stay separate: the hook contract requires stdout
	// to be exactly one JSON document (no plain text alongside it), and the
	// ambient environment here can carry deprecation warnings (e.g.
	// BD_OTEL_*) that bd prints to stderr. CombinedOutput would merge those
	// into the JSON this test parses and fail on noise that isn't the bug
	// under test.
	var stdout, stderr bytes.Buffer
	hookCmd.Stdout = &stdout
	hookCmd.Stderr = &stderr
	if err := hookCmd.Run(); err != nil {
		t.Fatalf("run Codex hook: %v\nstderr:\n%s", err, stderr.String())
	}

	var got codexHookResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("parse hook stdout: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve payload workspace: %v", err)
	}
	if !strings.Contains(got.HookSpecificOutput.AdditionalContext, fmt.Sprintf("Git context scope: %q", resolvedWorkspace)) {
		t.Fatalf("prime did not inspect payload cwd %q: %#v", resolvedWorkspace, got)
	}
	if strings.Contains(got.HookSpecificOutput.AdditionalContext, "No git remote configured for this workspace") {
		t.Fatalf("prime used the no-remote process cwd instead of the payload cwd: %#v", got)
	}
}

func TestCodexHookPreCompactWarnsWhenMemoriesUnavailable(t *testing.T) {
	stubCodexHookPrime(t, func(cwd string, memoriesOnly bool) (string, error) {
		if cwd != "/repo" {
			t.Fatalf("prime cwd = %q, want hook payload cwd /repo", cwd)
		}
		if !memoriesOnly {
			t.Fatal("PreCompact should request memories-only prime output")
		}
		return "", errors.New("workspace unavailable")
	})

	var out bytes.Buffer
	input := `{"session_id":"s1","cwd":"/repo","hook_event_name":"PreCompact","trigger":"manual"}`
	if err := runCodexHook(context.Background(), codexHookPreCompact, strings.NewReader(input), &out); err != nil {
		t.Fatalf("runCodexHook: %v", err)
	}

	var got codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if !strings.Contains(got.SystemMessage, "Beads context check failed") {
		t.Fatalf("expected warning systemMessage, got %#v", got)
	}
}

// TestCodexHookSessionStartWithoutPayloadCWDRunsUnscoped documents the
// current, deliberate behavior when a hook payload omits cwd: prime runs
// unscoped (cwd forwarded as "") rather than being rejected. runBdPrimeInDir
// treats "" as "no -C override," not a validation error — only a
// *non-empty* relative/missing/non-directory cwd is rejected (see
// agent_hook_test.go).
func TestCodexHookSessionStartWithoutPayloadCWDRunsUnscoped(t *testing.T) {
	stubCodexHookPrime(t, func(cwd string, memoriesOnly bool) (string, error) {
		if cwd != "" {
			t.Fatalf("prime cwd = %q, want empty cwd when payload omits it", cwd)
		}
		if memoriesOnly {
			t.Fatal("SessionStart should request full prime output")
		}
		return "BEADS PRIME\nbd ready --json\n", nil
	})

	var out bytes.Buffer
	input := `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`
	if err := runCodexHook(context.Background(), codexHookSessionStart, strings.NewReader(input), &out); err != nil {
		t.Fatalf("runCodexHook: %v", err)
	}

	var got codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if !strings.Contains(got.HookSpecificOutput.AdditionalContext, "bd ready --json") {
		t.Fatalf("expected unscoped prime output in additionalContext: %#v", got)
	}
}

func TestCodexHookPostCompactMarksAndUserPromptRefreshesOnce(t *testing.T) {
	dir := t.TempDir()
	codexHookMarkerDirOverride = dir
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })

	wantCWD := filepath.Join("repo", "sub")
	calls := 0
	stubCodexHookPrime(t, func(cwd string, memoriesOnly bool) (string, error) {
		calls++
		if cwd != wantCWD {
			t.Fatalf("prime cwd = %q, want hook payload cwd %q", cwd, wantCWD)
		}
		if memoriesOnly {
			t.Fatal("UserPromptSubmit refresh should request full prime output")
		}
		return "REFRESHED BEADS CONTEXT\n", nil
	})

	input := codexHookInput{SessionID: "s1", CWD: wantCWD, HookEventName: codexHookPostCompact}
	postJSON, _ := json.Marshal(input)
	if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(postJSON), ioDiscard{}); err != nil {
		t.Fatalf("PostCompact hook: %v", err)
	}
	marker := codexHookRefreshMarkerPath(input)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected refresh marker: %v", err)
	}

	input.HookEventName = codexHookUserPromptSubmit
	promptJSON, _ := json.Marshal(input)
	var out bytes.Buffer
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("UserPromptSubmit hook: %v", err)
	}
	if calls != 1 {
		t.Fatalf("prime calls = %d, want 1", calls)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("expected refresh marker removed, stat err=%v", err)
	}
	if !strings.Contains(out.String(), "REFRESHED BEADS CONTEXT") {
		t.Fatalf("expected refreshed context, got %s", out.String())
	}

	out.Reset()
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("second UserPromptSubmit hook: %v", err)
	}
	if calls != 1 {
		t.Fatalf("refresh should run once, prime calls = %d", calls)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no second refresh output, got %s", out.String())
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
