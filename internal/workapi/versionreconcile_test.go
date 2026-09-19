package workapi

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestPlanVersionReconcile pins the whole decision table of
// issueops.VersionReconciler in one place and with no database, so the
// conformance contract is free to assert only what a real backend can show.
//
// The two marker states a workspace reaches ONLY through something outside the
// role — a recorded version below the high-water mark — are here rather than
// there: reproducing them on three backends costs a seeded write per case to
// test arithmetic that never touches storage.
func TestPlanVersionReconcile(t *testing.T) {
	for _, test := range []struct {
		name          string
		cliVersion    string
		recorded      string
		highWaterMark string
		want          issueops.VersionReconcileResult
		wantVersion   bool
		wantMark      bool
	}{
		{
			name:        "a workspace with no markers records both",
			cliVersion:  "1.2.0",
			want:        issueops.VersionReconcileResult{Previous: "", Current: "1.2.0", Migrated: true},
			wantVersion: true,
			wantMark:    true,
		},
		{
			name:          "the same version again is a no-op",
			cliVersion:    "1.2.0",
			recorded:      "1.2.0",
			highWaterMark: "1.2.0",
			want:          issueops.VersionReconcileResult{Previous: "1.2.0", Current: "1.2.0"},
		},
		{
			name:          "an upgrade advances both markers",
			cliVersion:    "1.3.0",
			recorded:      "1.2.0",
			highWaterMark: "1.2.0",
			want:          issueops.VersionReconcileResult{Previous: "1.2.0", Current: "1.3.0", Migrated: true},
			wantVersion:   true,
			wantMark:      true,
		},
		{
			name:          "a binary older than the marker is refused",
			cliVersion:    "1.2.0",
			recorded:      "1.3.0",
			highWaterMark: "1.3.0",
			want:          issueops.VersionReconcileResult{Previous: "1.3.0", Current: "1.3.0", Downgrade: true},
		},
		{
			// The state only something outside this role produces, and the
			// whole reason the second marker exists: the marker says 1.2.0 and
			// would accept 1.3.0, the mark remembers that 1.4.0 has already
			// prepared this workspace.
			name:          "a binary older than the mark is refused even when the marker would accept it",
			cliVersion:    "1.3.0",
			recorded:      "1.2.0",
			highWaterMark: "1.4.0",
			want:          issueops.VersionReconcileResult{Previous: "1.2.0", Current: "1.2.0", Downgrade: true},
		},
		{
			name:          "catching up to the mark moves the marker and leaves the mark",
			cliVersion:    "1.4.0",
			recorded:      "1.2.0",
			highWaterMark: "1.4.0",
			want:          issueops.VersionReconcileResult{Previous: "1.2.0", Current: "1.4.0", Migrated: true},
			wantVersion:   true,
			wantMark:      false,
		},
		{
			name:          "a pre-release records over the release it equals",
			cliVersion:    "1.4.0-rc1",
			recorded:      "1.4.0",
			highWaterMark: "1.4.0",
			want:          issueops.VersionReconcileResult{Previous: "1.4.0", Current: "1.4.0-rc1", Migrated: true},
			wantVersion:   true,
			wantMark:      false,
		},
		{
			name:          "a missing component reads as zero",
			cliVersion:    "1.4",
			recorded:      "1.4.0",
			highWaterMark: "1.4.0",
			want:          issueops.VersionReconcileResult{Previous: "1.4.0", Current: "1.4", Migrated: true},
			wantVersion:   true,
			wantMark:      false,
		},
		{
			name:          "a marker this role cannot parse reads as zero and is overwritten",
			cliVersion:    "1.4.0",
			recorded:      "unknown",
			highWaterMark: "unknown",
			want:          issueops.VersionReconcileResult{Previous: "unknown", Current: "1.4.0", Migrated: true},
			wantVersion:   true,
			wantMark:      true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanVersionReconcile(test.cliVersion, test.recorded, test.highWaterMark)
			if err != nil {
				t.Fatalf("PlanVersionReconcile(%q, %q, %q) error = %v",
					test.cliVersion, test.recorded, test.highWaterMark, err)
			}
			if plan.Result != test.want {
				t.Errorf("result = %+v, want %+v", plan.Result, test.want)
			}
			if plan.RecordVersion != test.wantVersion {
				t.Errorf("RecordVersion = %v, want %v", plan.RecordVersion, test.wantVersion)
			}
			if plan.RecordHighWaterMark != test.wantMark {
				t.Errorf("RecordHighWaterMark = %v, want %v", plan.RecordHighWaterMark, test.wantMark)
			}
		})
	}
}

// TestPlanVersionReconcileRefusesAnEmptyVersion pins the one validation
// failure, and pins that it plans no write: recording "" over a real marker
// would take the downgrade guard down with it.
func TestPlanVersionReconcileRefusesAnEmptyVersion(t *testing.T) {
	for _, cliVersion := range []string{"", "   "} {
		plan, err := PlanVersionReconcile(cliVersion, "1.2.0", "1.2.0")
		if !errors.Is(err, issueops.ErrValidation) {
			t.Fatalf("PlanVersionReconcile(%q, ...) error = %v, want ErrValidation", cliVersion, err)
		}
		if plan.RecordVersion || plan.RecordHighWaterMark {
			t.Fatalf("PlanVersionReconcile(%q, ...) plans a write: %+v", cliVersion, plan)
		}
	}
}
