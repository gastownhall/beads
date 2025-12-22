package main

import (
	"testing"
)

func TestValidateExternalRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{
			name:    "valid external ref",
			ref:     "external:beads:mol-run-assignee",
			wantErr: false,
		},
		{
			name:    "valid with complex capability",
			ref:     "external:gastown:cross-project-deps",
			wantErr: false,
		},
		{
			name:    "missing external prefix",
			ref:     "beads:mol-run",
			wantErr: true,
		},
		{
			name:    "missing capability",
			ref:     "external:beads:",
			wantErr: true,
		},
		{
			name:    "missing project",
			ref:     "external::capability",
			wantErr: true,
		},
		{
			name:    "only external prefix",
			ref:     "external:",
			wantErr: true,
		},
		{
			name:    "too few parts",
			ref:     "external:beads",
			wantErr: true,
		},
		{
			name:    "local issue ID",
			ref:     "bd-xyz",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExternalRef(tt.ref)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateExternalRef(%q) error = %v, wantErr %v", tt.ref, err, tt.wantErr)
			}
		})
	}
}

func TestIsExternalRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"external:beads:capability", true},
		{"external:", true}, // prefix matches even if invalid
		{"bd-xyz", false},
		{"", false},
		{"External:beads:cap", false}, // case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			if got := IsExternalRef(tt.ref); got != tt.want {
				t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestParseExternalRef(t *testing.T) {
	tests := []struct {
		ref            string
		wantProject    string
		wantCapability string
	}{
		{"external:beads:mol-run-assignee", "beads", "mol-run-assignee"},
		{"external:gastown:cross-project", "gastown", "cross-project"},
		{"external:a:b", "a", "b"},
		{"bd-xyz", "", ""},            // not external
		{"external:", "", ""},          // invalid format
		{"external:proj", "", ""},      // missing capability
		{"", "", ""},                   // empty
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			gotProj, gotCap := ParseExternalRef(tt.ref)
			if gotProj != tt.wantProject || gotCap != tt.wantCapability {
				t.Errorf("ParseExternalRef(%q) = (%q, %q), want (%q, %q)",
					tt.ref, gotProj, gotCap, tt.wantProject, tt.wantCapability)
			}
		})
	}
}
