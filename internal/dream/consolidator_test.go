package dream

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// fakeAPI is a hand-rolled mock for the messagesNewer interface.
type fakeAPI struct {
	calls   int
	respond func(call int) (*anthropic.Message, error)
}

func (f *fakeAPI) New(_ context.Context, _ anthropic.MessageNewParams, _ ...option.RequestOption) (*anthropic.Message, error) {
	call := f.calls
	f.calls++
	return f.respond(call)
}

// toolUseMessage builds a canned anthropic.Message containing one tool_use
// block whose input is the JSON encoding of plan.
func toolUseMessage(t *testing.T, plan Plan) *anthropic.Message {
	t.Helper()
	input, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	raw := map[string]any{
		"id":   "msg_test",
		"type": "message",
		"role": "assistant",
		"content": []map[string]any{
			{
				"type":  "tool_use",
				"id":    "toolu_test",
				"name":  toolName,
				"input": json.RawMessage(input),
			},
		},
		"model":       "claude-test",
		"stop_reason": "tool_use",
		"usage": map[string]any{
			"input_tokens":  10,
			"output_tokens": 20,
		},
	}
	enc, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var msg anthropic.Message
	if err := json.Unmarshal(enc, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	return &msg
}

func newTestConsolidator(api messagesNewer) *Consolidator {
	return &Consolidator{
		api:            api,
		model:          "claude-test",
		maxRetries:     2,
		initialBackoff: 0,
	}
}

func TestConsolidate_Empty(t *testing.T) {
	c := newTestConsolidator(&fakeAPI{
		respond: func(int) (*anthropic.Message, error) {
			t.Fatalf("API should not be called for empty memories")
			return nil, nil
		},
	})
	got, err := c.Consolidate(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Summary != "no memories" {
		t.Errorf("expected sentinel plan, got %#v", got)
	}
}

func TestConsolidate_Success(t *testing.T) {
	want := Plan{
		Operations: []Operation{
			{Action: ActionForget, Key: "stale-1", Reason: "duplicate of fresh-1"},
		},
		Summary: "forgot 1, kept 2",
	}
	api := &fakeAPI{
		respond: func(int) (*anthropic.Message, error) {
			return toolUseMessage(t, want), nil
		},
	}
	c := newTestConsolidator(api)
	got, err := c.Consolidate(context.Background(), []Memory{
		{Key: "fresh-1", Content: "still relevant"},
		{Key: "stale-1", Content: "still relevant (older note)"},
		{Key: "other", Content: "unrelated"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Summary != want.Summary {
		t.Errorf("summary: got %q want %q", got.Summary, want.Summary)
	}
	if len(got.Operations) != 1 || got.Operations[0].Key != "stale-1" {
		t.Errorf("operations: got %#v", got.Operations)
	}
	if api.calls != 1 {
		t.Errorf("expected 1 API call, got %d", api.calls)
	}
}

func TestConsolidate_UnsafePlanRejected(t *testing.T) {
	// 4 memories, model proposes forgetting 3 → > 50% → reject.
	plan := Plan{
		Operations: []Operation{
			{Action: ActionForget, Key: "a", Reason: "x"},
			{Action: ActionForget, Key: "b", Reason: "x"},
			{Action: ActionForget, Key: "c", Reason: "x"},
		},
		Summary: "forgot 3",
	}
	c := newTestConsolidator(&fakeAPI{
		respond: func(int) (*anthropic.Message, error) {
			return toolUseMessage(t, plan), nil
		},
	})
	_, err := c.Consolidate(context.Background(), []Memory{
		{Key: "a", Content: "1"},
		{Key: "b", Content: "2"},
		{Key: "c", Content: "3"},
		{Key: "d", Content: "4"},
	})
	if !errors.Is(err, ErrUnsafePlan) {
		t.Errorf("expected ErrUnsafePlan, got %v", err)
	}
}

func TestConsolidate_UnsafePlanCountsMergeAbsorbed(t *testing.T) {
	// 4 memories, model proposes merging 3 into 1 → 3 deletions → > 50% → reject.
	plan := Plan{
		Operations: []Operation{
			{Action: ActionMerge, NewKey: "merged", NewContent: "x", AbsorbedKeys: []string{"a", "b", "c"}, Reason: "x"},
		},
		Summary: "merged 3",
	}
	c := newTestConsolidator(&fakeAPI{
		respond: func(int) (*anthropic.Message, error) {
			return toolUseMessage(t, plan), nil
		},
	})
	_, err := c.Consolidate(context.Background(), []Memory{
		{Key: "a", Content: "1"}, {Key: "b", Content: "2"},
		{Key: "c", Content: "3"}, {Key: "d", Content: "4"},
	})
	if !errors.Is(err, ErrUnsafePlan) {
		t.Errorf("expected ErrUnsafePlan, got %v", err)
	}
}

func TestConsolidate_EmptyPlanRejected(t *testing.T) {
	plan := Plan{} // no operations, no summary
	c := newTestConsolidator(&fakeAPI{
		respond: func(int) (*anthropic.Message, error) {
			return toolUseMessage(t, plan), nil
		},
	})
	_, err := c.Consolidate(context.Background(), []Memory{{Key: "a", Content: "x"}})
	if !errors.Is(err, ErrEmptyPlan) {
		t.Errorf("expected ErrEmptyPlan, got %v", err)
	}
}

func TestConsolidate_NoToolUseBlock(t *testing.T) {
	// Message with only a text block — model ignored the tool.
	c := newTestConsolidator(&fakeAPI{
		respond: func(int) (*anthropic.Message, error) {
			raw := map[string]any{
				"id": "x", "type": "message", "role": "assistant",
				"model": "m", "stop_reason": "end_turn",
				"content": []map[string]any{
					{"type": "text", "text": "I refuse to use the tool."},
				},
				"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
			}
			enc, _ := json.Marshal(raw)
			var msg anthropic.Message
			if err := json.Unmarshal(enc, &msg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			return &msg, nil
		},
	})
	_, err := c.Consolidate(context.Background(), []Memory{{Key: "a", Content: "x"}})
	if err == nil || !strings.Contains(err.Error(), "no consolidate_memories tool_use") {
		t.Errorf("expected no-tool-use error, got %v", err)
	}
}

// stubAPIError implements anthropic.Error semantics for retry tests.
type stubAPIError struct{ status int }

func (e *stubAPIError) Error() string { return "stub api error" }

func TestIsRetryable(t *testing.T) {
	if isRetryable(nil) {
		t.Errorf("nil should not be retryable")
	}
	if isRetryable(context.Canceled) {
		t.Errorf("context.Canceled should not be retryable")
	}
	if isRetryable(context.DeadlineExceeded) {
		t.Errorf("context.DeadlineExceeded should not be retryable")
	}
	// We do not synthesize an *anthropic.Error here since the SDK's struct
	// has unexported fields; coverage of the retry branch is exercised by
	// the integration test suite.
}

func TestSafetyCheck_BoundaryAllowed(t *testing.T) {
	// 4 memories, 2 deletions → exactly 50% → allowed.
	p := &Plan{
		Operations: []Operation{
			{Action: ActionForget, Key: "a"},
			{Action: ActionForget, Key: "b"},
		},
	}
	if err := safetyCheck(p, 4); err != nil {
		t.Errorf("expected 50%% to be allowed, got %v", err)
	}
}

func TestSafetyCheck_ZeroMemoriesNoOp(t *testing.T) {
	if err := safetyCheck(&Plan{}, 0); err != nil {
		t.Errorf("zero memories should not trip safety check: %v", err)
	}
}
