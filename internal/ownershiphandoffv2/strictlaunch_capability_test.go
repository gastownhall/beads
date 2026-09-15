package ownershiphandoffv2

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// stubStrictLaunchUnsupported makes this process look like darwin or Windows,
// where no kernel-bound launch identity exists.
func stubStrictLaunchUnsupported(t *testing.T) {
	t.Helper()
	prior := supportsStrictLaunch
	supportsStrictLaunch = func() bool { return false }
	t.Cleanup(func() { supportsStrictLaunch = prior })
}

// A platform that cannot bind a launched server to a durable launch intent must
// never reach the spawn. Not "must clean up after the spawn" — must not spawn,
// because a replacement bd cannot prove it started is one rollback cannot
// safely stop, and a server nobody can identify is exactly the orphan that
// outlives the test that made it.
//
// CI only runs the integration suite on Linux, which is the one platform where
// the capability is present, so the refusal is pinned here through a seam
// rather than left to a platform no test exercises.
func TestConfigureNeverSpawnsWhereItCannotProveTheLaunch(t *testing.T) {
	stubStrictLaunchUnsupported(t)

	root := canonicalTempDir(t)
	beadsDir := BeadsDir(root)
	dataDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.ProjectID = "workspace-under-test"
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	// A journal already at old_owner_stopped: configure is the next verb, and
	// nothing before it is relevant to what is being asserted.
	j := journalAt(t, root, PhaseOldOwnerStopped)
	if err := save(JournalPath(root), j); err != nil {
		t.Fatalf("save journal: %v", err)
	}

	before := doltServerCount(t)

	result, err := Run(context.Background(), VerbConfigure, Options{
		Root:      root,
		Database:  "scope_db",
		Workspace: "workspace-uuid",
		Endpoint:  Endpoint{Host: "127.0.0.1", Port: 3307},
		BDVersion: "test",
	})
	if ErrorCode(err) != CodeTargetLaunchFailed {
		t.Fatalf("configure returned %q, want %q (err: %v)", ErrorCode(err), CodeTargetLaunchFailed, err)
	}
	if result.Phase != PhaseOldOwnerStopped {
		t.Fatalf("the refusal advanced the phase to %s", result.Phase)
	}

	// The refusal has to come BEFORE the reservation, or a crash-recovery path
	// would go looking for a child that was never allowed to exist.
	reloaded, loadErr := Load(JournalPath(root))
	if loadErr != nil {
		t.Fatalf("reload journal: %v", loadErr)
	}
	if reloaded.Reservations.TargetLaunch != "" {
		t.Errorf("a launch was reserved on a platform that cannot prove one: %q",
			reloaded.Reservations.TargetLaunch)
	}
	if reloaded.Reservations.DataDirInit != "" {
		t.Errorf("the data dir was reserved for init before the capability refusal: %q",
			reloaded.Reservations.DataDirInit)
	}
	if reloaded.Target.LaunchID != "" || reloaded.Target.PID != 0 {
		t.Errorf("a target identity was recorded for a launch that never happened: %+v", reloaded.Target)
	}

	// Nothing was created that a later run would have to clean up.
	if entries, readErr := os.ReadDir(beadsDir); readErr == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "dolt-handoff-") {
				t.Errorf("a nonce-bound launch config was written for a launch that never happened: %s", entry.Name())
			}
		}
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, ".dolt")); statErr == nil {
		t.Error("the data dir was initialized before the capability refusal")
	}

	// And the decisive one: no server outlived the verb.
	if got := doltServerCount(t); got != before {
		t.Errorf("dolt sql-server count went %d -> %d; a refusal spawned a server", before, got)
	}

	// The refusal is journaled as a capability, not as a mystery.
	attempt := result.Evidence[string(PhaseOldOwnerStopped)+":attempt"]
	if got := attempt.Gates["strict_launch_supported"]; got != GateUnavailable {
		t.Errorf("gate strict_launch_supported is %q, want %q", got, GateUnavailable)
	}
}

// doltServerCount counts `dolt sql-server` processes visible to this user, so a
// test can assert that a code path started none. Counting rather than listing
// keeps it robust on a shared machine where other work is running.
func doltServerCount(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", "dolt sql-server").Output()
	if err != nil {
		// pgrep exits 1 when nothing matches, which is a count of zero rather
		// than a failure.
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// rollback must be able to retire an interrupted launch, and on a platform that
// cannot identify one it must say so rather than guess. Guessing here means
// signalling a pid nobody can prove.
func TestRollbackRefusesOrphanRecoveryWhereItCannotProveIdentity(t *testing.T) {
	stubStrictLaunchUnsupported(t)

	root := canonicalTempDir(t)
	if err := os.MkdirAll(BeadsDir(root), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	x := &run{
		path: JournalPath(root),
		req:  Request{Root: root},
		j: Journal{
			Phase:        PhaseRollbackStarted,
			Owner:        OwnerLegacy,
			Request:      Request{Root: root},
			Reservations: Reservations{TargetLaunch: ReservationInProgress},
			Target: Target{
				LaunchID:     "0123456789abcdef0123456789abcdef",
				LaunchConfig: filepath.Join(BeadsDir(root), "dolt-handoff-0123456789abcdef0123456789abcdef.yaml"),
				Executable:   "/usr/local/bin/dolt",
			},
		},
	}
	e := newEvidence("test")
	err := x.retireOrphanTarget(&e)
	if ErrorCode(err) != CodeTargetLaunchFailed {
		t.Fatalf("retireOrphanTarget returned %q, want %q (err: %v)", ErrorCode(err), CodeTargetLaunchFailed, err)
	}
	if got := e.Gates["orphan_retired"]; got != GateUnavailable {
		t.Errorf("gate orphan_retired is %q, want %q", got, GateUnavailable)
	}
}
