package issueops

import "testing"

// TestValidateRefAcceptsWellFormedRefs pins ValidateRef to its documented
// contract: it checks syntax only. It is the shared ref guard for AS OF,
// dolt_diff, the fast-forward path, CommitExists' hash-prefix lookup and
// federation remote refs, so it must never reject a well-formed ref by
// guessing what the ref means. Refs spelled entirely within Dolt's [0-9a-v]
// hash alphabet are the case that matters: no shape check can tell such a
// branch name from a hash prefix, and CommitExists is documented to accept
// prefixes. The rejection cases are covered by TestValidateRef in
// internal/storage/dolt.
func TestValidateRefAcceptsWellFormedRefs(t *testing.T) {
	valid := []string{
		"main",
		"release/v2.0",
		"feature/auth.flow",
		"wip/my-feature",
		// 16-character branch names spelled entirely within Dolt's [0-9a-v]
		// hash alphabet, so nothing about their shape separates them from a
		// hash prefix.
		"productionmirror",
		"releasecandidate",
		"integrationtests",
		// Hash prefixes, which CommitExists resolves with a LIKE match.
		"0123456789abcdef",
		"0123456789abcdefghijklmnopqrstu",
		// A full 32-character Dolt commit hash.
		"0123456789abcdefghijklmnopqrstuv",
	}

	for _, ref := range valid {
		t.Run(ref, func(t *testing.T) {
			if err := ValidateRef(ref); err != nil {
				t.Errorf("ValidateRef(%q) = %v, want nil", ref, err)
			}
		})
	}
}
