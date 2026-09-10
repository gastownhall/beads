package memoryops

import "testing"

// TestStorageKeyEncodesTheFullPrefix pins the encode against the constant
// rather than against a literal, and then pins the literal too. The constant
// alone would keep agreeing with itself if kvkeys were edited; the literal is
// what the rows in a shipped workspace actually say.
func TestStorageKeyEncodesTheFullPrefix(t *testing.T) {
	for _, test := range []struct{ userKey, want string }{
		{"dolt-phantoms", "kv.memory.dolt-phantoms"},
		{"issue_prefix", "kv.memory.issue_prefix"},
		{"Has Spaces.✓", "kv.memory.Has Spaces.✓"},
		{"a", "kv.memory.a"},
	} {
		if got := StorageKey(test.userKey); got != test.want {
			t.Fatalf("StorageKey(%q) = %q, want %q", test.userKey, got, test.want)
		}
	}
}

// TestMemoriesFromConfigNarrowsToTheMemoryPlane is the cheap half of the
// quadrant discipline the conformance contract applies against real backends:
// a config map holding all four classes at once, narrowed, with the other three
// expected to be absent.
//
// The three failures it exists to catch are each one character of difference
// and each produces a plausible-looking map: filtering on "kv." serves every
// `bd kv set` value as a memory, trimming "kv." while matching "kv.memory."
// re-keys every answer to "memory.<key>", and matching a settings row by
// substring instead of prefix puts the workspace's own configuration in the
// answer.
func TestMemoriesFromConfigNarrowsToTheMemoryPlane(t *testing.T) {
	all := map[string]string{
		// Settings rows.
		"issue_prefix":  "bd",
		"custom.limits": "10",
		// Generic kv rows — the neighbours one prefix up.
		"kv.deploy-target": "staging",
		"kv.a":             "generic",
		// Memory rows, including the shadow key: a memory NAMED after a
		// setting is a memory, and the setting is not.
		"kv.memory.dolt-phantoms": "Dolt phantom DBs hide in three places",
		"kv.memory.issue_prefix":  "the prefix rename runbook is in engdocs",
		"kv.memory.a":             "short key",
		"kv.memory.a-b":           "adjacent key",
		// A row whose key merely CONTAINS the prefix rather than starting
		// with it.
		"legacy.kv.memory.smuggled": "not a memory",
	}

	got := MemoriesFromConfig(all)

	want := map[string]string{
		"dolt-phantoms": "Dolt phantom DBs hide in three places",
		"issue_prefix":  "the prefix rename runbook is in engdocs",
		"a":             "short key",
		"a-b":           "adjacent key",
	}
	if len(got) != len(want) {
		t.Fatalf("MemoriesFromConfig = %v, want exactly %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("MemoriesFromConfig[%q] = %q, want %q", key, got[key], value)
		}
	}
}

// TestMemoriesFromConfigRoundTripsTheEncode pins the encode and the decode
// against EACH OTHER, which is the property the two implementations of the role
// depend on: the unit-of-work body composes the config use case rather than the
// InTx functions here, so what keeps the two routes seeing the same memories is
// that both go through this pair.
func TestMemoriesFromConfigRoundTripsTheEncode(t *testing.T) {
	for _, userKey := range []string{"dolt-phantoms", "issue_prefix", "Has Spaces.✓", "a", "kv.memory.nested"} {
		got := MemoriesFromConfig(map[string]string{StorageKey(userKey): "v"})
		if len(got) != 1 || got[userKey] != "v" {
			t.Fatalf("round trip of %q = %v, want a single entry under the original key", userKey, got)
		}
	}
}

// TestMemoriesFromConfigAnswersAnEmptyMap pins that a plane with nothing in it
// is an empty map rather than nil, so a caller can range over the answer
// without a guard.
func TestMemoriesFromConfigAnswersAnEmptyMap(t *testing.T) {
	if got := MemoriesFromConfig(map[string]string{"issue_prefix": "bd"}); got == nil || len(got) != 0 {
		t.Fatalf("MemoriesFromConfig with no memory rows = %v, want an empty non-nil map", got)
	}
	if got := MemoriesFromConfig(nil); got == nil || len(got) != 0 {
		t.Fatalf("MemoriesFromConfig(nil) = %v, want an empty non-nil map", got)
	}
}
