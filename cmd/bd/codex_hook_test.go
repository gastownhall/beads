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

	msg := requireCodexHookSystemMessage(t, out.Bytes())
	if !strings.Contains(msg, "Beads context check failed") {
		t.Fatalf("expected warning systemMessage, got %q", msg)
	}
}

// decodeCodexHookObject decodes hook stdout as exactly one JSON object and
// returns its raw wire keys, so a test sees fields the response type would
// hide (an emitted empty hookSpecificOutput decodes fine into the struct).
func decodeCodexHookObject(t *testing.T, out []byte) map[string]json.RawMessage {
	t.Helper()
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		t.Fatalf("hook output is not a JSON object: %q", out)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		t.Fatalf("parse hook output: %v\n%s", err, out)
	}
	return raw
}

// requireCodexHookSystemMessage asserts the warning response shape Codex
// accepts for every event: continue:true plus a nonempty systemMessage and no
// hookSpecificOutput key (an event-specific object must name hookEventName).
func requireCodexHookSystemMessage(t *testing.T, out []byte) string {
	t.Helper()
	raw := decodeCodexHookObject(t, out)
	if hso, ok := raw["hookSpecificOutput"]; ok {
		t.Fatalf("warning response must omit hookSpecificOutput, got %s in %s", hso, out)
	}
	if len(raw) != 2 {
		t.Fatalf("warning response keys = %d, want continue and systemMessage only: %s", len(raw), out)
	}
	var cont bool
	if err := json.Unmarshal(raw["continue"], &cont); err != nil || !cont {
		t.Fatalf("warning response continue = %s (err %v), want true", raw["continue"], err)
	}
	var msg string
	if err := json.Unmarshal(raw["systemMessage"], &msg); err != nil || strings.TrimSpace(msg) == "" {
		t.Fatalf("warning response systemMessage = %s (err %v), want nonempty string", raw["systemMessage"], err)
	}
	return msg
}

func TestCodexHookSystemMessageOmitsHookSpecificOutput(t *testing.T) {
	var out bytes.Buffer
	if err := writeCodexHookSystemMessage(&out, "Beads warning"); err != nil {
		t.Fatalf("writeCodexHookSystemMessage: %v", err)
	}
	if msg := requireCodexHookSystemMessage(t, out.Bytes()); msg != "Beads warning" {
		t.Fatalf("systemMessage = %q", msg)
	}
}

func TestCodexHookUserPromptRefreshFailureEmitsValidWarning(t *testing.T) {
	codexHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })
	stubCodexHookPrime(t, func(bool) (string, error) {
		return "", errors.New("bd prime: signal: killed")
	})

	input := codexHookInput{SessionID: "s1", CWD: "repo", HookEventName: codexHookPostCompact}
	postJSON, _ := json.Marshal(input)
	if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(postJSON), ioDiscard{}); err != nil {
		t.Fatalf("PostCompact hook: %v", err)
	}
	input.HookEventName = codexHookUserPromptSubmit
	promptJSON, _ := json.Marshal(input)
	var out bytes.Buffer
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("UserPromptSubmit hook: %v", err)
	}
	msg := requireCodexHookSystemMessage(t, out.Bytes())
	if !strings.Contains(msg, "Beads context refresh after compaction failed") {
		t.Fatalf("expected refresh warning, got %q", msg)
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
