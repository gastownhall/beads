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
	"time"
	"unicode/utf8"
)

func stubCodexHookPrime(t *testing.T, fn func(memoriesOnly bool) (string, error)) {
	t.Helper()
	orig := codexHookExecPrime
	codexHookExecPrime = func(_ context.Context, memoriesOnly bool) (string, error) {
		return fn(memoriesOnly)
	}
	t.Cleanup(func() { codexHookExecPrime = orig })
}

// decodeCodexHookObject decodes hook stdout as exactly one JSON object.
// Raw-message decoding, rather than unmarshalling codexHookResponse alone, is
// what catches an always-emitted empty hookSpecificOutput: the struct decoder
// cannot see a key the encoder was supposed to omit (gh#6039).
func decodeCodexHookObject(t *testing.T, out []byte) map[string]json.RawMessage {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("hook output must be exactly one JSON object: %v\n%s", err, out)
	}
	if raw == nil {
		t.Fatalf("hook output must be a JSON object, not null: %s", out)
	}
	return raw
}

// assertCodexHookOmitsHookSpecificOutput guards the wire contract Codex
// enforces: a hookSpecificOutput object without hookEventName is rejected, so
// system-message-only responses must not carry the key at all.
func assertCodexHookOmitsHookSpecificOutput(t *testing.T, raw map[string]json.RawMessage) {
	t.Helper()
	if hso, ok := raw["hookSpecificOutput"]; ok {
		t.Errorf("system-message response must omit hookSpecificOutput (Codex requires hookEventName inside it), got %s", hso)
	}
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

	raw := decodeCodexHookObject(t, out.Bytes())
	if _, ok := raw["systemMessage"]; ok {
		t.Errorf("success response must not carry a systemMessage, got %s", raw["systemMessage"])
	}
	var got codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if got.HookSpecificOutput == nil {
		t.Fatalf("success response must keep hookSpecificOutput, got %s", out.String())
	}
	if got.HookSpecificOutput.HookEventName != codexHookSessionStart {
		t.Fatalf("hook event = %q", got.HookSpecificOutput.HookEventName)
	}
	if got.HookSpecificOutput.AdditionalContext != "BEADS PRIME\nbd ready --json\n" {
		t.Fatalf("additionalContext must be the unchanged prime output, got %#v", got.HookSpecificOutput.AdditionalContext)
	}
	var hsoFields map[string]json.RawMessage
	if err := json.Unmarshal(raw["hookSpecificOutput"], &hsoFields); err != nil || hsoFields == nil {
		t.Fatalf("hookSpecificOutput must be a JSON object: %v (%s)", err, raw["hookSpecificOutput"])
	}
	if len(hsoFields) != 2 {
		t.Errorf("hookSpecificOutput keys = %v, want exactly hookEventName and additionalContext", hsoFields)
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

	raw := decodeCodexHookObject(t, out.Bytes())
	assertCodexHookOmitsHookSpecificOutput(t, raw)
	if len(raw) != 2 {
		t.Errorf("warning response keys = %v, want exactly continue and systemMessage", raw)
	}
	var cont bool
	if err := json.Unmarshal(raw["continue"], &cont); err != nil || !cont {
		t.Errorf("continue = %s, want true", raw["continue"])
	}
}

func TestCodexHookSystemMessageOmitsHookSpecificOutput(t *testing.T) {
	var out bytes.Buffer
	if err := writeCodexHookSystemMessage(&out, "Beads context check failed before compaction: workspace unavailable"); err != nil {
		t.Fatalf("writeCodexHookSystemMessage: %v", err)
	}
	raw := decodeCodexHookObject(t, out.Bytes())
	assertCodexHookOmitsHookSpecificOutput(t, raw)
	if len(raw) != 2 {
		t.Errorf("system-message keys = %v, want exactly continue and systemMessage", raw)
	}
	var cont bool
	if err := json.Unmarshal(raw["continue"], &cont); err != nil || !cont {
		t.Errorf("continue = %s, want true", raw["continue"])
	}
	var msg string
	if err := json.Unmarshal(raw["systemMessage"], &msg); err != nil || msg == "" {
		t.Errorf("systemMessage = %s, want nonempty", raw["systemMessage"])
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
	raw := decodeCodexHookObject(t, out.Bytes())
	if _, ok := raw["systemMessage"]; ok {
		t.Errorf("refresh success must not carry a systemMessage, got %s", raw["systemMessage"])
	}
	var got codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse refresh output: %v\n%s", err, out.String())
	}
	if got.HookSpecificOutput == nil {
		t.Fatalf("refresh success must keep hookSpecificOutput, got %s", out.String())
	}
	if got.HookSpecificOutput.HookEventName != codexHookUserPromptSubmit {
		t.Errorf("hook event = %q, want %s", got.HookSpecificOutput.HookEventName, codexHookUserPromptSubmit)
	}
	if got.HookSpecificOutput.AdditionalContext != "REFRESHED BEADS CONTEXT\n" {
		t.Errorf("additionalContext must be the unchanged prime output, got %#v", got.HookSpecificOutput.AdditionalContext)
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

func TestCodexHookUserPromptRefreshTimeoutIsNotRetried(t *testing.T) {
	dir := t.TempDir()
	codexHookMarkerDirOverride = dir
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })

	input := codexHookInput{SessionID: "s1", CWD: "repo", HookEventName: codexHookPostCompact}
	postJSON, _ := json.Marshal(input)
	if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(postJSON), ioDiscard{}); err != nil {
		t.Fatalf("PostCompact hook: %v", err)
	}
	marker := codexHookRefreshMarkerPath(input)

	calls := 0
	orig := codexHookExecPrime
	codexHookExecPrime = func(ctx context.Context, memoriesOnly bool) (string, error) {
		calls++
		if memoriesOnly {
			t.Error("UserPromptSubmit refresh should request full prime output")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("refresh marker must be consumed before prime runs, stat err=%v", err)
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	t.Cleanup(func() { codexHookExecPrime = orig })

	// An already-expired parent deadline exercises the real cancellation path
	// without waiting out the hook's own 30-second timeout.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	input.HookEventName = codexHookUserPromptSubmit
	promptJSON, _ := json.Marshal(input)
	var out bytes.Buffer
	if err := runCodexHook(expired, codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("UserPromptSubmit hook: %v", err)
	}
	raw := decodeCodexHookObject(t, out.Bytes())
	assertCodexHookOmitsHookSpecificOutput(t, raw)

	out.Reset()
	if err := runCodexHook(expired, codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("second UserPromptSubmit hook: %v", err)
	}
	if calls != 1 {
		t.Errorf("timed-out refresh retried on every prompt: prime calls = %d, want 1", calls)
	}
	if out.Len() != 0 {
		t.Errorf("later prompts must stay silent, got %s", out.String())
	}
}

func TestCodexHookRefreshMarkerEdgeCases(t *testing.T) {
	input := codexHookInput{SessionID: "s1", CWD: "repo", HookEventName: codexHookUserPromptSubmit}
	promptJSON, _ := json.Marshal(input)

	t.Run("absent marker never runs prime", func(t *testing.T) {
		codexHookMarkerDirOverride = t.TempDir()
		t.Cleanup(func() { codexHookMarkerDirOverride = "" })

		stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
			t.Error("prime must not run without a refresh marker")
			return "", nil
		})

		var out bytes.Buffer
		if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
			t.Fatalf("UserPromptSubmit hook: %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("expected no output without a marker, got %s", out.String())
		}
	})

	t.Run("legacy marker content still refreshes", func(t *testing.T) {
		codexHookMarkerDirOverride = t.TempDir()
		t.Cleanup(func() { codexHookMarkerDirOverride = "" })

		calls := 0
		stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
			calls++
			return "REFRESHED\n", nil
		})
		// Only marker presence matters; arbitrary legacy bytes must not block
		// the refresh.
		marker := codexHookRefreshMarkerPath(input)
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			t.Fatalf("mkdir marker dir: %v", err)
		}
		if err := os.WriteFile(marker, []byte("legacy-bytes"), 0o600); err != nil {
			t.Fatalf("seed marker: %v", err)
		}

		var out bytes.Buffer
		if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
			t.Fatalf("UserPromptSubmit hook: %v", err)
		}
		if calls != 1 || !strings.Contains(out.String(), "REFRESHED") {
			t.Fatalf("expected one refresh with injected context, calls=%d out=%s", calls, out.String())
		}
	})

	t.Run("unremovable marker is a silent no-op", func(t *testing.T) {
		codexHookMarkerDirOverride = t.TempDir()
		t.Cleanup(func() { codexHookMarkerDirOverride = "" })

		stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
			t.Error("prime must not run when the marker cannot be consumed")
			return "", nil
		})
		// A nonempty directory at the marker path makes removal fail without
		// depending on root-level permissions.
		marker := codexHookRefreshMarkerPath(input)
		if err := os.MkdirAll(marker, 0o700); err != nil {
			t.Fatalf("mkdir marker dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(marker, "child"), []byte("x"), 0o600); err != nil {
			t.Fatalf("seed marker dir child: %v", err)
		}

		var out bytes.Buffer
		if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
			t.Fatalf("UserPromptSubmit hook: %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("unremovable marker must be a silent no-op, got %s", out.String())
		}
	})
}

func TestCodexHookUserPromptRefreshRearmsAfterFailure(t *testing.T) {
	dir := t.TempDir()
	codexHookMarkerDirOverride = dir
	t.Cleanup(func() { codexHookMarkerDirOverride = "" })

	calls := 0
	stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
		calls++
		if memoriesOnly {
			t.Fatal("UserPromptSubmit refresh should request full prime output")
		}
		if calls == 1 {
			return "", errors.New("workspace unavailable")
		}
		return "REARMED BEADS CONTEXT\n", nil
	})

	postJSON, _ := json.Marshal(codexHookInput{SessionID: "s1", CWD: "repo", HookEventName: codexHookPostCompact})
	promptJSON, _ := json.Marshal(codexHookInput{SessionID: "s1", CWD: "repo", HookEventName: codexHookUserPromptSubmit})

	if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(postJSON), ioDiscard{}); err != nil {
		t.Fatalf("PostCompact hook: %v", err)
	}

	var out bytes.Buffer
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("first UserPromptSubmit hook: %v", err)
	}
	raw := decodeCodexHookObject(t, out.Bytes())
	assertCodexHookOmitsHookSpecificOutput(t, raw)
	var msg string
	if err := json.Unmarshal(raw["systemMessage"], &msg); err != nil || !strings.Contains(msg, "refresh after compaction failed") {
		t.Fatalf("expected refresh failure warning, got %s", out.String())
	}

	out.Reset()
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("second UserPromptSubmit hook: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("ordinary prompt after a failed refresh must stay silent, got %s", out.String())
	}
	if calls != 1 {
		t.Fatalf("failed refresh must not be retried without a new PostCompact, prime calls = %d, want 1", calls)
	}

	if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(postJSON), ioDiscard{}); err != nil {
		t.Fatalf("re-arming PostCompact hook: %v", err)
	}
	out.Reset()
	if err := runCodexHook(context.Background(), codexHookUserPromptSubmit, bytes.NewReader(promptJSON), &out); err != nil {
		t.Fatalf("third UserPromptSubmit hook: %v", err)
	}
	if calls != 2 {
		t.Fatalf("re-armed refresh must run prime exactly once, prime calls = %d, want 2", calls)
	}
	raw = decodeCodexHookObject(t, out.Bytes())
	if _, ok := raw["hookSpecificOutput"]; !ok {
		t.Fatalf("re-armed refresh success must inject context, got %s", out.String())
	}
	var got codexHookResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if got.HookSpecificOutput.HookEventName != codexHookUserPromptSubmit || !strings.Contains(got.HookSpecificOutput.AdditionalContext, "REARMED BEADS CONTEXT") {
		t.Fatalf("expected re-armed context injection, got %s", out.String())
	}
}

func TestCodexHookFailureMessagesAreBounded(t *testing.T) {
	const secondLine = "SECOND LINE DIAGNOSTIC MUST NOT LEAK"
	longFirstLine := strings.Repeat("é", 300)
	const recovery = "Run bd prime to reload context; automatic refresh will retry after the next compaction"

	cases := []struct {
		name          string
		refresh       bool // false: PreCompact check; true: marked UserPromptSubmit refresh
		expiredParent bool
		primeErr      error // used when expiredParent is false
		wantContains  []string
	}{
		{
			name:         "precompact multiline error collapses to first line",
			primeErr:     errors.New("workspace unavailable\n" + secondLine + "\nthird line"),
			wantContains: []string{"Beads context check failed before compaction: workspace unavailable"},
		},
		{
			name:         "precompact long unicode first line is capped",
			primeErr:     errors.New(longFirstLine + "\n" + secondLine),
			wantContains: []string{"Beads context check failed before compaction: "},
		},
		{
			name:          "precompact deadline explains the hook deadline",
			expiredParent: true,
			wantContains:  []string{"Beads context check failed before compaction: bd prime exceeded the hook deadline"},
		},
		{
			name:         "refresh multiline error collapses and names recovery",
			refresh:      true,
			primeErr:     errors.New("prime failed\n" + secondLine),
			wantContains: []string{"Beads context refresh after compaction failed: prime failed", recovery},
		},
		{
			name:          "refresh deadline explains the hook deadline and names recovery",
			refresh:       true,
			expiredParent: true,
			wantContains:  []string{"Beads context refresh after compaction failed: bd prime exceeded the hook deadline", recovery},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codexHookMarkerDirOverride = t.TempDir()
			t.Cleanup(func() { codexHookMarkerDirOverride = "" })

			if tc.expiredParent {
				orig := codexHookExecPrime
				codexHookExecPrime = func(ctx context.Context, memoriesOnly bool) (string, error) {
					<-ctx.Done()
					return "", ctx.Err()
				}
				t.Cleanup(func() { codexHookExecPrime = orig })
			} else {
				stubCodexHookPrime(t, func(memoriesOnly bool) (string, error) {
					return "", tc.primeErr
				})
			}

			event := codexHookPreCompact
			if tc.refresh {
				seed, _ := json.Marshal(codexHookInput{SessionID: "s1", CWD: "repo", HookEventName: codexHookPostCompact})
				if err := runCodexHook(context.Background(), codexHookPostCompact, bytes.NewReader(seed), ioDiscard{}); err != nil {
					t.Fatalf("PostCompact hook: %v", err)
				}
				event = codexHookUserPromptSubmit
			}
			parent := context.Background()
			if tc.expiredParent {
				expired, cancel := context.WithDeadline(parent, time.Now().Add(-time.Second))
				defer cancel()
				parent = expired
			}
			in, _ := json.Marshal(codexHookInput{SessionID: "s1", CWD: "repo", HookEventName: event})

			var out bytes.Buffer
			if err := runCodexHook(parent, event, bytes.NewReader(in), &out); err != nil {
				t.Fatalf("%s hook: %v", event, err)
			}
			raw := decodeCodexHookObject(t, out.Bytes())
			assertCodexHookOmitsHookSpecificOutput(t, raw)
			var msg string
			if err := json.Unmarshal(raw["systemMessage"], &msg); err != nil || msg == "" {
				t.Fatalf("systemMessage = %s, want nonempty", raw["systemMessage"])
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(msg, want) {
					t.Errorf("systemMessage missing %q: %s", want, msg)
				}
			}
			if strings.Contains(msg, secondLine) {
				t.Errorf("systemMessage leaked later error lines: %s", msg)
			}
			if strings.ContainsAny(msg, "\n\r") {
				t.Errorf("systemMessage must be a single line: %q", msg)
			}
			if utf8.RuneCountInString(msg) > 600 {
				t.Errorf("systemMessage is not bounded: %d runes", utf8.RuneCountInString(msg))
			}
			if !tc.refresh && strings.Contains(msg, "Run bd prime") {
				t.Errorf("precompact warning should not carry refresh recovery instructions: %s", msg)
			}
		})
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
