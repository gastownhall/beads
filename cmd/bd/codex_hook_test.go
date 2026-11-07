package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stubCodexHookPrime(t *testing.T, fn func(memoriesOnly bool) (string, error)) {
	t.Helper()
	orig := codexHookExecPrime
	codexHookExecPrime = func(_ context.Context, memoriesOnly bool) (string, error) {
		return fn(memoriesOnly)
	}
	t.Cleanup(func() { codexHookExecPrime = orig })
}

// stubCodexHookPrimeCtx is stubCodexHookPrime for stubs that observe the
// hook's context (deadline expiry).
func stubCodexHookPrimeCtx(t *testing.T, fn func(ctx context.Context, memoriesOnly bool) (string, error)) {
	t.Helper()
	orig := codexHookExecPrime
	codexHookExecPrime = fn
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

// codexHookRefreshSession points the marker dir at a temp dir and returns
// the PostCompact and UserPromptSubmit stdin payloads for one session.
func codexHookRefreshSession(t *testing.T) (codexHookInput, []byte, []byte) {
	t.Helper()
	codexHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })
	input := codexHookInput{SessionID: "s1", CWD: filepath.Join("repo", "sub"), HookEventName: codexHookPostCompact}
	postJSON, _ := json.Marshal(input)
	input.HookEventName = codexHookUserPromptSubmit
	promptJSON, _ := json.Marshal(input)
	return input, postJSON, promptJSON
}

func runCodexHookPayload(t *testing.T, ctx context.Context, event string, payload []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := runCodexHook(ctx, event, bytes.NewReader(payload), &out); err != nil {
		t.Fatalf("%s hook: %v", event, err)
	}
	return out.Bytes()
}

func TestCodexHookUserPromptRefreshFailureIsNotRetried(t *testing.T) {
	input, postJSON, promptJSON := codexHookRefreshSession(t)
	marker := codexHookRefreshMarkerPath(input)

	calls := 0
	stubCodexHookPrime(t, func(bool) (string, error) {
		calls++
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("marker must be consumed before prime runs, stat err=%v", err)
		}
		return "", errors.New("bd prime: signal: killed")
	})

	runCodexHookPayload(t, context.Background(), codexHookPostCompact, postJSON)
	requireCodexHookSystemMessage(t, runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON))
	for i := 0; i < 3; i++ {
		if out := runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON); len(out) != 0 {
			t.Fatalf("prompt %d after failed refresh: want no output, got %s", i+2, out)
		}
	}
	if calls != 1 {
		t.Fatalf("failed refresh retried: prime calls = %d, want 1", calls)
	}
}

func TestCodexHookUserPromptRefreshTimeoutIsNotRetried(t *testing.T) {
	input, postJSON, promptJSON := codexHookRefreshSession(t)
	marker := codexHookRefreshMarkerPath(input)

	calls := 0
	stubCodexHookPrimeCtx(t, func(ctx context.Context, _ bool) (string, error) {
		calls++
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("marker must be consumed before prime runs, stat err=%v", err)
		}
		<-ctx.Done()
		return "", ctx.Err()
	})

	// An already expired parent deadline drives the hook's real context path
	// without waiting out its 30-second timeout.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	runCodexHookPayload(t, context.Background(), codexHookPostCompact, postJSON)
	requireCodexHookSystemMessage(t, runCodexHookPayload(t, expired, codexHookUserPromptSubmit, promptJSON))
	for i := 0; i < 3; i++ {
		if out := runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON); len(out) != 0 {
			t.Fatalf("prompt %d after timed-out refresh: want no output, got %s", i+2, out)
		}
	}
	if calls != 1 {
		t.Fatalf("timed-out refresh retried: prime calls = %d, want 1", calls)
	}
}

func TestCodexHookUserPromptRefreshSkipsUnremovableMarker(t *testing.T) {
	input, _, promptJSON := codexHookRefreshSession(t)
	// A nonempty directory at the marker path cannot be removed, even by root.
	marker := codexHookRefreshMarkerPath(input)
	if err := os.MkdirAll(filepath.Join(marker, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	stubCodexHookPrime(t, func(bool) (string, error) {
		t.Fatal("prime must not run when the marker cannot be consumed")
		return "", nil
	})

	if out := runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON); len(out) != 0 {
		t.Fatalf("want no output, got %s", out)
	}
}

func TestCodexHookUserPromptRefreshKeepsMarkerArmedDuringAttempt(t *testing.T) {
	input, postJSON, promptJSON := codexHookRefreshSession(t)
	marker := codexHookRefreshMarkerPath(input)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	// Marker contents are irrelevant: a legacy or arbitrary payload still arms one refresh.
	if err := os.WriteFile(marker, []byte("legacy marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	stubCodexHookPrime(t, func(bool) (string, error) {
		calls++
		if calls == 1 {
			// A compaction lands while this refresh is still running.
			runCodexHookPayload(t, context.Background(), codexHookPostCompact, postJSON)
		}
		return fmt.Sprintf("REFRESH %d\n", calls), nil
	})

	if out := runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON); !strings.Contains(string(out), "REFRESH 1") {
		t.Fatalf("first refresh output = %s", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker written during the attempt must survive it: %v", err)
	}
	if out := runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON); !strings.Contains(string(out), "REFRESH 2") {
		t.Fatalf("second refresh output = %s", out)
	}
	if calls != 2 {
		t.Fatalf("prime calls = %d, want 2", calls)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func TestCodexHookUserPromptRefreshFailureEmitsValidOutputAndDoesNotRetry(t *testing.T) {
	dir := t.TempDir()
	codexHookMarkerDirOverride = dir
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })

	calls := 0
	stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
		calls++
		return "", context.DeadlineExceeded
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
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if hso, ok := raw["hookSpecificOutput"]; ok {
		t.Errorf("system-message response must omit hookSpecificOutput (Codex requires hookEventName), got %s", hso)
	}

	out.Reset()
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("second UserPromptSubmit hook: %v", err)
	}
	if calls != 1 {
		t.Errorf("failed refresh retried on every prompt: prime calls = %d, want 1", calls)
	}
}

func TestCodexHookUserPromptRefreshRearmsAfterFailure(t *testing.T) {
	_, postJSON, promptJSON := codexHookRefreshSession(t)

	calls := 0
	stubCodexHookPrime(t, func(bool) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("workspace unavailable")
		}
		return "REARMED BEADS CONTEXT\n", nil
	})

	runCodexHookPayload(t, context.Background(), codexHookPostCompact, postJSON)
	requireCodexHookSystemMessage(t, runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON))
	if out := runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON); len(out) != 0 {
		t.Fatalf("ordinary prompt after failed refresh: want no output, got %s", out)
	}

	runCodexHookPayload(t, context.Background(), codexHookPostCompact, postJSON)
	out := runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON)
	raw := decodeCodexHookObject(t, out)
	var hso codexHookSpecificOutput
	if err := json.Unmarshal(raw["hookSpecificOutput"], &hso); err != nil {
		t.Fatalf("re-armed refresh hookSpecificOutput: %v\n%s", err, out)
	}
	if hso.HookEventName != codexHookUserPromptSubmit || hso.AdditionalContext != "REARMED BEADS CONTEXT\n" {
		t.Fatalf("re-armed refresh output = %s", out)
	}
	if out := runCodexHookPayload(t, context.Background(), codexHookUserPromptSubmit, promptJSON); len(out) != 0 {
		t.Fatalf("prompt after re-armed refresh: want no output, got %s", out)
	}
	if calls != 2 {
		t.Fatalf("prime calls = %d, want 2", calls)
	}
}
