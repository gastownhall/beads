package tracker

import (
	"context"
	"slices"
	"testing"
)

// initWarnTracker is a tracker whose Init went on without answers it asked
// the remote for, and says so through InitWarner.
type initWarnTracker struct {
	*mockTracker
	warnings []string
}

func (m *initWarnTracker) InitWarnings() []string { return m.warnings }

// TestSyncReportsInitWarnings: each sync reports the tracker's Init warnings
// once, through OnWarning and in its result, a preview too. A tracker that
// does not implement InitWarner adds none.
func TestSyncReportsInitWarnings(t *testing.T) {
	ctx := context.Background()
	want := []string{"could not look up other/name", "a second gap"}
	for _, tc := range []struct {
		name    string
		tracker IssueTracker
		want    []string
	}{
		{"init warner", &initWarnTracker{mockTracker: newMockTracker("test"), warnings: want}, want},
		{"plain tracker", newMockTracker("test"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reported []string
			engine := NewEngine(tc.tracker, newPureTestStore(), "test-actor")
			engine.OnWarning = func(msg string) { reported = append(reported, msg) }
			for _, opts := range []SyncOptions{{Push: true, DryRun: true}, {Push: true}} {
				reported = nil
				result, err := engine.Sync(ctx, opts)
				if err != nil {
					t.Fatalf("Sync(%+v): %v", opts, err)
				}
				if !slices.Equal(reported, tc.want) || !slices.Equal(result.Warnings, tc.want) {
					t.Errorf("Sync(%+v) reported %q, result warnings %q; want %q in both",
						opts, reported, result.Warnings, tc.want)
				}
			}
		})
	}
}
