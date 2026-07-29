package kvkeys

import "testing"

// TestMemoryConfigKeyPrefix pins the canonical config-table prefix for
// persistent memories. The merge resolver auto-resolves config conflicts only
// when every conflicted key carries this prefix (GH#2474); if it ever drifts
// from what cmd/bd actually writes, memory conflicts silently fall back to the
// operator and the pull/sync config wedge returns for the renamed keys. This
// test makes such a rename a conscious, caught change rather than a silent one.
func TestMemoryConfigKeyPrefix(t *testing.T) {
	if got, want := MemoryConfigKeyPrefix, "kv.memory."; got != want {
		t.Fatalf("MemoryConfigKeyPrefix = %q, want %q (renaming it re-wedges memory merge conflicts)", got, want)
	}
	if MemoryConfigKeyPrefix != Prefix+MemoryPrefix {
		t.Fatalf("MemoryConfigKeyPrefix %q must equal Prefix %q + MemoryPrefix %q",
			MemoryConfigKeyPrefix, Prefix, MemoryPrefix)
	}
}
