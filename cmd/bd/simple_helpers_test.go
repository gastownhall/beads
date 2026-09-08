package main

import (
	"strings"
	"testing"
)

func TestParseLabelArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		expectIDs   int
		expectLabel string
	}{
		{
			name:        "single ID single label",
			args:        []string{"bd-1", "bug"},
			expectIDs:   1,
			expectLabel: "bug",
		},
		{
			name:        "multiple IDs single label",
			args:        []string{"bd-1", "bd-2", "critical"},
			expectIDs:   2,
			expectLabel: "critical",
		},
		{
			name:        "three IDs one label",
			args:        []string{"bd-1", "bd-2", "bd-3", "bug"},
			expectIDs:   3,
			expectLabel: "bug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, label := parseLabelArgs(tt.args)

			if len(ids) != tt.expectIDs {
				t.Errorf("Expected %d IDs, got %d", tt.expectIDs, len(ids))
			}

			if label != tt.expectLabel {
				t.Errorf("Expected label %q, got %q", tt.expectLabel, label)
			}
		})
	}
}

func TestListRejectsPositionalArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "no args is fine",
			args:    []string{},
			wantErr: false,
		},
		{
			name:      "ready is rejected with hint",
			args:      []string{"ready"},
			wantErr:   true,
			errSubstr: `did you mean "--ready"`,
		},
		{
			name:      "tree is rejected with hint",
			args:      []string{"tree"},
			wantErr:   true,
			errSubstr: `did you mean "--tree"`,
		},
		{
			name:      "unknown arg is rejected generically",
			args:      []string{"foobar"},
			wantErr:   true,
			errSubstr: "does not accept positional arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := listCmd.Args(listCmd, tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Expected error for args %v, got nil", tt.args)
				}
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("Error %q should contain %q", err.Error(), tt.errSubstr)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for args %v: %v", tt.args, err)
				}
			}
		})
	}
}
