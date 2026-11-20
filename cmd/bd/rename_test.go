package main

import "testing"

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
		{"abc", false},
		{"-abc", false},
		{"abc-", false},
		{"8ball-abc", false},
		{"a b-c", false},
		{"bd-$1", false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := renameTargetIDPattern.MatchString(tt.id); got != tt.want {
				t.Errorf("renameTargetIDPattern.MatchString(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
