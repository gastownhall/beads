package schema

import (
	"context"
	"fmt"
	"os"
	"strconv"
)

// SmartGateEnv controls the state-aware ("smart") remote-migrate gate
// (gastownhall/beads#4516). The smart gate is ON by default: when the blunt
// gate would fire, it consults the remote's cached schema state and
// auto-resolves the one provably-safe case (first-mover migrate), while still
// stopping — with sharper guidance — on the cases that genuinely need a human.
// Every fallback path (unreadable remote state, below the convergence floor,
// a clone behind the remote in data commits) degrades to the blunt #4515
// block, so the default is never less safe than the blunt gate; it only
// resolves cases the blunt gate cannot distinguish.
//
// Set BD_SMART_GATE=0 (or any boolean false) to opt out and keep the blunt
// #4515 behavior unconditionally: any remote-backed database with pending
// migrations refuses every write and hands the operator the
// migrate-or-adopt decision.
//
// It is consulted only once the blunt gate would otherwise fire, so exporting it
// permanently costs nothing on the common open path.
const SmartGateEnv = "BD_SMART_GATE"

// smartGateDefaultRemote is the sync remote the smart gate compares against
// when the caller does not know a more specific configured default. The gate
// reads the *cached* remote-tracking ref (remotes/<remote>/<branch>) and never
// fetches.
const smartGateDefaultRemote = "origin"

// LastNonDeterministicMigration is the highest migration version whose content is
// genuinely non-deterministic across clones (random UUID() primary keys: 0004,
// 0005, 0009, 0010, 0021, 0037, 0043 — the frozen pre-guard set in
// migrations/nondeterminism-allowlist.txt, excluding the query-time-safe VIEW in
// 0017 and the dolt-ignored local-only tables).
//
// A database already at or above this version has applied every non-deterministic
// migration, so its *pending* migrations are all deterministic by construction —
// the CI hygiene guard (scripts/check-migration-hygiene.sh check B) forbids new
// non-deterministic migrations without an allowlist entry under CODEOWNERS review.
// That makes a first-mover migrate provably convergent: two clones migrating the
// same deterministic batch independently reach byte-identical tables.
//
// TestConvergenceFloorMatchesAllowlist cross-checks this constant against the
// allowlist so the two cannot drift if a new entry is ever added.
const LastNonDeterministicMigration = 43

// smartGateEnvState classifies the raw BD_SMART_GATE value.
type smartGateEnvState int

const (
	smartGateEnvUnset smartGateEnvState = iota
	smartGateEnvEnabled
	smartGateEnvDisabled
	// smartGateEnvUnparseable: set but strconv.ParseBool rejected it. Treated
	// the same as smartGateEnvEnabled by SmartGateEnabled (the default holds),
	// but callers building the fallback-block message (gastownhall/beads#4551
	// follow-up) still want to tell the operator their value was ignored.
	smartGateEnvUnparseable
)

// smartGateEnvValue reads and classifies BD_SMART_GATE, returning the raw
// value alongside the classification so a caller can quote it back to the
// operator. Split out of SmartGateEnabled so "unset"/"valid true" (silently
// fine) can be told apart from "set but unparseable" (worth a note) without
// re-parsing.
func smartGateEnvValue() (smartGateEnvState, string) {
	v := os.Getenv(SmartGateEnv)
	if v == "" {
		return smartGateEnvUnset, ""
	}
	on, err := strconv.ParseBool(v)
	if err != nil {
		return smartGateEnvUnparseable, v
	}
	if on {
		return smartGateEnvEnabled, v
	}
	return smartGateEnvDisabled, v
}

// SmartGateEnabled reports whether the smart gate is active. It defaults to
// true; the operator opts out with BD_SMART_GATE=0 (any parseable boolean
// false). An unparseable value keeps the default rather than silently
// disabling the gate.
func SmartGateEnabled() bool {
	state, _ := smartGateEnvValue()
	return state != smartGateEnvDisabled
}

// smartGateDecision is the routing verdict for a remote-backed database with
// pending migrations, computed from the remote's cached schema state.
type smartGateDecision int

const (
	// smartUndetermined: the remote's cached schema state could not be read
	// (no cached ref, missing table/column, or query error). Fall back to the
	// blunt #4515 block — no surprise network, no guessing.
	smartUndetermined smartGateDecision = iota
	// smartAutoMigrate: remote is at the same version as local, no content
	// skew, and local is at/above the convergence floor — a safe first-mover.
	// Allow the in-place migrate to proceed; concurrent first-movers converge.
	smartAutoMigrate
	// smartAdopt: remote is ahead (already migrated) with no skew. Stop, but
	// direct the operator to adopt rather than migrate. Adoption is a
	// destructive re-clone, so it is never performed silently.
	smartAdopt
	// smartForkSkew: ContentHashSkew non-empty — two clones ran different
	// content for the same version. Genuine #4259 fork; human data-loss
	// decision. Stop.
	smartForkSkew
	// smartBelowFloor: remote == local but a legacy non-deterministic migration
	// is still pending (very old database). Conservative human block.
	smartBelowFloor
	// smartAdoptFastForward (mybd-ae1i): a strict refinement of smartAdopt.
	// Remote is ahead with no skew (same precondition as smartAdopt) AND
	// local HEAD is a strict ancestor of the cached remote ref AND the
	// working set is clean — adopting loses nothing. Only reachable when the
	// caller wired a *FastForwardAdopter; any missing callback, failed
	// precondition, or query error degrades to smartAdopt, so this verdict
	// can never be less safe than today's default. The caller
	// (checkRemoteMigrateGate) turns this verdict into action via
	// attemptFastForward: on success it auto fast-forwards silently (no
	// error, no directive); any execution failure (read-only, unwired write,
	// a TOCTOU miss, or the merge itself refusing) falls through to the
	// plain smartAdopt directive instead, never forcing the write.
	smartAdoptFastForward
	// smartDataBehind (gastownhall/beads#6575): schema parity with the cached
	// remote ref holds — the first-mover precondition — but local HEAD is
	// BEHIND that ref with no local commits of its own (ahead == 0,
	// behind >= 1), so this clone is missing DATA commits it has not pulled.
	// Migrating in place here mints local-only schema commits on a HEAD that
	// lacks those commits; when the pending batch moves a tracked table onto
	// the dolt_ignore plane (0062's events), every subsequent `bd dolt pull`
	// then refuses (#6368) and the clone can no longer fetch the very commits
	// it was behind on. Stop, and direct the operator to pull first — a pure
	// fast-forward in this shape — after which the same open auto-migrates as
	// a true first-mover.
	smartDataBehind
	// smartDataDiverged is smartDataBehind's other shape: behind >= 1 AND
	// ahead >= 1, i.e. this clone has unpushed local commits as well as
	// unpulled remote ones. It reaches the identical wedge — the pending
	// migration lands on a HEAD missing the remote's commits either way — so
	// it stops for the same reason. It is a separate verdict only because the
	// remedy behaves differently: `bd dolt pull` MERGES here rather than
	// fast-forwarding, and can report conflicts the operator has to resolve
	// before it completes. bd auto-commits every write, so this is the
	// ORDINARY multi-machine shape, not an exotic one.
	smartDataDiverged
)

// FastForwardAdopter injects the driver-side fast-forward primitives that
// live in internal/storage/versioncontrolops (LocalIsStrictAncestorOf,
// WorkingSetClean, FastForwardAdopt). The schema package sits below the
// driver layer and must not import versioncontrolops (import cycle), so the
// mode-specific store — which already imports both packages — constructs
// this from its own db handle and passes it in, mirroring the existing
// extraHasRemote func() bool callback on CheckRemoteMigrateGateWithRemoteCheck.
//
// A nil *FastForwardAdopter, or any nil field, is always safe: routing
// degrades to the pre-existing smartAdopt directive exactly as if this
// refinement did not exist. mybd-ae1i piece 3 additionally acts on a
// detected smartAdoptFastForward verdict by invoking FastForward; a nil
// FastForward field or ReadOnly=true both mean "cannot write here" and
// degrade the SAME way — the destructive smartAdopt directive, never a
// forced write.
type FastForwardAdopter struct {
	// IsStrictAncestor reports whether local HEAD is a strict ancestor of
	// ref (versioncontrolops.LocalIsStrictAncestorOf).
	IsStrictAncestor func(ctx context.Context, db DBConn, ref string) (bool, error)
	// AheadBehind reports the raw commit counts behind IsStrictAncestor:
	// how many commits local HEAD has that ref lacks, and how many ref has
	// that local lacks (versioncontrolops.LocalAheadBehind). The
	// data-behind stop (gastownhall/beads#6575) needs behind >= 1 whatever
	// ahead is, which IsStrictAncestor's ahead == 0 && behind >= 1 cannot
	// express, and needs to tell the two shapes apart so the remedy it
	// prints matches the pull the operator will actually get (fast-forward
	// vs merge).
	//
	// A nil AheadBehind is safe: localDataBehind falls back to
	// IsStrictAncestor, which reports only the ahead == 0 shape — the
	// narrower, pre-#6575-widening behavior — so an injection site that
	// wires only the older callback keeps working.
	AheadBehind func(ctx context.Context, db DBConn, ref string) (ahead, behind int, err error)
	// WorkingSetClean reports whether the working set has no uncommitted
	// changes, dolt-ignored wisp tables excepted
	// (versioncontrolops.WorkingSetClean).
	WorkingSetClean func(ctx context.Context, db DBConn) (bool, error)
	// FastForward performs the actual fast-forward-only adopt
	// (versioncontrolops.FastForwardAdopt). A nil FastForward means this
	// injection site never wired write execution (e.g. detection-only);
	// the router then never attempts it, exactly like ReadOnly below.
	FastForward func(ctx context.Context, db DBConn, ref string) error
	// ReadOnly marks that the store this adopter was built for was opened
	// read-only: IsStrictAncestor/WorkingSetClean may still run (they only
	// read), but FastForward must never be called — a read-only store
	// cannot accept the merge write. The router checks this before ever
	// invoking FastForward and falls back to the destructive smartAdopt
	// directive instead of attempting (and failing) the write.
	ReadOnly bool
}

// routeAdoptFastForward reports whether a remote-ahead, no-skew case
// additionally qualifies for the non-destructive fast-forward refinement:
// local HEAD is a strict ancestor of ref AND the working set is clean.
// adopt may be nil (no callback wired at this injection site) — treated
// the same as any failing precondition. Any callback error is treated
// conservatively as "not fast-forwardable": an inconclusive read must never
// promote past the existing smartAdopt directive.
//
// This only checks the read-side preconditions (used both for the initial
// routing verdict and, again, as the TOCTOU re-check immediately before the
// write — see attemptFastForward); whether the write itself can even be
// attempted is canAutoFastForward's job.
func routeAdoptFastForward(ctx context.Context, db DBConn, ref string, adopt *FastForwardAdopter) bool {
	if adopt == nil || adopt.IsStrictAncestor == nil || adopt.WorkingSetClean == nil {
		return false
	}
	ancestor, err := adopt.IsStrictAncestor(ctx, db, ref)
	if err != nil || !ancestor {
		return false
	}
	clean, err := adopt.WorkingSetClean(ctx, db)
	if err != nil || !clean {
		return false
	}
	return true
}

// localDataBehind reports whether local HEAD is missing commits that ref has
// (behind >= 1) and, when it is, whether the clone ALSO has commits of its own
// that ref lacks (ahead >= 1 — the diverged shape). It is the equal-version
// path's only ancestry fact (gastownhall/beads#6575).
//
// The predicate is behind >= 1 regardless of ahead, not the narrower strict-
// ancestor relation the remote-ahead path uses. Both shapes reach the same
// wedge: the migration lands on a HEAD missing the remote's commits either
// way, and the ignore-plane flip then refuses every later `bd dolt pull`.
// Since bd auto-commits every write, "has local commits AND is behind" is the
// ordinary multi-machine state, so restricting the stop to ahead == 0 would
// leave the larger half of the affected cohort unprotected. A level clone
// (behind == 0) and a purely-ahead clone (behind == 0, ahead >= 1) are still
// permitted — they are genuine first-movers with nothing to pull.
//
// adopt may be nil, or wire neither callback (the shared unit-of-work
// provider's injection site) — then there is no ancestry fact to read and
// routing stays exactly as it was before this check existed. With only
// IsStrictAncestor wired, the fact narrows to the ahead == 0 shape; the
// diverged shape then reads "not behind", which is the pre-widening
// behavior rather than a new hazard.
//
// Unlike routeAdoptFastForward, a query error is NOT folded into "false":
// here false means "permit the more automatic action", so an inconclusive
// read has to be reported to the caller and degrade to the blunt block.
func localDataBehind(ctx context.Context, db DBConn, ref string, adopt *FastForwardAdopter) (behind bool, diverged bool, err error) {
	if adopt == nil {
		return false, false, nil
	}
	if adopt.AheadBehind != nil {
		ahead, behindCount, err := adopt.AheadBehind(ctx, db, ref)
		if err != nil {
			return false, false, err
		}
		return behindCount >= 1, behindCount >= 1 && ahead >= 1, nil
	}
	if adopt.IsStrictAncestor == nil {
		return false, false, nil
	}
	ancestor, err := adopt.IsStrictAncestor(ctx, db, ref)
	if err != nil {
		return false, false, err
	}
	// A strict ancestor is by definition ahead == 0, so this fallback can
	// only ever report the non-diverged shape.
	return ancestor, false, nil
}

// canAutoFastForward reports whether adopt is actually able to EXECUTE the
// fast-forward once the read-side preconditions (routeAdoptFastForward) are
// satisfied: a FastForward callback must be wired, and the store must not be
// read-only. Both failures mean "cannot write here" — the caller degrades to
// the destructive smartAdopt directive rather than attempting the write.
func canAutoFastForward(adopt *FastForwardAdopter) bool {
	return adopt != nil && adopt.FastForward != nil && !adopt.ReadOnly
}

// remoteMaxAtRef re-reads the remote's cached content hashes at ref and
// returns the highest migration version found there, or ok=false if the ref
// cannot be read (absent/stale cache, or a query error). It mirrors the read
// routeSmartGate already performs, and exists so attemptFastForward's TOCTOU
// re-check can confirm remoteMax is STILL exactly the binary's latest
// immediately before the write, not just at the original routing decision
// (mybd-ae1i: the cached ref could in principle advance between routing and
// write — e.g. a concurrent `dolt fetch` in another session — and landing
// anywhere short of or past latest must never auto-execute; see
// routeSmartGate's remoteMax == latest gate).
func remoteMaxAtRef(ctx context.Context, db DBConn, ref string) (int, bool) {
	remote, err := ReadMigrationContentHashes(ctx, db, ref)
	if err != nil || len(remote) == 0 {
		return 0, false
	}
	return maxVersion(remote), true
}

// attemptFastForward performs the TOCTOU-guarded auto fast-forward once the
// smart router has already picked the smartAdoptFastForward verdict for ref
// AND confirmed the fast-forward would land exactly at the binary's latest
// migration (mybd-ae1i piece 3: turning detection into action; the
// remoteMax == latest gate is mybd-ae1i's follow-up fix — landing short of
// latest would leave MigrateUp to run unconditionally afterward with no gate
// re-evaluation, and landing past latest means a newer binary already
// migrated the remote further than this one understands). It re-verifies
// the exact ancestry/clean/remoteMax-at-latest preconditions in the SAME db
// session immediately before the write — a concurrent local writer or a
// cached-ref advance between the first check (inside routeSmartGate) and now
// would otherwise race the merge — and never forces it: any failure (a
// store that cannot write, a re-check miss, or DOLT_MERGE('--ff-only', ...)
// itself refusing a dirty working set, non-fast-forward, or concurrent
// writer) reports false so the caller falls through to the destructive
// smartAdopt directive instead of erroring out or silently skipping the
// migrate-or-adopt decision.
func attemptFastForward(ctx context.Context, db DBConn, ref string, adopt *FastForwardAdopter, latest int) bool {
	if !canAutoFastForward(adopt) {
		return false
	}
	if !routeAdoptFastForward(ctx, db, ref, adopt) {
		return false
	}
	remoteMax, ok := remoteMaxAtRef(ctx, db, ref)
	if !ok || remoteMax != latest {
		return false
	}
	return adopt.FastForward(ctx, db, ref) == nil
}

// routeSmartGate inspects the remote's cached schema state and returns the
// smart routing verdict, the content-skew versions (for smartForkSkew), the
// cached remote-tracking ref it compared against (needed by the caller to
// re-verify and execute a smartAdoptFastForward verdict — see
// attemptFastForward), and atLatest — whether the remote's max version is
// exactly the binary's latest (only meaningful, and only set, for the
// smartAdoptFastForward/smartAdopt verdicts; see below). It performs no
// network I/O: it reads only the already-cached remote-tracking ref, exactly
// like the doctor migration-skew check. current is the local schema
// version, latest is the binary's own maximum migration version
// (schema.LatestVersion()). adopt (optionally nil) injects the fast-forward
// ancestry callbacks; see FastForwardAdopter.
//
// atLatest gates whether the caller may EXECUTE a detected
// smartAdoptFastForward verdict (mybd-ae1i follow-up fix): fast-forwarding
// to a remote that is not exactly at latest would either leave pending
// migrations for MigrateUp to apply unconditionally in place afterward with
// no gate re-evaluation (remoteMax < latest — the #4259 fork risk this gate
// exists to prevent), or land past what this binary understands (remoteMax
// > latest — the #4135/#4137 stale-binary class). The verdict itself
// (smartAdoptFastForward vs smartAdopt) is still keyed only on the ancestor
// + clean preconditions, exactly as pre-piece-3, so the "loss-free adopt"
// directive stays reachable and accurate even when atLatest is false — only
// the AUTOMATIC fast-forward is conditioned on it (checkRemoteMigrateGate).
func routeSmartGate(ctx context.Context, db DBConn, current, latest int, remoteName string, adopt *FastForwardAdopter) (decision smartGateDecision, skewVersions []int, ref string, atLatest bool) {
	local, err := ReadMigrationContentHashes(ctx, db, "")
	if err != nil || len(local) == 0 {
		// No local hashes to compare (old database) — cannot assess safely.
		return smartUndetermined, nil, "", false
	}

	if remoteName == "" {
		remoteName = smartGateDefaultRemote
	}
	branch := smartGateActiveBranch(ctx, db)
	ref = "remotes/" + remoteName + "/" + branch
	remote, err := ReadMigrationContentHashes(ctx, db, ref)
	if err != nil {
		// Cached ref absent/stale (never pushed/pulled) or pre-content_hash:
		// nothing to compare — fall back to the blunt block.
		return smartUndetermined, nil, "", false
	}
	if len(remote) == 0 {
		return smartUndetermined, nil, "", false
	}

	if skew := ContentHashSkew(local, remote); len(skew) > 0 {
		return smartForkSkew, skew, ref, false
	}

	remoteMax := maxVersion(remote)
	if remoteMax > current {
		atLatest = remoteMax == latest
		if routeAdoptFastForward(ctx, db, ref, adopt) {
			return smartAdoptFastForward, nil, ref, atLatest
		}
		return smartAdopt, nil, ref, atLatest // remote already migrated — adopt, don't migrate
	}
	if remoteMax < current {
		// The cached remote is behind this clone. That is not the first-mover
		// state the smart gate is allowed to auto-resolve, so keep the human
		// coordination block rather than silently moving farther ahead.
		return smartUndetermined, nil, "", false
	}

	// remote == local on every shared version and at the same max version: a first-mover.
	if current >= LastNonDeterministicMigration {
		// Schema parity is not proof of first-mover status
		// (gastownhall/beads#6575). It says nothing about where this clone's
		// branch sits relative to the ref the hashes just came from: a clone
		// that is level on schema and behind on DATA satisfies every
		// precondition above. Read the ancestry fact the caller already wired
		// for the remote-ahead path before granting the automatic migrate.
		behind, diverged, err := localDataBehind(ctx, db, ref, adopt)
		if err != nil {
			// Inconclusive ancestry read on a ref whose content hashes just
			// resolved. Degrade to the blunt block rather than assume level:
			// the gate's contract is that every fallback is at least as safe
			// as #4515.
			return smartUndetermined, nil, "", false
		}
		if behind {
			// Same stop, same remedy command; the two verdicts differ only in
			// what that command DOES (fast-forward vs merge), which the
			// operator-facing text has to get right.
			if diverged {
				return smartDataDiverged, nil, ref, false
			}
			return smartDataBehind, nil, ref, false
		}
		return smartAutoMigrate, nil, ref, false
	}
	return smartBelowFloor, nil, ref, false
}

// smartGateActiveBranch returns the active branch, defaulting to "main" — the
// branch whose remote-tracking ref the skew comparison reads.
func smartGateActiveBranch(ctx context.Context, db DBConn) string {
	var active string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&active); err == nil && active != "" {
		return active
	}
	return "main"
}

func maxVersion(hashes map[int]string) int {
	max := 0
	for v := range hashes {
		if v > max {
			max = v
		}
	}
	return max
}

// smartGateAllowMigrate logs the auto-migrate decision and returns nil so the
// caller proceeds with MigrateUp. Mirrors the escape-hatch warning's shape.
func smartGateAllowMigrate(pending int, current int) {
	unit := "migrations"
	if pending == 1 {
		unit = "migration"
	}
	fmt.Fprintf(os.Stderr,
		"Smart gate (%s): auto-applying %d pending deterministic schema %s to a remote-backed database "+
			"(v%d, remote at same version — safe first-mover, concurrent migrators converge; #4516). Run `bd dolt push` after.\n",
		SmartGateEnv, pending, unit, current)
}

// smartGateNotifyFastForward logs the auto fast-forward adopt decision
// (mybd-ae1i piece 3): local HEAD already advanced to the remote's migrated
// schema via a lossless DOLT_MERGE('--ff-only', ...), so nothing local was
// discarded — unlike a plain destructive adopt. Mirrors
// smartGateAllowMigrate's shape. latest is accurate here specifically
// because checkRemoteMigrateGate only calls this after attemptFastForward
// succeeds, which itself only ever executes once remoteMax == latest (see
// routeSmartGate's atLatest gate) — so the printed "->v%d" is always the
// binary's latest, matching what actually landed.
func smartGateNotifyFastForward(current, latest int) {
	fmt.Fprintf(os.Stderr,
		"Smart gate (%s): fast-forwarded to remote's migrated schema (v%d->v%d), nothing discarded (#4259).\n",
		SmartGateEnv, current, latest)
}
