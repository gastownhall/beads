package doctor

import (
	"testing"
)

func TestCheckNameToSlug(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Git Hooks", "git-hooks"},
		{"CLI Version", "cli-version"},
		{"Pending Migrations", "pending-migrations"},
		{"Remote Consistency", "remote-consistency"},
		{"Role Configuration", "role-configuration"},
		{"Stale Closed Issues", "stale-closed-issues"},
		{"Large Database", "large-database"},
		{"Dolt Format", "dolt-format"},
		{"Lock Files", "lock-files"},
		{"Merge Artifacts", "merge-artifacts"},
		{"Orphaned Dependencies", "orphaned-dependencies"},
		{"Duplicate Issues", "duplicate-issues"},
		{"Multi-Repo Types", "multi-repo-types"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckNameToSlug(tt.name)
			if got != tt.want {
				t.Errorf("CheckNameToSlug(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
