package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

// aiMessagesStub serves one fixed Messages API reply whose content blocks
// come from the test, and counts the requests it received.
func aiMessagesStub(t *testing.T, contentBlocks string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","content":`+
			contentBlocks+
			`,"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":5}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// stubAIClient returns a client that talks only to the stub server.
func stubAIClient(baseURL string) anthropic.Client {
	return anthropic.NewClient(
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey("test-key"),
		option.WithBaseURL(baseURL),
		option.WithMaxRetries(0),
	)
}

// aiTestCandidates returns one mechanical candidate pair for analyzeWithAI.
func aiTestCandidates() []duplicatePair {
	return []duplicatePair{{
		IssueA:     &types.Issue{ID: "bd-aaa", Title: "Crash on save"},
		IssueB:     &types.Issue{ID: "bd-bbb", Title: "Saving crashes"},
		Similarity: 0.3,
		Method:     "mechanical",
	}}
}

// GH#7515: a reply that opens with a thinking block must still yield the
// first text block's verdict, and the first text block is the answer even
// when later text blocks exist.
func TestAnalyzeWithAI_SkipsLeadingThinkingBlock(t *testing.T) {
	const verdict = `[{"pair_index":0,"is_duplicate":true,"confidence":0.9,"reason":"same bug"}]`
	const laterVerdict = `[{"pair_index":0,"is_duplicate":true,"confidence":0.1,"reason":"later block"}]`
	cases := []struct {
		name    string
		content string
		wantAI  bool
	}{
		{"thinking then text", `[{"type":"thinking","thinking":"pondering","signature":"sig"},{"type":"text","text":` + verdict + `}]`, true},
		{"redacted thinking then text", `[{"type":"redacted_thinking","data":"opaque"},{"type":"text","text":` + verdict + `}]`, true},
		{"text first", `[{"type":"text","text":` + verdict + `}]`, true},
		{"first text block wins", `[{"type":"text","text":` + verdict + `},{"type":"text","text":` + laterVerdict + `}]`, true},
		{"empty first text block keeps candidates", `[{"type":"text","text":""},{"type":"text","text":` + verdict + `}]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, hits := aiMessagesStub(t, tc.content)
			got := analyzeWithAI(context.Background(), stubAIClient(srv.URL), "claude-test", aiTestCandidates())
			if len(got) != 1 {
				t.Fatalf("got %d pairs, want 1", len(got))
			}
			if tc.wantAI {
				if got[0].Method != "ai" || got[0].Similarity != 0.9 || got[0].Reason != "same bug" {
					t.Errorf("pair = {Method:%s Similarity:%v Reason:%q}, want {ai, 0.9, \"same bug\"}", got[0].Method, got[0].Similarity, got[0].Reason)
				}
			} else if got[0].Method != "mechanical" || got[0].Similarity != 0.3 {
				t.Errorf("pair = {Method:%s Similarity:%v}, want unchanged candidate {mechanical, 0.3}", got[0].Method, got[0].Similarity)
			}
			if n := hits.Load(); n != 1 {
				t.Errorf("stub received %d requests, want 1", n)
			}
		})
	}
}

// GH#7515: a reply with no text block at all keeps the mechanical candidates
// and warns with the content-block count, after exactly one request.
func TestAnalyzeWithAI_NoTextBlockKeepsCandidates(t *testing.T) {
	cases := []struct {
		name    string
		content string
		blocks  int
	}{
		{"thinking only", `[{"type":"thinking","thinking":"pondering","signature":"sig"}]`, 1},
		{"no blocks", `[]`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, hits := aiMessagesStub(t, tc.content)

			old := os.Stderr
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("pipe: %v", err)
			}
			os.Stderr = w

			got := analyzeWithAI(context.Background(), stubAIClient(srv.URL), "claude-test", aiTestCandidates())

			w.Close()
			os.Stderr = old
			var buf strings.Builder
			_, _ = io.Copy(&buf, r)
			warned := buf.String()

			want := fmt.Sprintf("no text block in %d content blocks", tc.blocks)
			if !strings.Contains(warned, want) {
				t.Errorf("stderr = %q, want it to contain %q", warned, want)
			}
			if len(got) != 1 || got[0].Method != "mechanical" || got[0].Similarity != 0.3 {
				t.Errorf("pairs = %+v, want the unchanged mechanical candidate", got)
			}
			if n := hits.Load(); n != 1 {
				t.Errorf("stub received %d requests, want 1", n)
			}
		})
	}
}
