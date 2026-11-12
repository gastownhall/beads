package main

import "testing"

// TestRenameTargetIDPattern pins the new-ID format bd rename accepts: the
// prefix alphabet bd init's normalizeIssuePrefix mints (digits, uppercase
// letters, underscores) on top of every ID the previous letters-only pattern
// accepted, while malformed targets stay refused (GH#6068).
func TestRenameTargetIDPattern(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"bd-dolt", true},
		{"gt-auth", true},
		{"bd-a3f8e9.1", true},
		{"web-app-abc123", true},
		{"probe8-abc", true},
		{"yii2-cgxdl", true},
		{"web3-app-x1", true},
		{"MyApp2-abc", true},
		{"my_app9-abc", true},
		{"bd_8ball-abc", true},
		{"GPUPolynomials_jl-abc", true},
		{"_scratch-abc", true},
		{"abc", false},
		{"", false},
		{"-abc", false},
		{"abc-", false},
		{"8ball-abc", false},
		{"a b-c", false},
		{"bd-$1", false},
	}
	for _, tt := range tests {
		name := tt.id
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			if got := renameTargetIDPattern.MatchString(tt.id); got != tt.want {
				t.Errorf("renameTargetIDPattern.MatchString(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
