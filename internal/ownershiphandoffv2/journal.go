// Package ownershiphandoffv2 transfers a Dolt scope's lifecycle ownership from
// a caller-managed server to bd, one journaled verb at a time.
//
// The caller drives the sequence and owns its own server; bd owns the journal,
// the replacement server, the fences and the rollback. bd never calls the
// caller back and spawns nothing but dolt. Every transition that changes what
// bd believes about ownership is gated on bd's own observation — the caller's
// inputs are hints, recorded as hints, and never sufficient on their own.
//
// Every mutation bd makes outside the journal is reserved in the journal before
// it happens and confirmed after, so a crash between the two is recoverable
// rather than ambiguous.
package ownershiphandoffv2

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

// SchemaVersion is written into every journal this package saves, at every
// phase, without exception.
//
// It is the only thing standing between a v2 journal and a bd that reads phase
// names from an older vocabulary. The phase sets overlap but the ORDER differs:
// old_owner_stopped means "target already configured" under v1 and "no target
// yet" under v2, so a v1 reader resuming a v2 journal would launch a second
// server over a running target. An absent field decodes to 0, which is why the
// writer must never omit it and the reader must never accept it.
const SchemaVersion = 2

// JournalName is the handoff journal's fixed name inside a workspace's .beads
// directory. The caller's projection reads it from exactly here.
const JournalName = "ownership-handoff.json"

// Owner identifies the lifecycle authority for a scope.
type Owner string

const (
	OwnerLegacy Owner = "legacy-gc"
	OwnerBD     Owner = "bd"
)

// Phase is a durable checkpoint. The forward track runs prepared → committed;
// the rollback track branches off any uncommitted phase and ends archived.
type Phase string

const (
	PhasePrepared         Phase = "prepared"
	PhaseOldOwnerStopped  Phase = "old_owner_stopped"
	PhaseTargetConfigured Phase = "target_configured"
	PhaseVerified         Phase = "verified"
	PhaseCommitted        Phase = "committed"

	PhaseRollbackStarted      Phase = "rollback_started"
	PhaseLegacyConfigRestored Phase = "legacy_config_restored"
	PhaseRolledBack           Phase = "rolled_back"
)

var forwardRank = map[Phase]int{
	PhasePrepared:         1,
	PhaseOldOwnerStopped:  2,
	PhaseTargetConfigured: 3,
	PhaseVerified:         4,
	PhaseCommitted:        5,
}

var rollbackRank = map[Phase]int{
	PhaseRollbackStarted:      1,
	PhaseLegacyConfigRestored: 2,
	PhaseRolledBack:           3,
}

// IsRollbackPhase reports whether p is on the rollback track.
func IsRollbackPhase(p Phase) bool { _, ok := rollbackRank[p]; return ok }

// Endpoint is a loopback TCP endpoint. Sockets are out of scope: a socket path
// is a containment problem of its own and the second hop needs it too.
type Endpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (e Endpoint) String() string { return fmt.Sprintf("%s:%d", e.Host, e.Port) }

// Request is the scope being transferred. It is exactly the shape the merged
// base uses, with no caller-specific fields: anything the caller knows that bd
// does not observe belongs in Hints.
type Request struct {
	Root      string   `json:"root"`
	Database  string   `json:"database"`
	Workspace string   `json:"workspace"`
	Endpoint  Endpoint `json:"endpoint"`
	Owner     Owner    `json:"owner"`
}

// Hints are what the caller believes. They may narrow what bd looks at; they
// never stand in for looking. Journaled so an operator can compare the caller's
// belief with what bd actually found.
type Hints struct {
	LegacyPID      int    `json:"legacy_pid,omitempty"`
	LegacyPIDBirth string `json:"legacy_pid_birth,omitempty"`
	Caller         string `json:"caller,omitempty"`
}

// Instance is a process bd resolved for itself. Resolved false means bd could
// not bind a process to this workspace, and records why; gates that would have
// rested on the identity are then recorded unavailable rather than passed.
type Instance struct {
	Resolved   bool      `json:"resolved"`
	PID        int       `json:"pid,omitempty"`
	Birth      string    `json:"birth,omitempty"`
	BoundBy    string    `json:"bound_by,omitempty"`
	Source     string    `json:"source,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

// Target is the replacement server bd launched, identified strictly enough to
// stop later without a pid-reuse race.
type Target struct {
	PID          int    `json:"pid,omitempty"`
	Birth        string `json:"birth,omitempty"`
	DataDir      string `json:"data_dir,omitempty"`
	LaunchID     string `json:"launch_id,omitempty"`
	LaunchConfig string `json:"launch_config,omitempty"`
	Executable   string `json:"executable,omitempty"`
	Host         string `json:"host,omitempty"`
	Port         int    `json:"port,omitempty"`
}

// Artifact is one file captured byte- and mode-exact before any mutation.
// Present distinguishes "was empty" from "was absent", which is the difference
// between restoring a file and removing one.
type Artifact struct {
	Present bool        `json:"present"`
	Data    []byte      `json:"data,omitempty"`
	Mode    os.FileMode `json:"mode,omitempty"`
}

// Sentinels are cheap facts about the served data, read through the legacy
// server before the transfer and re-read through the replacement after. They
// prove the replacement serves the same scope, not merely a scope.
type Sentinels struct {
	FirstIssueID      string `json:"first_issue_id"`
	FirstIssueFound   bool   `json:"first_issue_found"`
	FirstDependency   string `json:"first_dependency"`
	FirstDepFound     bool   `json:"first_dependency_found"`
	HeadHash          string `json:"head_hash"`
	DatabaseSelection string `json:"database_selection"`
}

// Snapshot is everything bd must be able to put back. Every key the commit
// write set touches is represented here, either as whole-file bytes or as the
// specific value that was there before.
type Snapshot struct {
	Metadata      Artifact `json:"metadata"`
	Config        Artifact `json:"config"`
	PortFile      Artifact `json:"port_file"`
	PIDFile       Artifact `json:"pid_file"`
	DataDirIsDolt bool     `json:"data_dir_is_dolt"`

	// MetadataDoltServerPort is the metadata.json dolt_server_port as found at
	// prepare. Recorded separately from the file bytes because clearing it is
	// what flips bd's own server-mode resolver from external to owned, so its
	// prior value is the single most load-bearing thing in the snapshot.
	MetadataDoltServerPort int `json:"metadata_dolt_server_port"`
	// CommitWriteSet names every key commit will write, resolved at prepare so
	// the snapshot can be checked for completeness before anything is touched.
	CommitWriteSet []string  `json:"commit_write_set"`
	Sentinels      Sentinels `json:"sentinels"`
}

// Reservation state for a mutation outside the journal.
const (
	ReservationInProgress = "in_progress"
	ReservationDone       = "done"
)

// Reservations record intent before the fact. A crash between reserving and
// confirming leaves in_progress on disk, which is what tells the next verb to
// go looking for a half-made change instead of assuming none was made.
type Reservations struct {
	DataDirInit    string `json:"data_dir_init,omitempty"`
	TargetLaunch   string `json:"target_launch,omitempty"`
	CommitWriteSet string `json:"commit_write_set,omitempty"`
}

// Evidence is what bd observed for one transition: each named gate's outcome,
// plus enough about the observer to judge the result later.
type Evidence struct {
	Gates        map[string]string `json:"gates"`
	Platform     string            `json:"platform"`
	BDVersion    string            `json:"bd_version"`
	PortHolder   string            `json:"port_holder,omitempty"`
	ObservedAt   time.Time         `json:"observed_at"`
	Observations map[string]string `json:"observations,omitempty"`
}

// Gate outcomes. Unavailable is only ever written for a gate the contract marks
// platform-dependent; a gate that could have been evaluated and was not is a
// bug, not a skip.
const (
	GatePassed      = "passed"
	GateUnavailable = "unavailable"
	GateSkipped     = "skipped"
)

// Journal is the durable state, saved atomically under a flock.
type Journal struct {
	SchemaVersion  int                 `json:"schema_version"`
	Phase          Phase               `json:"phase"`
	Owner          Owner               `json:"owner"`
	Request        Request             `json:"request"`
	Hints          Hints               `json:"hints"`
	LegacyInstance Instance            `json:"legacy_instance"`
	Target         Target              `json:"target"`
	Snapshot       Snapshot            `json:"snapshot"`
	Reservations   Reservations        `json:"reservations"`
	Evidence       map[string]Evidence `json:"evidence"`
	ErrorCode      string              `json:"error_code,omitempty"`
	Error          string              `json:"error,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

// JournalPath returns the journal's location for a workspace root.
func JournalPath(root string) string {
	return filepath.Join(root, ".beads", JournalName)
}

// BeadsDir returns the .beads directory for a workspace root.
func BeadsDir(root string) string { return filepath.Join(root, ".beads") }

// Load reads and validates a journal.
//
// A journal whose schema_version is not this package's is refused with
// CodeUnsupportedJournalVersion — including the absent field, which decodes to
// zero and means a v1 journal. There is deliberately no opt-in to read one: no
// released bd ever wrote a v1 journal, and the fixtures that need one for a
// test write raw JSON rather than round-tripping through this function.
func Load(path string) (Journal, error) {
	b, err := os.ReadFile(path) //nolint:gosec // caller-selected journal path
	if err != nil {
		return Journal{}, err
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return Journal{}, codedf(CodeJournalUnreadable, "decode handoff journal: %w", err)
	}
	if j.SchemaVersion != SchemaVersion {
		return Journal{}, codedf(CodeUnsupportedJournalVersion,
			"handoff journal schema version %d is not %d; this journal was written by a bd with a different phase vocabulary",
			j.SchemaVersion, SchemaVersion)
	}
	if err := validate(j); err != nil {
		return Journal{}, coded(CodeJournalUnreadable, err)
	}
	return j, nil
}

// validate rejects journal states the phase machine cannot have written. A
// reservation only means something in the phases that take it, and the owner
// flips exactly once, at commit.
func validate(j Journal) error {
	if j.Owner != OwnerLegacy && j.Owner != OwnerBD {
		return errors.New("handoff journal has unknown owner")
	}
	if _, forward := forwardRank[j.Phase]; !forward {
		if _, back := rollbackRank[j.Phase]; !back {
			return fmt.Errorf("handoff journal has unknown phase %q", j.Phase)
		}
	}
	if j.Phase == PhaseCommitted && j.Owner != OwnerBD {
		return errors.New("committed handoff journal must be owned by bd")
	}
	if j.Phase != PhaseCommitted && j.Owner != OwnerLegacy {
		return errors.New("an uncommitted handoff journal must remain owned by the legacy owner")
	}
	if err := validReservation(j.Reservations.DataDirInit); err != nil {
		return fmt.Errorf("data_dir_init reservation: %w", err)
	}
	if err := validReservation(j.Reservations.TargetLaunch); err != nil {
		return fmt.Errorf("target_launch reservation: %w", err)
	}
	if err := validReservation(j.Reservations.CommitWriteSet); err != nil {
		return fmt.Errorf("commit_write_set reservation: %w", err)
	}
	// A target launch cannot be complete before the phase that completes it,
	// and cannot be absent in a phase that depends on it.
	if j.Reservations.TargetLaunch == ReservationDone && forwardRank[j.Phase] < forwardRank[PhaseTargetConfigured] &&
		!IsRollbackPhase(j.Phase) {
		return errors.New("handoff journal records a completed target launch before it was configured")
	}
	if j.Phase == PhaseVerified || j.Phase == PhaseCommitted {
		if j.Target.PID <= 0 || j.Target.LaunchID == "" {
			return fmt.Errorf("handoff journal at %s has no target identity", j.Phase)
		}
	}
	if j.Phase == PhaseCommitted && j.Reservations.CommitWriteSet != ReservationDone {
		return errors.New("committed handoff journal has no completed write-set reservation")
	}
	return nil
}

func validReservation(v string) error {
	switch v {
	case "", ReservationInProgress, ReservationDone:
		return nil
	default:
		return fmt.Errorf("unknown state %q", v)
	}
}

// save writes the journal atomically: temp file in the same directory, fsync,
// rename, fsync the directory. SchemaVersion and UpdatedAt are stamped here, in
// one place, so no caller can produce a journal that forgets either.
func save(path string, j Journal) error {
	j.SchemaVersion = SchemaVersion
	j.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, JournalName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op once the rename succeeds
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // directory fsync
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// lockJournal takes the exclusive advisory lock that serializes verbs against
// each other. It is a separate file from the journal so the lock survives the
// journal's atomic rename.
func lockJournal(journalPath string) (*os.File, error) {
	path := journalPath + ".lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // sibling of the journal
	if err != nil {
		return nil, coded(CodeJournalUnreadable, err)
	}
	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		if lockfile.IsLocked(err) {
			return nil, codedf(CodeJournalBusy, "another ownership handoff verb holds %s", path)
		}
		return nil, coded(CodeJournalUnreadable, err)
	}
	return f, nil
}

func unlockJournal(f *os.File) {
	if f == nil {
		return
	}
	_ = lockfile.FlockUnlock(f)
	_ = f.Close()
}

// newEvidence starts an evidence record for one transition.
func newEvidence(bdVersion string) Evidence {
	return Evidence{
		Gates:      map[string]string{},
		Platform:   runtime.GOOS,
		BDVersion:  bdVersion,
		ObservedAt: time.Now().UTC(),
	}
}

func (e *Evidence) record(gate, outcome string) { e.Gates[gate] = outcome }

func (e *Evidence) note(key, value string) {
	if e.Observations == nil {
		e.Observations = map[string]string{}
	}
	e.Observations[key] = value
}

// hasUnavailable reports whether any gate in this evidence could not be
// evaluated. A commit carrying one is still a commit; status surfaces it so an
// operator can tell a four-gate transfer from a two-gate one.
func (e Evidence) hasUnavailable() bool {
	for _, outcome := range e.Gates {
		if outcome == GateUnavailable {
			return true
		}
	}
	return false
}

func (j *Journal) setEvidence(phase Phase, e Evidence) {
	if j.Evidence == nil {
		j.Evidence = map[string]Evidence{}
	}
	j.Evidence[string(phase)] = e
}

// archiveRolledBackJournal retires a completed rollback's journal. The archive
// name is derived from the journal itself so a concurrent reader admitting the
// same rollback picks the same one: an archive that already exists with the
// live journal gone is this rename having happened, not a collision, while one
// with the journal still present is ambiguous and must not be clobbered.
func archiveRolledBackJournal(path string, j Journal) error {
	stamp := j.UpdatedAt.UTC()
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}
	archive := fmt.Sprintf("%s.rolled-back-%d", path, stamp.UnixNano())
	if _, err := os.Lstat(archive); err == nil {
		if _, liveErr := os.Lstat(path); os.IsNotExist(liveErr) {
			return nil
		}
		return errors.New("rolled-back handoff archive already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(path, archive); err != nil {
		if os.IsNotExist(err) {
			// Only a concurrent admission of the same rollback removes it.
			// There is nothing left to archive and nothing to refuse.
			return nil
		}
		return err
	}
	return syncDir(filepath.Dir(path))
}
