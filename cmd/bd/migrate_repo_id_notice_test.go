package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/beads"
)

// bd-ek28z: the path-hash propagation notice must fire exactly when an
// existing repository ID would be replaced by a different, path-derived one —
// the case --yes/--json used to stamp silently (GH#4361 recurrence hole).
func TestPathHashRepoIDStampNotice(t *testing.T) {
	tests := []struct {
		name       string
		oldRepoID  string
		newRepoID  string
		source     beads.RepoIDSource
		wantNotice bool
	}{
		{"path hash replacing different id", "aaaa1111", "bbbb2222", beads.RepoIDSourcePath, true},
		{"no stored id (bootstrap)", "", "bbbb2222", beads.RepoIDSourcePath, false},
		{"unchanged id", "aaaa1111", "aaaa1111", beads.RepoIDSourcePath, false},
		{"remote-derived new id", "aaaa1111", "bbbb2222", beads.RepoIDSourceRemote, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathHashRepoIDStampNotice(tt.oldRepoID, tt.newRepoID, tt.source)
			if (got != "") != tt.wantNotice {
				t.Errorf("pathHashRepoIDStampNotice(%q, %q, %q) = %q, want notice=%v",
					tt.oldRepoID, tt.newRepoID, tt.source, got, tt.wantNotice)
			}
		})
	}
}
