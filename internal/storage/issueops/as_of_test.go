package issueops

import "testing"

// be-95c0y FIX B: ValidateRef must reject a ref that looks like a truncated
// Dolt commit hash -- Dolt's AS OF does not support hash-prefix resolution,
// so a short 0-9a-v-alphabet ref (e.g. the 8-char prefix `bd history` used
// to print) always fails confusingly deep inside a SQL error. Rejecting it
// early with a specific, actionable message is the fix.
//
// be-3phh0 round 2: round 1's heuristic (`^[0-9a-v]{1,31}$`) was too broad --
// it also rejected real short branch names ("main") and pre-existing
// alphanumeric test values ("abc123def456"), breaking the PRE-EXISTING
// internal/storage/dolt/dolt_test.go:TestValidateRef "valid hash"/"valid
// branch" subtests (not owned by this diff, not to be edited). The narrowed
// heuristic (`^[0-9a-v]{16,31}$`) only fires at 16+ chars -- well past
// "main"(4) or "abc123def456"(12) -- trading away the specific error for
// short hand-typed truncations (the original 8-char example; disclosed,
// accepted tradeoff since FIX A already removed the only first-party source
// of an 8-char truncated hash) in exchange for never false-positiving on a
// real short branch name again.

// TestValidateRefRejectsTruncatedHash covers acceptance criterion #2: a
// synthetic 16-31 character string drawn from Dolt's commit-hash alphabet
// ([0-9a-v], see doltCommitHashRE in blocked_merge.go) must be rejected with
// the exact message specified for this fix, not the generic format error.
// Cases below span the narrowed round-2 range (16-31); shorter strings that
// round 1 wrongly rejected are covered instead by
// TestValidateRefAcceptsExistingValidRefs.
func TestValidateRefRejectsTruncatedHash(t *testing.T) {
	cases := []struct {
		name string
		ref  string
	}{
		{"16-char, new minimum length for the truncated-hash heuristic", "0123456789abcdef"},
		{"24-char, mid-range example", "0123456789abcdefghijklmn"},
		{"31-char, one short of a full hash", "0123456789abcdefghijklmnopqrstu"},
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
// are not plausibly truncated hashes. Most branch names here deliberately
// include a character outside Dolt's [0-9a-v] hash alphabet (a slash or dot)
// so they cannot collide with the heuristic regex regardless of its length
// bounds. "main" is the exception, added deliberately: round 1's
// `^[0-9a-v]{1,31}$` heuristic rejected it (a known false-positive the round
// 1 version of this test admitted to and intentionally did not assert
// against) and it is exactly the case the pre-existing
// internal/storage/dolt/dolt_test.go:TestValidateRef/valid_branch subtest
// caught. Asserting it here (be-3phh0 round 2) closes that gap so a future
// change to the heuristic's lower bound cannot silently reopen it.
func TestValidateRefAcceptsExistingValidRefs(t *testing.T) {
	valid := []string{
		"release/v2.0",
		"feature/auth.flow",
		"wip/my-feature",
		"main",                             // bare short alphanumeric branch name: round 1's false-positive case
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
