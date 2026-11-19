package historyoffequiv

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Every leg's scenario creates ho-epic.1 under ho-epic with the ID set by the
// caller, so every leg must leave the same child_counters fingerprint: the row
// (ho-epic, 1), digested the way checkGolden digests a table. Before the
// proxied create path advanced the counter for a caller-named child
// (GH#4750), the unit-of-work golden pinned zero rows here while the embedded
// and server goldens pinned this one.
func TestGoldenChildCountersFingerprintMatchesAcrossLegs(t *testing.T) {
	parent, last := "ho-epic", "1"
	want := goldenTable{
		Columns: []string{"parent_id", "last_child"},
		Rows:    1,
		SHA256:  digest([][]*string{{&parent, &last}}),
	}
	for _, leg := range []string{"embedded", "server", "uow"} {
		b, err := goldenFS.ReadFile("golden/" + leg + ".json")
		if err != nil {
			t.Fatalf("golden/%s.json: %v", leg, err)
		}
		var g golden
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatalf("golden/%s.json: %v", leg, err)
		}
		got, ok := g.Tables["child_counters"]
		if !ok {
			t.Errorf("golden/%s.json has no child_counters table", leg)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("golden/%s.json child_counters = %+v, want %+v (the row (ho-epic, 1))", leg, got, want)
		}
	}
}
