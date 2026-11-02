package schema

import (
	"testing"
)

const (
	migration0075Up        = "0075_widen_residual_content_columns.up.sql"
	migration0075Down      = "0075_widen_residual_content_columns.down.sql"
	ignoredMigration0031Up = "0031_widen_event_comment_columns.up.sql"
)

// TestLatestVersionIncludesMigration0075 pins the slots this migration claims,
// main 0075 and ignored 0031, superseding 0074's own version of this test.
// A hardcoded literal for the reason the earlier ones were: a drift to 75 for
// the wrong reason is caught by the column tests below failing to explain why
// 75 widens the residual content columns.
func TestLatestVersionIncludesMigration0075(t *testing.T) {
	const want = 75
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (residual content column widening)", got, want)
	}
	const wantIgnored = 31
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (event comment widening on fresh clones)", got, wantIgnored)
	}
}
