package ownershiphandoffv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// everyPhase is every durable checkpoint the machine can write, forward and
// rollback. Tests that must cover "at every phase" iterate this rather than a
// hand-copied list, so a new phase cannot be added without them noticing.
var everyPhase = []Phase{
	PhasePrepared, PhaseOldOwnerStopped, PhaseTargetConfigured, PhaseVerified, PhaseCommitted,
	PhaseRollbackStarted, PhaseLegacyConfigRestored, PhaseRolledBack,
}

// journalAt builds a journal that validate accepts at the given phase. The
// per-phase fixups are the invariants validate enforces, spelled out here so a
// test reads as "a valid journal at X" rather than as a struct literal.
func journalAt(t *testing.T, root string, phase Phase) Journal {
	t.Helper()
	j := Journal{
		Phase: phase,
		Owner: OwnerLegacy,
		Request: Request{
			Root:      root,
			Database:  "scope_db",
			Workspace: "workspace-uuid",
			Endpoint:  Endpoint{Host: "127.0.0.1", Port: 3307},
			Owner:     OwnerLegacy,
		},
	}
	switch phase {
	case PhaseVerified, PhaseCommitted:
		j.Target = Target{PID: 4242, Birth: "birth-token", LaunchID: "0123456789abcdef0123456789abcdef"}
		j.Reservations.TargetLaunch = ReservationDone
	}
	if phase == PhaseCommitted {
		j.Owner = OwnerBD
		j.Reservations.CommitWriteSet = ReservationDone
	}
	return j
}

// A v2 journal that forgets schema_version decodes to 0 under an older bd and
// is then read with the wrong phase semantics. The only defence is that the
// writer never omits it, so assert it at every phase rather than at one.
func TestSaveStampsSchemaVersionAtEveryPhase(t *testing.T) {
	for _, phase := range everyPhase {
		t.Run(string(phase), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, JournalName)
			j := journalAt(t, dir, phase)
			// Deliberately zero: save is the single place that stamps it, and no
			// caller should be able to write a journal without one.
			j.SchemaVersion = 0
			if err := save(path, j); err != nil {
				t.Fatalf("save: %v", err)
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var probe map[string]any
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got, present := probe["schema_version"]
			if !present {
				t.Fatalf("journal at %s has no schema_version field: %s", phase, raw)
			}
			if got != float64(SchemaVersion) {
				t.Fatalf("journal at %s has schema_version %v, want %d", phase, got, SchemaVersion)
			}

			loaded, err := Load(path)
			if err != nil {
				t.Fatalf("Load after save at %s: %v", phase, err)
			}
			if loaded.Phase != phase {
				t.Fatalf("round trip changed phase: got %s want %s", loaded.Phase, phase)
			}
			if loaded.UpdatedAt.IsZero() {
				t.Fatal("save did not stamp updated_at")
			}
		})
	}
}

// A v1 journal has no schema_version at all. It must be refused with the typed
// code rather than read under this package's phase vocabulary: the phase names
// overlap but old_owner_stopped means the opposite thing about the target.
func TestLoadRefusesV1Journal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, JournalName)
	v1 := `{
  "request": {"root": "` + dir + `", "database": "scope_db", "workspace": "w",
              "endpoint": {"host": "127.0.0.1", "port": 3307}, "owner": "legacy-gc"},
  "phase": "old_owner_stopped",
  "owner": "legacy-gc",
  "updated_at": "2026-01-01T00:00:00Z"
}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("write v1 fixture: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a v1 journal")
	}
	if code := ErrorCode(err); code != CodeUnsupportedJournalVersion {
		t.Fatalf("Load returned %q, want %q (error: %v)", code, CodeUnsupportedJournalVersion, err)
	}
}

func TestLoadRefusesFutureSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, JournalName)
	future := `{"schema_version": 99, "phase": "prepared", "owner": "legacy-gc",
	            "request": {"root": "` + dir + `"}}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := Load(path); ErrorCode(err) != CodeUnsupportedJournalVersion {
		t.Fatalf("Load returned %q, want %q", ErrorCode(err), CodeUnsupportedJournalVersion)
	}
}

// TODO(beads#6545): once the merged ownershiphandoff package exports
// ErrUnsupportedJournalVersion, this asserts that v2's Load maps that sentinel
// onto CodeUnsupportedJournalVersion rather than collapsing it into
// CodeJournalUnreadable. The mirror guard is #6545's to land (branch
// guard/journal-schema-version); this branch deliberately does not carry it, so
// the assertion is written against the code both sides agree on.
func TestMergedSentinelMapsToUnsupportedJournalVersion(t *testing.T) {
	t.Skip("pending beads#6545: merged ownershiphandoff.ErrUnsupportedJournalVersion does not exist yet")
}

func TestValidateRejectsImpossibleStates(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name  string
		mutue func(*Journal)
	}{
		{"unknown phase", func(j *Journal) { j.Phase = "halfway" }},
		{"unknown owner", func(j *Journal) { j.Owner = "somebody-else" }},
		{"committed but owned by legacy", func(j *Journal) { j.Phase = PhaseCommitted; j.Owner = OwnerLegacy }},
		{"uncommitted but owned by bd", func(j *Journal) { j.Phase = PhaseVerified; j.Owner = OwnerBD }},
		{"unknown reservation state", func(j *Journal) { j.Reservations.DataDirInit = "maybe" }},
		{"launch complete before configure", func(j *Journal) {
			j.Phase = PhasePrepared
			j.Reservations.TargetLaunch = ReservationDone
		}},
		{"verified without a target identity", func(j *Journal) {
			j.Phase = PhaseVerified
			j.Target = Target{}
		}},
		{"committed without a write-set reservation", func(j *Journal) {
			j.Reservations.CommitWriteSet = ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := journalAt(t, root, PhaseCommitted)
			tc.mutue(&j)
			if err := validate(j); err == nil {
				t.Fatalf("validate accepted %s", tc.name)
			}
		})
	}
}

func TestArchiveRolledBackJournalIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, JournalName)
	j := journalAt(t, dir, PhaseRolledBack)
	j.UpdatedAt = time.Date(2026, 9, 13, 0, 0, 0, 12345, time.UTC)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := archiveRolledBackJournal(path, j); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("archive left the live journal in place")
	}
	// A second admission of the same rollback picks the same archive name and
	// must treat "already archived, journal gone" as done, not as a collision.
	if err := archiveRolledBackJournal(path, j); err != nil {
		t.Fatalf("second archive: %v", err)
	}

	// With the live journal back, the same name IS ambiguous and must refuse
	// rather than clobber an archive that may be a different rollback.
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if err := archiveRolledBackJournal(path, j); err == nil {
		t.Fatal("archive clobbered an existing archive while a live journal existed")
	}
}

func TestJournalLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, JournalName)
	first, err := lockJournal(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer unlockJournal(first)

	if _, err := lockJournal(path); ErrorCode(err) != CodeJournalBusy {
		t.Fatalf("second lock returned %q, want %q", ErrorCode(err), CodeJournalBusy)
	}
}

func TestReachedComparesTracks(t *testing.T) {
	cases := []struct {
		phase Phase
		verb  string
		want  bool
	}{
		{PhasePrepared, VerbPrepare, true},
		{PhaseCommitted, VerbPrepare, true},
		{PhasePrepared, VerbCommit, false},
		{PhaseLegacyConfigRestored, VerbRollback, true},
		{PhaseRollbackStarted, VerbRollback, false},
		{PhaseRolledBack, VerbRollbackFinish, true},
		{PhaseLegacyConfigRestored, VerbRollbackFinish, false},
		// A rolled-back transfer never satisfies a forward phase, however far
		// it got before it was undone.
		{PhaseRolledBack, VerbCommit, false},
		{PhaseRolledBack, VerbPrepare, false},
	}
	for _, tc := range cases {
		got := Result{Phase: tc.phase}.Reached(tc.verb)
		if got != tc.want {
			t.Errorf("Result{%s}.Reached(%s) = %v, want %v", tc.phase, tc.verb, got, tc.want)
		}
	}
}
