package main

import (
	"strings"
	"testing"
)

// TestValidateLabelRename pins the argument-refusal rules for `bd label
// rename` - the checks that must fire BEFORE any store is touched, so they
// need no dolt/docker fixture to test.
func TestValidateLabelRename(t *testing.T) {
	tests := []struct {
		name        string
		rawOld      string
		rawNew      string
		wantOld     string
		wantNew     string
		wantErr     bool
		errContains string
	}{
		{
			name:    "trims whitespace",
			rawOld:  "  backend  ",
			rawNew:  "  server  ",
			wantOld: "backend",
			wantNew: "server",
		},
		{
			name:        "empty old label refused",
			rawOld:      "",
			rawNew:      "server",
			wantErr:     true,
			errContains: "empty",
		},
		{
			name:        "empty new label refused",
			rawOld:      "backend",
			rawNew:      "   ",
			wantErr:     true,
			errContains: "empty",
		},
		{
			name:        "identical labels refused",
			rawOld:      "backend",
			rawNew:      "backend",
			wantErr:     true,
			errContains: "itself",
		},
		{
			name:        "reserved provides prefix on new label refused",
			rawOld:      "backend",
			rawNew:      "provides:auth",
			wantErr:     true,
			errContains: "reserved",
		},
		{
			name:        "reserved provides prefix on old label refused",
			rawOld:      "provides:auth",
			rawNew:      "backend",
			wantErr:     true,
			errContains: "reserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOld, gotNew, err := validateLabelRename(tt.rawOld, tt.rawNew)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got none (old=%q new=%q)", gotOld, gotNew)
				}
				if tt.errContains != "" && !strings.Contains(strings.ToLower(err.Error()), tt.errContains) {
					t.Errorf("error %q does not mention %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotOld != tt.wantOld || gotNew != tt.wantNew {
				t.Errorf("got (%q, %q), want (%q, %q)", gotOld, gotNew, tt.wantOld, tt.wantNew)
			}
		})
	}
}
