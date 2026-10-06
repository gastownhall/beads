//go:build cgo

// Written fresh for OSS beads S6 (no bd-enterprise source copied). The
// in-process server wiring mirrors internal/httpclient/served_harness_test.go
// (itself under provenance there), reused here as a black-box harness: this
// file never imports the wire client, it shells out to a real `bd` binary so
// the CLI dispatch (cmd/bd/http_backend.go's Register call, `bd connect`,
// `bd update --if-revision`'s exit code contract) is what gets exercised, not
// the internal client package.
package bdhttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// e2eProjectID is the identity the in-process server publishes; `bd connect`
// reads it back off the handshake and pins the workspace to it.
const e2eProjectID = "proj-e2e-s6"

const e2eDatabase = "httpe2e"

// TestE2E_BDOverHTTP drives the real `bd` binary against an in-process `bd
// serve`, end to end: connect, create, an update guarded by --if-revision
// (both the matching case and the stale case, which must exit 13 with the
// precondition_failed body), close, dep add, ready, count, delete.
func TestE2E_BDOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)

	addr := startE2EServer(t)
	workspace := t.TempDir()

	connect := runBD(t, bin, workspace, nil,
		"connect", "http://"+addr, "--expect-project-id", e2eProjectID, "--json")
	if connect.code != 0 {
		t.Fatalf("bd connect failed (exit %d): stdout=%s stderr=%s", connect.code, connect.stdout, connect.stderr)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".beads", "http_target.json")); err != nil {
		t.Fatalf("bd connect did not write the activation sidecar: %v", err)
	}
	gitignore, err := os.ReadFile(filepath.Join(workspace, ".beads", ".gitignore"))
	if err != nil {
		t.Fatalf("bd connect did not ensure a .beads/.gitignore: %v", err)
	}
	if !strings.Contains(string(gitignore), "http_target.json") {
		t.Errorf(".beads/.gitignore does not cover http_target.json: %s", gitignore)
	}

	// create
	create := runBD(t, bin, workspace, nil, "create", "A title", "-d", "a description", "--json")
	if create.code != 0 {
		t.Fatalf("bd create failed (exit %d): stdout=%s stderr=%s", create.code, create.stdout, create.stderr)
	}
	id := firstJSONField(t, create.stdout, "id")
	if id == "" {
		t.Fatalf("bd create --json produced no id: %s", create.stdout)
	}

	// read back the revision bd show reports, so the update guard below
	// names a token the row actually carries.
	show := runBD(t, bin, workspace, nil, "show", id, "--json")
	if show.code != 0 {
		t.Fatalf("bd show failed (exit %d): stderr=%s", show.code, show.stderr)
	}
	revision := firstJSONField(t, show.stdout, "revision")
	if revision == "" {
		t.Fatalf("bd show --json reported no revision: %s", show.stdout)
	}

	// update with a MATCHING --if-revision guard: must succeed.
	okUpdate := runBD(t, bin, workspace, nil, "update", id, "-a", "alice", "--if-revision", revision, "--json")
	if okUpdate.code != 0 {
		t.Fatalf("bd update --if-revision %s (matching) failed (exit %d): stdout=%s stderr=%s",
			revision, okUpdate.code, okUpdate.stdout, okUpdate.stderr)
	}

	// update again with the SAME (now stale) token: must refuse with exit 13
	// and the precondition_failed machine body on the last stderr line.
	staleUpdate := runBD(t, bin, workspace, nil, "update", id, "-a", "bob", "--if-revision", revision, "--json")
	if staleUpdate.code != 13 {
		t.Fatalf("bd update --if-revision %s (stale) exit = %d, want 13 (ExitGuardMismatch): stdout=%s stderr=%s",
			revision, staleUpdate.code, staleUpdate.stdout, staleUpdate.stderr)
	}
	body := lastJSONLine(t, staleUpdate.stderr)
	if body["code"] != "precondition_failed" {
		t.Errorf("stale --if-revision body code = %v, want \"precondition_failed\": %s", body["code"], staleUpdate.stderr)
	}
	if body["id"] != id {
		t.Errorf("stale --if-revision body id = %v, want %q: %s", body["id"], id, staleUpdate.stderr)
	}

	// dep add + a second issue to depend on, then ready/count/close/delete.
	// id depends on (is blocked by) depID (dep.go's own doc: "bd dep add
	// issue-123 issue-456" means issue-123 depends on issue-456), so depID
	// is the one issue the ready set must show and id is the one it must not.
	dep := runBD(t, bin, workspace, nil, "create", "A dependency", "--json")
	if dep.code != 0 {
		t.Fatalf("bd create (dependency) failed (exit %d): stderr=%s", dep.code, dep.stderr)
	}
	depID := firstJSONField(t, dep.stdout, "id")

	if r := runBD(t, bin, workspace, nil, "dep", "add", id, depID, "--json"); r.code != 0 {
		t.Fatalf("bd dep add failed (exit %d): stdout=%s stderr=%s", r.code, r.stdout, r.stderr)
	}

	ready := runBD(t, bin, workspace, nil, "ready", "--json")
	if ready.code != 0 {
		t.Fatalf("bd ready failed (exit %d): stdout=%s stderr=%s", ready.code, ready.stdout, ready.stderr)
	}
	readyIDs := jsonIDSet(t, ready.stdout)
	if !readyIDs[depID] {
		t.Errorf("bd ready did not list %s (no deps of its own, open): %s", depID, ready.stdout)
	}
	if readyIDs[id] {
		t.Errorf("bd ready listed %s, which depends on the still-open %s: %s", id, depID, ready.stdout)
	}

	count := runBD(t, bin, workspace, nil, "count", "--json")
	if count.code != 0 {
		t.Fatalf("bd count failed (exit %d): stdout=%s stderr=%s", count.code, count.stdout, count.stderr)
	}
	if n := jsonIntField(t, count.stdout, "count"); n != 2 {
		t.Errorf("bd count = %d, want 2 (id and depID, the only issues this workspace has): %s", n, count.stdout)
	}

	if r := runBD(t, bin, workspace, nil, "close", depID, "--json"); r.code != 0 {
		t.Fatalf("bd close failed (exit %d): stdout=%s stderr=%s", r.code, r.stdout, r.stderr)
	}
	closedShow := runBD(t, bin, workspace, nil, "show", depID, "--json")
	if closedShow.code != 0 {
		t.Fatalf("bd show %s (after close) failed (exit %d): stderr=%s", depID, closedShow.code, closedShow.stderr)
	}
	if status := firstJSONField(t, closedShow.stdout, "status"); status != "closed" {
		t.Errorf("bd show %s status = %q after bd close, want %q: %s", depID, status, "closed", closedShow.stdout)
	}

	if r := runBD(t, bin, workspace, nil, "delete", id, "--force", "--json"); r.code != 0 {
		t.Fatalf("bd delete failed (exit %d): stdout=%s stderr=%s", r.code, r.stdout, r.stderr)
	}
	deletedShow := runBD(t, bin, workspace, nil, "show", id, "--json")
	if deletedShow.code == 0 {
		t.Errorf("bd show %s succeeded (exit 0) after bd delete --force; want a not-found refusal: stdout=%s", id, deletedShow.stdout)
	}
}

// TestE2E_ConnectRefusesPlainHTTPToNonLoopback kills the mutation that
// disables connect.go's plaintext-host refusal (`&& !connectAllowPlaintext`
// widened to `&& !connectAllowPlaintext && false`). A bearer credential this
// workspace would send to a non-loopback http:// server travels the network
// unencrypted, so connect must refuse before ever dialing — which this test
// also verifies directly: 198.51.100.1 is RFC 5737 TEST-NET-2, reserved for
// documentation and globally unroutable, so even a mutated build that skipped
// the refusal could not reach a real server here, keeping this test on the
// loopback-only network the rest of this suite requires.
func TestE2E_ConnectRefusesPlainHTTPToNonLoopback(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	workspace := t.TempDir()

	const nonLoopbackURL = "http://198.51.100.1:1/"
	refused := runBD(t, bin, workspace, nil, "connect", nonLoopbackURL, "--json")
	if refused.code == 0 {
		t.Fatalf("bd connect %s succeeded; want a refusal (not loopback, plain http, no --allow-plaintext): stdout=%s", nonLoopbackURL, refused.stdout)
	}
	if !strings.Contains(refused.stderr, "not loopback") {
		t.Errorf("bd connect %s stderr does not name the loopback refusal; want the host-safety gate, not some other failure: exit=%d stdout=%s stderr=%s",
			nonLoopbackURL, refused.code, refused.stdout, refused.stderr)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".beads", "http_target.json")); err == nil {
		t.Error("bd connect wrote the activation sidecar despite refusing the connection")
	}
}

// TestE2E_ConnectRefusesSwitchingBackendWithoutForce kills the mutation that
// disables connect.go's backend-switch force gate (`&& !connectForce`
// widened to `&& !connectForce && false`). A workspace that already selects
// a different backend must refuse to switch to http without --force; with
// --force it must proceed PAST that gate, so the test tells the two
// refusals apart by what they say, not just that they both fail: a port
// nothing listens on (still loopback, so it clears the plaintext-host gate
// above) turns "the force gate let this through" into its own distinct
// dial failure rather than a false pass.
func TestE2E_ConnectRefusesSwitchingBackendWithoutForce(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	const target = "http://127.0.0.1:1/"

	withoutForce := runBD(t, bin, workspace, nil, "connect", target, "--json")
	if withoutForce.code == 0 {
		t.Fatalf("bd connect %s without --force succeeded; workspace already selects backend %q", target, "dolt")
	}
	if !strings.Contains(withoutForce.stderr, "--force") {
		t.Errorf("bd connect without --force did not mention --force in its refusal; want the backend-switch gate, not some other failure: exit=%d stdout=%s stderr=%s",
			withoutForce.code, withoutForce.stdout, withoutForce.stderr)
	}

	withForce := runBD(t, bin, workspace, nil, "connect", target, "--force", "--json")
	if withForce.code == 0 {
		t.Fatalf("bd connect %s --force succeeded against a port nothing listens on", target)
	}
	if strings.Contains(withForce.stderr, "--force") {
		t.Errorf("bd connect --force still hit the force-gate refusal; the gate was not bypassed: stderr=%s", withForce.stderr)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	if !strings.Contains(string(data), `"dolt"`) {
		t.Errorf("metadata.json no longer names backend dolt after the refused connect attempt: %s", data)
	}
}

// TestE2E_ConnectRedactsCredentialsEverywhereItPrintsTheURL is MED-3's
// regression test: a URL pasted with a userinfo password must never have
// that password echoed back by connect, in any output stream, on any path —
// success or failure. The target here fails at the handshake dial (loopback,
// nothing listening on port 1), which is exactly the path that used to wrap
// the raw argument into "connecting to %s: %v".
func TestE2E_ConnectRedactsCredentialsEverywhereItPrintsTheURL(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	workspace := t.TempDir()

	const password = "s3cr3t-password-998" //nolint — a fixture value, not a real credential.
	target := "http://alice:" + password + "@127.0.0.1:1/"

	r := runBD(t, bin, workspace, nil, "connect", target, "--json")
	if r.code == 0 {
		t.Fatalf("bd connect %s succeeded against a port nothing listens on", target)
	}
	if strings.Contains(r.stdout, password) || strings.Contains(r.stderr, password) {
		t.Errorf("bd connect's output carries the literal userinfo password %q: stdout=%s stderr=%s", password, r.stdout, r.stderr)
	}
}

// TestE2E_ReconnectRefusesADifferentProjectUnlessForced is MED-5's test: a
// bare re-connect (no --expect-project-id, no --force) against a server that
// answers with a DIFFERENT project id than the one the workspace's sidecar
// already pinned must refuse, not silently re-pin — connect.go used to build
// the new Target from --expect-project-id alone, so a plain `bd connect
// <other-server>` with nothing on the command line would adopt whatever the
// new server said with no refusal at all. --force must still get past it.
func TestE2E_ReconnectRefusesADifferentProjectUnlessForced(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)

	firstAddr := startE2EServer(t) // publishes e2eProjectID
	secondAddr := startE2EServerWithIdentity(t, "proj-e2e-s6-OTHER", "httpe2e_other")
	workspace := t.TempDir()

	first := runBD(t, bin, workspace, nil, "connect", "http://"+firstAddr, "--json")
	if first.code != 0 {
		t.Fatalf("first bd connect failed (exit %d): stdout=%s stderr=%s", first.code, first.stdout, first.stderr)
	}
	pinned, err := os.ReadFile(filepath.Join(workspace, ".beads", "http_target.json"))
	if err != nil {
		t.Fatalf("read sidecar after first connect: %v", err)
	}
	if !strings.Contains(string(pinned), e2eProjectID) {
		t.Fatalf("sidecar after first connect does not carry %q: %s", e2eProjectID, pinned)
	}

	// A bare re-connect to the DIFFERENT server, no --expect-project-id, no
	// --force: must refuse, carrying the prior pin forward rather than
	// adopting the new server's answer.
	again := runBD(t, bin, workspace, nil, "connect", "http://"+secondAddr, "--json")
	if again.code == 0 {
		t.Fatalf("bare re-connect to a different project's server succeeded; want a refusal: stdout=%s", again.stdout)
	}
	if !strings.Contains(again.stderr, e2eProjectID) {
		t.Errorf("re-connect refusal does not name the prior pin %q: stderr=%s", e2eProjectID, again.stderr)
	}
	stillPinned, err := os.ReadFile(filepath.Join(workspace, ".beads", "http_target.json"))
	if err != nil {
		t.Fatalf("read sidecar after refused re-connect: %v", err)
	}
	if string(stillPinned) != string(pinned) {
		t.Errorf("sidecar changed after a REFUSED re-connect: before=%s after=%s", pinned, stillPinned)
	}

	// --force gets past it and re-pins to the new server's project.
	forced := runBD(t, bin, workspace, nil, "connect", "http://"+secondAddr, "--force", "--json")
	if forced.code != 0 {
		t.Fatalf("bd connect --force to the other project failed (exit %d): stdout=%s stderr=%s", forced.code, forced.stdout, forced.stderr)
	}
	refetched, err := os.ReadFile(filepath.Join(workspace, ".beads", "http_target.json"))
	if err != nil {
		t.Fatalf("read sidecar after forced re-connect: %v", err)
	}
	if !strings.Contains(string(refetched), "proj-e2e-s6-OTHER") {
		t.Errorf("sidecar after --force re-connect does not carry the new project id: %s", refetched)
	}
}

// TestE2E_SwitchingBackToDoltLeavesOriginalIdentityIntact is MED-6's test:
// `bd connect --force` on a workspace that already selects dolt must not
// rewrite that workspace's git-tracked metadata.json project_id/database —
// the http identity belongs in the gitignored sidecar alone. This proves it
// by round-tripping: a dolt workspace with its own original project_id and
// database connects to http, and metadata.json's original dolt fields must
// still read back unchanged afterward — exactly what "switching back to
// dolt" would see, since nothing ever touched them to switch back FROM.
func TestE2E_SwitchingBackToDoltLeavesOriginalIdentityIntact(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t) // publishes e2eProjectID, unrelated to the dolt identity below.
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	const (
		originalProjectID = "dolt-original-project"
		originalDatabase  = "dolt-original-db"
	)
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	original := fmt.Sprintf(`{"backend":"dolt","database":%q,"project_id":%q}`, originalDatabase, originalProjectID)
	if err := os.WriteFile(metadataPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	forced := runBD(t, bin, workspace, nil, "connect", "http://"+addr, "--force", "--json")
	if forced.code != 0 {
		t.Fatalf("bd connect --force failed (exit %d): stdout=%s stderr=%s", forced.code, forced.stdout, forced.stderr)
	}

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata.json after connect: %v", err)
	}
	var after struct {
		Backend   string `json:"backend"`
		Database  string `json:"database"`
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatalf("parse metadata.json after connect: %v\n%s", err, data)
	}
	if after.Backend != "http" {
		t.Errorf("metadata.json backend = %q after connect, want %q: %s", after.Backend, "http", data)
	}
	// The MED-6 assertion: connect must NOT have overwritten these with the
	// http server's own database/project_id (e2eDatabase/e2eProjectID) — a
	// future switch back to dolt reads these same, untouched fields.
	if after.Database != originalDatabase {
		t.Errorf("metadata.json database = %q after bd connect --force, want the original %q untouched (MED-6: connect must not rewrite it): %s",
			after.Database, originalDatabase, data)
	}
	if after.ProjectID != originalProjectID {
		t.Errorf("metadata.json project_id = %q after bd connect --force, want the original %q untouched (MED-6: connect must not rewrite it): %s",
			after.ProjectID, originalProjectID, data)
	}

	// The http identity itself lives in the sidecar, not metadata.json.
	sidecar, err := os.ReadFile(filepath.Join(beadsDir, "http_target.json"))
	if err != nil {
		t.Fatalf("read sidecar after connect: %v", err)
	}
	if !strings.Contains(string(sidecar), e2eProjectID) {
		t.Errorf("sidecar does not carry the http server's project id %q: %s", e2eProjectID, sidecar)
	}
}

// TestE2E_ForceConnectWritesSucceedDespiteDifferingLocalProjectID pins the
// bee-ghosttrack CHANGES_REQUESTED finding on #7288: after `bd connect
// --force` over a workspace that already had a LOCAL (dolt) identity, every
// write used to fail with "workspace identity mismatch detected". That
// check compared metadata.json's project_id (the original dolt workspace's
// identity, left untouched by connect per MED-6 — see
// TestE2E_SwitchingBackToDoltLeavesOriginalIdentityIntact above) against the
// live server's _project_id, and those two are legitimately different
// projects; the check needs the http backend's OWN pin (the sidecar's
// ExpectProjectID, set by this same connect) instead.
//
// This reuses that test's exact setup — a workspace with its own original
// dolt project_id, forced onto an unrelated server's project — but goes
// further: it actually writes (create, update, comments add) over the new
// http connection, which is what the finding says used to fail outright.
func TestE2E_ForceConnectWritesSucceedDespiteDifferingLocalProjectID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t) // publishes e2eProjectID, unrelated to the dolt identity below.
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	const (
		originalProjectID = "dolt-original-project-for-writes"
		originalDatabase  = "dolt-original-db-for-writes"
	)
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	original := fmt.Sprintf(`{"backend":"dolt","database":%q,"project_id":%q}`, originalDatabase, originalProjectID)
	if err := os.WriteFile(metadataPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	forced := runBD(t, bin, workspace, nil, "connect", "http://"+addr, "--force", "--json")
	if forced.code != 0 {
		t.Fatalf("bd connect --force failed (exit %d): stdout=%s stderr=%s", forced.code, forced.stdout, forced.stderr)
	}

	create := runBD(t, bin, workspace, nil, "create", "A title over http after forced connect", "--json")
	if create.code != 0 {
		t.Fatalf("bd create after forced connect failed (exit %d): stdout=%s stderr=%s", create.code, create.stdout, create.stderr)
	}
	if strings.Contains(create.stderr, "workspace identity mismatch") {
		t.Fatalf("bd create tripped the workspace-identity-mismatch refusal despite connect having just pinned this project: stderr=%s", create.stderr)
	}
	id := firstJSONField(t, create.stdout, "id")
	if id == "" {
		t.Fatalf("bd create --json produced no id: %s", create.stdout)
	}

	update := runBD(t, bin, workspace, nil, "update", id, "-a", "alice", "--json")
	if update.code != 0 {
		t.Fatalf("bd update after forced connect failed (exit %d): stdout=%s stderr=%s", update.code, update.stdout, update.stderr)
	}
	if strings.Contains(update.stderr, "workspace identity mismatch") {
		t.Fatalf("bd update tripped the workspace-identity-mismatch refusal despite connect having just pinned this project: stderr=%s", update.stderr)
	}

	comment := runBD(t, bin, workspace, nil, "comments", "add", id, "a comment over http after forced connect", "--json")
	if comment.code != 0 {
		t.Fatalf("bd comments add after forced connect failed (exit %d): stdout=%s stderr=%s", comment.code, comment.stdout, comment.stderr)
	}
	if strings.Contains(comment.stderr, "workspace identity mismatch") {
		t.Fatalf("bd comments add tripped the workspace-identity-mismatch refusal despite connect having just pinned this project: stderr=%s", comment.stderr)
	}
}

// startE2EServer boots an in-process `bd serve` over embedded Dolt and
// returns its bound 127.0.0.1 address. It is the same role-binding shape as
// internal/httpclient's servedEnv harness, kept separate because that one is
// unexported in a different package: this test drives the CLI, not the
// internal wire client, so it owns its own (smaller) copy rather than
// reaching across the package boundary.
//
// This stays the one server topology this file uses — a real `bd serve`
// SUBPROCESS, gated on BEADS_TEST_DOLT_SERVER=local the way
// internal/testutil's local Dolt backend is elsewhere, was considered and
// rejected:
//
//  1. bd serve permanently refuses an embedded-Dolt workspace
//     (cmd/bd/serve.go's serveDatabaseSource, "EMBEDDED DOLT IS PERMANENT").
//     A real `bd` subprocess pointed at a `bd init`-created workspace with no
//     registered backend would therefore refuse to start at all — there is no
//     bd-init-then-bd-serve shape for this backend to drive as a subprocess.
//  2. The one way around that refusal, a registered backend whose Open hands
//     back a real embedded store (what cmd/bd/serve_registered_backend_test.go
//     does), only exists as Go wiring injected into the SAME test process —
//     registration happens at init() time, so a spawned `bd` subprocess has
//     nothing registered and cannot reproduce it. That file's own comment
//     reaches the identical conclusion for the identical reason.
//  3. The other way around it is a real, non-embedded Dolt SQL server behind
//     "shared"/proxied mode (BEADS_DOLT_SHARED_SERVER=1, internal/testutil's
//     local-or-container harness, cmd/bd/serve_proxied_integration_test.go's
//     startServe). That subprocess `bd serve` lifecycle contract — the bound
//     address on stdout's first line, signal-driven shutdown, the auth
//     posture warnings — is already pinned end to end there, against exactly
//     that topology. Reproducing it a second time in this package would
//     stand up a second real Dolt SQL server fixture per run to re-test the
//     same `bd serve` CLI plumbing that package already owns, not anything
//     specific to the public backend/http door this package exists to prove.
//
// What this e2e tier specifically has to show — bd connect, then full CRUD,
// actually working end to end over the wire against a real listening server
// — is exactly what the in-process httpapi.Listen harness below proves: it
// is the identical httpapi.Config / Listen / Serve call bd serve's own
// serveListen makes, bound to a real TCP socket a real `bd` binary dials
// unmodified. A real `bd serve` subprocess on top of that would add only
// coverage of serve's own flag parsing and workspace-source resolution,
// which belongs to (and is already owned by) cmd/bd/serve_test.go and
// cmd/bd/serve_proxied_integration_test.go.
func startE2EServer(t *testing.T) string {
	t.Helper()
	return startE2EServerWithIdentity(t, e2eProjectID, e2eDatabase)
}

// startE2EServerWithIdentity is startE2EServer, parameterized on the identity
// the in-process server publishes at handshake. MED-5's reconnect-mismatch
// test needs a SECOND server that answers with a DIFFERENT project id than
// the one a prior `bd connect` already pinned, to prove the pin is honored
// rather than silently replaced.
func startE2EServerWithIdentity(t *testing.T, projectID, database string) string {
	t.Helper()
	beadsDir := t.TempDir()
	ctx := context.Background()
	reference, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open the reference store: %v", err)
	}
	t.Cleanup(func() { _ = reference.Close() })
	if err := reference.SetConfig(ctx, "issue_prefix", "e2e"); err != nil {
		t.Fatalf("set the issue prefix: %v", err)
	}

	cfg := e2eServeConfig(t, reference, projectID, database)
	srv, err := httpapi.Listen(cfg)
	if err != nil {
		t.Fatalf("bind the in-process server: %v", err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the in-process server did not shut down")
		}
	})
	return srv.Addr()
}

func e2eServeConfig(t *testing.T, s storage.DoltStorage, projectID, database string) httpapi.Config {
	t.Helper()
	cfg := httpapi.Config{
		Addr:      "127.0.0.1:0",
		Stdout:    io.Discard,
		Stderr:    io.Discard,
		Workspace: domain.ContextInfo{ProjectID: projectID, Database: database},
	}
	var err error
	fail := func(name string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s(): %v", name, err)
		}
	}
	cfg.Reader, err = s.IssueReader()
	fail("IssueReader")
	cfg.Claimer, err = s.IssueClaimer()
	fail("IssueClaimer")
	cfg.ReadyClaimer, err = s.ReadyClaimer()
	fail("ReadyClaimer")
	cfg.Releaser, err = s.Releaser()
	fail("Releaser")
	cfg.Lifecycle, err = s.IssueLifecycle()
	fail("IssueLifecycle")
	cfg.Settings, err = s.WorkspaceConfig()
	fail("WorkspaceConfig")
	cfg.Stats, err = s.StatsReporter()
	fail("StatsReporter")
	cfg.CycleDetector, err = s.CycleDetector()
	fail("CycleDetector")
	cfg.EdgeReader, err = s.EdgeReader()
	fail("EdgeReader")
	cfg.GraphCounter, err = s.GraphCounter()
	fail("GraphCounter")
	cfg.Relations, err = s.IssueRelations()
	fail("IssueRelations")
	cfg.Commenter, err = s.Commenter()
	fail("Commenter")
	cfg.BlockingAnnotator, err = s.BlockingAnnotator()
	fail("BlockingAnnotator")
	cfg.TreeWalker, err = s.TreeWalker()
	fail("TreeWalker")
	cfg.ReadyCounter, err = s.ReadyCounter()
	fail("ReadyCounter")
	cfg.Counter, err = s.Counter()
	fail("Counter")
	cfg.Querier, err = s.Querier()
	fail("Querier")
	cfg.Sweeper, err = s.Sweeper()
	fail("Sweeper")
	cfg.Deleter, err = s.Deleter()
	fail("Deleter")
	cfg.BatchCreator, err = s.BatchCreator()
	fail("BatchCreator")
	cfg.BatchCloser, err = s.BatchCloser()
	fail("BatchCloser")
	cfg.DependencyEditor, err = s.DependencyEditor()
	fail("DependencyEditor")
	cfg.MetadataCAS, err = s.MetadataCAS()
	fail("MetadataCAS")
	cfg.BatchApplier, err = s.BatchApplier()
	fail("BatchApplier")
	cfg.Memories, err = s.Memories()
	fail("Memories")
	return cfg
}

// skipUnlessEmbeddedDolt mirrors internal/httpclient's gate: this tier needs
// the cgo-linked embedded engine, and BEADS_HTTP_TEST_REQUIRED=1 is how the
// served-surface lane says that tier is mandatory rather than best-effort.
func skipUnlessEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") == "1" {
		return
	}
	if os.Getenv("BEADS_HTTP_TEST_REQUIRED") == "1" {
		t.Fatal("BEADS_HTTP_TEST_REQUIRED=1 but BEADS_TEST_EMBEDDED_DOLT is not 1; the http e2e tier is not enforced")
	}
	t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run the http backend e2e tier")
}

type bdResult struct {
	stdout, stderr string
	code           int
}

// runBD shells the built bd binary out in dir, the same subprocess pattern
// test/conformance's e2e runner uses, kept local here so this tier has no
// dependency on that package's unexported helpers.
func runBD(t *testing.T, bin, dir string, env []string, args ...string) bdResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = minimalBDEnv(dir, env...)
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("running bd %s: %v", strings.Join(args, " "), err)
		}
	}
	return bdResult{stdout: o.String(), stderr: e.String(), code: code}
}

// minimalBDEnv builds a hermetic environment for a bd subprocess, the same
// sanitizing shape cmd/bd's own proxied-integration harness uses
// (bdProxiedEnv): PATH plus the few variables a Go binary needs to start,
// HOME repointed at dir rather than the real one, and nothing BEADS_*
// carried over from whatever invoked `go test`. A stray BEADS_DIR or
// BEADS_HTTP_TOKEN* exported in this process's own environment must never
// leak into a workspace this test means to fully control — this harness
// ran the full os.Environ() through unfiltered before, which a credential
// or workspace-override variable set in the ambient shell could silently
// have reached. extra layers on top (a test case's own deliberate override),
// so it is applied last and wins any collision.
func minimalBDEnv(dir string, extra ...string) []string {
	env := []string{
		"HOME=" + dir,
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
	}
	for _, key := range []string{"PATH", "TMPDIR", "SYSTEMROOT", "WINDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return append(env, extra...)
}

// jsonIDSet collects every top-level "id" field out of a bd --json array
// response (bd ready among others), for the containment checks a real
// assertion needs rather than firstJSONField's single-match convenience.
func jsonIDSet(t *testing.T, out string) map[string]bool {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("parse JSON array output: %v\n%s", err, out)
	}
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		if id, ok := row["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

// jsonIntField pulls one numeric top-level field out of a bd --json object
// response (bd count's {"count": N}).
func jsonIntField(t *testing.T, out, field string) int64 {
	t.Helper()
	var generic map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &generic); err != nil {
		t.Fatalf("parse JSON output for field %q: %v\n%s", field, err, out)
	}
	v, ok := generic[field].(float64)
	if !ok {
		t.Fatalf("field %q missing or not numeric in: %s", field, out)
	}
	return int64(v)
}

// firstJSONField pulls one string-valued top-level field out of a bd --json
// response, tolerating the schema_version envelope some commands wrap their
// payload in and the bare-object shape others return directly.
func firstJSONField(t *testing.T, out, field string) string {
	t.Helper()
	var generic any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &generic); err != nil {
		t.Fatalf("parse JSON output for field %q: %v\n%s", field, err, out)
	}
	return findField(generic, field)
}

func findField(v any, field string) string {
	switch m := v.(type) {
	case map[string]any:
		if s, ok := m[field].(string); ok {
			return s
		}
		for _, nested := range []string{"data", "issue"} {
			if inner, ok := m[nested]; ok {
				if s := findField(inner, field); s != "" {
					return s
				}
			}
		}
	case []any:
		for _, item := range m {
			if s := findField(item, field); s != "" {
				return s
			}
		}
	}
	return ""
}

// lastJSONLine parses the final non-blank line of stderr as JSON, matching
// reportIfRevisionFailure's own documented contract ("the JSON body is the
// LAST line on stderr").
func lastJSONLine(t *testing.T, stderr string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(line), &body); err == nil {
			return body
		}
		t.Fatalf("last stderr line is not JSON: %q\nfull stderr:\n%s", line, stderr)
	}
	t.Fatalf("stderr had no non-blank line to parse:\n%s", stderr)
	return nil
}

var (
	buildBDOnce   sync.Once
	bdBinPath     string
	bdBuildErr    error
	bdBinOwnBuild bool // true only on the go-build-from-source path; false for Bazel's prebuilt binary.
)

// buildBD builds the bd binary once per test process, matching the
// gms_pure_go embedded-engine tag used throughout cmd/bd's own CLI tests
// (e.g. cmd/bd/create_deps_atomic_test.go). Under Bazel there is no module
// tree to build from, so the prebuilt test binary is used instead.
//
// The binary is shared, via buildBDOnce, across every test in this package
// that calls buildBD — that sharing is the whole point, one build instead of
// one per test. Cleanup therefore cannot be a per-call t.Cleanup: the first
// caller to win the Once race would be the first test to finish, and its
// Cleanup would delete the binary every LATER test in the same process still
// needs (exactly what happened before this comment existed — the second and
// third test to call buildBD got "no such file or directory" because the
// first test's Cleanup had already removed it). TestMain below owns deleting
// it exactly once, after every test in the package has finished with it.
func buildBD(t *testing.T) string {
	t.Helper()
	buildBDOnce.Do(func() {
		if bazeltest.IsBazel() {
			bdBinPath, bdBuildErr = bazeltest.PrebuiltBD()
			return
		}
		dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "bd-http-e2e")
		if err != nil {
			bdBuildErr = fmt.Errorf("mkdir temp for bd binary: %w", err)
			return
		}
		bin := filepath.Join(dir, "bd-http-e2e")
		cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", bin, "./cmd/bd")
		cmd.Dir = e2eRepoRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			bdBuildErr = fmt.Errorf("build bd: %w\n%s", err, out)
			_ = os.RemoveAll(dir)
			return
		}
		bdBinPath = bin
		bdBinOwnBuild = true
	})
	if bdBuildErr != nil {
		t.Fatal(bdBuildErr)
	}
	return bdBinPath
}

// TestMain exists for exactly one reason: to delete the temp directory
// buildBD's go-build-from-source path creates, once, after every test in
// this package has had its turn with the shared binary inside it. See
// buildBD's doc comment for why a per-test t.Cleanup cannot do this job.
func TestMain(m *testing.M) {
	code := m.Run()
	if bdBinOwnBuild && bdBinPath != "" {
		_ = os.RemoveAll(filepath.Dir(bdBinPath))
	}
	os.Exit(code)
}

func e2eRepoRoot() string {
	_, file, _, _ := runtime.Caller(0) // <repo>/backend/http/e2e_cli_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}
