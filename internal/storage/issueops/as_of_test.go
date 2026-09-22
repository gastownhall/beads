package issueops

import "testing"

// be-95c0y FIX B: ValidateRef must reject a ref that looks like a truncated
// Dolt commit hash -- Dolt's AS OF does not support hash-prefix resolution,
// so a short 0-9a-v-alphabet ref (e.g. the 8-char prefix `bd history` used
// to print) always fails confusingly deep inside a SQL error. Rejecting it
// early with a specific, actionable message is the fix.

// TestValidateRefRejectsTruncatedHash covers acceptance criterion #2: a
// synthetic short string drawn from Dolt's commit-hash alphabet ([0-9a-v],
// see doltCommitHashRE in blocked_merge.go) must be rejected with the exact
// message specified for this fix, not the generic format error.
func TestValidateRefRejectsTruncatedHash(t *testing.T) {
	cases := []struct {
		name string
		ref  string
	}{
		{"8-char prefix (old bd history truncation)", "01234567"},
		{"31-char, one short of a full hash", "0123456789abcdefghijklmnopqrstu"},
		{"single character", "a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRef(tc.ref)
			if err == nil {
				t.Fatalf("ValidateRef(%q) = nil, want a truncated-hash error", tc.ref)
			}
			want := `"` + tc.ref + `" looks like a truncated commit hash -- Dolt's AS OF does not support hash-prefix resolution; use the full 32-character hash (bd history --json) or an exact branch name`
			if got := err.Error(); got != want {
				t.Errorf("ValidateRef(%q) error = %q, want %q", tc.ref, got, want)
			}
		})
	}
}

// TestValidateRefAcceptsExistingValidRefs is a regression guard for the FIX B
// heuristic: it must not start rejecting refs that were already valid and
// are not plausibly truncated hashes. Branch names here deliberately include
// a character outside Dolt's [0-9a-v] hash alphabet (a slash or dot) so they
// cannot collide with the new heuristic regex `^[0-9a-v]{1,31}$` -- a bare
// short alphanumeric branch name (e.g. "main") is a known false-positive of
// that heuristic and is intentionally not asserted here.
func TestValidateRefAcceptsExistingValidRefs(t *testing.T) {
	valid := []string{
		"release/v2.0",
		"feature/auth.flow",
		"wip/my-feature",
		"0123456789abcdefghijklmnopqrstuv", // full 32-char hash: must NOT be flagged as truncated
	}

	for _, ref := range valid {
		t.Run(ref, func(t *testing.T) {
			if err := ValidateRef(ref); err != nil {
				t.Errorf("ValidateRef(%q) = %v, want nil (valid ref must still pass)", ref, err)
			}
		})
	}
}
