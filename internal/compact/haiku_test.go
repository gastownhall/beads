package compact

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/steveyegge/beads/internal/types"
)

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

// mockTimeoutError implements net.Error for timeout testing
type mockTimeoutError struct {
	timeout bool
}

func (e *mockTimeoutError) Error() string   { return "mock timeout error" }
func (e *mockTimeoutError) Timeout() bool   { return e.timeout }
func (e *mockTimeoutError) Temporary() bool { return false }

func TestIsRetryable_NetworkTimeout(t *testing.T) {
	// Network timeout should be retryable
	timeoutErr := &mockTimeoutError{timeout: true}
	if !isRetryable(timeoutErr) {
		t.Error("network timeout error should be retryable")
	}

	// Non-timeout network error should not be retryable
	nonTimeoutErr := &mockTimeoutError{timeout: false}
	if isRetryable(nonTimeoutErr) {
		t.Error("non-timeout network error should not be retryable")
	}
}

func TestIsRetryable_APIErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		expected   bool
	}{
		{"rate limit 429", 429, true},
		{"server error 500", 500, true},
		{"server error 502", 502, true},
		{"server error 503", 503, true},
		{"bad request 400", 400, false},
		{"unauthorized 401", 401, false},
		{"forbidden 403", 403, false},
		{"not found 404", 404, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiErr := &anthropic.Error{StatusCode: tt.statusCode}
			got := isRetryable(apiErr)
			if got != tt.expected {
				t.Errorf("isRetryable(API error %d) = %v, want %v", tt.statusCode, got, tt.expected)
			}
		})
	}
}

// createMockAnthropicServer creates a mock server that returns Anthropic API responses
func createMockAnthropicServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

// mockAnthropicResponse creates a valid Anthropic Messages API response
func mockAnthropicResponse(text string) map[string]interface{} {
	return map[string]interface{}{
		"id":           "msg_test123",
		"type":         "message",
		"role":         "assistant",
		"model":        "claude-3-5-haiku-20241022",
		"stop_reason":  "end_turn",
		"stop_sequence": nil,
		"usage": map[string]int{
			"input_tokens":  100,
			"output_tokens": 50,
		},
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": text,
			},
		},
	}
}

func TestSummarizeTier1_MockAPI(t *testing.T) {
	// Create mock server that returns a valid summary
	server := createMockAnthropicServer(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and path
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("expected /messages path, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		resp := mockAnthropicResponse("**Summary:** Fixed auth bug.\n\n**Key Decisions:** Used OAuth.\n\n**Resolution:** Complete.")
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	client, err := NewHaikuClient("test-key", option.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	issue := &types.Issue{
		ID:          "bd-1",
		Title:       "Fix authentication bug",
		Description: "OAuth login was broken",
		Status:      types.StatusClosed,
	}

	ctx := context.Background()
	result, err := client.SummarizeTier1(ctx, issue)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "**Summary:**") {
		t.Error("result should contain Summary section")
	}
	if !strings.Contains(result, "Fixed auth bug") {
		t.Error("result should contain summary text")
	}
}

func TestSummarizeTier1_APIError(t *testing.T) {
	// Create mock server that returns an error
	server := createMockAnthropicServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "invalid_request_error",
				"message": "Invalid API key",
			},
		})
	})
	defer server.Close()

	client, err := NewHaikuClient("test-key", option.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	issue := &types.Issue{
		ID:          "bd-1",
		Title:       "Test",
		Description: "Test",
		Status:      types.StatusClosed,
	}

	ctx := context.Background()
	_, err = client.SummarizeTier1(ctx, issue)
	if err == nil {
		t.Fatal("expected error from API")
	}
	if !strings.Contains(err.Error(), "non-retryable") {
		t.Errorf("expected non-retryable error, got: %v", err)
	}
}

func TestCallWithRetry_RetriesOn429(t *testing.T) {
	var attempts int32

	server := createMockAnthropicServer(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt <= 2 {
			// First two attempts return 429
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"type": "error",
				"error": map[string]interface{}{
					"type":    "rate_limit_error",
					"message": "Rate limited",
				},
			})
			return
		}
		// Third attempt succeeds
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockAnthropicResponse("Success after retries"))
	})
	defer server.Close()

	// Disable SDK's internal retries to test our retry logic only
	client, err := NewHaikuClient("test-key", option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	// Use short backoff for testing
	client.initialBackoff = 10 * time.Millisecond

	ctx := context.Background()
	result, err := client.callWithRetry(ctx, "test prompt")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if result != "Success after retries" {
		t.Errorf("expected 'Success after retries', got: %s", result)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got: %d", attempts)
	}
}

func TestCallWithRetry_RetriesOn500(t *testing.T) {
	var attempts int32

	server := createMockAnthropicServer(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt == 1 {
			// First attempt returns 500
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"type": "error",
				"error": map[string]interface{}{
					"type":    "api_error",
					"message": "Internal server error",
				},
			})
			return
		}
		// Second attempt succeeds
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockAnthropicResponse("Recovered from 500"))
	})
	defer server.Close()

	// Disable SDK's internal retries to test our retry logic only
	client, err := NewHaikuClient("test-key", option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	client.initialBackoff = 10 * time.Millisecond

	ctx := context.Background()
	result, err := client.callWithRetry(ctx, "test prompt")
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if result != "Recovered from 500" {
		t.Errorf("expected 'Recovered from 500', got: %s", result)
	}
}

func TestCallWithRetry_ExhaustsRetries(t *testing.T) {
	var attempts int32

	server := createMockAnthropicServer(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		// Always return 429
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "rate_limit_error",
				"message": "Rate limited",
			},
		})
	})
	defer server.Close()

	// Disable SDK's internal retries to test our retry logic only
	client, err := NewHaikuClient("test-key", option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	client.initialBackoff = 1 * time.Millisecond
	client.maxRetries = 2

	ctx := context.Background()
	_, err = client.callWithRetry(ctx, "test prompt")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "failed after") {
		t.Errorf("expected 'failed after' error, got: %v", err)
	}
	// Initial attempt + 2 retries = 3 total
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got: %d", attempts)
	}
}

func TestCallWithRetry_NoRetryOn400(t *testing.T) {
	var attempts int32

	server := createMockAnthropicServer(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "invalid_request_error",
				"message": "Bad request",
			},
		})
	})
	defer server.Close()

	client, err := NewHaikuClient("test-key", option.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	client.initialBackoff = 10 * time.Millisecond

	ctx := context.Background()
	_, err = client.callWithRetry(ctx, "test prompt")
	if err == nil {
		t.Fatal("expected error for bad request")
	}
	if !strings.Contains(err.Error(), "non-retryable") {
		t.Errorf("expected non-retryable error, got: %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected only 1 attempt for non-retryable error, got: %d", attempts)
	}
}

func TestCallWithRetry_ContextTimeout(t *testing.T) {
	server := createMockAnthropicServer(func(w http.ResponseWriter, r *http.Request) {
		// Delay longer than context timeout
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockAnthropicResponse("too late"))
	})
	defer server.Close()

	client, err := NewHaikuClient("test-key", option.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = client.callWithRetry(ctx, "test prompt")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
}

func TestCallWithRetry_EmptyContent(t *testing.T) {
	server := createMockAnthropicServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return response with empty content array
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "msg_test123",
			"type":    "message",
			"role":    "assistant",
			"model":   "claude-3-5-haiku-20241022",
			"content": []map[string]interface{}{},
		})
	})
	defer server.Close()

	client, err := NewHaikuClient("test-key", option.WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()
	_, err = client.callWithRetry(ctx, "test prompt")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), "no content blocks") {
		t.Errorf("expected 'no content blocks' error, got: %v", err)
	}
}

func TestBytesWriter(t *testing.T) {
	w := &bytesWriter{}

	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("expected n=5, got %d", n)
	}

	n, err = w.Write([]byte(" world"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 6 {
		t.Errorf("expected n=6, got %d", n)
	}

	if string(w.buf) != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", string(w.buf))
	}
}

// Verify net.Error interface is properly satisfied for test mocks
var _ net.Error = (*mockTimeoutError)(nil)
