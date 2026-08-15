//go:build cgo && unix

package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestAutoPruneProxiedServer is the auto-prune guard for bd's SECOND write
// plumbing.
//
// It is not a duplicate of the embedded test. Proxied mode opens no store at
// all: maintenance runs through the unit-of-work provider's own transaction,
// which has to commit the delete into the working set WITHOUT minting a Dolt
// commit (the ephemeral form the leases heartbeat uses) — and it is the one
// topology where the journal's writer is a short-lived CLI process against a
// long-lived SQL server, so a missing trigger here means a workspace that
// journals fast and never prunes.
func TestAutoPruneProxiedServer(t *testing.T) {
	requireProxiedServerEnv(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "pap")

	// Every knob is set explicitly, auto-prune included. bdProxiedEnv passes the
	// ambient environment through and these extras are appended (last wins), so
	// stating the default is what stops a developer with
	// BD_EVENTS_JOURNAL_AUTO_PRUNE exported from silently inverting the case
	// under test.
	base := []string{
		"BD_EVENTS_JOURNAL=1",
		"BD_EVENTS_JOURNAL_RETAIN_DAYS=0",
		"BD_EVENTS_JOURNAL_RETAIN_ROWS=2",
		"BD_EVENTS_JOURNAL_AUTO_PRUNE=1",
	}
	noAutoPrune := append(append([]string{}, base...), "BD_EVENTS_JOURNAL_AUTO_PRUNE=0")
	mustRun := func(env []string, args ...string) string {
		t.Helper()
		stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, env, args...)
		if err != nil {
			t.Fatalf("bd %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
		}
		return stdout
	}

	// Build the backlog with auto-prune off. Nothing is pruned, and — because
	// the throttle watermark is only ever stamped by a pass that runs — the
	// next command with it on is this workspace's first pass.
	for i := range 5 {
		mustRun(noAutoPrune, "create", fmt.Sprintf("backlog %d", i), "--silent")
	}
	if records := decodeEventRecords(t, mustRun(noAutoPrune, "events", "export")); len(records) < 5 {
		t.Fatalf("auto-prune disabled still deleted records: %d survive a floor of 2", len(records))
	}

	mustRun(base, "create", "the trigger", "--silent")

	stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, base, "events", "export", "--json")
	if err == nil {
		t.Fatalf("export from seq 0 succeeded after a proxied mutation; the prefix should be gone\n%s", stdout)
	}
	var trunc struct {
		Code  string `json:"code"`
		Floor int64  `json:"floor"`
		Head  int64  `json:"head"`
	}
	if decErr := json.Unmarshal([]byte(firstJSONObject(stdout+stderr)), &trunc); decErr != nil {
		t.Fatalf("export failure is not a JSON object: %v\nstdout:\n%s\nstderr:\n%s", decErr, stdout, stderr)
	}
	if trunc.Code != storage.EventsJournalTruncatedCode {
		t.Fatalf("code = %q, want %q\nstdout:\n%s\nstderr:\n%s", trunc.Code, storage.EventsJournalTruncatedCode, stdout, stderr)
	}
	if trunc.Floor <= 1 {
		t.Fatalf("floor = %d: the proxied post-command trigger pruned nothing", trunc.Floor)
	}
	if retained := trunc.Head - trunc.Floor + 1; retained != 2 {
		t.Errorf("journal retained %d records, want the 2 the rows floor protects (floor %d, head %d)",
			retained, trunc.Floor, trunc.Head)
	}

	// The retained window is intact and resumable: maintenance took a prefix,
	// not an arbitrary subset.
	resumed := decodeEventRecords(t, mustRun(base, "events", "tail", "--since", itoa(trunc.Floor-1)))
	if len(resumed) != 2 || resumed[0].Seq != trunc.Floor {
		t.Fatalf("resume from the floor returned %+v, want the 2 retained records from seq %d", resumed, trunc.Floor)
	}
}
