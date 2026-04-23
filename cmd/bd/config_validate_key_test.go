package main

import (
	"strings"
	"testing"
)

func TestIsRecognizedConfigKey(t *testing.T) {
	recognized := []string{
		"export.auto", "dolt.auto-push", "jira.url", "custom.anything",
		"doctor.suppress.git-hooks", "no-git-ops", "beads.role",
		"status.custom", "ai.model", "backup.enabled",
	}
	for _, key := range recognized {
		if !isRecognizedConfigKey(key) {
			t.Errorf("isRecognizedConfigKey(%q) = false, want true", key)
		}
	}

	unrecognized := []string{
		"totally.bogus", "exprot.auto", "xport.path", "nodb",
	}
	for _, key := range unrecognized {
		if isRecognizedConfigKey(key) {
			t.Errorf("isRecognizedConfigKey(%q) = true, want false", key)
		}
	}
}

func TestSuggestConfigKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"exprot.auto", "export.auto"},
		{"exoprt.path", "export.path"},
		{"totally.bogus", ""},
	}
	for _, tt := range tests {
		got := suggestConfigKey(tt.input)
		if got != tt.want {
			t.Errorf("suggestConfigKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRejectProtectedConfigKey(t *testing.T) {
	rejectedKeys := []string{"issue_prefix", "issue-prefix"}
	for _, key := range rejectedKeys {
		msg, rejected := rejectProtectedConfigKey(key)
		if !rejected {
			t.Errorf("rejectProtectedConfigKey(%q) = (_, false), want rejected", key)
			continue
		}
		// Error message must surface the three lifecycle alternatives.
		wantSubstrings := []string{"bd init --prefix", "bd bootstrap", "bd rename-prefix"}
		for _, want := range wantSubstrings {
			if !strings.Contains(msg, want) {
				t.Errorf("rejectProtectedConfigKey(%q) message missing %q; got:\n%s", key, want, msg)
			}
		}
	}

	allowedKeys := []string{"allowed_prefixes", "export.auto", "status.custom", "custom.anything"}
	for _, key := range allowedKeys {
		if _, rejected := rejectProtectedConfigKey(key); rejected {
			t.Errorf("rejectProtectedConfigKey(%q) = (_, true), want not rejected", key)
		}
	}
}

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"export", "exprot", 2},
		{"dolt", "bolt", 1},
		{"abc", "", 3},
	}
	for _, tt := range tests {
		got := levenshteinDistance(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("levenshteinDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
