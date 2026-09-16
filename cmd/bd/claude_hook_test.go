package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeHookUserPromptSubmitRecordsSecretFreeReceipt(t *testing.T) {
	origID := steeringOwningBeadID
	origList := steeringCommentLister
	origAdd := steeringCommentAdder
	t.Cleanup(func() {
		steeringOwningBeadID = origID
		steeringCommentLister = origList
		steeringCommentAdder = origAdd
	})

	steeringOwningBeadID = func() string { return "bd-own" }
	var added []string
	steeringCommentLister = func(context.Context, string) ([]string, error) { return append([]string{}, added...), nil }
	steeringCommentAdder = func(_ context.Context, _ string, text string) error {
		if strings.Contains(text, "SECRET-TOKEN") {
			t.Fatalf("raw prompt leaked: %s", text)
		}
		added = append(added, text)
		return nil
	}

	var out bytes.Buffer
	input := `{"hook_event_name":"UserPromptSubmit","session_id":"sess-1","prompt":"do not store SECRET-TOKEN"}`
	if err := runClaudeHook(context.Background(), claudeHookUserPromptSubmit, strings.NewReader(input), &out); err != nil {
		t.Fatalf("UserPromptSubmit: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("parse output: %v\n%s", err, out.String())
	}
	if len(added) != 1 {
		t.Fatalf("added %d receipts, want 1", len(added))
	}
	if !strings.Contains(added[0], "provider: claude") {
		t.Fatalf("receipt missing provider:\n%s", added[0])
	}
	if !strings.Contains(added[0], "thread_id: sess-1") {
		t.Fatalf("receipt missing thread:\n%s", added[0])
	}
}

func TestClaudeHookUnsupportedEvent(t *testing.T) {
	if err := runClaudeHook(context.Background(), "SessionStart", strings.NewReader(`{}`), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for unsupported event")
	}
}
