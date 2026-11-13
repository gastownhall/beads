package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDeletedReferencePatternAgreesWithCitesID pins the regular-expression
// spelling of the citation rule to the rule itself: wherever one says a text
// cites an id, so does the other.
func TestDeletedReferencePatternAgreesWithCitesID(t *testing.T) {
	cases := []struct {
		text string
		id   string
	}{
		{"step fx-k0008.1.2 failed", "fx-k0008.1"},
		{"step fx-k0008.1.2 failed", "fx-k0008"},
		{"step fx-k0008.1.2 failed", "fx-k0008.1.2"},
		{"was [deleted:fx-k0008.1.2]", "fx-k0008.1"},
		{"see fx-k0008.1.", "fx-k0008.1"},
		{"see be-1...", "be-1"},
		{"(be-1)", "be-1"},
		{"..be-1", "be-1"},
		{".be-1", "be-1"},
		{"x.be-1", "be-1"},
		{"bd-1.bd-2", "bd-1"},
		{"bd-1.bd-2", "bd-2"},
		{"be-1.a be-1._x be-1.-x", "be-1"},
		{"xbe-1 be-12 x-be-1 be-1-x be-1_x", "be-1"},
		{"«be-1»", "be-1"},
		{"see a+b(1)", "a+b(1)"},
		{"be-1", "be-1"},
		{"", "be-1"},
	}
	for _, tc := range cases {
		got := DeletedReferencePattern(tc.id).MatchString(tc.text)
		if want := types.CitesID(tc.text, tc.id); got != want {
			t.Errorf("DeletedReferencePattern(%q).MatchString(%q) = %v, types.CitesID = %v", tc.id, tc.text, got, want)
		}
	}
}
