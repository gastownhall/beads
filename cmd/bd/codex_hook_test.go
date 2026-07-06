package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubCodexHookPrime(t *testing.T, fn func(memoriesOnly bool) (string, error)) {
	t.Helper()
	orig := codexHookExecPrime
	codexHookExecPrime = func(_ context.Context, memoriesOnly bool) (string, error) {
		return fn(memoriesOnly)
	}
	t.Cleanup(func() { codexHookExecPrime = orig })
}

func TestCodexHookSessionStartInjectsPrimeContext(t *testing.T) {
	stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
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

func TestCodexHookPreCompactWarnsWhenMemoriesUnavailable(t *testing.T) {
	stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
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

func TestCodexHookPostCompactMarksAndUserPromptRefreshesOnce(t *testing.T) {
	dir := t.TempDir()
	codexHookMarkerDirOverride = dir
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })

	calls := 0
	stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
		calls++
		if memoriesOnly {
			t.Fatal("UserPromptSubmit refresh should request full prime output")
		}
		return "REFRESHED BEADS CONTEXT\n", nil
	})

	input := codexHookInput{SessionID: "s1", CWD: filepath.Join("repo", "sub"), HookEventName: codexHookPostCompact}
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
