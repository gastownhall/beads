package ownershiphandoff

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRolledBackAdmissionIsOneTimeAndArchivesTheJournal pins the lifetime of
// the restored-artifact comparison. It decides that a rollback completed; it
// cannot also be a standing invariant, because the rollback's own last step is
// asking the legacy owner to start again, and that owner canonicalises the
// config of a city it has just taken back.
func TestRolledBackAdmissionIsOneTimeAndArchivesTheJournal(t *testing.T) {
	const token = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	restored := map[string][]byte{
		"metadata.json":    []byte(`{"project_id":"ws-1"}`),
		"config.yaml":      []byte("dolt.host: 127.0.0.1\ntypes.custom: bug,chore\n"),
		"dolt-server.port": []byte("3307"),
	}
	setup := func(t *testing.T, phase Phase) (string, string, Journal) {
		t.Helper()
		r := validRequest(t)
		beadsDir := filepath.Join(r.Root, ".beads")
		if err := os.Mkdir(beadsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, data := range restored {
			if err := os.WriteFile(filepath.Join(beadsDir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		journal := Journal{Request: r, Phase: phase, Owner: OwnerLegacyGC, SnapshotCaptured: true, MutationOccurred: true,
			Snapshot: Snapshot{Metadata: validGCMetadata(t, r, token), Sentinel: token,
				WorkspaceMetadata: restored["metadata.json"], WorkspaceMetadataPresent: true, WorkspaceMetadataMode: 0o600,
				WorkspaceConfig: restored["config.yaml"], WorkspaceConfigPresent: true, WorkspaceConfigMode: 0o600,
				WorkspacePort: restored["dolt-server.port"], WorkspacePortPresent: true, WorkspacePortMode: 0o600}}
		path := filepath.Join(beadsDir, JournalName)
		if err := save(path, journal); err != nil {
			t.Fatal(err)
		}
		return beadsDir, path, journal
	}
	// The legacy owner's ordinary upgrade behaviour for a city it has just
	// taken back: a rewrite of a file the journal's snapshot predates.
	canonicalise := func(t *testing.T, beadsDir string) {
		t.Helper()
		merged := append(append([]byte{}, restored["config.yaml"]...), []byte("gc.vocabulary: merged\n")...)
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), merged, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("matching artifacts admit once and archive", func(t *testing.T) {
		beadsDir, path, _ := setup(t, PhaseRolledBack)
		if err := CheckNormalOpen(beadsDir); err != nil {
			t.Fatalf("a rolled-back journal whose artifacts are back refused normal open: %v", err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("the live journal survived the admission: %v", err)
		}
		archives, err := filepath.Glob(path + ".rolled-back-*")
		if err != nil || len(archives) != 1 {
			t.Fatalf("archives=%v err=%v, want the admission recorded as one archive", archives, err)
		}
		canonicalise(t, beadsDir)
		if err := CheckNormalOpen(beadsDir); err != nil {
			t.Fatalf("an admitted rollback refused normal open after the legacy owner rewrote config.yaml: %v", err)
		}
	})

	t.Run("artifacts that never matched stay refused", func(t *testing.T) {
		beadsDir, path, _ := setup(t, PhaseRolledBack)
		canonicalise(t, beadsDir)
		err := CheckNormalOpen(beadsDir)
		if err == nil || handoffErrorCode(err, "") != "invalid_journal" {
			t.Fatalf("error=%v, want typed invalid_journal for a restore that was never proven", err)
		}
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("an unproven rollback archived its journal anyway: %v", statErr)
		}
	})

	t.Run("a mid-rollback journal is still a fence", func(t *testing.T) {
		beadsDir, path, _ := setup(t, PhaseLegacyConfigRestored)
		err := CheckNormalOpen(beadsDir)
		if err == nil || handoffErrorCode(err, "") != "lifecycle_busy" {
			t.Fatalf("error=%v, want typed lifecycle_busy while the rollback is unfinished", err)
		}
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("an unfinished rollback lost its journal: %v", statErr)
		}
	})

	t.Run("an archived rollback is an absent journal", func(t *testing.T) {
		beadsDir, path, journal := setup(t, PhaseRolledBack)
		if err := archiveRolledBackJournal(path, journal); err != nil {
			t.Fatal(err)
		}
		canonicalise(t, beadsDir)
		if err := CheckNormalOpen(beadsDir); err != nil {
			t.Fatalf("an archived rollback fenced normal open: %v", err)
		}
	})
}

// TestArchivingARolledBackJournalTwiceIsTheSameArchive keeps the ordinary
// concurrent case — a reader admitting the rollback while a fresh handoff
// starts — from becoming a refusal. The archive name is derived from the
// journal, so whoever loses the race finds its own work already done.
func TestArchivingARolledBackJournalTwiceIsTheSameArchive(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, JournalName)
	journal := Journal{Request: r, Phase: PhaseRolledBack, Owner: OwnerLegacyGC, SnapshotCaptured: true, MutationOccurred: true,
		UpdatedAt: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	if err := save(path, journal); err != nil {
		t.Fatal(err)
	}
	if err := archiveRolledBackJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	if err := archiveRolledBackJournal(path, journal); err != nil {
		t.Fatalf("re-archiving an already archived rollback: %v", err)
	}
	archives, err := filepath.Glob(path + ".rolled-back-*")
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives=%v err=%v, want exactly one", archives, err)
	}
	// A live journal beside an archive of the same name is ambiguous, and the
	// archive is an audit record: it must not be clobbered.
	if err := save(path, journal); err != nil {
		t.Fatal(err)
	}
	if err := archiveRolledBackJournal(path, journal); err == nil {
		t.Fatal("a second journal was archived over an existing archive")
	}
}
