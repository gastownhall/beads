package main

import (
	"testing"
	"time"
)

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"no truncation needed", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncate", "hello world", 8, "hello wo"},
		{"very short max", "hello", 3, "hel"},
		{"max of 4", "hello world", 4, "hell"},
		{"empty string", "", 5, ""},
		// Note: truncateString operates on bytes, not runes
		{"unicode", "hello\u4e16\u754c", 15, "hello\u4e16\u754c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestPluralize(t *testing.T) {
	tests := []struct {
		count int
		want  string
	}{
		{0, "s"},
		{1, ""},
		{2, "s"},
		{100, "s"},
		{-1, "s"},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := pluralize(tt.count)
			if got != tt.want {
				t.Errorf("pluralize(%d) = %q, want %q", tt.count, got, tt.want)
			}
		})
	}
}

func TestFormatTimeAgo(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name         string
		t            time.Time
		wantContains string
	}{
		{"just now", now.Add(-30 * time.Second), "just now"},
		{"1 minute ago", now.Add(-1 * time.Minute), "1 min ago"},
		{"5 minutes ago", now.Add(-5 * time.Minute), "5 mins ago"},
		{"1 hour ago", now.Add(-1 * time.Hour), "1 hour ago"},
		{"3 hours ago", now.Add(-3 * time.Hour), "3 hours ago"},
		{"1 day ago", now.Add(-24 * time.Hour), "1 day ago"},
		{"3 days ago", now.Add(-72 * time.Hour), "3 days ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTimeAgo(tt.t)
			if got != tt.wantContains {
				t.Errorf("formatTimeAgo() = %q, want %q", got, tt.wantContains)
			}
		})
	}
}

func TestContainsLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		label  string
		want   bool
	}{
		{"empty labels", []string{}, "bug", false},
		{"single match", []string{"bug"}, "bug", true},
		{"no match", []string{"feature", "enhancement"}, "bug", false},
		{"match in list", []string{"bug", "feature", "urgent"}, "feature", true},
		{"case sensitive", []string{"Bug"}, "bug", false},
		{"nil labels", nil, "bug", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsLabel(tt.labels, tt.label)
			if got != tt.want {
				t.Errorf("containsLabel(%v, %q) = %v, want %v", tt.labels, tt.label, got, tt.want)
			}
		})
	}
}

func TestGetContributorsSorted(t *testing.T) {
	// Test that contributors are returned in sorted order by commit count
	contributors := getContributorsSorted()

	if len(contributors) == 0 {
		t.Skip("No contributors defined")
	}

	// Check that we have at least some contributors
	if len(contributors) < 1 {
		t.Error("Expected at least one contributor")
	}

	// Verify first contributor has most commits (descending order)
	// We can't easily check counts, but we can verify the result is non-empty strings
	for i, c := range contributors {
		if c == "" {
			t.Errorf("Contributor at index %d is empty string", i)
		}
	}
}

func TestExtractIDSuffix(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"hierarchical ID", "bd-123.1.2", "2"},
		{"prefix-hash ID", "bd-abc123", "abc123"},
		{"simple ID", "123", "123"},
		{"multi-dot hierarchical", "prefix-xyz.1.2.3", "3"},
		{"dot only", "a.b", "b"},
		{"dash only", "a-b", "b"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIDSuffix(tt.id)
			if got != tt.want {
				t.Errorf("extractIDSuffix(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"no truncation", "short", 10, "short"},
		{"exact length", "exact", 5, "exact"},
		{"truncate needed", "long string here", 10, "long st..."},
		{"very short max", "hello world", 5, "he..."},
		{"empty string", "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.s, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestTruncateDescription(t *testing.T) {
	tests := []struct {
		name   string
		desc   string
		maxLen int
		want   string
	}{
		{"no truncation", "short", 10, "short"},
		{"multiline takes first", "first line\nsecond line", 20, "first line"},
		{"truncate with ellipsis", "a very long description here", 15, "a very long ..."},
		{"multiline and truncate", "first line is long\nsecond", 10, "first l..."},
		{"empty", "", 10, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateDescription(tt.desc, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateDescription(%q, %d) = %q, want %q", tt.desc, tt.maxLen, got, tt.want)
			}
		})
	}
}

// Test showCleanupDeprecationHint - just ensure it doesn't panic
func TestShowCleanupDeprecationHint(t *testing.T) {
	// This function just prints to stdout, so we just verify it doesn't panic
	showCleanupDeprecationHint()
}

