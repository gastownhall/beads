//go:build cgo

package main

// The release-candidate move through the real version-bump front door: one
// bd startup is trackBdVersion() deciding from .local_version, then
// autoMigrateOnVersionBump() reconciling through the store, and what bd doctor
// reads afterwards is the bd_version marker.
//
// The planner table in internal/workapi cannot see this. The reconciler is only
// reached when trackBdVersion reports an upgrade, which is decided by a second
// comparator (doctor.CompareVersions), and both used to read a dotted
// candidate ("1.4.0-rc.3") as newer than its release (#6595).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/issueops"
)

func TestVersionBump_ReleaseRecordsOverItsCandidate(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}
	ensureCleanGlobalState(t)

	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	database := uniqueTestDBName(t)
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   database,
	}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        database,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
	// What a 1.4.0-rc.3 binary leaves behind.
	if err := store.SetLocalMetadata(ctx, "bd_version", "1.4.0-rc.3"); err != nil {
		t.Fatalf("seed bd_version: %v", err)
	}
	if err := store.SetLocalMetadata(ctx, "bd_version_max", "1.4.0-rc.3"); err != nil {
		t.Fatalf("seed bd_version_max: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}
	// autoMigrateOnVersionBump stats cfg.DatabasePath before opening anything.
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("create local dolt data dir: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Chdir(repo)

	origVersion, origDetected, origPrevious := Version, versionUpgradeDetected, previousVersion
	t.Cleanup(func() { Version, versionUpgradeDetected, previousVersion = origVersion, origDetected, origPrevious })

	recorded := func() issueops.RecordedVersionResult {
		t.Helper()
		s, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{ReadOnly: true})
		if err != nil {
			t.Fatalf("reopen store: %v", err)
		}
		defer func() { _ = s.Close() }()
		r, err := s.VersionReconciler()
		if err != nil {
			t.Fatalf("version reconciler: %v", err)
		}
		got, err := r.RecordedVersion(ctx, issueops.RecordedVersionRequest{})
		if err != nil {
			t.Fatalf("RecordedVersion: %v", err)
		}
		return got
	}
	// run is one bd startup with this binary version: the two pre-run calls
	// main.go makes, in order.
	run := func(bin string) {
		t.Helper()
		Version, versionUpgradeDetected, previousVersion = bin, false, ""
		trackBdVersion()
		autoMigrateOnVersionBump(beadsDir)
		got := recorded()
		t.Logf("after %-10s upgradeDetected=%-5v markers=%+v", bin, versionUpgradeDetected, got)
	}

	if err := writeLocalVersion(filepath.Join(beadsDir, localVersionFile), "1.4.0-rc.3"); err != nil {
		t.Fatalf("write .local_version: %v", err)
	}

	run("1.4.0")
	if got := recorded(); got.Recorded != "1.4.0" {
		t.Errorf("rc.3 -> 1.4.0: bd_version = %q, want %q (bd doctor would show \"version %s (CLI: 1.4.0)\")",
			got.Recorded, "1.4.0", got.Recorded)
	}

	// An rc excursion and back must end on the release, too.
	run("1.4.0-rc.3")
	run("1.4.0")
	if got := recorded(); got.Recorded != "1.4.0" {
		t.Errorf("1.4.0 -> rc.3 -> 1.4.0: bd_version = %q, want %q", got.Recorded, "1.4.0")
	}
}
