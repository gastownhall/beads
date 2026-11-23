package storage

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestBatchClosePolicySnapshotAndForce(t *testing.T) {
	refs := map[string][]string{"blocked": {"external:remote:payments"}, "empty": {}}
	policy := NewBatchClosePolicy(refs)
	refs["blocked"][0] = "changed"
	delete(refs, "blocked")
	if err := policy.CheckClose("blocked", false); !errors.Is(err, ErrCloseBlocked) || !strings.Contains(err.Error(), "external:remote:payments") {
		// The snapshot owns its refs; caller mutation cannot change a refusal.
		t.Fatalf("close refusal = %v", err)
	}
	if err := policy.CheckClose("blocked", true); err != nil {
		t.Fatal(err)
	}
	if err := policy.CheckClose("eligible", false); err != nil {
		t.Fatal(err)
	}
	filter := types.WorkFilter{ExcludeIDs: []string{"existing"}}
	got := policy.FilterClaim(filter)
	if !reflect.DeepEqual(got.ExcludeIDs, []string{"existing", "blocked"}) {
		t.Fatalf("claim exclusions = %v", got.ExcludeIDs)
	}
	got.ExcludeIDs[0] = "changed"
	if filter.ExcludeIDs[0] != "existing" {
		t.Fatal("changed caller filter")
	}
}

func TestBatchClosePolicyUnsupportedStoreFailsClosed(t *testing.T) {
	// A decorator that cannot carry the policy must not silently drop it.
	var store struct{ DoltStorage }
	_, err := BatchCloserWithPolicy(&store, NewBatchClosePolicy(map[string][]string{"blocked": {"external:p:c"}}))
	var unsupported *ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want unsupported", err)
	}
}
