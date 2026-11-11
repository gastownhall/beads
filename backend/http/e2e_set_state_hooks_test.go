//go:build cgo

package bdhttp_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestE2E_SetStateHooksOverHTTP pins what a hook-enabled `bd connect`
// workspace sees for a set-state (GH#5637): on_create for the event, on_update
// for the event carrying its parent-child edge to the subject, and on_update
// for the subject carrying its new label, for a durable subject and a wisp
// alike, and nothing for a refused set-state. That is the shape the direct and
// proxied-server routes pin (cmd/bd's
// TestEmbeddedSetStateHooksSeeTheEventItsEdgeAndTheSubject and
// TestProxiedServerSetState), so a script sees one shape on every route.
//
// Here the hooks run client-side over the http store (divergence ledger L11),
// and the batch-apply answer carries no row (L-apply-snapshot), so the hook
// layer reads each row it landed back from the server. Before it did, only the
// event's edge update fired on this route.
func TestE2E_SetStateHooksOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	if runtime.GOOS == "windows" {
		t.Skip("hook script form is POSIX shell")
	}
	bin := buildBD(t)
	f := newHTTPFixture(t, bin, startE2EServer(t))
	durable := f.create("Hooked durable subject", "-t", "task")
	wisp := f.create("Hooked wisp subject", "-t", "task", "--ephemeral")
	beadsDir := filepath.Join(f.dir, ".beads")

	for _, subject := range []string{durable, wisp} {
		// Planted after the setup, so every firing it saves is this
		// set-state's.
		logDir := plantHookRecorder(t, beadsDir)
		res := f.mustRun(nil, "set-state", subject, "patrol=muted", "--json")
		eventID := firstJSONField(t, res.stdout, "event_id")
		if eventID == "" {
			t.Fatalf("set-state %s --json named no event: %s", subject, res.stdout)
		}
		assertSetStateHookFirings(t, readHookFirings(t, logDir, 3), eventID, subject, "patrol:muted")
	}

	logDir := plantHookRecorder(t, beadsDir)
	if r := f.run(nil, "set-state", durable, "patrol="+strings.Repeat("v", 260)); r.code == 0 {
		t.Fatalf("set-state with a 267-character label succeeded, want it refused: stdout=%s", r.stdout)
	}
	readHookFirings(t, logDir, 0)
}

// hookFiring is one hook run a plantHookRecorder script saved: the event and
// the issue id the runner passed it as $2 and $1, and the issue payload it
// read on stdin.
type hookFiring struct {
	event, id string
	payload   hookPayload
}

// hookPayload is the part of a hook's issue payload the set-state hook
// contract reads.
type hookPayload struct {
	ID           string           `json:"id"`
	Labels       []string         `json:"labels"`
	Dependencies []hookDependency `json:"dependencies"`
}

type hookDependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

// plantHookRecorder installs on_create, on_update and on_close scripts that
// save each firing's payload to a file of its own, named for the event, the
// issue and the script's pid, and returns the directory they save into. A file
// per firing keeps hook processes that run at once from interleaving their
// writes.
func plantHookRecorder(t *testing.T, beadsDir string) string {
	t.Helper()
	logDir := t.TempDir()
	if strings.Contains(logDir, "'") {
		t.Fatalf("temp dir %q cannot be single-quoted in a hook script", logDir)
	}
	hooksDir := filepath.Join(beadsDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	script := "#!/bin/sh\ncat > '" + logDir + "'/\"$2 $1 $$.json\"\n"
	for _, name := range []string{"on_create", "on_update", "on_close"} {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(script), 0o755); err != nil { //nolint:gosec // G306: a hook must be executable to run at all
			t.Fatalf("plant %s: %v", name, err)
		}
	}
	return logDir
}

// readHookFirings waits for want saved firings and decodes them. The command
// has returned by then, and bd waits for the hooks it fired before it exits,
// so a count past want is a hook that fired too often.
func readHookFirings(t *testing.T, logDir string, want int) []hookFiring {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		entries, err := os.ReadDir(logDir)
		if err != nil {
			t.Fatalf("read the hook recorder's directory: %v", err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if len(names) > want {
			t.Fatalf("hooks fired %d times, want %d: %q", len(names), want, names)
		}
		if len(names) == want {
			if firings, ok := decodeHookFirings(logDir, names); ok {
				return firings
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("hooks fired %d times, want %d: %q", len(names), want, names)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// decodeHookFirings reads every saved firing, reporting false while a script
// is still writing one.
func decodeHookFirings(logDir string, names []string) ([]hookFiring, bool) {
	firings := make([]hookFiring, 0, len(names))
	for _, name := range names {
		fields := strings.Fields(strings.TrimSuffix(name, ".json"))
		if len(fields) != 3 {
			return nil, false
		}
		data, err := os.ReadFile(filepath.Join(logDir, name)) // #nosec G304 -- this test's own temp file
		if err != nil {
			return nil, false
		}
		firing := hookFiring{event: fields[0], id: fields[1]}
		if err := json.Unmarshal(data, &firing.payload); err != nil {
			return nil, false
		}
		firings = append(firings, firing)
	}
	return firings, true
}

// assertSetStateHookFirings checks one set-state's firings against the hook
// contract every route shares: on_create for the event, on_update for the
// event carrying its parent-child edge to the subject, which is the payload a
// script tells the changed bead by, and on_update for the subject carrying its
// new label. Nothing else fires.
func assertSetStateHookFirings(t *testing.T, firings []hookFiring, eventID, subject, label string) {
	t.Helper()
	got := map[string]int{}
	for _, firing := range firings {
		key := firing.event + " " + firing.id
		got[key]++
		if firing.payload.ID != firing.id {
			t.Errorf("%s: the payload names %q", key, firing.payload.ID)
		}
		switch key {
		case "update " + eventID:
			parent := hookDependency{IssueID: eventID, DependsOnID: subject, Type: "parent-child"}
			if !slices.Contains(firing.payload.Dependencies, parent) {
				t.Errorf("%s: payload dependencies = %+v, want the parent-child edge %s -> %s", key, firing.payload.Dependencies, eventID, subject)
			}
		case "update " + subject:
			if !slices.Contains(firing.payload.Labels, label) {
				t.Errorf("%s: payload labels = %v, want %s among them", key, firing.payload.Labels, label)
			}
		}
	}
	if want := map[string]int{"create " + eventID: 1, "update " + eventID: 1, "update " + subject: 1}; !maps.Equal(got, want) {
		t.Errorf("hooks fired %v, want %v", got, want)
	}
}
