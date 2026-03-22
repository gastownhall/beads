package ado

import (
	"strings"
	"testing"
)

func TestHTMLToMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "plain text no HTML",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "bold",
			input:    "<strong>bold</strong>",
			expected: "**bold**",
		},
		{
			name:     "italic",
			input:    "<em>italic</em>",
			expected: "*italic*",
		},
		{
			name:     "link",
			input:    `<a href="https://example.com">text</a>`,
			expected: "[text](https://example.com)",
		},
		{
			name:  "unordered list",
			input: "<ul><li>item1</li><li>item2</li></ul>",
		},
		{
			name:  "ordered list",
			input: "<ol><li>first</li><li>second</li></ol>",
		},
		{
			name:     "inline code",
			input:    "<code>code</code>",
			expected: "`code`",
		},
		{
			name:  "code block",
			input: "<pre><code>code block</code></pre>",
		},
		{
			name:     "header h1",
			input:    "<h1>Title</h1>",
			expected: "# Title",
		},
		{
			name:  "nested bold italic",
			input: "<strong><em>bold italic</em></strong>",
		},
		{
			name:  "malicious script tag",
			input: "<script>alert('xss')</script><p>safe</p>",
		},
		{
			name:  "malicious event handler",
			input: `<p onclick="alert('xss')">text</p>`,
		},
		{
			name:  "malicious iframe",
			input: `<iframe src="evil.com"></iframe><p>safe</p>`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := HTMLToMarkdown(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			switch tc.name {
			case "empty string":
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
			case "plain text no HTML":
				if strings.TrimSpace(result) != "hello world" {
					t.Errorf("expected %q, got %q", "hello world", result)
				}
			case "bold":
				if !strings.Contains(result, "**bold**") {
					t.Errorf("expected bold markdown, got %q", result)
				}
			case "italic":
				if !strings.Contains(result, "*italic*") {
					t.Errorf("expected italic markdown, got %q", result)
				}
			case "link":
				if !strings.Contains(result, "[text](https://example.com)") {
					t.Errorf("expected link markdown, got %q", result)
				}
			case "unordered list":
				if !strings.Contains(result, "item1") || !strings.Contains(result, "item2") {
					t.Errorf("expected list items, got %q", result)
				}
				if !strings.Contains(result, "- ") && !strings.Contains(result, "* ") {
					t.Errorf("expected unordered list markers, got %q", result)
				}
			case "ordered list":
				if !strings.Contains(result, "first") || !strings.Contains(result, "second") {
					t.Errorf("expected list items, got %q", result)
				}
				if !strings.Contains(result, "1.") || !strings.Contains(result, "2.") {
					t.Errorf("expected ordered list markers, got %q", result)
				}
			case "inline code":
				if !strings.Contains(result, "`code`") {
					t.Errorf("expected inline code, got %q", result)
				}
			case "code block":
				if !strings.Contains(result, "code block") {
					t.Errorf("expected code block content, got %q", result)
				}
				if !strings.Contains(result, "```") && !strings.Contains(result, "    ") {
					t.Errorf("expected fenced or indented code block, got %q", result)
				}
			case "header h1":
				if !strings.Contains(result, "# Title") {
					t.Errorf("expected h1 markdown, got %q", result)
				}
			case "nested bold italic":
				if !strings.Contains(result, "bold italic") {
					t.Errorf("expected nested formatting content, got %q", result)
				}
				if !strings.Contains(result, "**") || !strings.Contains(result, "*") {
					t.Errorf("expected bold+italic markers, got %q", result)
				}
			case "malicious script tag":
				if strings.Contains(result, "script") || strings.Contains(result, "alert") {
					t.Errorf("script tag should be stripped, got %q", result)
				}
				if !strings.Contains(result, "safe") {
					t.Errorf("safe content should be preserved, got %q", result)
				}
			case "malicious event handler":
				if strings.Contains(result, "onclick") || strings.Contains(result, "alert") {
					t.Errorf("event handler should be stripped, got %q", result)
				}
				if !strings.Contains(result, "text") {
					t.Errorf("text content should be preserved, got %q", result)
				}
			case "malicious iframe":
				if strings.Contains(result, "iframe") || strings.Contains(result, "evil") {
					t.Errorf("iframe should be stripped, got %q", result)
				}
				if !strings.Contains(result, "safe") {
					t.Errorf("safe content should be preserved, got %q", result)
				}
			default:
				if tc.expected != "" && strings.TrimSpace(result) != tc.expected {
					t.Errorf("expected %q, got %q", tc.expected, result)
				}
			}
		})
	}
}

func TestMarkdownToHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:     "empty string",
			input:    "",
			contains: nil,
		},
		{
			name:     "plain text",
			input:    "plain text",
			contains: []string{"<p>", "plain text", "</p>"},
		},
		{
			name:     "bold",
			input:    "**bold**",
			contains: []string{"<strong>bold</strong>"},
		},
		{
			name:     "italic",
			input:    "*italic*",
			contains: []string{"<em>italic</em>"},
		},
		{
			name:     "link",
			input:    "[text](https://example.com)",
			contains: []string{`<a href="https://example.com">text</a>`},
		},
		{
			name:     "unordered list",
			input:    "- item1\n- item2",
			contains: []string{"<li>", "item1", "item2"},
		},
		{
			name:     "code block",
			input:    "```\ncode here\n```",
			contains: []string{"<pre>", "<code>", "code here"},
		},
		{
			name:     "header h1",
			input:    "# Title",
			contains: []string{"<h1>", "Title", "</h1>"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := MarkdownToHTML(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.name == "empty string" {
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
				return
			}

			for _, substr := range tc.contains {
				if !strings.Contains(result, substr) {
					t.Errorf("expected result to contain %q, got %q", substr, result)
				}
			}
		})
	}
}

func TestHTMLToMarkdown_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantEmpty bool
		contains  string
	}{
		{"whitespace only", "   \t\n  ", true, ""},
		{"only script tag (sanitized to empty)", "<script>alert('xss')</script>", true, ""},
		{"only style tag", "<style>body{color:red}</style>", true, ""},
		{"unclosed tags", "<p>hello <b>world", false, "hello"},
		{"nested divs", "<div><div><p>deep</p></div></div>", false, "deep"},
		{"br tags", "line1<br/>line2", false, "line1"},
		{"HTML entities", "&amp; &lt; &gt;", false, "&"},
		{"table HTML", "<table><tr><td>cell</td></tr></table>", false, "cell"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := HTMLToMarkdown(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantEmpty {
				if strings.TrimSpace(result) != "" {
					t.Errorf("expected empty result, got %q", result)
				}
				return
			}
			if tt.contains != "" && !strings.Contains(result, tt.contains) {
				t.Errorf("expected result to contain %q, got %q", tt.contains, result)
			}
		})
	}
}

func TestMarkdownToHTML_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:     "whitespace only",
			input:    "   \t\n  ",
			contains: nil,
		},
		{
			name:     "code block with language",
			input:    "```go\nfmt.Println(\"hello\")\n```",
			contains: []string{"<pre>", "<code", "fmt.Println"},
		},
		{
			name:     "table-like markdown preserved",
			input:    "| A | B |\n|---|---|\n| 1 | 2 |",
			contains: []string{"A", "B", "1", "2"},
		},
		{
			name:     "inline code",
			input:    "Use `fmt.Println` to print",
			contains: []string{"<code>fmt.Println</code>"},
		},
		{
			name:     "multiple headers",
			input:    "# H1\n## H2\n### H3",
			contains: []string{"<h1>", "<h2>", "<h3>"},
		},
		{
			name:     "blockquote",
			input:    "> quoted text",
			contains: []string{"<blockquote>"},
		},
		{
			name:     "horizontal rule",
			input:    "above\n\n---\n\nbelow",
			contains: []string{"<hr"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := MarkdownToHTML(tc.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.name == "whitespace only" {
				if result != "" {
					t.Errorf("expected empty string, got %q", result)
				}
				return
			}

			for _, substr := range tc.contains {
				if !strings.Contains(result, substr) {
					t.Errorf("expected result to contain %q, got %q", substr, result)
				}
			}
		})
	}
}

func TestHTMLToMarkdown_RoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		original  string
		preserved []string
	}{
		{
			name:      "bold text round-trip",
			original:  "**bold text**",
			preserved: []string{"bold text", "**"},
		},
		{
			name:      "link round-trip",
			original:  "[click here](https://example.com)",
			preserved: []string{"click here", "https://example.com"},
		},
		{
			name:      "list items round-trip",
			original:  "- apple\n- banana\n- cherry",
			preserved: []string{"apple", "banana", "cherry"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			htmlContent, err := MarkdownToHTML(tc.original)
			if err != nil {
				t.Fatalf("MarkdownToHTML error: %v", err)
			}

			roundTripped, err := HTMLToMarkdown(htmlContent)
			if err != nil {
				t.Fatalf("HTMLToMarkdown error: %v", err)
			}

			for _, substr := range tc.preserved {
				if !strings.Contains(roundTripped, substr) {
					t.Errorf("round-trip lost %q: original=%q html=%q result=%q",
						substr, tc.original, htmlContent, roundTripped)
				}
			}
		})
	}
}
