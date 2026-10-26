package doltversion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RequiredMin is the oldest dolt bd works with outside embedded mode: the
// `dolt` binary that managed proxied-server mode and `bd dolt start` launch,
// and the `dolt sql-server` that server mode and proxied-server mode connect
// to. Below it bd refuses to run (see CheckRequired and CheckServer) with an
// error that names the version it found, this minimum, and how to upgrade.
// Embedded mode links the 2.6.0 engine into bd and never consults it.
//
// 2.6.0 is the floor because it is the release the embedded engine, CI
// (scripts/ci/install-dolt.sh, kept at or above this value by
// TestPinnedDoltCLIAtLeastRequiredMin) and the servers bd is run against all
// pin, so it is the only server release bd is tested with. It changes
// behavior bd relies on: when two histories have more than one merge base
// (criss-cross merges, e.g. federation peers that pull from each other, or
// branches merged into each other), dolt_merge() merges against a virtual
// base like git's ORT strategy (dolthub/dolt#12125), so some merges that
// 2.5.x completed silently now conflict and some spurious conflicts no
// longer occur; pull/push through a single remote is unaffected. Earlier
// releases also carry known defects, measured in
// gastownhall/beads docs/architecture/dolt.md ("Which Dolt version to
// install"):
// 2.4.0 through 2.4.2 return no row for an ungrouped MAX or MIN over an
// unmatched primary-key prefix (dolthub/dolt#12093) and panic on an ungrouped
// MAX of a non-key column under a fully bound primary key
// (dolthub/go-mysql-server#4037); releases before 2.4.0 mis-plan a join
// (dolthub/dolt#11886); 2.3.0 and 2.3.1 regressed CALL DOLT_RESET('--hard')
// (dolthub/dolt#11581); and releases before 2.3.3 cannot write to a database
// holding a descending index column (dolthub/dolt#11746), which every
// database bd creates in embedded mode holds.
//
// This used to be RecommendedMin, a warning only: an earlier hard floor was
// rejected as "policy without evidence" while the policy question was open.
// It is now settled policy, so the floor refuses. A version that cannot be
// determined is still not refused (see ProbeWithPolicy and CheckServer):
// the floor acts on evidence that a dolt is too old, not on its absence.
var RequiredMin = MustParse("2.6.0")

// releaseNotesURL is the release whose notes describe what changed at
// RequiredMin; installDocsURL names the pinned install steps.
const (
	releaseNotesURL = "https://github.com/dolthub/dolt/releases/tag/v2.6.0"
	installDocsURL  = "https://beads.gascity.com/architecture/dolt#which-dolt-version-to-install"
)

// BelowMinimumError reports a dolt below RequiredMin. It wraps
// ErrBelowRequiredMin, so callers can branch with errors.Is, and carries
// the detected and required versions for callers that render them.
type BelowMinimumError struct {
	// Detected is the version the binary or server reported.
	Detected Version
	// Required is RequiredMin at the time of the check.
	Required Version
	// Subject names what was checked, e.g. `dolt binary "/usr/bin/dolt"` or
	// "dolt sql-server at 127.0.0.1:3307".
	Subject string
	// Server is true when Subject is a running sql-server rather than a
	// binary bd launches, which changes the upgrade advice: a server has to
	// be restarted on the new release.
	Server bool
}

func (e *BelowMinimumError) Error() string {
	restart := ""
	if e.Server {
		restart = ", then restart the server on it"
	}
	return fmt.Sprintf(
		"%s is version %s, but bd requires dolt %s or newer outside embedded mode. "+
			"Upgrade dolt to %s or newer%s: install steps at %s "+
			"(releases at https://github.com/dolthub/dolt/releases); release notes for %s at %s",
		e.Subject, e.Detected, e.Required, e.Required, restart, installDocsURL, e.Required, releaseNotesURL,
	)
}

func (e *BelowMinimumError) Unwrap() error { return ErrBelowRequiredMin }

// CheckRequired returns a *BelowMinimumError when v is below RequiredMin,
// and nil otherwise. subject names what reported v (see
// BelowMinimumError.Subject); server says whether it is a running server.
func CheckRequired(v Version, subject string, server bool) error {
	if v.AtLeast(RequiredMin) {
		return nil
	}
	return &BelowMinimumError{Detected: v, Required: RequiredMin, Subject: subject, Server: server}
}

// RowQuerier is the one method CheckServer needs from a *sql.DB, *sql.Conn
// or *sql.Tx.
type RowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ServerVersion asks a running dolt sql-server for its version with
// SELECT dolt_version(). It returns ErrUnparseableVersion (wrapped) when the
// answer is not a version, and the query error when the query fails.
func ServerVersion(ctx context.Context, q RowQuerier) (Version, error) {
	var raw string
	if err := q.QueryRowContext(ctx, "SELECT dolt_version()").Scan(&raw); err != nil {
		return Version{}, err
	}
	return ParseVersion(raw)
}

// CheckServer enforces RequiredMin against the sql-server q is connected to.
// It returns a *BelowMinimumError when the server reports an older version.
//
// It returns nil when the version cannot be determined: the query fails
// (a server or gateway that does not offer dolt_version(), a dropped
// connection that the caller's next query reports in its own words, a
// canceled context) or answers something that is not a version (a custom
// build). That matches ProbeWithPolicy, which lets a binary whose version
// does not parse run with a warning: the floor refuses a dolt shown to be
// too old, never one it could not identify. undetermined, when non-nil, is
// called with the reason so the caller can log it.
func CheckServer(ctx context.Context, q RowQuerier, subject string, undetermined func(error)) error {
	v, err := ServerVersion(ctx, q)
	if err != nil {
		if undetermined != nil {
			undetermined(err)
		}
		return nil
	}
	return CheckRequired(v, subject, true)
}

// warningKind distinguishes the situations ProbeWithPolicy can warn about,
// so Warning.Message can render the right wording for each. A version below
// RequiredMin is not one of them: that is an error (see CheckRequired).
type warningKind int

const (
	// warningUnverifiable: the probe succeeded (the binary ran and exited
	// zero) but its output didn't parse as a version this package
	// recognizes — most commonly a dev/custom build. It is a warning, not
	// an error: RequiredMin refuses a dolt shown to be too old, and there is
	// no version here to show that (see ProbeWithPolicy).
	warningUnverifiable warningKind = iota
)

// Warning is a non-fatal advisory produced by ProbeWithPolicy. It is a
// distinct type (rather than a plain string) so that callers can choose to
// log it, surface it once and suppress repeats, or escalate it to a hard
// failure under a stricter local policy, without doing string matching to
// recognize it.
//
// Warning deliberately does NOT implement the error interface (an earlier
// version of this type had an Error() string method). A *Warning that
// happens to be nil satisfies a non-nil `error` interface value when
// assigned through an `error`-typed variable or returned as one — the
// classic Go "typed nil" trap — which is exactly the kind of bug a type
// meant to represent "maybe nothing happened" should not invite. Message()
// avoids the trap: callers must explicitly nil-check *Warning before
// calling it, the same as they already must for any pointer.
type Warning struct {
	kind warningKind
	// RawOutput is the first line of the probe's output, included so the
	// warning tells an operator what dolt actually printed, not just that
	// parsing failed.
	RawOutput string
}

// Message renders w as a human-readable advisory line. Named Message
// rather than Error deliberately — see the Warning doc comment.
func (w *Warning) Message() string {
	return fmt.Sprintf(
		"could not verify dolt version (raw output: %q); proceeding without checking it against the required minimum %s "+
			"(see gastownhall/beads doltversion package docs)",
		w.RawOutput, RequiredMin,
	)
}

// probedSubject names a probed binary for BelowMinimumError.Subject.
func probedSubject(id Identity) string {
	path := id.GivenPath
	if path == "" {
		path = id.RealPath
	}
	return fmt.Sprintf("dolt binary %q", path)
}

// ProbeWithPolicy composes Probe with CheckRequired so call sites
// (uow_factory.go, init preflight) stay a one-line "resolve, then check"
// instead of re-deriving this sequence. A version below RequiredMin is
// returned as a *BelowMinimumError (wrapping ErrBelowRequiredMin), with the
// probed Identity, so the binary is refused before bd launches it.
//
// It demotes ErrUnparseableVersion to a *Warning instead of returning it as
// an error. This was originally gated behind an opt-in
// BEADS_DOLT_ALLOW_UNVERIFIED env var (an escape hatch a caller had to
// explicitly enable), but a binary whose version this package simply
// couldn't parse (most commonly a perfectly good dev/custom build)
// hard-failed every command unless an operator knew to set an env var. It
// also does not match the parser this package generalizes from
// (doltserver.doltVersionAtLeast), which fails soft — an unparseable
// version there just means "assume unsupported feature", not "refuse to
// proceed". The required minimum keeps that posture: it refuses a binary
// that reports an older version, and warns about one that reports nothing
// this package recognizes.
//
// Hard errors remain for everything that means the binary itself is
// missing or broken: ErrNotFound, ErrNotExecutable, and ErrProbeFailed
// (exec failures, timeouts, non-zero exits). Those are not, and should
// never be, downgradable to a warning — there is no result to proceed
// with.
//
// When Probe returns ErrUnparseableVersion, the Identity it also returned
// (GivenPath/RealPath/FileSize/FileModTime/RawOutput, with a zero-value
// Version) is passed through unchanged, so callers still get file identity
// even though no version was recognized.
func ProbeWithPolicy(ctx context.Context, path string) (Identity, *Warning, error) {
	id, err := Probe(ctx, path)
	if err != nil {
		if errors.Is(err, ErrUnparseableVersion) {
			return id, &Warning{kind: warningUnverifiable, RawOutput: id.RawOutput}, nil
		}
		return id, nil, err
	}
	return id, nil, CheckRequired(id.Version, probedSubject(id), false)
}
