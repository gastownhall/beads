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
// Every fallback path (unreadable remote state, below the convergence floor)
// degrades to the blunt #4515 block, so the default is never less safe than
// the blunt gate; it only resolves cases the blunt gate cannot distinguish.
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

// SmartGateEnabled reports whether the smart gate is active. It defaults to
// true; the operator opts out with BD_SMART_GATE=0 (any parseable boolean
// false). An unparseable value keeps the default rather than silently
// disabling the gate.
func SmartGateEnabled() bool {
	v := os.Getenv(SmartGateEnv)
	if v == "" {
		return true
	}
	on, err := strconv.ParseBool(v)
	if err != nil {
		return true
	}
	return on
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
)

// routeSmartGate inspects the remote's cached schema state and returns the smart
// routing verdict plus the content-skew versions (for smartForkSkew). It performs
// no network I/O: it reads only the already-cached remote-tracking ref, exactly
// like the doctor migration-skew check. current is the local schema version.
func routeSmartGate(ctx context.Context, db DBConn, current int, remoteName string) (smartGateDecision, []int) {
	local, err := ReadMigrationContentHashes(ctx, db, "")
	if err != nil || len(local) == 0 {
		// No local hashes to compare (old database) — cannot assess safely.
		return smartUndetermined, nil
	}

	if remoteName == "" {
		remoteName = smartGateDefaultRemote
	}
	branch := smartGateActiveBranch(ctx, db)
	ref := "remotes/" + remoteName + "/" + branch
	remote, err := ReadMigrationContentHashes(ctx, db, ref)
	if err != nil {
		// Cached ref absent/stale (never pushed/pulled) or pre-content_hash:
		// nothing to compare — fall back to the blunt block.
		return smartUndetermined, nil
	}
	if len(remote) == 0 {
		return smartUndetermined, nil
	}

	if skew := ContentHashSkew(local, remote); len(skew) > 0 {
		return smartForkSkew, skew
	}

	remoteMax := maxVersion(remote)
	if remoteMax > current {
		return smartAdopt, nil // remote already migrated — adopt, don't migrate
	}
	if remoteMax < current {
		// The cached remote is behind this clone. That is not the first-mover
		// state the smart gate is allowed to auto-resolve, so keep the human
		// coordination block rather than silently moving farther ahead.
		return smartUndetermined, nil
	}

	// remote == local on every shared version and at the same max version: a first-mover.
	if current >= LastNonDeterministicMigration {
		return smartAutoMigrate, nil
	}
	return smartBelowFloor, nil
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
