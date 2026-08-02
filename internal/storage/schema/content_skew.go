package schema

import "sort"

// ContentHashSkew returns the migration versions that have a recorded content
// hash on BOTH sides but whose hashes DIFFER — i.e. two clones applied divergent
// content for the same migration version. That is the silent schema fork from
// gastownhall/beads#4259: two clones report the same MAX(version) yet ran
// different migration content, which only surfaces (cryptically) when a merge
// fails.
//
// Versions missing from either side, or carrying an empty/unknown (NULL) hash on
// either side, are ignored — only a definite hash-vs-hash mismatch counts. The
// result is sorted ascending. It is a pure comparison so any caller (a doctor
// check, a future pre-merge gate, either storage mode) can reuse it.
func ContentHashSkew(local, remote map[int]string) []int {
	var skewed []int
	for version, localHash := range local {
		remoteHash, ok := remote[version]
		if !ok || localHash == "" || remoteHash == "" {
			continue
		}
		if localHash != remoteHash {
			skewed = append(skewed, version)
		}
	}
	sort.Ints(skewed)
	return skewed
}
