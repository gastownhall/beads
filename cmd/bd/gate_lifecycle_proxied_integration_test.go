//go:build cgo

package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

var gateCreatedIDRe = regexp.MustCompile(`Created gate (\S+) \(type:`)

// parseCreatedGateID extracts the gate id from `bd gate create`'s first output
// line. Downstream scripts parse the id from exactly this line, so the test
// deliberately goes through the printed text rather than --json.
func parseCreatedGateID(t *testing.T, out string) string {
	t.Helper()
	m := gateCreatedIDRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no 'Created gate <id>' line in gate create output:\n%s", out)
	}
	return m[1]
}

func gateReadyIDs(t *testing.T, bd string, p proxiedProject) map[string]bool {
	t.Helper()
	ids := map[string]bool{}
	for _, r := range bdProxiedReadyJSON(t, bd, p) {
		ids[r.ID] = true
	}
	return ids
}

// TestProxiedServerGateLifecycle journeys the newly-ported gate subcommands
// (create, show, add-waiter, resolve) end to end against a proxied server,
// pinning the exact output shapes the wyvern wheelhouse scripts consume.
func TestProxiedServerGateLifecycle(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	// The wh-heartbeat/wh-gate-sweep shape: a human gate blocks a bead until
	// someone resolves it; waiters registered on the gate are woken at resolve.
	t.Run("human_gate_journey", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ghj")
		target := bdProxiedCreate(t, bd, p.dir, "Blocked by human gate")

		if ids := gateReadyIDs(t, bd, p); !ids[target.ID] {
			t.Fatalf("target %s should be ready before the gate exists", target.ID)
		}

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir,
			"gate", "create", "--type=human", "--blocks", target.ID, "--reason", "Need design review")
		if err != nil {
			t.Fatalf("gate create failed: %v\nstderr:\n%s", err, stderr)
		}
		gateID := parseCreatedGateID(t, out)
		for _, want := range []string{
			"Blocks: " + target.ID,
			"Reason: Need design review",
			"Resolve with: bd gate resolve " + gateID,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("gate create output missing %q:\n%s", want, out)
			}
		}

		// ONE dolt commit for the whole create (issue + blocking edge).
		if ids := gateReadyIDs(t, bd, p); ids[target.ID] {
			t.Errorf("target %s must not be ready while the gate is open", target.ID)
		}

		// gate show: plain text markers the wheelhouse greps for.
		out, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "gate", "show", gateID)
		if err != nil {
			t.Fatalf("gate show failed: %v\nstderr:\n%s", err, stderr)
		}
		for _, want := range []string{
			gateID,
			"Status: open",
			"Await Type: human",
			"Reason: Need design review",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("gate show output missing %q:\n%s", want, out)
			}
		}

		// add-waiter, then again: second registration is a reported no-op.
		out, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "gate", "add-waiter", gateID, "shelley")
		if err != nil {
			t.Fatalf("gate add-waiter failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "Added waiter to gate "+gateID+": shelley") {
			t.Errorf("expected added-waiter confirmation, got:\n%s", out)
		}
		out, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "gate", "add-waiter", gateID, "shelley")
		if err != nil {
			t.Fatalf("repeat gate add-waiter failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "Waiter already registered on gate "+gateID) {
			t.Errorf("expected already-registered message, got:\n%s", out)
		}

		out, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "gate", "show", gateID)
		if err != nil {
			t.Fatalf("gate show after add-waiter failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "Waiters:") || !strings.Contains(out, "- shelley") {
			t.Errorf("gate show should list the registered waiter, got:\n%s", out)
		}

		// gate list --all --json: the consumers read .id/.status/.description
		// (containing "Reason: ") and .waiters, so pin the raw JSON keys.
		out, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "gate", "list", "--all", "--json")
		if err != nil {
			t.Fatalf("gate list --all --json failed: %v\nstderr:\n%s", err, stderr)
		}
		var listed []map[string]any
		s := strings.TrimSpace(out)
		if start := strings.Index(s, "["); start < 0 {
			t.Fatalf("no JSON array in gate list output:\n%s", out)
		} else if err := json.Unmarshal([]byte(s[start:]), &listed); err != nil {
			t.Fatalf("parse gate list JSON: %v\n%s", err, out)
		}
		var row map[string]any
		for _, r := range listed {
			if r["id"] == gateID {
				row = r
			}
		}
		if row == nil {
			t.Fatalf("gate %s missing from gate list --all --json:\n%s", gateID, out)
		}
		if row["status"] != "open" {
			t.Errorf("gate list .status = %v, want open", row["status"])
		}
		if desc, _ := row["description"].(string); !strings.Contains(desc, "Reason: Need design review") {
			t.Errorf("gate list .description missing 'Reason: ' marker: %v", row["description"])
		}
		if waiters, ok := row["waiters"].([]any); !ok || len(waiters) != 1 || waiters[0] != "shelley" {
			t.Errorf("gate list .waiters = %v, want [shelley]", row["waiters"])
		}

		// resolve: rc-only consumption, gate closes, target unblocks.
		out, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "gate", "resolve", gateID, "--reason", "design approved")
		if err != nil {
			t.Fatalf("gate resolve failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "Gate resolved: "+gateID) || !strings.Contains(out, "Reason: design approved") {
			t.Errorf("unexpected gate resolve output:\n%s", out)
		}
		db := openProxiedDB(t, p)
		if got := readStatus(t, db, gateID); got != types.StatusClosed {
			t.Errorf("resolved gate should be closed, got %q", got)
		}
		if ids := gateReadyIDs(t, bd, p); !ids[target.ID] {
			t.Errorf("target %s should be ready again after resolve", target.ID)
		}

		// Double-resolve is a reported no-op: exit 0, gate stays closed.
		// (Audit + close hooks are guarded on CloseIssueResult.Closed, so the
		// second resolve records nothing new.)
		_, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "gate", "resolve", gateID, "--reason", "again")
		if err != nil {
			t.Fatalf("double gate resolve should be idempotent, failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gateID); got != types.StatusClosed {
			t.Errorf("gate should stay closed after double resolve, got %q", got)
		}
	})

	// The wh-park shape: a bead gate names the awaited bead in --await-id, and
	// --json create hands scripts the gate as an object.
	t.Run("bead_gate_wh_park_shape", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gbp")
		parked := bdProxiedCreate(t, bd, p.dir, "Parked bead")
		awaited := bdProxiedCreate(t, bd, p.dir, "Awaited bead")

		out, stderr, err := bdProxiedRunBuffers(t, bd, p.dir,
			"gate", "create", "--type=bead", "--blocks", parked.ID, "--await-id", awaited.ID, "--reason", "waiting on "+awaited.ID)
		if err != nil {
			t.Fatalf("bead gate create failed: %v\nstderr:\n%s", err, stderr)
		}
		gateID := parseCreatedGateID(t, out)

		out, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "gate", "show", gateID)
		if err != nil {
			t.Fatalf("gate show failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "Await Type: bead") || !strings.Contains(out, "Await ID: "+awaited.ID) {
			t.Errorf("gate show missing bead await markers:\n%s", out)
		}

		// The already-ported gate check consumes this gate: closing the awaited
		// bead resolves it, proving create wired await_type/await_id for real.
		bdProxiedClose(t, bd, p.dir, awaited.ID)
		out, stderr, err = bdProxiedRunBuffers(t, bd, p.dir, "gate", "check", "--type", "bead")
		if err != nil {
			t.Fatalf("gate check failed: %v\nstderr:\n%s", err, stderr)
		}
		if !strings.Contains(out, "1 resolved") {
			t.Errorf("expected the bead gate to resolve via gate check, got:\n%s", out)
		}
		db := openProxiedDB(t, p)
		if got := readStatus(t, db, gateID); got != types.StatusClosed {
			t.Errorf("bead gate should be closed after gate check, got %q", got)
		}

		// --json create for script consumers that prefer the object.
		out, stderr, err = bdProxiedRunBuffers(t, bd, p.dir,
			"gate", "create", "--type=human", "--blocks", parked.ID, "--json")
		if err != nil {
			t.Fatalf("gate create --json failed: %v\nstderr:\n%s", err, stderr)
		}
		var created map[string]any
		s := strings.TrimSpace(out)
		if start := strings.Index(s, "{"); start < 0 {
			t.Fatalf("no JSON object in gate create --json output:\n%s", out)
		} else if err := json.Unmarshal([]byte(s[start:]), &created); err != nil {
			t.Fatalf("parse gate create JSON: %v\n%s", err, out)
		}
		if id, _ := created["id"].(string); id == "" {
			t.Errorf("gate create --json must carry .id, got: %v", created)
		}
		if created["issue_type"] != "gate" {
			t.Errorf("gate create --json .issue_type = %v, want gate", created["issue_type"])
		}
	})

	// Fail-closed: every bad read or bad target exits nonzero with a
	// distinguishable message; nothing prints a success shape.
	t.Run("error_paths_fail_closed", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gfe")
		notGate := bdProxiedCreate(t, bd, p.dir, "Ordinary task")

		cases := []struct {
			name string
			args []string
			want string
		}{
			{"show_missing", []string{"gate", "show", "gfe-doesnotexist"}, "gate not found"},
			{"show_non_gate", []string{"gate", "show", notGate.ID}, "is not a gate issue"},
			{"resolve_missing", []string{"gate", "resolve", "gfe-doesnotexist"}, "gate not found"},
			{"resolve_non_gate", []string{"gate", "resolve", notGate.ID}, "is not a gate issue"},
			{"add_waiter_missing", []string{"gate", "add-waiter", "gfe-doesnotexist", "w"}, "gate not found"},
			{"add_waiter_non_gate", []string{"gate", "add-waiter", notGate.ID, "w"}, "is not a gate issue"},
			{"create_missing_target", []string{"gate", "create", "--blocks", "gfe-doesnotexist"}, "issue not found"},
		}
		for _, tc := range cases {
			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, tc.args...)
			if err == nil {
				t.Errorf("%s: expected nonzero exit, got success:\n%s", tc.name, stdout)
				continue
			}
			if combined := stdout + stderr; !strings.Contains(combined, tc.want) {
				t.Errorf("%s: expected %q in output, got:\n%s", tc.name, tc.want, combined)
			}
		}

		if got := readStatus(t, openProxiedDB(t, p), notGate.ID); got == types.StatusClosed {
			t.Error("gate resolve on a non-gate must not close it")
		}
	})
}
