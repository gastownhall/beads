//go:build cgo

package embeddeddolt

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

func TestLenientGateWarningBody_ShapedDecisions(t *testing.T) {
	tests := []struct {
		name     string
		decision string
		want     string
		unwanted string
		skew     []int
	}{
		{
			name:     "adopt",
			decision: "adopt",
			want:     "The remote has already been migrated by another clone",
			unwanted: "designated migrator",
		},
		{
			name:     "adopt fast-forward",
			decision: "adopt-ff",
			want:     "strict ancestor of the remote's",
			unwanted: "designated migrator",
		},
		{
			name:     "fork skew",
			decision: "fork-skew",
			skew:     []int{42},
			want:     "The schema has forked",
			unwanted: "designated migrator",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gateErr := &schema.RemoteMigrateGateError{
				CurrentVersion: 41,
				LatestVersion:  42,
				Pending:        1,
				Decision:       tc.decision,
				SkewVersions:   tc.skew,
			}

			warning := lenientGateWarningBody(gateErr)
			if !strings.Contains(warning, tc.want) {
				t.Errorf("warning is missing shaped guidance %q; got:\n%s", tc.want, warning)
			}
			if strings.Contains(warning, tc.unwanted) {
				t.Errorf("warning still contains blunt guidance %q; got:\n%s", tc.unwanted, warning)
			}
		})
	}
}

func TestLenientGateWarningBody_BluntRefusalKeepsSharedGuidance(t *testing.T) {
	gateErr := &schema.RemoteMigrateGateError{
		CurrentVersion: 41,
		LatestVersion:  42,
		Pending:        1,
	}

	warning := lenientGateWarningBody(gateErr)
	if !strings.Contains(warning, "designated migrator") {
		t.Fatalf("blunt refusal lost shared migrate-or-adopt guidance:\n%s", warning)
	}
}
