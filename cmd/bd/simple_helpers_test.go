package main

import (
	"strings"
	"testing"
)

func TestParseLabelArgs(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		expectIDs    int
		expectLabels []string
	}{
		{
			name:         "single ID single label",
			args:         []string{"bd-1", "bug"},
			expectIDs:    1,
			expectLabels: []string{"bug"},
		},
		{
			name:         "multiple IDs single label",
			args:         []string{"bd-1", "bd-2", "critical"},
			expectIDs:    2,
			expectLabels: []string{"critical"},
		},
		{
			name:         "three IDs one label",
			args:         []string{"bd-1", "bd-2", "bd-3", "bug"},
			expectIDs:    3,
			expectLabels: []string{"bug"},
		},
		{
			name:         "comma-separated labels",
			args:         []string{"bd-1", "bug,critical,needs-review"},
			expectIDs:    1,
			expectLabels: []string{"bug", "critical", "needs-review"},
		},
		{
			name:         "comma-separated labels with whitespace and empties",
			args:         []string{"bd-1", " bug , ,critical,"},
			expectIDs:    1,
			expectLabels: []string{"bug", "critical"},
		},
		{
			name:         "whitespace-only label yields no labels",
			args:         []string{"bd-1", "  "},
			expectIDs:    1,
			expectLabels: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, labels := parseLabelArgs(tt.args)

			if len(ids) != tt.expectIDs {
				t.Errorf("Expected %d IDs, got %d", tt.expectIDs, len(ids))
			}

			if len(labels) != len(tt.expectLabels) {
				t.Fatalf("Expected labels %v, got %v", tt.expectLabels, labels)
			}
			for i, want := range tt.expectLabels {
				if labels[i] != want {
					t.Errorf("Expected label %d to be %q, got %q", i, want, labels[i])
				}
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
