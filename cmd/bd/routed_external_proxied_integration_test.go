//go:build cgo

package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/types"
)

// Routed reads into a proxied-server rig whose sidecar names an external Dolt
// server (gastownhall/beads#5861), against the shard's shared dolt
// sql-server. newSharedProxiedProject gives each rig its own database and a
// scoped user that can see only it, so a read that reached B's bead through
// A's user or database would fail here. Unit cases that need no server are in
// foreign_proxied_external_read_test.go.

// routeExternalRig writes a routes.jsonl into from's .beads sending prefix
// to the rig directory to, relative to from (the town root routes resolve
// against is the directory holding the launcher's .beads).
func routeExternalRig(t *testing.T, from, to, prefix string) {
	t.Helper()
	rel, err := filepath.Rel(from, to)
	if err != nil {
		t.Fatalf("relative route %s -> %s: %v", from, to, err)
	}
	line := `{"prefix":"` + prefix + `-","path":"` + filepath.ToSlash(rel) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(from, ".beads", "routes.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
}

// externalOnlyCopy is a rig directory holding only rig's metadata.json and
// proxied-server sidecar, with dolt_database replaced when database is
// non-empty: a clone of rig's workspace that has never run a proxy, so a test
// can watch every file a foreign read could touch.
func externalOnlyCopy(t *testing.T, rig proxiedProject, database string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "rig")
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := configfile.LoadForDiscovery(rig.beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load %s metadata: %v", rig.beadsDir, err)
	}
	if database != "" {
		cfg.DoltDatabase = database
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save copy metadata: %v", err)
	}
	info, err := configfile.LoadProxiedServerClientInfo(rig.beadsDir)
	if err != nil || info == nil || info.External == nil {
		t.Fatalf("rig %s has no external sidecar: %v (info=%+v)", rig.beadsDir, err, info)
	}
	if err := configfile.SaveProxiedServerClientInfo(beadsDir, &configfile.ProxiedServerClientInfo{External: info.External}); err != nil {
		t.Fatalf("save copy sidecar: %v", err)
	}
	return dir
}

// sharedServerState is what a foreign read must not change on the shared
// server: the databases it holds and the working root of database.
func sharedServerState(t *testing.T, port int, database string) string {
	t.Helper()
	db, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: database}.String())
	if err != nil {
		t.Fatalf("open shared server: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		t.Fatalf("SHOW DATABASES: %v", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	_ = rows.Close()
	sort.Strings(names)
	var working string
	// database is uniqueProxiedDatabase's bdtest_<n>: safe to splice.
	if err := db.QueryRowContext(ctx, "SELECT @@"+database+"_working").Scan(&working); err != nil {
		t.Fatalf("read %s working root: %v", database, err)
	}
	return strings.Join(names, ",") + " " + working
}

// TestProxiedServerRoutedExternalTargetInProcess runs the routed read in this
// process, as a direct launcher does, so it can drive routing's globals
// (dbPath) and the launcher's environment. It is not parallel for that
// reason.
func TestProxiedServerRoutedExternalTargetInProcess(t *testing.T) {
	port := requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)

	a := newSharedProxiedProject(t, bd, "rla")
	b := newSharedProxiedProject(t, bd, "rlb")
	target := bdProxiedCreate(t, bd, b.dir, "Routed external target")
	// The read goes to a copy of B's configuration that never ran a proxy, so
	// every file under it is one the read alone could have written.
	bCopy := externalOnlyCopy(t, b, "")
	missing := externalOnlyCopy(t, b, b.database+"_missing")

	// A's own connection settings, which must not reach B's server.
	isolateBeadsDirForTest(t)
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", a.database)
	t.Setenv("BEADS_DOLT_SERVER_USER", a.database)
	t.Setenv("BEADS_DOLT_SERVER_PORT", "1")
	t.Setenv("BEADS_DOLT_PASSWORD", "launcher-secret")
	t.Setenv(configfile.ExternalDoltPasswordEnvVar, "")
	oldDBPath := dbPath
	dbPath = filepath.Join(a.beadsDir, "dolt")
	t.Cleanup(func() { dbPath = oldDBPath })
	ctx := context.Background()

	// AC-1: the routed read returns B's bead from B's database as B's user.
	t.Run("routed_read_returns_target_bead", func(t *testing.T) {
		routeExternalRig(t, a.dir, bCopy, "rlb")
		result, err := resolveViaPrefixRouting(ctx, target.ID)
		if err != nil {
			t.Fatalf("routed read of %s: %v", target.ID, err)
		}
		defer result.Close()
		if result.Issue == nil || result.Issue.ID != target.ID || result.Issue.Title != "Routed external target" {
			t.Fatalf("routed read returned %+v, want B's bead %s", result.Issue, target.ID)
		}
		if !result.Routed {
			t.Error("result not marked routed")
		}
	})

	// AC-2 (direct launcher) and AC-5 (absent bead): the routed gate lookup
	// reports the target's actual state, and an absent bead as unconfirmed.
	t.Run("gate_lookup_reports_target_state", func(t *testing.T) {
		routeExternalRig(t, a.dir, bCopy, "rlb")
		issue, err := prefixRoutedBeadGateTarget(ctx, target.ID)
		if err != nil || issue == nil || issue.Status != types.StatusOpen {
			t.Fatalf("gate lookup of open %s = %+v, %v; want it open", target.ID, issue, err)
		}
		gateTarget := bdProxiedCreate(t, bd, b.dir, "Closed external target")
		bdProxiedClose(t, bd, b.dir, gateTarget.ID)
		issue, err = prefixRoutedBeadGateTarget(ctx, gateTarget.ID)
		if err != nil || issue == nil || issue.Status != types.StatusClosed {
			t.Fatalf("gate lookup of closed %s = %+v, %v; want it closed", gateTarget.ID, issue, err)
		}
		issue, err = prefixRoutedBeadGateTarget(ctx, "rlb-nosuchbead")
		if issue != nil || !errors.Is(err, errBeadGateTargetUnconfirmed) || !strings.Contains(err.Error(), `no issue found matching "rlb-nosuchbead"`) {
			t.Fatalf("gate lookup of an absent bead = %+v, %v; want it unconfirmed", issue, err)
		}
	})

	// AC-4: reads that succeed, miss, and name a database the server does not
	// hold change no file of the target workspace and nothing on the server.
	t.Run("reads_write_nothing", func(t *testing.T) {
		before := sharedServerState(t, port, b.database)
		copyFiles := snapshotForeignTree(t, bCopy)
		missingFiles := snapshotForeignTree(t, missing)

		routeExternalRig(t, a.dir, bCopy, "rlb")
		if result, err := resolveViaPrefixRouting(ctx, target.ID); err != nil {
			t.Fatalf("routed read of %s: %v", target.ID, err)
		} else {
			result.Close()
		}
		if _, err := resolveViaPrefixRouting(ctx, "rlb-nosuchbead"); err == nil {
			t.Fatal("routed read of an absent bead succeeded")
		}
		assertForeignTreeUnchanged(t, bCopy, copyFiles)

		routeExternalRig(t, a.dir, missing, "rlb")
		_, err := resolveViaPrefixRouting(ctx, target.ID)
		if err == nil {
			t.Fatal("routed read of a database the server does not hold succeeded")
		}
		if isProxiedStoreUnrouted(err) {
			t.Fatalf("routed read refused instead of tried: %v", err)
		}
		issue, gateErr := prefixRoutedBeadGateTarget(ctx, target.ID)
		if issue != nil || !errors.Is(gateErr, errBeadGateTargetUnconfirmed) {
			t.Fatalf("gate lookup into a missing database = %+v, %v; want it unconfirmed", issue, gateErr)
		}
		assertForeignTreeUnchanged(t, missing, missingFiles)

		if after := sharedServerState(t, port, b.database); after != before {
			t.Errorf("shared server changed under foreign reads:\nbefore %s\nafter  %s", before, after)
		}
	})
}

// TestProxiedServerGateRoutedExternalTarget drives bd gate check and bd close
// in proxied-server rig A on gates awaiting beads in external proxied-server
// rig B, reached through A's routes.jsonl.
func TestProxiedServerGateRoutedExternalTarget(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	// AC-2: gate check leaves the gate pending on B's open bead, for that
	// reason, and resolves it once the bead closes.
	t.Run("gate_check_follows_target_state", func(t *testing.T) {
		t.Parallel()
		a := newSharedProxiedProject(t, bd, "rga")
		b := newSharedProxiedProject(t, bd, "rgb")
		routeExternalRig(t, a.dir, b.dir, "rgb")
		target := bdProxiedCreate(t, bd, b.dir, "Awaited external bead")
		gate := bdProxiedCreate(t, bd, a.dir, "Cross-rig bead gate", "--type", "gate")
		db := openProxiedDB(t, a)
		seedGateAwait(t, db, gate.ID, "bead", target.ID, time.Now().UTC(), 0)

		out, stderr, err := bdProxiedRunBuffers(t, bd, a.dir, "gate", "check", "--type", "bead")
		if err != nil {
			t.Fatalf("gate check (target open) failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got == types.StatusClosed {
			t.Fatal("gate resolved while its external target is open")
		}
		if combined := out + stderr; strings.Contains(combined, "cannot confirm") || strings.Contains(combined, "proxied-server route") {
			t.Fatalf("gate pending because its target could not be read, not because it is open:\n%s", combined)
		}

		bdProxiedClose(t, bd, b.dir, target.ID)
		out, stderr, err = bdProxiedRunBuffers(t, bd, a.dir, "gate", "check", "--type", "bead")
		if err != nil {
			t.Fatalf("gate check (target closed) failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got != types.StatusClosed {
			t.Fatalf("gate should resolve once its external target closes, got %q\nstdout:\n%s\nstderr:\n%s", got, out, stderr)
		}
		if !strings.Contains(out, "1 resolved") {
			t.Errorf("expected 1 resolved, got:\n%s", out)
		}
	})

	// AC-3: bd close's gate pre-check refuses while B's bead is open and
	// permits the close once it is closed.
	t.Run("close_precheck_follows_target_state", func(t *testing.T) {
		t.Parallel()
		a := newSharedProxiedProject(t, bd, "rca")
		b := newSharedProxiedProject(t, bd, "rcb")
		routeExternalRig(t, a.dir, b.dir, "rcb")
		target := bdProxiedCreate(t, bd, b.dir, "Awaited external bead")
		gate := bdProxiedCreate(t, bd, a.dir, "Cross-rig bead gate", "--type", "gate")
		db := openProxiedDB(t, a)
		seedGateAwait(t, db, gate.ID, "bead", target.ID, time.Now().UTC(), 0)

		out, stderr, err := bdProxiedRunBuffers(t, bd, a.dir, "close", gate.ID)
		if err == nil {
			t.Fatalf("bd close of a gate awaiting an open external bead succeeded:\n%s", out)
		}
		combined := out + stderr
		if !strings.Contains(combined, "gate condition not satisfied") || !strings.Contains(combined, target.ID+" is open") {
			t.Fatalf("bd close refused for another reason than the open target:\n%s", combined)
		}
		if got := readStatus(t, db, gate.ID); got == types.StatusClosed {
			t.Fatal("refused close closed the gate")
		}

		bdProxiedClose(t, bd, b.dir, target.ID)
		bdProxiedClose(t, bd, a.dir, gate.ID)
		if got := readStatus(t, db, gate.ID); got != types.StatusClosed {
			t.Errorf("gate should close once its external target is closed, got %q", got)
		}
	})

	// AC-5: an awaited bead B does not hold leaves the gate pending as
	// unconfirmed, not resolved as gone.
	t.Run("absent_target_stays_unconfirmed", func(t *testing.T) {
		t.Parallel()
		a := newSharedProxiedProject(t, bd, "rua")
		b := newSharedProxiedProject(t, bd, "rub")
		routeExternalRig(t, a.dir, b.dir, "rub")
		gate := bdProxiedCreate(t, bd, a.dir, "Cross-rig bead gate", "--type", "gate")
		db := openProxiedDB(t, a)
		seedGateAwait(t, db, gate.ID, "bead", "rub-nosuchbead", time.Now().UTC(), 0)

		out, stderr, err := bdProxiedRunBuffers(t, bd, a.dir, "gate", "check", "--type", "bead")
		if err != nil {
			t.Fatalf("gate check failed: %v\nstderr:\n%s", err, stderr)
		}
		if got := readStatus(t, db, gate.ID); got == types.StatusClosed {
			t.Fatal("gate resolved on an awaited bead its rig does not hold")
		}
		if combined := out + stderr; !strings.Contains(combined, `cannot confirm the awaited bead is gone: bead rub-nosuchbead routes to`) {
			t.Errorf("expected the routed miss reported unconfirmed, got:\n%s", combined)
		}
	})
}

// TestProxiedServerDepAddRoutedExternalTarget is AC-8: bd dep add in
// proxied-server rig A verifies a target in external proxied-server rig B
// through A's route, so an existing target is stored without the
// unresolved-target warning and an absent one is stored with it.
func TestProxiedServerDepAddRoutedExternalTarget(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	a := newSharedProxiedProject(t, bd, "rda")
	b := newSharedProxiedProject(t, bd, "rdb")
	routeExternalRig(t, a.dir, b.dir, "rdb")
	target := bdProxiedCreate(t, bd, b.dir, "External dependency target")
	existing := bdProxiedCreate(t, bd, a.dir, "Depends on an existing external bead")
	absent := bdProxiedCreate(t, bd, a.dir, "Depends on an absent external bead")
	db := openProxiedDB(t, a)
	const warning = "warning: dependency target"

	_, stderr, err := bdProxiedRunBuffers(t, bd, a.dir, "dep", "add", existing.ID, target.ID)
	if err != nil {
		t.Fatalf("dep add %s -> %s failed: %v\nstderr:\n%s", existing.ID, target.ID, err, stderr)
	}
	if strings.Contains(stderr, warning) {
		t.Errorf("an existing external target was reported unresolved:\n%s", stderr)
	}
	assertProxiedDepExists(t, db, existing.ID, target.ID)

	const missingID = "rdb-nosuchbead"
	_, stderr, err = bdProxiedRunBuffers(t, bd, a.dir, "dep", "add", absent.ID, missingID)
	if err != nil {
		t.Fatalf("dep add %s -> %s failed: %v\nstderr:\n%s", absent.ID, missingID, err, stderr)
	}
	if !strings.Contains(stderr, warning) || !strings.Contains(stderr, "was not found in this database or through routing") {
		t.Errorf("an absent external target was not reported unresolved:\n%s", stderr)
	}
	assertProxiedDepExists(t, db, absent.ID, missingID)
}

// directTownStore opens a direct (non-proxied) launcher's town store on the
// shared container, the way newTestStoreIsolatedDB does in the non-proxied
// lane. In the proxied lane startTestDoltServer never sets
// testDoltServerPort, so that helper would skip; the town needs no isolation
// machinery beyond its own database, since only the routed rig is reopened by
// path. The database is left for container teardown, like the harness's
// bdtest ones (dropping databases live on the shared container crashes it).
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

// TestProxiedServerDirectGateGetterRoutedExternalTarget is AC-2's direct
// launcher half: an ordinary direct-mode town routing into external
// proxied-server rig B reads B's bead through routedBeadGateGetter, so
// checkBeadGate resolves a closed target and reports an open one's status
// instead of an unconfirmed absence. Not parallel: it pins dbPath and the
// working directory.
func TestProxiedServerDirectGateGetterRoutedExternalTarget(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)

	b := newSharedProxiedProject(t, bd, "gxd")
	rigClosed := bdProxiedCreate(t, bd, b.dir, "Routed target that closes")
	bdProxiedClose(t, bd, b.dir, rigClosed.ID)
	rigOpen := bdProxiedCreate(t, bd, b.dir, "Routed target that stays open")

	ctx := context.Background()
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatalf("create direct town .beads: %v", err)
	}
	townDB := filepath.Join(townBeadsDir, "dolt")
	townStore := directTownStore(t, townDB)
	routeExternalRig(t, townRoot, b.dir, "gxd")

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
