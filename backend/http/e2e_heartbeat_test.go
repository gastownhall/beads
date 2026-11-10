//go:build cgo

package bdhttp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestE2E_HeartbeatOverHTTP runs `bd heartbeat <id>` — the keepalive gc's
// mol-do-work formula issues every ~90s (`gc bd heartbeat`, corpus shapes
// gc-heartbeat*) — through the real `bd` binary against an in-process `bd
// serve`.
//
// The CLI takes the store's Heartbeater, which over HTTP dials POST
// /v0/beads/issues/{id}:heartbeat. What a worker relies on is pinned here end
// to end: exit 0 and the {"id","status":"heartbeat","owner"} shape for the
// holder, with the row's revision and updated_at UNCHANGED (the heartbeat
// writes only the lease, so the holder's guarded writes keep their token);
// exit 1 for a foreign holder (whose lease stays untouched), a closed issue, a
// wisp and an unclaimed issue, each saying which. Against a server that does
// not advertise issues.heartbeat (an older server's handshake shape) the
// client refuses BEFORE the dial, naming the token, and never falls back.
func TestE2E_HeartbeatOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr, reference := startE2EServerWithStore(t, e2eProjectID, e2eDatabase)
	leaseRow := func(id string) string {
		t.Helper()
		var row string
		if err := reference.RunEventsMaintenanceTx(context.Background(), func(ctx context.Context, tx issueops.DBTX) error {
			return tx.QueryRowContext(ctx,
				"SELECT CONCAT(holder, '|', CAST(lease_expires_at AS CHAR), '|', CAST(heartbeat_at AS CHAR)) FROM leases WHERE issue_id = ?", id,
			).Scan(&row)
		}); err != nil {
			t.Fatalf("read the lease row on %s: %v", id, err)
		}
		return row
	}
	ageLease := func(id string) {
		t.Helper()
		aged := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		if err := reference.RunEventsMaintenanceTx(context.Background(), func(ctx context.Context, tx issueops.DBTX) error {
			_, err := tx.ExecContext(ctx, "UPDATE leases SET lease_expires_at = ?, heartbeat_at = ? WHERE issue_id = ?", aged, aged, id)
			return err
		}); err != nil {
			t.Fatalf("age the lease on %s: %v", id, err)
		}
	}

	heartbeatPosts := func(paths []string) int {
		n := 0
		for _, p := range paths {
			if strings.HasPrefix(p, "POST ") && strings.HasSuffix(p, ":heartbeat") {
				n++
			}
		}
		return n
	}

	t.Run("advertised", func(t *testing.T) {
		proxyAddr, counter := startCountingProxy(t, addr)
		f := newHTTPFixture(t, bin, proxyAddr)

		id := f.create("heartbeat target")
		f.mustRun(nil, "update", id, "--claim", "--actor", "worker-7")
		before := f.mustRun(nil, "show", id, "--json").stdout
		revision := firstJSONField(t, before, "revision")
		if revision == "" {
			t.Fatalf("bd show --json reported no revision: %s", before)
		}
		updatedAt := firstJSONField(t, before, "updated_at")

		counter.reset()
		r := f.mustRun(nil, "heartbeat", id, "--actor", "worker-7", "--json")
		var out map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(r.stdout)), &out); err != nil {
			t.Fatalf("parse bd heartbeat --json: %v\n%s", err, r.stdout)
		}
		if out["id"] != id || out["status"] != "heartbeat" || out["owner"] != "worker-7" {
			t.Errorf("bd heartbeat --json = %v, want {id: %s, status: heartbeat, owner: worker-7}", out, id)
		}
		if n := heartbeatPosts(counter.snapshot()); n != 1 {
			t.Errorf("bd heartbeat sent %d :heartbeat requests, want exactly one: %v", n, counter.snapshot())
		}
		for _, p := range counter.snapshot() {
			if strings.HasPrefix(p, "PATCH ") || (strings.HasPrefix(p, "POST ") && !strings.HasSuffix(p, ":heartbeat")) {
				t.Errorf("bd heartbeat sent %s; a heartbeat writes nothing but the lease", p)
			}
		}

		after := f.mustRun(nil, "show", id, "--json").stdout
		if got := firstJSONField(t, after, "revision"); got != revision {
			t.Errorf("revision moved across a heartbeat: %s -> %s; a guarded write against the pre-heartbeat token would now fail", revision, got)
		}
		if got := firstJSONField(t, after, "updated_at"); got != updatedAt {
			t.Errorf("updated_at moved across a heartbeat: %s -> %s", updatedAt, got)
		}
		// The holder's guarded write with the pre-heartbeat token still lands.
		f.mustRun(nil, "update", id, "--notes", "still mine", "--if-revision", revision, "--actor", "worker-7")

		// A second heartbeat is fine: it only moves the expiry later.
		f.mustRun(nil, "heartbeat", id, "--actor", "worker-7")
	})

	t.Run("refusals", func(t *testing.T) {
		f := newHTTPFixture(t, bin, addr)

		foreign := f.create("held by alice")
		f.mustRun(nil, "update", foreign, "--claim", "--actor", "alice")
		// Aged first, so a heartbeat that ignored the holder fence would
		// really change the row rather than rewrite the claim's own second.
		ageLease(foreign)
		held := leaseRow(foreign)
		r := f.run(nil, "heartbeat", foreign, "--actor", "bob")
		if r.code != 1 || !strings.Contains(r.stderr, "claimed by alice") {
			t.Errorf("heartbeat by a non-holder: exit %d stderr=%s, want exit 1 naming the holder", r.code, r.stderr)
		}
		if got := leaseRow(foreign); got != held {
			t.Errorf("a refused heartbeat by a non-holder moved alice's lease: %s -> %s", held, got)
		}

		// Each lease_not_held names the one state it found, and no other.
		says := func(what, stderr, want string, not ...string) {
			t.Helper()
			if !strings.Contains(stderr, want) {
				t.Errorf("heartbeat on %s: stderr=%s, want it to say %q", what, stderr, want)
			}
			for _, n := range not {
				if strings.Contains(stderr, n) {
					t.Errorf("heartbeat on %s: stderr=%s says %q, which is not the state it found", what, stderr, n)
				}
			}
		}

		closed := f.create("closed under its worker")
		f.mustRun(nil, "update", closed, "--claim", "--actor", "worker-7")
		f.mustRun(nil, "close", closed, "--actor", "worker-7")
		r = f.run(nil, "heartbeat", closed, "--actor", "worker-7")
		if r.code != 1 {
			t.Errorf("heartbeat on a closed issue exited %d, want 1: %s", r.code, r.stderr)
		}
		says("a closed issue", r.stderr, "is closed", "ephemeral", "not claimed")

		wisp := f.create("a wisp is never leased", "--ephemeral")
		r = f.run(nil, "heartbeat", wisp, "--actor", "worker-7")
		if r.code != 1 {
			t.Errorf("heartbeat on a wisp exited %d, want 1: %s", r.code, r.stderr)
		}
		says("a wisp", r.stderr, "ephemeral wisp", "closed", "not claimed")

		unclaimed := f.create("never claimed")
		r = f.run(nil, "heartbeat", unclaimed, "--actor", "worker-7")
		if r.code != 1 {
			t.Errorf("heartbeat on an unclaimed issue exited %d, want 1: %s", r.code, r.stderr)
		}
		says("an unclaimed issue", r.stderr, "not claimed", "closed", "ephemeral")
	})

	t.Run("older server mask", func(t *testing.T) {
		proxyAddr, counter := startMaskingProxy(t, addr, mcContextMask)
		f := newHTTPFixture(t, bin, proxyAddr)
		id := f.create("an old server cannot refresh this lease")
		f.mustRun(nil, "update", id, "--claim", "--actor", "worker-7")

		counter.reset()
		r := f.run(nil, "heartbeat", id, "--actor", "worker-7")
		if r.code != 1 {
			t.Fatalf("bd heartbeat against a server without issues.heartbeat exited %d, want 1: %s", r.code, r.stdout)
		}
		if !strings.Contains(r.stderr, "issues.heartbeat") {
			t.Errorf("refusal does not name the missing capability: %s", r.stderr)
		}
		for _, p := range counter.snapshot() {
			if !strings.HasPrefix(p, "GET ") {
				t.Errorf("a refused heartbeat sent %s; the refusal must come before any write", p)
			}
		}
	})
}
