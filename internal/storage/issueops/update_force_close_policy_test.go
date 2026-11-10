package issueops

import (
	"testing"

	publicops "github.com/steveyegge/beads/issueops"
)

func TestPopForceClosePolicy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		updates   map[string]interface{}
		want      bool
		wantAfter bool // whether the key survives the pop
	}{
		{"absent", map[string]interface{}{"status": "closed"}, false, false},
		{"true", map[string]interface{}{"status": "closed", OpForceClosePolicy: true}, true, false},
		{"false", map[string]interface{}{"status": "closed", OpForceClosePolicy: false}, false, false},
		{"non-bool survives", map[string]interface{}{"status": "closed", OpForceClosePolicy: "yes"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := PopForceClosePolicy(tc.updates)
			if got != tc.want {
				t.Errorf("PopForceClosePolicy() = %v, want %v", got, tc.want)
			}
			if _, present := tc.updates[OpForceClosePolicy]; present != tc.wantAfter {
				t.Errorf("key present after pop = %v, want %v", present, tc.wantAfter)
			}
			if tc.updates["status"] != "closed" {
				t.Errorf("pop disturbed the rest of the map: %v", tc.updates)
			}
		})
	}
}

// TestForceClosePolicyKeyIsNotAnUpdateField pins the fail-loud half of the
// reserved-key transport. Neither write funnel names the override as a column,
// so an occurrence that survives to field validation is refused instead of
// being written or quietly dropped. That is what makes a caller which forgets
// to plumb force break visibly rather than run unforced.
func TestForceClosePolicyKeyIsNotAnUpdateField(t *testing.T) {
	t.Parallel()
	if IsAllowedUpdateField(OpForceClosePolicy) {
		t.Fatalf("%q is an allowed update field; an unpopped override would be written as a column", OpForceClosePolicy)
	}
}

// TestCloneUpdateRequestCarriesForceClosePolicy keeps the override inside the
// snapshot every transaction attempt is cloned from. A clone that rebuilt the
// request field by field and missed this one would drop force silently on
// retry — the shape of failure this whole transport exists to prevent.
func TestCloneUpdateRequestCarriesForceClosePolicy(t *testing.T) {
	t.Parallel()
	request := publicops.UpdateRequest{Actor: "actor", IssueID: "bd-clone", ForceClosePolicy: true}
	if !CloneUpdateRequest(request).ForceClosePolicy {
		t.Fatal("CloneUpdateRequest() dropped ForceClosePolicy")
	}
	if CloneUpdateRequest(publicops.UpdateRequest{Actor: "actor", IssueID: "bd-clone"}).ForceClosePolicy {
		t.Fatal("CloneUpdateRequest() invented ForceClosePolicy")
	}
}
