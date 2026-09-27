package doltversion

import (
	"context"
	"errors"
	"fmt"
)

// RecommendedMin is the dolt version this package recommends for managed
// proxied-server mode, based on the cross-version compatibility matrix run
// 2026-07-25: it showed that cross-reading storage written by the beads
// Dolt Go module (as opposed to writing it, which older dolt CLIs can also
// do) requires an external dolt >= 2.0.0 — dolt 1.85 can *serve* proxied
// mode but cannot *read* storage the module wrote, and dolt 1.52.1 fails at
// both serving and reading. This is deliberately a recommendation, not a
// hard floor (see CheckRecommended): an earlier design that hard-failed
// startup below a version floor was rejected in adversarial review as
// "policy without evidence" — the matrix only establishes an empirical
// compatibility boundary for the read path, not a product decision to
// refuse older binaries outright, and rollout should be allowed to proceed
// on a warning while that policy question is settled.
var RecommendedMin = MustParse("2.0.0")

// warningKind distinguishes the two situations ProbeWithPolicy can warn
// about, so Warning.Message can render the right wording for each without
// the caller needing to inspect which fields are populated.
type warningKind int

const (
	// warningOldVersion: the probe succeeded and parsed a version, but it
	// is below RecommendedMin.
	warningOldVersion warningKind = iota
	// warningUnverifiable: the probe succeeded (the binary ran and exited
	// zero) but its output didn't parse as a version this package
	// recognizes — most commonly a dev/custom build. See the "no hard
	// floor" posture below: this is demoted to a warning, not an error,
	// for the same reason a known-old version is only a warning.
	warningUnverifiable
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
	// Probed is the version that triggered a warningOldVersion warning.
	// Zero value for warningUnverifiable (there is no parsed version to
	// report).
	Probed Version
	// Recommended is the floor Probed fell short of, for warningOldVersion.
	// Zero value for warningUnverifiable.
	Recommended Version
	// RawOutput is the first line of the probe's output, for
	// warningUnverifiable — included so the warning tells an operator what
	// dolt actually printed, not just that parsing failed. Empty for
	// warningOldVersion.
	RawOutput string
}

// Message renders w as a human-readable advisory line. Named Message
// rather than Error deliberately — see the Warning doc comment.
func (w *Warning) Message() string {
	switch w.kind {
	case warningUnverifiable:
		return fmt.Sprintf(
			"could not verify dolt version (raw output: %q); proceeding without a version recommendation check "+
				"(see gastownhall/beads doltversion package docs)",
			w.RawOutput,
		)
	default:
		return fmt.Sprintf(
			"dolt %s is older than the recommended minimum %s for proxied-server mode; "+
				"cross-reading storage written by newer dolt Go modules may fail (see gastownhall/beads doltversion package docs)",
			w.Probed, w.Recommended,
		)
	}
}

// CheckRecommended compares id.Version against RecommendedMin and returns a
// non-nil *Warning when it falls short. It never returns an error: falling
// below the recommended minimum is advisory only (see RecommendedMin for
// why there is no hard floor), and CheckRecommended has no basis for
// judging id itself invalid — that is Probe's job.
func CheckRecommended(id Identity) *Warning {
	if id.Version.AtLeast(RecommendedMin) {
		return nil
	}
	return &Warning{kind: warningOldVersion, Probed: id.Version, Recommended: RecommendedMin}
}

// ProbeWithPolicy composes Probe with CheckRecommended so call sites
// (uow_factory.go, init preflight) stay a one-line "resolve, then check"
// instead of re-deriving this sequence.
//
// It demotes ErrUnparseableVersion to a *Warning instead of returning it as
// an error. This was originally gated behind an opt-in
// BEADS_DOLT_ALLOW_UNVERIFIED env var (an escape hatch a caller had to
// explicitly enable), but that was inconsistent with this package's
// no-hard-floor posture elsewhere: a dolt binary running a KNOWN-bad
// version (1.52.1, well below RecommendedMin) only warns, while a binary
// whose version this package simply couldn't parse (most commonly a
// perfectly good dev/custom build) hard-failed every command unless an
// operator knew to set an env var. It also does not match the parser this
// package generalizes from (doltserver.doltVersionAtLeast), which already
// fails soft — an unparseable version there just means "assume unsupported
// feature", not "refuse to proceed". Treating "ran fine, exit zero, just
// didn't print a version this package recognizes" as a warning rather than
// a hard error is the consistent choice.
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
	return id, CheckRecommended(id), nil
}
