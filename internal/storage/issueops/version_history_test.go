package issueops

import "testing"

// These tests pin the derivation rule for issue_versions.attribution_status,
// the NOT NULL column migration 0068 step 6 adds. Its vocabulary is BDP's
// carried-attribution status (gastownhall/bdp#18, merged 2026-09-07): exactly
// two values, "claimed" and "unknown". "imported" is provenance, not an
// assertion, and is not a status — it returns as a separate provenance marker
// in the phase that first imports history, for which no writer exists yet.
//
// RecordVersionInTx receives only a plain actor string from every call site
// (none passes any additional attribution context), so the only signal
// available to derive attribution_status from is whether actor is empty: a
// non-empty actor is "claimed", an empty one is "unknown".
func TestAttributionStatusForActor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		actor string
		want  string
	}{
		{name: "non-empty actor is claimed", actor: "alice", want: attributionStatusClaimed},
		{name: "empty actor is unknown", actor: "", want: attributionStatusUnknown},
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

// TestAttributionStatusValuesMatchBDP pins the two legal values verbatim
// against bdp#18's vocabulary, so a future edit cannot silently rename one,
// or reintroduce a value BDP does not carry, without failing here — and
// checks that the derivation never yields anything outside that set.
func TestAttributionStatusValuesMatchBDP(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"claimed": attributionStatusClaimed,
		"unknown": attributionStatusUnknown,
	}
	for want, got := range values {
		if got != want {
			t.Errorf("attribution status constant = %q, want %q", got, want)
		}
	}

	legal := map[string]bool{"claimed": true, "unknown": true}
	for _, actor := range []string{"", "alice", "agent:builder"} {
		if got := attributionStatusForActor(actor); !legal[got] {
			t.Errorf("attributionStatusForActor(%q) = %q, want one of claimed|unknown", actor, got)
		}
	}
}
