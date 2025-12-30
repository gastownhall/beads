package compact

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/steveyegge/beads/internal/types"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestNewHaikuClient_RequiresAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := NewHaikuClient("")
	if err == nil {
		t.Fatal("expected error when API key is missing")
	}
	if !errors.Is(err, ErrAPIKeyRequired) {
		t.Fatalf("expected ErrAPIKeyRequired, got %v", err)
	}
	if !strings.Contains(err.Error(), "API key required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewHaikuClient_EnvVarUsedWhenNoExplicitKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key-from-env")

	client, err := NewHaikuClient("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewHaikuClient_EnvVarOverridesExplicitKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key-from-env")

	client, err := NewHaikuClient("test-key-explicit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestRenderTier1Prompt(t *testing.T) {
	client, err := NewHaikuClient("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	issue := &types.Issue{
		ID:                 "bd-1",
		Title:              "Fix authentication bug",
		Description:        "Users can't log in with OAuth",
		Design:             "Add error handling to OAuth flow",
		AcceptanceCriteria: "Users can log in successfully",
		Notes:              "Related to issue bd-2",
		Status:             types.StatusClosed,
	}

	prompt, err := client.renderTier1Prompt(issue)
	if err != nil {
		t.Fatalf("failed to render prompt: %v", err)
	}

	if !strings.Contains(prompt, "Fix authentication bug") {
		t.Error("prompt should contain title")
	}
	if !strings.Contains(prompt, "Users can't log in with OAuth") {
		t.Error("prompt should contain description")
	}
	if !strings.Contains(prompt, "Add error handling to OAuth flow") {
		t.Error("prompt should contain design")
	}
	if !strings.Contains(prompt, "Users can log in successfully") {
		t.Error("prompt should contain acceptance criteria")
	}
	if !strings.Contains(prompt, "Related to issue bd-2") {
		t.Error("prompt should contain notes")
	}
	if !strings.Contains(prompt, "**Summary:**") {
		t.Error("prompt should contain format instructions")
	}
}

func TestRenderTier1Prompt_HandlesEmptyFields(t *testing.T) {
	client, err := NewHaikuClient("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	issue := &types.Issue{
		ID:          "bd-1",
		Title:       "Simple task",
		Description: "Just a simple task",
		Status:      types.StatusClosed,
	}

	prompt, err := client.renderTier1Prompt(issue)
	if err != nil {
		t.Fatalf("failed to render prompt: %v", err)
	}

	if !strings.Contains(prompt, "Simple task") {
		t.Error("prompt should contain title")
	}
	if !strings.Contains(prompt, "Just a simple task") {
		t.Error("prompt should contain description")
	}
}

func TestRenderTier1Prompt_UTF8(t *testing.T) {
	client, err := NewHaikuClient("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	issue := &types.Issue{
		ID:          "bd-1",
		Title:       "Fix bug with émojis 🎉",
		Description: "Handle UTF-8: café, 日本語, emoji 🚀",
		Status:      types.StatusClosed,
	}

	prompt, err := client.renderTier1Prompt(issue)
	if err != nil {
		t.Fatalf("failed to render prompt: %v", err)
	}

	if !strings.Contains(prompt, "🎉") {
		t.Error("prompt should preserve emoji in title")
	}
	if !strings.Contains(prompt, "café") {
		t.Error("prompt should preserve accented characters")
	}
	if !strings.Contains(prompt, "日本語") {
		t.Error("prompt should preserve unicode characters")
	}
	if !strings.Contains(prompt, "🚀") {
		t.Error("prompt should preserve emoji in description")
	}
}

func TestCallWithRetry_ContextCancellation(t *testing.T) {
	client, err := NewHaikuClient("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client.initialBackoff = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = client.callWithRetry(ctx, "test prompt")
	if err == nil {
		t.Fatal("expected error when context is canceled")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled error, got: %v", err)
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"context canceled", context.Canceled, false},
		{"context deadline exceeded", context.DeadlineExceeded, false},
		{"generic error", errors.New("some error"), false},
		{"timeout error", timeoutErr{}, true},
		{"anthropic 429", &anthropic.Error{StatusCode: 429}, true},
		{"anthropic 500", &anthropic.Error{StatusCode: 500}, true},
		{"anthropic 400", &anthropic.Error{StatusCode: 400}, false},
		{"wrapped timeout", fmt.Errorf("wrap: %w", timeoutErr{}), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryable(tt.err)
			if got != tt.expected {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestBytesWriterAppends(t *testing.T) {
	w := &bytesWriter{}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	if _, err := w.Write([]byte(" world")); err != nil {
		t.Fatalf("second write failed: %v", err)
	}
	if got := string(w.buf); got != "hello world" {
		t.Fatalf("unexpected buffer content: %q", got)
	}
}
