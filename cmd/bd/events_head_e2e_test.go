//go:build cgo && unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEventsHeadReportsHeadAndFloorWithoutReplayingRows is the bug as a
// consumer meets it (#7084): a checkpoint past the head returns nothing
// without saying where the head is, and the only other way to learn it is
// replaying the whole retained window from --since 0 (or bisecting with
// --limit 1 probes). `bd events head` answers both without reading a single
// journal row.
func TestEventsHeadReportsHeadAndFloorWithoutReplayingRows(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "evh", "--skip-hooks", "--skip-agents")

	env := append(autoPruneTestEnv(dir), "BD_EVENTS_JOURNAL=1")
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	decodeHead := func(out string) (head, floor int64) {
		t.Helper()
		var h struct {
			Head  int64 `json:"head"`
			Floor int64 `json:"floor"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &h); err != nil {
			t.Fatalf("events head output is not a JSON object: %v\n%s", err, out)
		}
		return h.Head, h.Floor
	}

	// A never-written journal: head and floor are both 0.
	out, err := run("events", "head", "--json")
	if err != nil {
		t.Fatalf("events head on an empty journal: %v\n%s", err, out)
	}
	if head, floor := decodeHead(out); head != 0 || floor != 0 {
		t.Fatalf("events head on empty journal = (%d, %d), want (0, 0)", head, floor)
	}

	for i := range 3 {
		if out, err := run("create", fmt.Sprintf("t%d", i)); err != nil {
			t.Fatalf("create %d: %v\n%s", i, err, out)
		}
	}

	// The repro's complaint: a checkpoint past the head returns nothing and
	// says nothing about where the head is.
	out, err = run("events", "tail", "--since", "1000000", "--json")
	if err != nil {
		t.Fatalf("tail past head: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("tail past head printed something, want nothing: %q", out)
	}

	// events head answers it directly, without replaying anything.
	out, err = run("events", "head", "--json")
	if err != nil {
		t.Fatalf("events head after 3 creates: %v\n%s", err, out)
	}
	head, floor := decodeHead(out)
	if head != 3 {
		t.Fatalf("events head after 3 creates: head = %d, want 3", head)
	}
	if floor != 1 {
		t.Fatalf("events head after 3 creates: floor = %d, want 1 (nothing pruned)", floor)
	}

	// A consumer that reads the head once can then follow everything
	// committed after this moment with a plain tail — no replay, no probing.
	if out, err := run("create", "after the head read"); err != nil {
		t.Fatalf("create after head read: %v\n%s", err, out)
	}
	out, err = run("events", "tail", "--since", itoa(head), "--json")
	if err != nil {
		t.Fatalf("tail --since head: %v\n%s", err, out)
	}
	records := decodeEventRecords(t, out)
	if len(records) != 1 {
		t.Fatalf("tail --since head returned %d record(s), want exactly the one mutation after it", len(records))
	}

	// Non-JSON mode prints a human line rather than failing.
	textOut, err := run("events", "head")
	if err != nil {
		t.Fatalf("events head (text mode): %v\n%s", err, textOut)
	}
	if !strings.Contains(textOut, "head=") || !strings.Contains(textOut, "floor=") {
		t.Fatalf("events head text output missing head=/floor=: %q", textOut)
	}
}
