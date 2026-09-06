package issueops

import "testing"

// These tests pin the derivation rule for issue_versions.attribution_status
// (design §16.4 on be-hs42e.3, R14): a NOT NULL column added by migration
// 0068 step 6. Phase 2 is the sole writer of this column and, per §16.4,
// writes only three of its four legal values — "imported" is reserved for a
// later phase's import/backfill tooling and is never minted here.
//
// RecordVersionInTx currently receives only a plain actor string from every
// call site (7 production call sites, none passing any additional
// attribution context), so the only signal available to derive
// attribution_status from, without widening this bead's diff into a
// call-site-by-call-site attribution audit, is whether actor is empty.
// "not_supplied" would assert a confident, deliberate absence of attribution
// that no current call site actually claims; "undetermined" is the honest,
// conservative reading for a call site that simply has no actor threaded to
// it. attributionStatusForActor and its constants do not exist yet: this
// file is RED until version_history.go defines them.
func TestAttributionStatusForActor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		actor string
		want  string
	}{
		{name: "non-empty actor is supplied", actor: "alice", want: attributionStatusSupplied},
		{name: "empty actor is undetermined", actor: "", want: attributionStatusUndetermined},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := attributionStatusForActor(tc.actor); got != tc.want {
				t.Errorf("attributionStatusForActor(%q) = %q, want %q", tc.actor, got, tc.want)
			}
		})
	}
}

// TestAttributionStatusValuesMatchR14 pins the four legal values verbatim
// against design §16.4's own text, so a future edit cannot silently rename
// or drop one without failing here.
func TestAttributionStatusValuesMatchR14(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"supplied":     attributionStatusSupplied,
		"not_supplied": attributionStatusNotSupplied,
		"undetermined": attributionStatusUndetermined,
		"imported":     attributionStatusImported,
	}
	for want, got := range values {
		if got != want {
			t.Errorf("attribution status constant = %q, want %q", got, want)
		}
	}
}
