package ownershiphandoffv2

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/doltserver"
)

// Verb names, as typed on the command line.
const (
	VerbPrepare        = "prepare"
	VerbLegacyGone     = "legacy-gone"
	VerbConfigure      = "configure"
	VerbVerify         = "verify"
	VerbCommit         = "commit"
	VerbRollback       = "rollback"
	VerbRollbackFinish = "rollback-finish"
	VerbStatus         = "status"
)

// Verbs lists the phases in the order a caller drives them, for help text and
// for validation.
var Verbs = []string{
	VerbPrepare, VerbLegacyGone, VerbConfigure, VerbVerify, VerbCommit,
	VerbRollback, VerbRollbackFinish, VerbStatus,
}

// targetPhase is the phase a verb is trying to reach. A verb exits zero exactly
// when the journal has reached it.
var targetPhase = map[string]Phase{
	VerbPrepare:        PhasePrepared,
	VerbLegacyGone:     PhaseOldOwnerStopped,
	VerbConfigure:      PhaseTargetConfigured,
	VerbVerify:         PhaseVerified,
	VerbCommit:         PhaseCommitted,
	VerbRollback:       PhaseLegacyConfigRestored,
	VerbRollbackFinish: PhaseRolledBack,
}

// Options are everything a verb needs. Root, Database, Workspace and Endpoint
// are the request; Hints are the caller's beliefs; the rest are seams.
type Options struct {
	Root      string
	Database  string
	Workspace string
	Endpoint  Endpoint
	Hints     Hints
	BDVersion string

	// AfterSpawn models a crash between the replacement server's exec and its
	// lifecycle files being recorded. Production leaves it nil.
	AfterSpawn func(pid int) error
	// AfterReserve models a crash after a reservation reaches disk and before
	// the mutation it covers. It is called with the reservation's name.
	// Production leaves it nil.
	AfterReserve func(reservation string) error
}

// Result is the one JSON object every verb prints.
type Result struct {
	SchemaVersion  int                 `json:"schema_version"`
	Phase          Phase               `json:"phase"`
	Owner          Owner               `json:"owner"`
	Mutates        bool                `json:"mutates"`
	Request        Request             `json:"request"`
	LegacyInstance Instance            `json:"legacy_instance"`
	Target         Target              `json:"target"`
	Evidence       map[string]Evidence `json:"evidence"`
	ErrorCode      string              `json:"error_code,omitempty"`
	Error          string              `json:"error,omitempty"`
	// EvidenceIncomplete is true when any journaled gate was unavailable. A
	// commit carrying one is still a commit, but an operator can tell a
	// four-gate transfer from a two-gate one without reading the journal.
	EvidenceIncomplete bool `json:"evidence_incomplete"`
}

// Reached reports whether the journal got to the phase the verb was asking
// for, which is exactly the exit-zero condition.
func (r Result) Reached(verb string) bool {
	want, ok := targetPhase[verb]
	if !ok {
		return r.ErrorCode == ""
	}
	if IsRollbackPhase(want) {
		return IsRollbackPhase(r.Phase) && rollbackRank[r.Phase] >= rollbackRank[want]
	}
	// A rolled-back journal never satisfies a forward phase, however far the
	// transfer got before it was undone.
	if IsRollbackPhase(r.Phase) {
		return false
	}
	return forwardRank[r.Phase] >= forwardRank[want]
}

// Run executes one verb against the workspace's journal, under its lock.
func Run(ctx context.Context, verb string, opts Options) (Result, error) {
	req, err := buildRequest(opts)
	if err != nil {
		return Result{SchemaVersion: SchemaVersion, ErrorCode: ErrorCode(err), Error: err.Error()}, err
	}
	if verb == VerbStatus {
		return status(req)
	}
	if _, known := targetPhase[verb]; !known {
		err := codedf(CodeInvalidRequest, "unknown ownership handoff phase %q; expected one of %s",
			verb, strings.Join(Verbs, ", "))
		return Result{SchemaVersion: SchemaVersion, Request: req, ErrorCode: ErrorCode(err), Error: err.Error()}, err
	}

	journalPath := JournalPath(req.Root)
	lock, err := lockJournal(journalPath)
	if err != nil {
		return Result{SchemaVersion: SchemaVersion, Request: req, ErrorCode: ErrorCode(err), Error: err.Error()}, err
	}
	defer unlockJournal(lock)

	j, existed, err := loadExisting(journalPath)
	if err != nil {
		return Result{SchemaVersion: SchemaVersion, Request: req, ErrorCode: ErrorCode(err), Error: err.Error()}, err
	}
	if existed {
		if err := sameScope(j.Request, req); err != nil {
			return resultOf(j, err), err
		}
	} else {
		if verb != VerbPrepare {
			err := codedf(CodePhaseOrder, "no ownership handoff journal for %s; run prepare first", req.Root)
			return Result{SchemaVersion: SchemaVersion, Request: req, ErrorCode: ErrorCode(err), Error: err.Error()}, err
		}
		j = Journal{Phase: "", Owner: OwnerLegacy, Request: req}
	}
	// Hints are refreshed on every verb: they describe the caller's current
	// belief, and a stale one is less useful than none.
	j.Hints = opts.Hints

	x := &run{j: j, path: journalPath, opts: opts, req: req, ctx: ctx, existed: existed}
	var runErr error
	switch verb {
	case VerbPrepare:
		runErr = x.prepare()
	case VerbLegacyGone:
		runErr = x.legacyGone()
	case VerbConfigure:
		runErr = x.configure()
	case VerbVerify:
		runErr = x.verify()
	case VerbCommit:
		runErr = x.commit()
	case VerbRollback:
		runErr = x.rollback()
	case VerbRollbackFinish:
		runErr = x.rollbackFinish()
	}
	return resultOf(x.j, runErr), runErr
}

// run carries one verb's state. Every mutation of j is followed by a save
// before the next observable side effect, which is what makes a crash
// recoverable rather than ambiguous.
type run struct {
	j    Journal
	path string
	opts Options
	req  Request
	ctx  context.Context
	// existed records whether a journal was on disk when this verb started, so
	// a refusal can tell leaving one behind from updating one already there.
	existed bool
}

func (x *run) save() error {
	if err := save(x.path, x.j); err != nil {
		return coded(CodeJournalUnreadable, err)
	}
	return nil
}

// advance records a completed transition: the phase, its evidence, and a
// cleared error from any previous attempt.
func (x *run) advance(phase Phase, e Evidence) error {
	x.j.Phase = phase
	x.j.setEvidence(phase, e)
	x.j.ErrorCode = ""
	x.j.Error = ""
	return x.save()
}

// fail records a refusal in the journal without advancing. The journal is the
// record of what bd tried, not only of what worked.
//
// One exception: a prepare that refuses before it ever reached a phase leaves
// NO journal. prepare's contract is that it mutates nothing, and dropping a
// file into someone else's .beads directory is a mutation — one the caller
// would then have to clean up before it could retry.
func (x *run) fail(err error, e *Evidence) error {
	if !x.existed && x.j.Phase == "" {
		return err
	}
	x.j.ErrorCode = ErrorCode(err)
	x.j.Error = err.Error()
	if e != nil {
		x.j.setEvidence(Phase(x.j.Phase.String()+":attempt"), *e)
	}
	_ = x.save()
	return err
}

// String lets a phase name itself inside an evidence key.
func (p Phase) String() string { return string(p) }

func (x *run) evidence() Evidence { return newEvidence(x.opts.BDVersion) }

func (x *run) beadsDir() string { return BeadsDir(x.req.Root) }

// dataDir is where bd's own resolver says this workspace's Dolt storage lives.
// It is deliberately not <root>/.beads/dolt spelled out: an env override or a
// dolt_data_dir in metadata.json moves it, and the replacement server is
// launched against whatever this returns — so a gate that looked somewhere else
// would be proving things about a directory nobody is serving.
func (x *run) dataDir() string { return doltserver.ResolveDoltDir(x.beadsDir()) }

// reserve records an intent to mutate, durably, before the mutation happens.
func (x *run) reserve(name, state string) error {
	switch name {
	case "data_dir_init":
		x.j.Reservations.DataDirInit = state
	case "target_launch":
		x.j.Reservations.TargetLaunch = state
	case "commit_write_set":
		x.j.Reservations.CommitWriteSet = state
	default:
		return fmt.Errorf("unknown reservation %q", name)
	}
	if err := x.save(); err != nil {
		return err
	}
	if state == ReservationInProgress && x.opts.AfterReserve != nil {
		return x.opts.AfterReserve(name)
	}
	return nil
}

func loadExisting(path string) (Journal, bool, error) {
	j, err := Load(path)
	if err == nil {
		return j, true, nil
	}
	if os.IsNotExist(err) {
		return Journal{}, false, nil
	}
	return Journal{}, false, err
}

// sameScope refuses a request that does not match the journal's. Journals are
// never merged: a request naming a different database or endpoint is a
// different transfer, and silently adopting the journal would hand it the
// snapshot and reservations of a scope it never observed.
func sameScope(have, want Request) error {
	switch {
	case have.Root != want.Root:
		return codedf(CodeIdentityConflict, "journal is for root %q, not %q", have.Root, want.Root)
	case have.Database != want.Database:
		return codedf(CodeIdentityConflict, "journal is for database %q, not %q", have.Database, want.Database)
	case have.Workspace != want.Workspace:
		return codedf(CodeIdentityConflict, "journal is for workspace %q, not %q", have.Workspace, want.Workspace)
	case have.Endpoint != want.Endpoint:
		return codedf(CodeIdentityConflict, "journal is for endpoint %s, not %s", have.Endpoint, want.Endpoint)
	}
	return nil
}

func buildRequest(opts Options) (Request, error) {
	root := opts.Root
	if root == "" {
		return Request{}, coded(CodeInvalidRequest, errors.New("--root is required"))
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return Request{}, codedf(CodeInvalidRequest, "--root %q must be an absolute, cleaned path", root)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if opts.Database == "" {
		return Request{}, coded(CodeInvalidRequest, errors.New("--database is required"))
	}
	if opts.Workspace == "" {
		return Request{}, coded(CodeInvalidRequest, errors.New("--workspace is required"))
	}
	if opts.Endpoint.Host == "" || opts.Endpoint.Port <= 0 || opts.Endpoint.Port > 65535 {
		return Request{}, codedf(CodeInvalidRequest, "--legacy-endpoint must be host:port, got %s", opts.Endpoint)
	}
	if !isLoopback(opts.Endpoint.Host) {
		return Request{}, codedf(CodeUnsupportedScope,
			"--legacy-endpoint host %q is not loopback; only a local, directly-managed server can be taken over",
			opts.Endpoint.Host)
	}
	return Request{
		Root:      root,
		Database:  opts.Database,
		Workspace: opts.Workspace,
		Endpoint:  opts.Endpoint,
		Owner:     OwnerLegacy,
	}, nil
}

// isLoopback accepts only addresses that cannot leave this machine. A hostname
// is rejected rather than resolved: resolution can change between the check and
// the connection, and "localhost" on a host with a creative /etc/hosts is not a
// guarantee of anything.
func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ParseEndpoint splits a host:port flag value.
func ParseEndpoint(s string) (Endpoint, error) {
	host, portText, err := net.SplitHostPort(s)
	if err != nil {
		return Endpoint{}, codedf(CodeInvalidRequest, "parse endpoint %q: %w", s, err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		return Endpoint{}, codedf(CodeInvalidRequest, "parse endpoint port %q: %w", portText, err)
	}
	return Endpoint{Host: host, Port: port}, nil
}

func resultOf(j Journal, err error) Result {
	r := Result{
		SchemaVersion:  SchemaVersion,
		Phase:          j.Phase,
		Owner:          j.Owner,
		Request:        j.Request,
		LegacyInstance: j.LegacyInstance,
		Target:         j.Target,
		Evidence:       j.Evidence,
		Mutates:        mutated(j),
	}
	if r.Owner == "" {
		r.Owner = OwnerLegacy
	}
	for _, e := range j.Evidence {
		if e.hasUnavailable() {
			r.EvidenceIncomplete = true
			break
		}
	}
	if err != nil {
		r.ErrorCode = ErrorCode(err)
		r.Error = err.Error()
	}
	return r
}

// mutated reports whether anything outside the journal has been touched,
// cumulatively. A reservation counts even at in_progress: that is the whole
// point of writing it down first.
func mutated(j Journal) bool {
	return j.Reservations.DataDirInit != "" ||
		j.Reservations.TargetLaunch != "" ||
		j.Reservations.CommitWriteSet != "" ||
		j.Phase == PhaseCommitted ||
		IsRollbackPhase(j.Phase)
}

// status reports the journal without touching it.
func status(req Request) (Result, error) {
	j, existed, err := loadExisting(JournalPath(req.Root))
	if err != nil {
		return Result{SchemaVersion: SchemaVersion, Request: req, ErrorCode: ErrorCode(err), Error: err.Error()}, err
	}
	if !existed {
		return Result{SchemaVersion: SchemaVersion, Request: req, Owner: OwnerLegacy}, nil
	}
	return resultOf(j, nil), nil
}
