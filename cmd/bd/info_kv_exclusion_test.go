package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/kvkeys"
	"github.com/steveyegge/beads/internal/workapi"
)

// TestInfoConfigExcludesTheKVPlane pins that `bd info --json` does not serve
// memories.
//
// It asserts the FILTER both info routes now apply, rather than shelling out:
// the two call sites differ only in which seam supplies the map, and the thing
// that can regress is somebody re-inlining GetAllConfig at one of them.
//
// Why it matters more than an ordinary config read: the beads MCP server's
// get_schema_info tool runs `bd info --schema --json` and returns the parsed
// dict whole, so every memory key and VALUE landed in the transcript of any
// agent that asked a schema question — and `bd info` is the diagnostic people
// paste into bug reports.
func TestInfoConfigExcludesTheKVPlane(t *testing.T) {
	stored := map[string]string{
		"issue_prefix":    "bd",
		"custom.statuses": "awaiting_review",
		kvkeys.MemoryConfigKeyPrefix + "deploy-notes": "the staging deploy token is sk-live-000",
		kvkeys.Prefix + "release.channel":             "beta",
		// Near misses that must SURVIVE: the rule is a prefix, not a substring.
		"kvetch":                    "not under the kv prefix",
		"custom.mentions.kv.inside": "kept",
	}

	got := workapi.FilterSettingsEnumeration(stored)

	for key := range got {
		if strings.HasPrefix(key, kvkeys.Prefix) {
			t.Errorf("bd info would serve %q: the kv plane is not settings, and this map reaches "+
				"agent transcripts through the MCP get_schema_info tool", key)
		}
	}
	for _, want := range []string{"issue_prefix", "custom.statuses", "kvetch", "custom.mentions.kv.inside"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%q was dropped; only keys under %q may be", want, kvkeys.Prefix)
		}
	}
}
