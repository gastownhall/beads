package historyoffequiv

import (
	"encoding/json"
	"slices"
	"testing"
)

// TestGoldenChildCountersFingerprintMatchesAcrossLegs: every leg runs the same
// scenario, which names ho-epic.1, and every create front door advances the
// parent's child counter for a caller-named child (#5122 for the store legs,
// bd-qp2au6 / #4750 for the unit-of-work leg). So the three goldens must pin
// one child_counters fingerprint; a leg that diverges has a create path that
// leaves the counter behind.
func TestGoldenChildCountersFingerprintMatchesAcrossLegs(t *testing.T) {
	legs := []string{"embedded", "server", "uow"}
	var want goldenTable
	for i, leg := range legs {
		b, err := goldenFS.ReadFile("golden/" + leg + ".json")
		if err != nil {
			t.Fatalf("read golden/%s.json: %v", leg, err)
		}
		var g golden
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatalf("golden/%s.json: %v", leg, err)
		}
		got, ok := g.Tables["child_counters"]
		if !ok {
			t.Fatalf("golden/%s.json has no child_counters table", leg)
		}
		if got.Rows == 0 {
			t.Errorf("golden/%s.json pins no child_counters row; the scenario's ho-epic.1 must advance ho-epic's counter", leg)
		}
		if i == 0 {
			want = got
			continue
		}
		if got.Rows != want.Rows || got.SHA256 != want.SHA256 || !slices.Equal(got.Columns, want.Columns) {
			t.Errorf("golden/%s.json child_counters = %d rows %s %v, want golden/%s.json's %d rows %s %v",
				leg, got.Rows, got.SHA256, got.Columns, legs[0], want.Rows, want.SHA256, want.Columns)
		}
	}
}
