package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
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

func TestBuildDirectInfoSeparatesAccessFromDoltTopology(t *testing.T) {
	socket := "/tmp/shared-dolt.sock"
	got := buildDirectInfo("/repo/.beads/dolt", &configfile.Config{
		Backend:          configfile.BackendDolt,
		DoltMode:         configfile.DoltModeServer,
		DoltServerSocket: socket,
	})

	if got["access_mode"] != "direct" {
		t.Fatalf("access_mode = %v, want direct", got["access_mode"])
	}
	if got["dolt_mode"] != configfile.DoltModeServer {
		t.Fatalf("dolt_mode = %v, want server", got["dolt_mode"])
	}
	if got["transport"] != "unix_socket" || got["socket"] != socket {
		t.Fatalf("expected Unix-socket topology, got %#v", got)
	}

	out := captureStdout(t, func() error { return renderInfo(got, false, "/repo/.beads/dolt") })
	for _, want := range []string{"Access mode: direct", "Dolt mode: server", "Transport: unix_socket", "Socket: " + socket} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\nMode: direct") {
		t.Fatalf("ambiguous topology label remains in output:\n%s", out)
	}
}
