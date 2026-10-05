package wireshape_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/wireshape"
)

// TestWireShapeDigest is the drift gate ContextResponse.wire_revision
// documents: it fails the moment the shape of any EXISTING response member
// changes without CurrentWireRevision increasing to match.
//
// It is deliberately two separate comparisons rather than one struct-equal,
// because the two ways this can go red call for different instructions. A
// changed or removed entry at the SAME wire_revision is drift nobody signed
// off on — regenerate only after bumping CurrentWireRevision (see
// internal/httpapi/wire_revision.go) and the `wire_revision` property's
// revision table in openapi.v0.yaml, never before. A changed wire_revision
// with an unchanged entry set (or only additions) means this golden is simply
// stale — regenerate it with:
//
//	go run ./internal/httpapi/wireshape/cmd/gendigest
func TestWireShapeDigest(t *testing.T) {
	golden := loadGolden(t)
	got, err := wireshape.Compute(httpapi.CurrentWireRevision)
	if err != nil {
		t.Fatalf("compute digest: %v", err)
	}

	if len(got.Entries) < 20 {
		// The document describes dozens of response members; a near-empty
		// digest means the walk found nothing, which would make every
		// assertion below pass vacuously.
		t.Fatalf("digest has %d entries, want the full walk", len(got.Entries))
	}

	wantByKey := map[string]wireshape.Entry{}
	for _, e := range golden.Entries {
		wantByKey[e.Schema+"\x00"+e.Member] = e
	}
	gotByKey := map[string]wireshape.Entry{}
	for _, e := range got.Entries {
		gotByKey[e.Schema+"\x00"+e.Member] = e
	}

	var changed, removed, added []string
	for key, want := range wantByKey {
		got, ok := gotByKey[key]
		if !ok {
			removed = append(removed, key)
			continue
		}
		if !reflect.DeepEqual(want, got) {
			changed = append(changed, key)
		}
	}
	for key := range gotByKey {
		if _, ok := wantByKey[key]; !ok {
			added = append(added, key)
		}
	}

	if len(changed) > 0 || len(removed) > 0 {
		if got.WireRevision <= golden.WireRevision {
			t.Errorf("response member shape changed without a wire_revision bump: changed=%v removed=%v\n"+
				"bump CurrentWireRevision (internal/httpapi/wire_revision.go) and the revision table "+
				"in openapi.v0.yaml's `wire_revision` property, THEN regenerate the golden with "+
				"`go run ./internal/httpapi/wireshape/cmd/gendigest`", changed, removed)
		} else {
			t.Errorf("response member shape changed (changed=%v removed=%v) and wire_revision moved "+
				"%d -> %d, but the golden was not regenerated: run "+
				"`go run ./internal/httpapi/wireshape/cmd/gendigest` and commit the result",
				changed, removed, golden.WireRevision, got.WireRevision)
		}
	} else if len(added) > 0 && got.WireRevision == golden.WireRevision {
		t.Errorf("response members were added (%v) but the golden was not regenerated: "+
			"this is additive and needs no wire_revision bump, but still run "+
			"`go run ./internal/httpapi/wireshape/cmd/gendigest` and commit the result", added)
	}

	if got.WireRevision != golden.WireRevision && len(changed) == 0 && len(removed) == 0 && len(added) == 0 {
		t.Errorf("CurrentWireRevision is %d but the golden still says %d, with no shape change to justify "+
			"either: regenerate with `go run ./internal/httpapi/wireshape/cmd/gendigest`",
			got.WireRevision, golden.WireRevision)
	}
}

func loadGolden(t *testing.T) wireshape.Digest {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join("testdata", "golden.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var d wireshape.Digest
	if err := json.Unmarshal(blob, &d); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	return d
}
