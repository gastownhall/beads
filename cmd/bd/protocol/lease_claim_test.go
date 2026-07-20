// lease_claim_test.go — CLI-level conformance for the claim/lease clauses of
// protocol v0 §L that the §11 manifest listed as unpinned: the unassigned-only
// claim rule (L3.1), the empty-front success contract (L3.4/E1), filter
// composition (L3.5), and the JSONL round-trip of the lease fields (L1.2).
//
// These live at the CLI surface rather than the store because they are what a
// worker fleet actually drives: `bd ready --claim` is the whole coordination
// protocol between N workers and one queue.
package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claimReady runs `bd ready --claim --json` as actor and returns the claimed
// issues (empty when the front holds nothing claimable). It fails the test if
// the command exits nonzero — an empty front is success (L3.4), not an error.
func claimReady(t *testing.T, w *workspace, actor string, args ...string) []map[string]any {
	t.Helper()
	full := append([]string{"ready", "--claim", "--json"}, args...)
	out, err := w.tryRunEnv([]string{"BEADS_ACTOR=" + actor}, full...)
	if err != nil {
		t.Fatalf("bd %s as %s: %v\n%s", strings.Join(full, " "), actor, err, out)
	}
	return parseJSONOutput(t, out)
}

// TestProtocol_ClaimIsUnassignedOnly pins clause L3.1, the single most
// bitten-in-production rule of the claim protocol: `--claim` considers only
// issues that are open AND unassigned. An open issue that carries an assignee —
// even with no lease on it, even though `bd ready` happily LISTS it — is never a
// claim candidate. This is the gotcha behind a backlog generated with
// self-assigned creates: it looks ready and claims nothing, and a worker fleet
// pointed at it spins on an empty front while the queue sits full.
func TestProtocol_ClaimIsUnassignedOnly(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	// Same priority and creation order, so the only thing separating them is the
	// assignee: if --claim ignored L3.1, the assigned issue is a live candidate.
	assigned := w.create("--title", "Open but assigned", "--type", "task", "-p", "1", "-a", "someone-else")
	free := w.create("--title", "Open and unassigned", "--type", "task", "-p", "1")

	// The listing is NOT filtered by assignee (§R): both are on the ready front.
	ready := parseReadyIDs(t, w)
	if !ready[assigned] || !ready[free] {
		t.Fatalf("precondition: bd ready should list both %s (assigned) and %s (unassigned), got %v", assigned, free, ready)
	}

	claimed := claimReady(t, w, "alice")
	if len(claimed) != 1 {
		t.Fatalf("bd ready --claim returned %d issues, want 1", len(claimed))
	}
	if got := claimed[0]["id"]; got != free {
		t.Fatalf("L3.1: --claim returned %v, want the unassigned issue %s (the assigned one %s must never be a candidate)", got, free, assigned)
	}

	// With only the assigned issue left, the front is empty for claiming — even
	// though bd ready still lists it.
	if again := claimReady(t, w, "alice"); len(again) != 0 {
		t.Errorf("L3.1: --claim returned %v with only an assigned-but-open issue left; want nothing claimable", again)
	}
	if still := parseReadyIDs(t, w); !still[assigned] {
		t.Errorf("bd ready stopped listing the assigned issue %s; only --claim filters to unassigned (§R)", assigned)
	}

	// The rejected candidate was not touched: still open, still someone else's.
	got := w.showJSON(assigned)
	if got["status"] != "open" {
		t.Errorf("assigned issue status = %v, want open (--claim must not mutate a non-candidate)", got["status"])
	}
	if got["assignee"] != "someone-else" {
		t.Errorf("assigned issue assignee = %v, want someone-else", got["assignee"])
	}
}

// TestProtocol_ReadyClaimEmptyFrontIsSuccess pins clause L3.4 (and §E1): when
// nothing is claimable, `bd ready --claim --json` MUST exit 0 with an explicit
// empty JSON array. Queue-drained is the steady state of a worker fleet, not an
// error — a nonzero exit here would make every idle worker look like a failure.
func TestProtocol_ReadyClaimEmptyFrontIsSuccess(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	// (a) Never had any work.
	out, err := w.tryRunEnv([]string{"BEADS_ACTOR=alice"}, "ready", "--claim", "--json")
	if err != nil {
		t.Fatalf("L3.4: bd ready --claim --json on an empty tracker exited nonzero: %v\n%s", err, out)
	}
	if trimmed := strings.TrimSpace(out); trimmed != "[]" {
		t.Errorf("L3.4: empty front printed %q, want the explicit empty array []", trimmed)
	}

	// (b) Drained: the one issue is claimed, so the next claim comes up empty.
	only := w.create("--title", "The only work", "--type", "task")
	if claimed := claimReady(t, w, "alice"); len(claimed) != 1 || claimed[0]["id"] != only {
		t.Fatalf("setup: first claim = %v, want [%s]", claimed, only)
	}
	out, err = w.tryRunEnv([]string{"BEADS_ACTOR=bob"}, "ready", "--claim", "--json")
	if err != nil {
		t.Fatalf("L3.4: bd ready --claim --json on a drained queue exited nonzero: %v\n%s", err, out)
	}
	if trimmed := strings.TrimSpace(out); trimmed != "[]" {
		t.Errorf("L3.4: drained queue printed %q, want []", trimmed)
	}
}

// TestProtocol_ReadyClaimComposesWithFilters pins clause L3.5: `--claim` composes
// with the ready-front filters, and the issue it claims satisfies the same filter
// the plain `bd ready` listing would. This is what lets several fleets drain one
// shared queue without colliding — each claims only its own lane (--label) or its
// own epic (--parent, transitive per R3).
func TestProtocol_ReadyClaimComposesWithFilters(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	epic := w.create("--title", "Epic under drain", "--type", "epic")
	child := w.create("--title", "Child", "--type", "task", "--parent", epic)
	grandchild := w.create("--title", "Grandchild", "--type", "task", "--parent", child)
	outsider := w.create("--title", "Not under the epic", "--type", "task")
	lane := w.create("--title", "Lane work", "--type", "task", "-l", "lane:x")

	// --parent claims only transitive descendants of the epic, never the
	// outsider and never the lane issue.
	underEpic := map[string]bool{child: true, grandchild: true}
	for i := 0; i < 2; i++ {
		claimed := claimReady(t, w, "alice", "--parent", epic)
		if len(claimed) != 1 {
			t.Fatalf("L3.5: --claim --parent %s returned %d issues on pass %d, want 1", epic, len(claimed), i+1)
		}
		id, _ := claimed[0]["id"].(string)
		if !underEpic[id] {
			t.Fatalf("L3.5: --claim --parent %s claimed %s, which is not a descendant of the epic", epic, id)
		}
		delete(underEpic, id) // never the same issue twice
	}
	if len(underEpic) != 0 {
		t.Errorf("L3.5: --claim --parent left %v unclaimed; --parent must be transitive (R3)", underEpic)
	}
	if drained := claimReady(t, w, "alice", "--parent", epic); len(drained) != 0 {
		t.Errorf("L3.5: --claim --parent %s claimed %v after its subtree drained; the filter leaked", epic, drained)
	}

	// --label claims only the matching lane, and the claimed issue really carries
	// the label the filter named.
	claimed := claimReady(t, w, "bob", "--label", "lane:x")
	if len(claimed) != 1 || claimed[0]["id"] != lane {
		t.Fatalf("L3.5: --claim --label lane:x returned %v, want [%s]", claimed, lane)
	}
	labels, _ := w.showJSON(lane)["labels"].([]any)
	found := false
	for _, l := range labels {
		if l == "lane:x" {
			found = true
		}
	}
	if !found {
		t.Errorf("L3.5: claimed issue %s does not carry the filtered label lane:x (labels=%v)", lane, labels)
	}

	// The outsider was never a candidate under either filter and is still ready.
	// (An unassigned issue reports assignee as JSON null or "", never an owner.)
	out := w.showJSON(outsider)
	if assignee, _ := out["assignee"].(string); out["status"] != "open" || assignee != "" {
		t.Errorf("L3.5: filtered claims touched the out-of-filter issue %s: status=%v assignee=%v",
			outsider, out["status"], out["assignee"])
	}
}

// exportLeaseFixture claims one issue, leaves another unclaimed, exports the
// tracker to JSONL, and returns the export path plus the parsed lines by id.
func exportLeaseFixture(t *testing.T, w *workspace, leased, idle string) (string, map[string]map[string]any) {
	t.Helper()
	w.runEnv([]string{"BEADS_ACTOR=alice"}, "update", leased, "--claim")

	path := filepath.Join(w.dir, "export.jsonl")
	w.run("export", "-o", path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}

	exported := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("export line is not JSON: %v\n%s", err, line)
		}
		if id, ok := obj["id"].(string); ok {
			exported[id] = obj
		}
	}
	if _, ok := exported[leased]; !ok {
		t.Fatalf("export omitted the claimed issue %s", leased)
	}
	if _, ok := exported[idle]; !ok {
		t.Fatalf("export omitted the unclaimed issue %s", idle)
	}
	return path, exported
}

// TestProtocol_LeaseFieldsExportToJSONL pins the export half of clause L1.2: a
// claimed issue carries both lease fields into the JSONL interchange as RFC 3339
// timestamps (§J2.4), and an issue holding no lease carries NEITHER — the fields
// are omitted, not emitted as null.
func TestProtocol_LeaseFieldsExportToJSONL(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	leased := w.create("--title", "Claimed and leased", "--type", "task")
	idle := w.create("--title", "Never claimed", "--type", "task")
	_, exported := exportLeaseFixture(t, w, leased, idle)

	expires := requireRFC3339(t, exported[leased], "lease_expires_at", leased)
	heartbeat := requireRFC3339(t, exported[leased], "heartbeat_at", leased)
	if !expires.After(heartbeat) {
		t.Errorf("L1.2: lease_expires_at (%v) is not after heartbeat_at (%v)", expires, heartbeat)
	}
	for _, field := range []string{"lease_expires_at", "heartbeat_at"} {
		if v, present := exported[idle][field]; present {
			t.Errorf("L1.2: unclaimed issue %s exported %s = %v, want the field omitted", idle, field, v)
		}
	}
}

// TestProtocol_LeaseFieldsRoundTripJSONL pins the other half of clause L1.2: the
// lease fields must survive the round trip, not just the export. bd import once
// dropped them (wy-urlct: the sole issue INSERT path, issueops.InsertIssueIntoTable,
// listed no lease columns), so an issue exported while claimed was imported as
// in_progress with a NULL lease. Reclaim only recovers leased claims (L5.1),
// which made such an issue unreclaimable by any
// reaper: stranded forever under a worker that may be long dead — exactly the
// recoverability the lease exists to provide. Leases now live in the ephemeral
// leases table (bd-lrgn1) and import restores them via
// issueops.RestoreLeaseOnImportInTx, so an import can restore a lease but
// snapshot data can never clobber a live (unexpired) local lease — pinned by
// TestProtocol_ImportNeverClobbersLiveLocalLease.
func TestProtocol_LeaseFieldsRoundTripJSONL(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	leased := w.create("--title", "Claimed and leased", "--type", "task")
	idle := w.create("--title", "Never claimed", "--type", "task")
	path, exported := exportLeaseFixture(t, w, leased, idle)
	expires := requireRFC3339(t, exported[leased], "lease_expires_at", leased)
	heartbeat := requireRFC3339(t, exported[leased], "heartbeat_at", leased)

	fresh := newWorkspace(t)
	fresh.run("import", path)
	restored := fresh.showJSON(leased)

	if back := requireRFC3339(t, restored, "lease_expires_at", leased); !back.Equal(expires) {
		t.Errorf("L1.2: lease_expires_at did not round-trip: exported %v, imported %v", expires, back)
	}
	if back := requireRFC3339(t, restored, "heartbeat_at", leased); !back.Equal(heartbeat) {
		t.Errorf("L1.2: heartbeat_at did not round-trip: exported %v, imported %v", heartbeat, back)
	}
	if restored["assignee"] != "alice" || restored["status"] != "in_progress" {
		t.Errorf("L1.2: lease owner did not round-trip: assignee=%v status=%v, want alice/in_progress",
			restored["assignee"], restored["status"])
	}
}

// TestProtocol_ImportNeverClobbersLiveLocalLease pins the lease-restore guard
// of clause L1.2 under the ephemeral leases table (bd-lrgn1): leases are
// node-local — only enforceable on the node that granted them — so a snapshot
// imported over a LIVE (unexpired) local lease must leave the local lease
// untouched, even when the snapshot's issue row is strictly newer and its
// other fields win the stale-guard merge. Only an expired (or absent) local
// lease may be replaced by snapshot lease data.
func TestProtocol_ImportNeverClobbersLiveLocalLease(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	leased := w.create("--title", "Locally claimed", "--type", "task")
	w.runEnv([]string{"BEADS_ACTOR=alice"}, "update", leased, "--claim")
	local := w.showJSON(leased)
	localExpiry := requireRFC3339(t, local, "lease_expires_at", leased)
	if !localExpiry.After(time.Now()) {
		t.Fatalf("precondition: local lease already expired (%v)", localExpiry)
	}

	// Craft a snapshot of the same issue that is strictly NEWER by updated_at
	// and carries different (also-live) lease timestamps — as if exported from
	// another node that also believes it holds the claim.
	snapshot := map[string]any{}
	for k, v := range local {
		snapshot[k] = v
	}
	delete(snapshot, "labels")
	snapshot["updated_at"] = time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	snapshot["lease_expires_at"] = time.Now().UTC().Add(30 * time.Hour).Format(time.RFC3339)
	snapshot["heartbeat_at"] = time.Now().UTC().Add(29 * time.Hour).Format(time.RFC3339)
	snapshot["notes"] = "remote edit that must land"

	line, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	path := filepath.Join(w.dir, "remote.jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	w.run("import", path)

	restored := w.showJSON(leased)
	// The newer snapshot's row fields won the merge...
	if restored["notes"] != "remote edit that must land" {
		t.Errorf("newer snapshot's field edit did not land: notes=%v", restored["notes"])
	}
	// ...but the live local lease was NOT clobbered by the snapshot's lease.
	if back := requireRFC3339(t, restored, "lease_expires_at", leased); !back.Equal(localExpiry) {
		t.Errorf("import clobbered a live local lease: expiry was %v, now %v (snapshot's was ~30h out)",
			localExpiry, back)
	}
}

// requireRFC3339 asserts obj[field] is a non-empty RFC 3339 timestamp (§J2.4)
// and returns it.
func requireRFC3339(t *testing.T, obj map[string]any, field, id string) time.Time {
	t.Helper()
	raw, ok := obj[field].(string)
	if !ok || raw == "" {
		t.Fatalf("L1.2: issue %s carries no %s (got %v)", id, field, obj[field])
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("L1.2: %s of %s is not RFC 3339: %q (%v)", field, id, raw, err)
	}
	return ts
}
