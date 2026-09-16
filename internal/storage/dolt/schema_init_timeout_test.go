package dolt

import (
	"testing"
	"time"
)

// TestSchemaInitTimeoutHasLoadHeadroom guards be-696w: testTimeout (dolt_test.go)
// bounds every migrating store-open in this package, directly or via
// testContext. At 45s it was measured too tight under real host contention —
// cross_project_test.go and TestMigratingOpen_FirstReadSucceeds both failed
// with "failed to initialize schema: context deadline exceeded" at exactly
// that bound (see be-696w). This is a floor, not an exact-value check, so a
// future deliberate increase does not need to touch this test — only a
// regression back toward the too-tight value trips it.
func TestSchemaInitTimeoutHasLoadHeadroom(t *testing.T) {
	const minTestTimeout = 90 * time.Second
	if testTimeout < minTestTimeout {
		t.Fatalf("testTimeout = %s, want >= %s (be-696w: 45s measured too tight under host contention)",
			testTimeout, minTestTimeout)
	}
}
