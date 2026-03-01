package doltutil

import "testing"

func TestIsSSHURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		// SSH URLs
		{"git+ssh://git@github.com/org/repo.git", true},
		{"ssh://git@github.com/org/repo.git", true},
		{"git@github.com:org/repo.git", true},
		{"git+ssh://github.com/org/repo", true},
		{"ssh://user@host:2222/path", true},
		{"git@bitbucket.org:team/repo.git", true},

		// Non-SSH URLs
		{"https://dolthub.com/org/repo", false},
		{"http://localhost:50051/repo", false},
		{"aws://[table:bucket]/db", false},
		{"gs://bucket/db", false},
		{"file:///local/path", false},
		{"/absolute/local/path", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := IsSSHURL(tt.url); got != tt.want {
				t.Errorf("IsSSHURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
