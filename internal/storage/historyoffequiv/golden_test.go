package historyoffequiv

import (
	"encoding/json"
	"testing"
)

// TestGoldenChildCountersFingerprintMatchesAcrossLegs pins, with no database,
// that the three legs' goldens digest one identical child_counters table.
// The scenario names ho-epic.1 through a caller-named create, so every leg
// must pin the same (ho-epic, 1) counter row (bd-qp2au6, #4750): the storage
// legs' issueops create has advanced the counter since #5122, and the
// unit-of-work leg caught up when the proxied domain create learned to. A leg
// whose golden digests a different table is a leg whose flag-off writes
// diverge from the other two, whatever its differential run says today.
func TestGoldenChildCountersFingerprintMatchesAcrossLegs(t *testing.T) {
	digests := map[string]goldenTable{}
	for _, leg := range []string{"embedded", "server", "uow"} {
		b, err := goldenFS.ReadFile("golden/" + leg + ".json")
		if err != nil {
			t.Fatalf("no golden for leg %q: %v", leg, err)
		}
		var g golden
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatalf("golden/%s.json: %v", leg, err)
		}
		table, ok := g.Tables["child_counters"]
		if !ok {
			t.Fatalf("golden/%s.json has no child_counters table", leg)
		}
		digests[leg] = table
	}
	for _, leg := range []string{"server", "uow"} {
		if digests[leg].SHA256 != digests["embedded"].SHA256 || digests[leg].Rows != digests["embedded"].Rows {
			t.Errorf("golden/%s.json child_counters = {rows: %d, sha256: %s}, want the embedded leg's {rows: %d, sha256: %s}",
				leg, digests[leg].Rows, digests[leg].SHA256, digests["embedded"].Rows, digests["embedded"].SHA256)
		}
	}
	if digests["embedded"].Rows == 0 {
		t.Errorf("golden/embedded.json child_counters has no rows: the scenario's ho-epic.1 create must pin (ho-epic, 1)")
	}
}
