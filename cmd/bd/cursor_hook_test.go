package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func stubCursorHookPrime(t *testing.T, fn func() (string, error)) {
	t.Helper()
	orig := cursorHookExecPrime
	cursorHookExecPrime = func(_ context.Context) (string, error) { return fn() }
	t.Cleanup(func() { cursorHookExecPrime = orig })
}

func TestCursorHookSessionStartInjectsPrime(t *testing.T) {
	cursorHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { cursorHookMarkerDirOverride = "" })

	stubCursorHookPrime(t, func() (string, error) {
		return "BEADS PRIME\nbd ready --json\n", nil
	})

	var out bytes.Buffer
	input := `{"session_id":"s1","conversation_id":"s1","hook_event_name":"sessionStart","composer_mode":"agent","workspace_roots":["/repo"]}`
	if err := runCursorHook(context.Background(), cursorHookSessionStart, strings.NewReader(input), &out); err != nil {
		t.Fatalf("runCursorHook: %v", err)
	}

	var got cursorHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if got.Continue == nil || !*got.Continue {
		t.Fatalf("expected continue=true, got %#v", got)
	}
	if !strings.Contains(got.AdditionalContext, "bd ready --json") {
		t.Fatalf("expected prime output in additional_context: %#v", got)
	}
}

func TestCursorHookSessionStartEmptyPrimeStillValid(t *testing.T) {
	cursorHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { cursorHookMarkerDirOverride = "" })
	stubCursorHookPrime(t, func() (string, error) { return "", nil })

	var out bytes.Buffer
	input := `{"session_id":"s1","hook_event_name":"sessionStart"}`
	if err := runCursorHook(context.Background(), cursorHookSessionStart, strings.NewReader(input), &out); err != nil {
		t.Fatalf("runCursorHook: %v", err)
	}
	var got cursorHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if got.Continue == nil || !*got.Continue {
		t.Fatalf("expected continue=true even when prime empty: %#v", got)
	}
	if got.AdditionalContext != "" {
		t.Fatalf("expected empty additional_context, got %q", got.AdditionalContext)
	}
}

func TestCursorHookSessionStartPrimeErrorStillContinues(t *testing.T) {
	cursorHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { cursorHookMarkerDirOverride = "" })
	stubCursorHookPrime(t, func() (string, error) { return "", errors.New("workspace unavailable") })

	var out bytes.Buffer
	input := `{"hook_event_name":"sessionStart","session_id":"s9"}`
	if err := runCursorHook(context.Background(), cursorHookSessionStart, strings.NewReader(input), &out); err != nil {
		t.Fatalf("runCursorHook: %v", err)
	}
	var got cursorHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if got.Continue == nil || !*got.Continue {
		t.Fatalf("expected continue=true on prime error, got %#v", got)
	}
}

func TestCursorHookPreCompactThenPostToolRefreshesOnce(t *testing.T) {
	cursorHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { cursorHookMarkerDirOverride = "" })

	calls := 0
	stubCursorHookPrime(t, func() (string, error) {
		calls++
		return "REFRESHED BEADS CONTEXT\n", nil
	})

	// preCompact arms the one-shot marker and emits a user message.
	preInput := `{"hook_event_name":"preCompact","conversation_id":"c1","workspace_roots":["/repo"],"trigger":"auto"}`
	var preOut bytes.Buffer
	if err := runCursorHook(context.Background(), cursorHookPreCompact, strings.NewReader(preInput), &preOut); err != nil {
		t.Fatalf("preCompact: %v", err)
	}
	var preResp cursorHookResponse
	if err := json.Unmarshal(preOut.Bytes(), &preResp); err != nil {
		t.Fatalf("parse preCompact: %v", err)
	}
	if preResp.UserMessage == "" {
		t.Fatalf("expected preCompact user_message, got %#v", preResp)
	}
	marker := cursorHookRefreshMarkerPath(cursorHookInput{ConversationID: "c1", WorkspaceRoots: []string{"/repo"}})
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected refresh marker after preCompact: %v", err)
	}

	// First postToolUse after compaction re-injects context and clears marker.
	toolInput := `{"hook_event_name":"postToolUse","conversation_id":"c1","workspace_roots":["/repo"],"tool_name":"Shell"}`
	var toolOut bytes.Buffer
	if err := runCursorHook(context.Background(), cursorHookPostToolUse, strings.NewReader(toolInput), &toolOut); err != nil {
		t.Fatalf("postToolUse: %v", err)
	}
	var toolResp cursorHookResponse
	if err := json.Unmarshal(toolOut.Bytes(), &toolResp); err != nil {
		t.Fatalf("parse postToolUse: %v", err)
	}
	if !strings.Contains(toolResp.AdditionalContext, "REFRESHED BEADS CONTEXT") {
		t.Fatalf("expected refreshed context in additional_context, got %#v", toolResp)
	}
	if toolResp.Continue == nil || !*toolResp.Continue {
		t.Fatalf("expected continue=true on post-compaction refresh, got %#v", toolResp)
	}
	if calls != 1 {
		t.Fatalf("prime calls = %d, want 1", calls)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("expected marker removed after refresh, stat err=%v", err)
	}

	// Second postToolUse is a no-op.
	toolOut.Reset()
	if err := runCursorHook(context.Background(), cursorHookPostToolUse, strings.NewReader(toolInput), &toolOut); err != nil {
		t.Fatalf("second postToolUse: %v", err)
	}
	if calls != 1 {
		t.Fatalf("refresh should run once, prime calls = %d", calls)
	}
	if strings.TrimSpace(toolOut.String()) != "{}" {
		t.Fatalf("expected {} no-op on second postToolUse, got %q", toolOut.String())
	}
}

func TestCursorHookPostToolUseNoMarkerIsNoop(t *testing.T) {
	cursorHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { cursorHookMarkerDirOverride = "" })
	stubCursorHookPrime(t, func() (string, error) {
		t.Fatal("prime must not run without an armed marker")
		return "", nil
	})

	var out bytes.Buffer
	input := `{"hook_event_name":"postToolUse","conversation_id":"c2","workspace_roots":["/repo"],"tool_name":"Read"}`
	if err := runCursorHook(context.Background(), cursorHookPostToolUse, strings.NewReader(input), &out); err != nil {
		t.Fatalf("postToolUse: %v", err)
	}
	if strings.TrimSpace(out.String()) != "{}" {
		t.Fatalf("expected {} no-op, got %q", out.String())
	}
}

func TestCursorHookSessionStartClearsStaleMarker(t *testing.T) {
	cursorHookMarkerDirOverride = t.TempDir()
	t.Cleanup(func() { cursorHookMarkerDirOverride = "" })
	stubCursorHookPrime(t, func() (string, error) { return "ctx", nil })

	// Arm a marker via preCompact, then a new sessionStart for the same
	// conversation should clear it so we don't double-inject.
	in := `{"conversation_id":"c3","workspace_roots":["/repo"]}`
	if err := runCursorHook(context.Background(), cursorHookPreCompact, strings.NewReader(in), &bytes.Buffer{}); err != nil {
		t.Fatalf("preCompact: %v", err)
	}
	marker := cursorHookRefreshMarkerPath(cursorHookInput{ConversationID: "c3", WorkspaceRoots: []string{"/repo"}})
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected marker: %v", err)
	}
	start := `{"hook_event_name":"sessionStart","session_id":"c3","conversation_id":"c3","workspace_roots":["/repo"]}`
	if err := runCursorHook(context.Background(), cursorHookSessionStart, strings.NewReader(start), &bytes.Buffer{}); err != nil {
		t.Fatalf("sessionStart: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("expected sessionStart to clear stale marker, stat err=%v", err)
	}
}

func TestCursorHookUnsupportedEvent(t *testing.T) {
	var out bytes.Buffer
	input := `{"hook_event_name":"sessionEnd"}`
	if err := runCursorHook(context.Background(), "sessionEnd", strings.NewReader(input), &out); err == nil {
		t.Fatal("expected error for unsupported event")
	}
}
