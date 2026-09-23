package main

import "testing"

// TestMatchPrefixRoute pins longest-prefix route selection: multi-hyphen
// route prefixes must match their IDs instead of being shadowed by a
// first-dash cut (GH#5048).
func TestMatchPrefixRoute(t *testing.T) {
	routes := []prefixRoute{
		{Prefix: "claude-os-", Path: "rigA"},
		{Prefix: "solo-", Path: "rigB"},
		{Prefix: "hq-", Path: "hq"},
		{Prefix: "hq-cv-", Path: "convoys"},
		{Prefix: "nodash", Path: "bogus"},
	}
	tests := []struct {
		id   string
		want string // matched route path; "" means no match
	}{
		{"claude-os-sar", "rigA"},
		{"claude-os-76l.1", "rigA"},
		{"solo-x", "rigB"},
		{"hq-abc", "hq"},
		{"hq-cv-abc", "convoys"},
		{"claude-abc", ""},
		{"other-abc", ""},
		{"nodash-abc", ""},
		{"solo", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := matchPrefixRoute(routes, tt.id)
			gotPath := ""
			if got != nil {
				gotPath = got.Path
			}
			if gotPath != tt.want {
				t.Errorf("matchPrefixRoute(%q) = %q, want %q", tt.id, gotPath, tt.want)
			}
		})
	}
}
