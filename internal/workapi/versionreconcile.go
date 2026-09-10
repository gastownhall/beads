package workapi

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/issueops"
)

// This file holds the DECISION half of issueops.VersionReconciler: given the
// running binary's version and the two markers a workspace currently holds,
// what should happen. It is a pure function for the reason BuildCountFilter and
// ValidateCountGroup are — every implementation decides through it, so the
// answer cannot differ by backend, and the cases that matter (a downgrade below
// the marker, a downgrade below the mark, a catch-up to the mark) are pinned in
// milliseconds without a database.

// MetadataKeyVersion and MetadataKeyVersionMax are the two clone-local,
// dolt-ignored keys the version markers live under. Three implementations and
// three conformance wirings write them, and a key spelled differently in one of
// those places is a marker nothing else can find — which looks exactly like a
// workspace that was never reconciled.
const (
	MetadataKeyVersion    = "bd_version"
	MetadataKeyVersionMax = "bd_version_max"
)

// VersionReconcilePlan is what a reconciliation decided, plus which of the two
// markers that decision has to write. The writes are reported as flags rather
// than performed here because the three backends reach the metadata plane
// differently — two through a store handle, one through a unit of work.
type VersionReconcilePlan struct {
	// Result is what the caller returns, unchanged, once the writes below
	// succeed. It is complete before any write happens, so there is no field a
	// partial write could still be filling in.
	Result issueops.VersionReconcileResult
	// RecordVersion asks for MetadataKeyVersion to be set to Result.Current.
	// It is set only for a migration; a no-op and a refusal both write nothing.
	RecordVersion bool
	// RecordHighWaterMark asks for MetadataKeyVersionMax to be set to
	// Result.Current. It is a SEPARATE flag because the two do not always move
	// together: a workspace catching up to a mark a newer binary already left
	// moves the marker to the mark and leaves the mark where it is.
	RecordHighWaterMark bool
}

// ValidateReconcileVersion checks the version a caller wants to record, and
// returns it unchanged.
//
// It is separate from the planner so a body can refuse BEFORE it reads anything
// or opens a unit of work. This runs on every startup, so a refusal that costs
// a round trip is a refusal that costs every command.
//
// The only rule at this end is that a version has to name something. Recording
// "" over a real marker would silently take the downgrade guard down with it,
// and the empty marker it left behind is indistinguishable from a workspace
// nothing has ever reconciled.
func ValidateReconcileVersion(cliVersion string) (string, error) {
	if strings.TrimSpace(cliVersion) == "" {
		return "", fmt.Errorf("%w: cli version must not be empty", issueops.ErrValidation)
	}
	return cliVersion, nil
}

// PlanVersionReconcile decides what recording cliVersion should do to a
// workspace whose markers currently read recorded and highWaterMark.
//
// The order of the checks is the contract, not an implementation detail. An
// exact match short-circuits first, so the steady-state path — every startup
// that is not the first one after an upgrade — decides without consulting the
// mark at all. Both downgrade guards then run BEFORE any write is planned, so
// a refusal is a decision rather than a write that is undone.
func PlanVersionReconcile(cliVersion, recorded, highWaterMark string) (VersionReconcilePlan, error) {
	if _, err := ValidateReconcileVersion(cliVersion); err != nil {
		return VersionReconcilePlan{}, err
	}

	if recorded == cliVersion {
		return VersionReconcilePlan{
			Result: issueops.VersionReconcileResult{Previous: recorded, Current: recorded},
		}, nil
	}

	// A marker BELOW the running binary is the ordinary upgrade. A marker above
	// it means an older binary is opening a workspace a newer one has already
	// prepared, and relabelling it would make the next upgrade re-run work that
	// is already done. The mark catches the same thing after someone has since
	// moved the marker back down — the only reason the second key exists.
	refuse := (recorded != "" && compareDottedVersions(cliVersion, recorded) < 0) ||
		(highWaterMark != "" && compareDottedVersions(cliVersion, highWaterMark) < 0)
	if refuse {
		return VersionReconcilePlan{
			Result: issueops.VersionReconcileResult{Previous: recorded, Current: recorded, Downgrade: true},
		}, nil
	}

	return VersionReconcilePlan{
		Result:              issueops.VersionReconcileResult{Previous: recorded, Current: cliVersion, Migrated: true},
		RecordVersion:       true,
		RecordHighWaterMark: highWaterMark == "" || compareDottedVersions(cliVersion, highWaterMark) > 0,
	}, nil
}

// compareDottedVersions orders two release strings component by component,
// reading a missing or non-numeric component as 0.
//
// It is deliberately NOT a semver comparison, and issueops.VersionReconcileRequest
// says so where callers will read it: "1.2.0" and "1.2.0-rc1" compare EQUAL
// here, so a pre-release binary reconciles a workspace its release counterpart
// already recorded without either refusing the other. Tightening it would start
// refusing binaries that work today.
func compareDottedVersions(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var p1, p2 int
		if i < len(parts1) {
			_, _ = fmt.Sscanf(parts1[i], "%d", &p1)
		}
		if i < len(parts2) {
			_, _ = fmt.Sscanf(parts2[i], "%d", &p2)
		}
		if p1 < p2 {
			return -1
		}
		if p1 > p2 {
			return 1
		}
	}
	return 0
}
