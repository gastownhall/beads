package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/steveyegge/beads/internal/types"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]int
	}{
		{
			name:  "simple words",
			input: "fix authentication bug",
			expected: map[string]int{
				"fix":            1,
				"authentication": 1,
				"bug":            1,
			},
		},
		{
			name:  "duplicate words",
			input: "fix the bug fix",
			expected: map[string]int{
				"fix": 2,
				"the": 1,
				"bug": 1,
			},
		},
		{
			name:  "mixed case and punctuation",
			input: "Fix: Authentication BUG!",
			expected: map[string]int{
				"fix":            1,
				"authentication": 1,
				"bug":            1,
			},
		},
		{
			name:  "hyphenated words preserved",
			input: "auto-import feature",
			expected: map[string]int{
				"auto-import": 1,
				"feature":     1,
			},
		},
		{
			name:     "single chars filtered",
			input:    "a b c hello",
			expected: map[string]int{"hello": 1},
		},
		{
			name:     "empty string",
			input:    "",
			expected: map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tokenize(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("tokenize(%q) got %d tokens, want %d: %v", tt.input, len(result), len(tt.expected), result)
				return
			}
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("tokenize(%q)[%q] = %d, want %d", tt.input, k, result[k], v)
				}
			}
		})
	}
}

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     map[string]int
		expected float64
		epsilon  float64
	}{
		{
			name:     "identical sets",
			a:        map[string]int{"fix": 1, "bug": 1},
			b:        map[string]int{"fix": 1, "bug": 1},
			expected: 1.0,
			epsilon:  0.001,
		},
		{
			name:     "disjoint sets",
			a:        map[string]int{"fix": 1, "bug": 1},
			b:        map[string]int{"add": 1, "feature": 1},
			expected: 0.0,
			epsilon:  0.001,
		},
		{
			name:     "partial overlap",
			a:        map[string]int{"fix": 1, "bug": 1, "auth": 1},
			b:        map[string]int{"fix": 1, "auth": 1, "login": 1},
			expected: 0.5, // intersection=2, union=4
			epsilon:  0.001,
		},
		{
			name:     "both empty",
			a:        map[string]int{},
			b:        map[string]int{},
			expected: 0.0,
			epsilon:  0.001,
		},
		{
			name:     "one empty",
			a:        map[string]int{"fix": 1},
			b:        map[string]int{},
			expected: 0.0,
			epsilon:  0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := jaccardSimilarity(tt.a, tt.b)
			diff := result - tt.expected
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.epsilon {
				t.Errorf("jaccardSimilarity() = %f, want %f (diff %f)", result, tt.expected, diff)
			}
		})
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     map[string]int
		expected float64
		epsilon  float64
	}{
		{
			name:     "identical",
			a:        map[string]int{"fix": 1, "bug": 1},
			b:        map[string]int{"fix": 1, "bug": 1},
			expected: 1.0,
			epsilon:  0.001,
		},
		{
			name:     "orthogonal",
			a:        map[string]int{"fix": 1},
			b:        map[string]int{"add": 1},
			expected: 0.0,
			epsilon:  0.001,
		},
		{
			name:     "empty sets",
			a:        map[string]int{},
			b:        map[string]int{},
			expected: 0.0,
			epsilon:  0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cosineSimilarity(tt.a, tt.b)
			diff := result - tt.expected
			if diff < 0 {
				diff = -diff
			}
			if diff > tt.epsilon {
				t.Errorf("cosineSimilarity() = %f, want %f", result, tt.expected)
			}
		})
	}
}

func TestFindMechanicalDuplicates(t *testing.T) {
	issues := []*types.Issue{
		{
			ID:          "bd-001",
			Title:       "Fix authentication bug in login flow",
			Description: "The login page fails when using SSO authentication",
		},
		{
			ID:          "bd-002",
			Title:       "Authentication login bug fix",
			Description: "SSO authentication fails on the login page",
		},
		{
			ID:          "bd-003",
			Title:       "Add dark mode support",
			Description: "Implement dark mode theme for the application",
		},
	}

	pairs := findMechanicalDuplicates(issues, 0.3)

	// Should find bd-001 and bd-002 as similar
	found := false
	for _, p := range pairs {
		if (p.IssueA.ID == "bd-001" && p.IssueB.ID == "bd-002") ||
			(p.IssueA.ID == "bd-002" && p.IssueB.ID == "bd-001") {
			found = true
			if p.Similarity < 0.3 {
				t.Errorf("expected similarity >= 0.3 for auth bug pair, got %f", p.Similarity)
			}
		}
	}
	if !found {
		t.Error("expected to find auth bug pair as similar")
	}

	// bd-003 should NOT match with bd-001 or bd-002
	for _, p := range pairs {
		if p.IssueA.ID == "bd-003" || p.IssueB.ID == "bd-003" {
			t.Errorf("dark mode issue should not match auth issues, got similarity %f", p.Similarity)
		}
	}
}

func TestFindMechanicalDuplicatesHighThreshold(t *testing.T) {
	issues := []*types.Issue{
		{ID: "bd-001", Title: "Fix bug A", Description: "Something completely different"},
		{ID: "bd-002", Title: "Add feature B", Description: "Totally unrelated content"},
	}

	pairs := findMechanicalDuplicates(issues, 0.9)
	if len(pairs) != 0 {
		t.Errorf("expected no pairs at threshold 0.9 for different issues, got %d", len(pairs))
	}
}

func TestFindMechanicalDuplicatesMinIssues(t *testing.T) {
	// Single issue - no pairs possible
	issues := []*types.Issue{
		{ID: "bd-001", Title: "Only issue"},
	}
	pairs := findMechanicalDuplicates(issues, 0.1)
	if len(pairs) != 0 {
		t.Errorf("expected no pairs for single issue, got %d", len(pairs))
	}
}

func TestIssueText(t *testing.T) {
	issue := &types.Issue{
		Title:       "Fix bug",
		Description: "detailed description",
	}
	text := issueText(issue)
	if text != "Fix bug detailed description" {
		t.Errorf("issueText() = %q, want %q", text, "Fix bug detailed description")
	}

	// No description
	issue2 := &types.Issue{Title: "Just title"}
	text2 := issueText(issue2)
	if text2 != "Just title" {
		t.Errorf("issueText() = %q, want %q", text2, "Just title")
	}
}

// stubAIClient returns an Anthropic client whose Messages replies carry the
// given content blocks, plus a count of the requests the stub served.
func stubAIClient(t *testing.T, content []map[string]any) (anthropic.Client, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		hits.Add(1)
		body := map[string]any{
			"id":            "msg_test",
			"type":          "message",
			"role":          "assistant",
			"model":         "claude-haiku-4-5",
			"content":       content,
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": 1, "output_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encode stub reply: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	client := anthropic.NewClient(
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey("test-key"),
		option.WithBaseURL(srv.URL),
		option.WithMaxRetries(0),
	)
	return client, &hits
}

func aiTestCandidates() []duplicatePair {
	return []duplicatePair{{
		IssueA:     &types.Issue{ID: "bd-1", Title: "Login fails on Safari"},
		IssueB:     &types.Issue{ID: "bd-2", Title: "Safari login broken"},
		Similarity: 0.3,
		Method:     "mechanical",
	}}
}

// TestAnalyzeWithAI_SkipsLeadingThinkingBlock pins GH#7515: a reply may open
// with non-text blocks (thinking), and the verdict is in the first text block.
func TestAnalyzeWithAI_SkipsLeadingThinkingBlock(t *testing.T) {
	verdict := func(confidence float64, reason string) map[string]any {
		return map[string]any{"type": "text", "text": `[{"pair_index":0,"is_duplicate":true,"confidence":` +
			mustJSON(t, confidence) + `,"reason":` + mustJSON(t, reason) + `}]`}
	}
	thinking := map[string]any{"type": "thinking", "thinking": "pondering", "signature": "sig"}
	redacted := map[string]any{"type": "redacted_thinking", "data": "opaque"}

	tests := []struct {
		name       string
		content    []map[string]any
		wantMethod string
		wantSim    float64
		wantReason string
	}{
		{"thinking then text", []map[string]any{thinking, verdict(0.9, "same bug")}, "ai", 0.9, "same bug"},
		{"redacted thinking then text", []map[string]any{redacted, verdict(0.9, "same bug")}, "ai", 0.9, "same bug"},
		{"text only", []map[string]any{verdict(0.9, "same bug")}, "ai", 0.9, "same bug"},
		{"first of two text blocks", []map[string]any{thinking, verdict(0.8, "first"), verdict(0.7, "second")}, "ai", 0.8, "first"},
		// An empty first text block is still the answer: it fails to parse and
		// falls back to the mechanical scores rather than reading a later block.
		{"empty first text block", []map[string]any{thinking, {"type": "text", "text": ""}, verdict(0.7, "second")}, "mechanical", 0.3, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, hits := stubAIClient(t, tt.content)
			var got []duplicatePair
			captureStderr(t, func() {
				got = analyzeWithAI(context.Background(), client, "claude-haiku-4-5", aiTestCandidates())
			})
			if len(got) != 1 {
				t.Fatalf("got %d pairs, want 1: %+v", len(got), got)
			}
			p := got[0]
			if p.Method != tt.wantMethod || p.Similarity != tt.wantSim || p.Reason != tt.wantReason {
				t.Fatalf("pair = {Method:%s Similarity:%v Reason:%q}, want {Method:%s Similarity:%v Reason:%q}",
					p.Method, p.Similarity, p.Reason, tt.wantMethod, tt.wantSim, tt.wantReason)
			}
			if p.IssueA.ID != "bd-1" || p.IssueB.ID != "bd-2" {
				t.Fatalf("pair issues = %s/%s, want bd-1/bd-2", p.IssueA.ID, p.IssueB.ID)
			}
			if n := hits.Load(); n != 1 {
				t.Fatalf("requests = %d, want 1", n)
			}
		})
	}
}

// TestAnalyzeWithAI_NoTextBlockKeepsCandidates pins that a reply with no text
// block warns and keeps the mechanical candidates after one request.
func TestAnalyzeWithAI_NoTextBlockKeepsCandidates(t *testing.T) {
	tests := []struct {
		name     string
		content  []map[string]any
		wantWarn string
	}{
		{"thinking only", []map[string]any{{"type": "thinking", "thinking": "pondering", "signature": "sig"}},
			"Warning: unexpected AI response format: no text block in 1 content blocks"},
		{"no content", []map[string]any{}, "Warning: unexpected AI response format: no text block in 0 content blocks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, hits := stubAIClient(t, tt.content)
			candidates := aiTestCandidates()
			var got []duplicatePair
			stderr := captureStderr(t, func() {
				got = analyzeWithAI(context.Background(), client, "claude-haiku-4-5", candidates)
			})
			if !reflect.DeepEqual(got, aiTestCandidates()) {
				t.Fatalf("pairs = %+v, want the input candidates unchanged", got)
			}
			if !strings.Contains(stderr, tt.wantWarn) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, tt.wantWarn)
			}
			if n := hits.Load(); n != 1 {
				t.Fatalf("requests = %d, want 1", n)
			}
		})
	}
}
