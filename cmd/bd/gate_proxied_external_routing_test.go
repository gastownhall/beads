//go:build cgo

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// routedProxiedPair builds the #5861 item 2 topology: a proxied town whose
// routes.jsonl sends the rig prefix into a second proxied rig on the same
// shared external SQL server — the rig's sidecar names that server, so the
// routed read must go through the external connection. The route crosses two
// temp directories the way a real multi-rig tree crosses repo roots: a
// symlink under the town's root.
type routedProxiedPair struct {
	town, rig proxiedProject
}

func newRoutedProxiedPair(t *testing.T, bd, townPrefix, rigPrefix string) *routedProxiedPair {
	t.Helper()
	pair := &routedProxiedPair{
		town: newSharedProxiedProject(t, bd, townPrefix),
		rig:  newSharedProxiedProject(t, bd, rigPrefix),
	}
	link := filepath.Join(filepath.Dir(pair.town.beadsDir), rigPrefix+"-rig")
	if err := os.Symlink(pair.rig.dir, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, pair.rig.dir, err)
	}
	routes := []byte(`{"prefix":"` + rigPrefix + `-","path":"` + rigPrefix + `-rig"}`)
	if err := os.WriteFile(filepath.Join(pair.town.beadsDir, "routes.jsonl"), routes, 0o644); err != nil {
		t.Fatalf("write town routes.jsonl: %v", err)
	}
	return pair
}

// routedBeadGate creates a bead gate in the town awaiting awaitID, the way
// seedGateAwait does for local targets, and returns the gate's id and the
// town's proxy database for status reads.
func routedBeadGate(t *testing.T, bd string, pair *routedProxiedPair, awaitID string) (string, *sql.DB) {
	t.Helper()
	gate := bdProxiedCreate(t, bd, pair.town.dir, "Routed bead gate", "--type", "gate")
	db := openProxiedDB(t, pair.town)
	seedGateAwait(t, db, gate.ID, "bead", awaitID, time.Now().UTC(), 0)
	return gate.ID, db
}

// TestProxiedServerGateCheckRoutedExternalTarget proves #5861 item 2's gate
// outcome end to end. A proxied town's bead gate awaiting a bead that lives in
// another proxied rig — whose sidecar names the external SQL server — resolves
// when that bead closes and stays pending while it is open, and the direct
// (non-proxied) getter answers the same route. Before the fix the routed read
// dead-ended at the proxy.store.unrouted refusal, so no such gate could be
// confirmed resolved, closed target or not.
func TestProxiedServerGateCheckRoutedExternalTarget(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	pair := newRoutedProxiedPair(t, bd, "gxa", "gxb")
	rigClosed := bdProxiedCreate(t, bd, pair.rig.dir, "Routed target that closes")
	bdProxiedClose(t, bd, pair.rig.dir, rigClosed.ID)
	rigOpen := bdProxiedCreate(t, bd, pair.rig.dir, "Routed target that stays open")

	gateClosed, db := routedBeadGate(t, bd, pair, rigClosed.ID)
	gateOpen, _ := routedBeadGate(t, bd, pair, rigOpen.ID)

	out, stderr, err := bdProxiedRunBuffers(t, bd, pair.town.dir, "gate", "check", "--type", "bead")
	if err != nil {
		t.Fatalf("gate check over the routed external target failed: %v\nstderr:\n%s", err, stderr)
	}
	if got := readStatus(t, db, gateClosed); got != types.StatusClosed {
		t.Errorf("gate awaiting a closed routed external target must resolve, got %q\n%s", got, out)
	}
	if got := readStatus(t, db, gateOpen); got != types.StatusOpen {
		t.Errorf("gate awaiting an open routed external target must stay pending, got %q\n%s", got, out)
	}
	if !strings.Contains(out, "1 resolved") {
		t.Errorf("expected exactly one of the two routed gates to resolve, got:\n%s", out)
	}
}

// directTownStore opens the direct launcher's town store on the shared
// container, the way newTestStoreIsolatedDB does in the non-proxied lane. In
// the proxied lane startTestDoltServer never sets testDoltServerPort, so that
// helper would skip; the town needs no isolation machinery beyond its own
// database — it is never reopened by path, only the routed rig is. The
// database is left for container teardown, like the harness's bdtest ones
// (dropping databases live on the shared container crashes it).
func directTownStore(t *testing.T, dbPath string) *dolt.DoltStore {
	t.Helper()
	ensureTestMode(t)
	cfg := &dolt.Config{
		Path:            dbPath,
		ServerHost:      "127.0.0.1",
		ServerPort:      sharedProxiedServerPort(t),
		Database:        uniqueTestDBName(t),
		CommitterName:   "test",
		CommitterEmail:  "test@test.com",
		CreateIfMissing: true,
	}
	doltNewMutex.Lock()
	s, err := dolt.New(context.Background(), cfg)
	doltNewMutex.Unlock()
	if err != nil {
		t.Fatalf("create direct town store: %v", err)
	}
	if err := s.SetConfig(context.Background(), "issue_prefix", "hq"); err != nil {
		s.Close()
		t.Fatalf("set issue_prefix: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestDirectGateGetterRoutedExternalTarget is the direct (non-proxied)
// launcher's half of #5861 item 2's gate outcome: an ordinary direct-mode town
// routing into a proxied rig reads the same external target through
// routedBeadGateGetter, so a closed target resolves the check and an open one
// reports its status instead of an unconfirmed absence. Not parallel: it pins
// dbPath and the working directory.
func TestDirectGateGetterRoutedExternalTarget(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)

	pair := newRoutedProxiedPair(t, bd, "gxc", "gxd")
	rigClosed := bdProxiedCreate(t, bd, pair.rig.dir, "Routed target that closes")
	bdProxiedClose(t, bd, pair.rig.dir, rigClosed.ID)
	rigOpen := bdProxiedCreate(t, bd, pair.rig.dir, "Routed target that stays open")

	ctx := context.Background()
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatalf("create direct town .beads: %v", err)
	}
	townDB := filepath.Join(townBeadsDir, "dolt")
	townStore := directTownStore(t, townDB)
	defer townStore.Close()
	link := filepath.Join(townRoot, "gxd-rig")
	if err := os.Symlink(pair.rig.dir, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, pair.rig.dir, err)
	}
	routes := []byte(`{"prefix":"gxd-","path":"gxd-rig"}`)
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), routes, 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath := dbPath
	dbPath = townDB
	t.Cleanup(func() { dbPath = oldDBPath })
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("change to direct town root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	getter := routedBeadGateGetter{localStore: townStore}
	resolved, _, err := checkBeadGate(ctx, getter, rigClosed.ID)
	if err != nil {
		t.Fatalf("direct getter failed on the closed routed external target: %v", err)
	}
	if !resolved {
		t.Error("direct getter must resolve a closed routed external target")
	}
	resolved, reason, err := checkBeadGate(ctx, getter, rigOpen.ID)
	if err != nil {
		t.Fatalf("direct getter failed on the open routed external target: %v", err)
	}
	if resolved {
		t.Error("direct getter must not resolve an open routed external target")
	}
	if !gateTestContainsIgnoreCase(reason, "open") {
		t.Errorf("pending reason %q does not report the target's status", reason)
	}
}
