//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestProxiedServerRetainedTypes is TestEmbeddedRetainedTypes on the proxied
// route, whose `bd gc` decay reads through its own unit of work rather than
// the store: with types.retained naming a type, a plain `bd prune` and
// `bd gc` keep that type's closed beads and report them as
// retention_required under --json.
func TestProxiedServerRetainedTypes(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "pk")
	// set-many writes the batch on its own unit of work on this route; it
	// refuses a malformed value as `bd config set` does.
	if out, err := bdProxiedRun(t, bd, p.dir, "config", "set-many", "types.retained=[chore]"); err == nil {
		t.Errorf("bd config set-many types.retained=[chore] succeeded:\n%s\nwant the malformed value refused", out)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "config", "set", "types.retained", "chore"); err != nil {
		t.Fatalf("bd config set types.retained: %v\n%s", err, out)
	}
	keyed := bdProxiedCreate(t, bd, p.dir, "keyed", "--id", "pk-keyed1", "--type", "chore").ID
	if out, err := bdProxiedRun(t, bd, p.dir, "close", keyed); err != nil {
		t.Fatalf("bd close %s: %v\n%s", keyed, err, out)
	}
	decayJSON := func(args ...string) map[string]any {
		t.Helper()
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, append(args, "--json")...)
		if err != nil {
			t.Fatalf("bd %s --json: %v\n%s\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(stdout[strings.Index(stdout, "{"):]), &payload); err != nil {
			t.Fatalf("bd %s --json output is not JSON: %v\n%s", strings.Join(args, " "), err, stdout)
		}
		return payload
	}

	pruned := decayJSON("prune", "--pattern", keyed, "--force")
	if pruned["pruned_count"] != float64(0) || pruned["retention_required"] != float64(1) {
		t.Errorf("bd prune --force --json = %v, want pruned_count 0 and retention_required 1", pruned)
	}
	// --older-than 0 selects closed_at before now cut to the second, and a
	// close stamp is stored rounded half-up to the second, so a bead closed
	// in the last 1.5 s can still read as not closed before the cutoff.
	time.Sleep(2 * time.Second)
	report := decayJSON("gc", "--older-than", "0", "--force", "--skip-dolt")
	if report["retention_required"] != float64(1) {
		t.Errorf("bd gc --json = %v, want retention_required 1", report)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "show", keyed, "--json"); err != nil {
		t.Errorf("bd show %s after a plain prune and gc: %v\n%s; a retained bead was deleted", keyed, err, out)
	}
}
