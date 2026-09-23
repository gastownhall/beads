package schema

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

// AllowRemoteMigrateEnv, when set to a boolean true ("1", "true", ...), lets
// the designated migrator apply pending schema migrations to a remote-backed
// database despite the gate below. It is consulted only when the gate would
// otherwise fire, so exporting it permanently does not warn on every store open.
// The CLI-flag twin `bd migrate --force` is the preferred interactive form;
// this env var remains supported for scripted/CI use.
//
// It also unlocks the shared-store arm below, where a shared database has NO
// remote at all — "REMOTE" in the name is then a wart. It is deliberate: this
// is the single knob operators and CI already export for exactly this
// semantic ("I am the designated migrator; apply pending migrations to a
// database other clients depend on"), and a second variable would multiply
// the escape-hatch surface for no new meaning.
const AllowRemoteMigrateEnv = "BD_ALLOW_REMOTE_MIGRATE"

// forceAllowRemoteMigrate is the programmatic (in-process) twin of
// AllowRemoteMigrateEnv. Set by SetForceAllowRemoteMigrate before the store
// opens; unlike os.Setenv(AllowRemoteMigrateEnv), it cannot leak into child
// processes (git hooks, dolt subprocesses).
var forceAllowRemoteMigrate bool

// SetForceAllowRemoteMigrate sets (or clears) the programmatic override that
// allows a pending schema migration on a remote-backed database. It is called
// by `bd migrate --force` / `bd migrate schema --force` in the root
// PersistentPreRunE, before both autoMigrateOnVersionBump and the main store
// open. External test packages may reset it to false after each test case.
func SetForceAllowRemoteMigrate(v bool) { forceAllowRemoteMigrate = v }

// sharedMigrateConsent records that the operator typed the migrate verb
// itself. It unlocks ONLY the shared-store-without-a-remote arm of the gate:
// with no remote there is no cross-clone fork to risk, so the single hazard is
// locking out co-resident clients of the same server — a hazard the operator
// accepts by asking for a migration by name. A remote-backed database still
// needs --force / AllowRemoteMigrateEnv, because #4259 coordination is a
// stronger, different contract.
var sharedMigrateConsent bool

// SetSharedMigrateConsent sets (or clears) the verb consent above. Like
// SetForceAllowRemoteMigrate it is set unconditionally (set-or-clear) in the
// root PersistentPreRunE, before both autoMigrateOnVersionBump and the store
// open, and is process-local so it cannot leak into child processes. External
// test packages may reset it to false after each test case.
func SetSharedMigrateConsent(v bool) { sharedMigrateConsent = v }

// RemoteMigrateGateError is returned when bd is about to auto-apply pending
// schema migrations to an existing database that has a remote configured.
//
// gastownhall/beads#4259: bd auto-runs pending migrations the first time a new
// binary opens an existing database. If two clones that sync through a shared
// remote each upgrade independently, both migrate in place and the schema forks
// — `bd dolt pull` then fails to merge with no bd-level recovery. The supported
// flow is "only ONE machine migrates the database; every other client adopts the
// migrated database from the remote". This gate refuses the silent in-place
// migration and makes the operator choose migrate vs. adopt. It applies to both
// server mode and embedded mode (the mode the original report was filed against).
//
// The type also carries the shared-store refusal (Decision "shared-no-remote",
// gastownhall/beads#5920), where there is no remote at all and the hazard is
// co-resident clients of one server rather than forked clones. The name is
// imperfect for that arm; every consumer — the server retry loop's permanence
// classification, embedded's lenient handling, the CLI text and JSON
// rendering, the AgentDirective/Options contract — already handles this type,
// and a parallel error universe would cost far more than the imprecise name.
type RemoteMigrateGateError struct {
	CurrentVersion int
	LatestVersion  int
	Pending        int
	// UnrecognizedEnv carries a BD_ALLOW_REMOTE_MIGRATE value that was set but
	// not understood (only boolean values unlock, e.g. "1"/"true"), so a
	// typo'd escape hatch fails with a hint instead of silently staying locked
	// (bd-6dnrw.34).
	UnrecognizedEnv string

	// Decision records why the smart gate (#4516) stopped, when it is enabled.
	// Empty is the default blunt #4515 stop (also used for the smart gate's
	// "undetermined"/"below-floor" fallbacks); the messaging below is then
	// byte-identical to #4515. "adopt", "adopt-ff", and "fork-skew" tailor
	// the guidance. "adopt-ff" (mybd-ae1i piece 2) is a strict refinement of
	// "adopt": the remote is ahead with no skew AND this clone's local
	// history is a strict ancestor of the remote's with a clean working set,
	// so adopting is provably loss-free (unlike a plain "adopt").
	Decision string
	// SkewVersions lists the migration versions whose content diverged between
	// this clone and the remote (Decision == "fork-skew").
	SkewVersions []int

	// FallbackReason names why the smart gate (#4516) could not do better than
	// this blunt stop, when Decision is empty (gastownhall/beads#4551
	// follow-up: every fallback path used to produce the byte-identical #4515
	// block with no way to tell them apart). See the fallbackReason* constants
	// for the recognized values. Always empty when Decision is "adopt",
	// "adopt-ff", or "fork-skew" — those stops already explain themselves.
	FallbackReason string
	// Shared marks a refusal that came from a database served to co-resident
	// bd clients (gastownhall/beads#5920). It is set on EVERY arm of the
	// shared gate, not just "shared-no-remote": the remote-backed arms keep
	// their #4259 decisions, but their remedies carry an extra consequence
	// there — adopting or migrating promotes the schema for every client of
	// the server at once — so the guidance has to say so.
	Shared bool
	// UnrecognizedSmartGateEnv carries a BD_SMART_GATE value that was set but
	// not understood (only boolean values are recognized), mirroring
	// UnrecognizedEnv above but for the smart gate's own opt-out variable, so
	// a typo'd BD_SMART_GATE is surfaced instead of only its silent effect
	// (the gate staying enabled by default). Set only when
	// FallbackReason == fallbackReasonUnparseableEnv.
	UnrecognizedSmartGateEnv string

	// DataDiverged distinguishes the two shapes of the data-behind stop
	// (gastownhall/beads#6575). Both are "this clone is missing commits the
	// remote has", and both take the same remedy COMMAND — `bd dolt pull` —
	// but false means the clone has no commits of its own so the pull is a
	// pure fast-forward, and true means it has both, so the pull MERGES and
	// can report conflicts to resolve first. Set only when
	// FallbackReason == fallbackReasonDataBehind; meaningless otherwise.
	DataDiverged bool
}

const (
	gateDecisionAdopt = "adopt"
	// gateDecisionAdoptFastForward (mybd-ae1i piece 2): a strict refinement of
	// gateDecisionAdopt. Set only when the smart router additionally proved
	// local HEAD a strict ancestor of the cached remote ref with a clean
	// working set — adopting is loss-free, unlike a plain adopt.
	gateDecisionAdoptFastForward = "adopt-ff"
	gateDecisionForkSkew         = "fork-skew"
	// gateDecisionSharedNoRemote (gastownhall/beads#5920): the database is
	// shared with co-resident bd clients (a dolt sql-server, directly or
	// through proxied-server mode) and has NO remote, so there is no fork to
	// reason about — but migrating still promotes the schema cursor for every
	// client at once and locks out each one still on an older binary. Unlocked
	// by the migrate verb, --force, or AllowRemoteMigrateEnv.
	gateDecisionSharedNoRemote = "shared-no-remote"
)

// fallbackReason* enumerates why the smart gate (#4516) fell back to the
// blunt #4515 stop instead of resolving or tailoring it. Exactly one applies
// per blunt stop (gastownhall/beads#4551 follow-up).
const (
	// fallbackReasonUnreadableState: the remote's cached schema state could
	// not be read (no cached ref, a stale/pre-content_hash one, or the cached
	// remote is simply behind this clone and so not a safe first-mover).
	fallbackReasonUnreadableState = "unreadable-remote-state"
	// fallbackReasonBelowFloor: remote and local agree, but a legacy
	// non-deterministic migration is still pending (below the convergence
	// floor the smart gate trusts for an unattended first-mover migrate).
	fallbackReasonBelowFloor = "below-convergence-floor"
	// fallbackReasonOptedOut: the operator disabled the smart gate outright
	// (BD_SMART_GATE=0).
	fallbackReasonOptedOut = "opted-out"
	// fallbackReasonUnparseableEnv: BD_SMART_GATE was set but not a
	// recognized boolean, so the gate stayed enabled by default (same as
	// unset) but still could not resolve this particular stop.
	fallbackReasonUnparseableEnv = "unparseable-env"
	// fallbackReasonSharedStore (gastownhall/beads#5920): the smart gate
	// resolved a safe first-mover migrate, but this store is shared, so
	// auto-executing it would promote the schema for every co-resident client.
	// Structural, not a routing outcome — it outranks the other reasons.
	fallbackReasonSharedStore = "shared-store"
	// fallbackReasonDataBehind (gastownhall/beads#6575): schema parity with
	// the cached remote ref held, but this clone's local HEAD is a strict
	// ancestor of that ref — it is behind the remote in data commits it has
	// not pulled, so it is not the first-mover the smart gate may
	// auto-resolve. Unlike the reasons above, this one has a one-command
	// remedy the operator can run right now (`bd dolt pull`), after which the
	// migrate is a true first-mover.
	fallbackReasonDataBehind = "data-behind"
)

// DataBehindRemedyCommand is the one command that moves a data-behind clone
// forward, and the single source for every surface that names it: the human
// body and note, the agent directive, the JSON option, and the store-open
// classification that has to let this command through the very refusal that
// prescribes it (gastownhall/beads#6575 F1 — a refusal whose documented
// recovery is blocked by the refusal is a fence with no gate; the #4566
// precedent for `bd dolt commit` is the same shape).
const DataBehindRemedyCommand = "bd dolt pull"

// IsDataBehind reports whether this refusal is the #6575 data-behind stop:
// the clone is level with the cached remote ref on schema but missing commits
// the ref has, so the pending migration must not be applied until it pulls.
//
// It is exported because the recovery for this particular stop is itself a
// store-opening command (DataBehindRemedyCommand), so the mode-specific store
// opens have to recognize this reason — and ONLY this reason — to open
// leniently for it. Every other refusal stays fatal on those opens.
func (e *RemoteMigrateGateError) IsDataBehind() bool {
	return e != nil && e.Decision == "" && e.FallbackReason == fallbackReasonDataBehind
}

func (e *RemoteMigrateGateError) Error() string {
	unit := "migrations"
	if e.Pending == 1 {
		unit = "migration"
	}
	switch e.Decision {
	case gateDecisionAdopt:
		return fmt.Sprintf("refusing to migrate a remote-backed database (v%d -> v%d): the remote is already migrated — adopt it instead of migrating here (#4259)",
			e.CurrentVersion, e.LatestVersion)
	case gateDecisionAdoptFastForward:
		return fmt.Sprintf("refusing to migrate a remote-backed database (v%d -> v%d): the remote is already migrated and this clone can fast-forward to it losslessly — adopt instead of migrating here (#4259)",
			e.CurrentVersion, e.LatestVersion)
	case gateDecisionForkSkew:
		return fmt.Sprintf("refusing to migrate a remote-backed database (v%d -> v%d): this clone and the remote applied different content for migration(s) %s — the schema has already forked (#4259)",
			e.CurrentVersion, e.LatestVersion, FormatMigrationVersions(e.SkewVersions))
	case gateDecisionSharedNoRemote:
		return fmt.Sprintf("refusing to auto-apply %d pending schema %s to a shared server database (v%d -> v%d): migrating would lock out every co-resident bd client still on the old schema (#5920)",
			e.Pending, unit, e.CurrentVersion, e.LatestVersion)
	default:
		return fmt.Sprintf("refusing to auto-apply %d pending schema %s to a remote-backed database (v%d -> v%d): migrating clones independently forks the schema (#4259)",
			e.Pending, unit, e.CurrentVersion, e.LatestVersion)
	}
}

// FormatMigrationVersions renders migration versions as zero-padded 4-digit ids.
func FormatMigrationVersions(versions []int) string {
	if len(versions) == 0 {
		return ""
	}
	parts := make([]string, len(versions))
	for i, v := range versions {
		parts[i] = fmt.Sprintf("%04d", v)
	}
	return strings.Join(parts, ", ")
}

// UserMessage returns the full multi-line error block for terminal output.
func (e *RemoteMigrateGateError) UserMessage() string {
	msg := e.Error() + "\n" + e.userBody()
	msg += e.fallbackReasonNote()
	if e.UnrecognizedEnv != "" {
		msg += "\n" +
			"  Note: " + AllowRemoteMigrateEnv + "=" + e.UnrecognizedEnv + " is set but was not recognized —\n" +
			"  use " + AllowRemoteMigrateEnv + "=1 to unlock.\n"
	}
	return msg
}

// fallbackReasonNote returns the self-explaining line naming why the smart
// gate (#4516) fell back to this blunt stop instead of resolving or
// tailoring it (gastownhall/beads#4551 follow-up). Empty when FallbackReason
// is unset — the smart-tailored adopt and fork-skew stops never set it and
// already explain themselves in userBody.
func (e *RemoteMigrateGateError) fallbackReasonNote() string {
	var why string
	switch e.FallbackReason {
	case fallbackReasonUnreadableState:
		why = "it could not read the remote's cached schema state (no cached ref, a stale/pre-content_hash one, or the cached remote is behind this clone)"
	case fallbackReasonBelowFloor:
		why = "this database is below the convergence floor — a legacy non-deterministic migration is still pending, so an unattended first-mover migrate is not safe to trust"
	case fallbackReasonOptedOut:
		why = "it is disabled (" + SmartGateEnv + "=0)"
	case fallbackReasonUnparseableEnv:
		why = SmartGateEnv + "=" + e.UnrecognizedSmartGateEnv + " is set but was not recognized (only boolean values enable/disable it), so it stayed enabled by default but still could not resolve this stop"
	case fallbackReasonSharedStore:
		why = "this store is shared (dolt sql-server); the first-mover auto-migrate is disabled here because it would lock out co-resident clients (#5920)"
	case fallbackReasonDataBehind:
		why = "this clone is behind the remote in data commits it has not pulled, so it is not the first-mover it looks like on schema alone — migrating in place would diverge from those commits, and can leave every later `bd dolt pull` refusing to merge (#6575, #6368). Run `" + DataBehindRemedyCommand + "` first, then retry: this clone is then the true first-mover it looked like"
		// Only promise the retry resolves itself where it actually does. On a
		// shared store the first-mover auto-migrate is suppressed regardless
		// (fallbackReasonSharedStore, #5920), so pulling clears THIS stop but
		// the retry still needs explicit consent — saying otherwise would send
		// the operator in a loop.
		if !e.Shared {
			why += " and the migrate proceeds on its own"
		} else {
			// #5920's consequence has to travel with this reason too. Before
			// this arm existed a shared, data-behind store reported
			// fallbackReasonSharedStore, whose note names the co-resident
			// lockout; data-behind now outranks it, so without this clause the
			// operator loses the warning that promoting the schema on a shared
			// server locks out every co-resident client still on an older bd.
			why += " — but on this shared store the migrate still needs explicit consent (`" + SharedConsentCommandForced + "`), because it promotes the schema for every co-resident bd client at once and clients still on an older binary will refuse this database until they are upgraded (#5920)"
		}
	default:
		return ""
	}
	return "\n  Smart gate (#4516): " + why + ".\n"
}

// dataBehindBody is the guidance block for the #6575 data-behind stop. It
// REPLACES the blunt migrate-or-adopt body rather than decorating it, because
// in this state both of that body's options are wrong and were measured wrong:
// `bd migrate --force` is exactly the migration this stop exists to prevent
// (and the `bd dolt push` that follows it is rejected as a non-fast-forward,
// since the clone is still behind), and `bd bootstrap` against an existing
// workspace reports "Database already exists … Nothing to do." and exits 0.
// The one command that moves this clone forward is the pull.
func (e *RemoteMigrateGateError) dataBehindBody() string {
	body := "\n" +
		"  This clone is BEHIND the remote: the remote has commits this clone has not\n" +
		"  pulled. On schema alone it looks like a first-mover, which is why the line\n" +
		"  above reads the way it does — but applying the pending migration here would\n" +
		"  put new schema commits on a history that is missing those commits. Once a\n" +
		"  migration moves a tracked table onto Dolt's ignore plane (0062's `events`),\n" +
		"  every later `" + DataBehindRemedyCommand + "` refuses to merge and this clone can no longer\n" +
		"  fetch the commits it was already behind on (#6575, #6368).\n" +
		"\n" +
		"  Pull first — that is the whole remedy:\n" +
		"        " + DataBehindRemedyCommand + "\n"
	if e.DataDiverged {
		body += "" +
			"  This clone also has commits of its own, so the pull MERGES rather than\n" +
			"  fast-forwards. If it reports conflicts, resolve them and let the pull\n" +
			"  finish before retrying (`" + DataBehindRemedyCommand + " --strategy ours|theirs` decides them\n" +
			"  in bulk on embedded storage; `bd conflicts resolve` on a server store).\n"
	} else {
		body += "" +
			"  This clone has no commits of its own, so the pull is a pure fast-forward:\n" +
			"  nothing local is merged or discarded.\n"
	}
	body += "" +
		"\n" +
		"  Then re-run what you just ran.\n"
	if e.Shared {
		// #5920: on a shared store the first-mover auto-migrate stays
		// suppressed after the pull, so the retry needs explicit consent.
		// Saying "it proceeds on its own" here would loop the operator.
		// The command is the FORCED verb, not the bare one: this stop always
		// has a remote (that is where behind-ness is read from), and the bare
		// verb's consent is only read on the no-remote arm — see
		// SharedConsentCommandForced.
		body += "" +
			"  This database is served to co-resident bd clients, so the migration itself\n" +
			"  still needs explicit consent afterwards — it promotes the schema for EVERY\n" +
			"  client at once, and clients still on an older bd will refuse this database\n" +
			"  until they are upgraded (#5920). Once the pull above has completed:\n" +
			"        " + SharedConsentCommandForced + "\n" +
			"        (" + SharedConsentCommandForcedGlobal + " for the shared global database)\n"
	} else {
		body += "" +
			"  The migration then runs on its own: with nothing left to pull, this clone\n" +
			"  is the first-mover it looked like.\n"
	}
	body += "" +
		"\n" +
		"  Do NOT use `bd migrate --force` to get past this. It applies the migration\n" +
		"  while this clone is still behind — the exact state this stop exists to\n" +
		"  prevent — and the `bd dolt push` that would follow is rejected as a\n" +
		"  non-fast-forward, because the clone is still missing the remote's commits.\n"
	if e.Shared {
		// The consent command this arm prescribes is itself a forced migrate,
		// so the warning above has to say what makes that one different: the
		// pull having already landed, not the flag.
		body += "" +
			"  The same goes for the consent command above until the pull has finished:\n" +
			"  what makes it safe is that there is nothing left to pull by then.\n"
	}
	return body
}

// userBody returns the decision-specific guidance block. The default (blunt
// #4515) body is byte-identical to before the smart gate existed.
func (e *RemoteMigrateGateError) userBody() string {
	// The #6575 stop is a blunt stop (empty Decision) with a remedy the blunt
	// body contradicts, so it is keyed on the fallback reason instead.
	if e.IsDataBehind() {
		return e.dataBehindBody()
	}
	switch e.Decision {
	case gateDecisionAdopt:
		return "\n" +
			"  The remote has already been migrated by another clone. Do NOT migrate here —\n" +
			"  adopt the remote's migrated database instead:\n" +
			"        bd bootstrap\n" +
			"  Re-cloning replaces your local database: any local issues you have not pushed\n" +
			"  are LOST. Push first (`bd dolt push`) or save a copy\n" +
			"  (`bd export --all -o backup.jsonl`) before re-cloning.\n"
	case gateDecisionAdoptFastForward:
		return "\n" +
			"  The remote has already been migrated by another clone, and this clone's local\n" +
			"  history is a strict ancestor of the remote's with a clean working set — nothing\n" +
			"  local would be lost by adopting it (unlike a typical adopt):\n" +
			"        bd bootstrap\n" +
			"  Unlike the usual re-clone, this clone has no unpushed commits and no\n" +
			"  uncommitted local changes to discard.\n"
	case gateDecisionSharedNoRemote:
		return "\n" +
			"  This database is served to multiple clients (dolt sql-server /\n" +
			"  proxied-server mode). Applying schema migrations promotes the schema\n" +
			"  version for EVERY client at once: bd clients still running an older\n" +
			"  version will refuse this database until they are upgraded.\n" +
			"\n" +
			"  To migrate (explicit consent):\n" +
			"    1. Upgrade bd on every client of this server first (or accept that\n" +
			"       older clients refuse until they are upgraded).\n" +
			"    2. Then run, once, from a workspace already set up against this\n" +
			"       server:\n" +
			"        " + SharedConsentCommand + "\n" +
			"        (" + SharedConsentCommandGlobal + " for the shared global database;\n" +
			"        " + AllowRemoteMigrateEnv + "=1 bd <cmd> in scripted/CI use)\n" +
			"\n" +
			"  Read commands keep working against the current schema in the meantime;\n" +
			"  writes stay refused until the schema is migrated.\n" +
			"\n" +
			"  If the migration then reports dirty tables, that working set has to be\n" +
			"  committed first — see the recovery the dirty-table error names; do not\n" +
			"  re-run this command in a loop.\n"
	case gateDecisionForkSkew:
		return "\n" +
			"  This clone and the remote already applied DIFFERENT content for migration(s) " +
			FormatMigrationVersions(e.SkewVersions) + ".\n" +
			"  The schema has forked (#4259); `bd dolt pull` can no longer merge. Migrating\n" +
			"  cannot un-fork it. This is a data-loss decision, not an auto-fix:\n" +
			"    • Pick ONE clone as canonical, then re-bootstrap every other clone from it —\n" +
			"      unpushed work on the discarded clones is LOST. Export it first\n" +
			"      (`bd export --all -o backup.jsonl`) if you need it.\n" +
			"        bd bootstrap\n"
	default:
		return "\n" +
			"  This database syncs with a remote. Applying schema migrations on more than\n" +
			"  one clone independently forks the schema so `bd dolt pull` can no longer\n" +
			"  merge — the break is silent and unrecoverable.\n" +
			"\n" +
			"  Choose one:\n" +
			"    • You are the designated migrator (only ONE machine should be): migrate,\n" +
			"      then publish the migrated database to the remote:\n" +
			"        bd migrate --force\n" +
			"        (or " + AllowRemoteMigrateEnv + "=1 bd migrate in scripted/CI use)\n" +
			"        bd dolt push\n" +
			"    • Another machine has already migrated: adopt its database instead of\n" +
			"      migrating here — re-clone from the remote so you receive the migrated\n" +
			"      schema:\n" +
			"        bd bootstrap\n" +
			"      Re-cloning replaces your local database: any local issues you have not\n" +
			"      pushed are LOST. Push first (`bd dolt push`) or save a copy\n" +
			"      (`bd export --all -o backup.jsonl`) before re-cloning.\n" +
			"\n" +
			"  Several machines on one remote? Only ONE migrates. Get every other clone\n" +
			"  fully in sync on its current binary, then after the migrator pushes run\n" +
			"  `bd dolt pull` on each before upgrading it — its upgrade then has nothing\n" +
			"  to migrate and needs no re-clone.\n"
	}
}

// EscapeHint returns the escape-hatch string for JSON error output.
func (e *RemoteMigrateGateError) EscapeHint() string {
	if e.Decision == gateDecisionSharedNoRemote {
		// No remote, so there is no designated-migrator coordination to force
		// past: typing the verb IS the consent.
		return SharedConsentCommand
	}
	return "bd migrate --force"
}

// AgentDirective is the non-runnable instruction surfaced to agents in place of
// a ready-to-run migrate command. Migrating a shared remote is a coordination
// decision — only ONE clone may migrate, and a second clone migrating
// independently forks the schema unrecoverably (#4259) — so bd deliberately does
// NOT hand an agent an auto-runnable "fix". The agent should surface the options
// to the operator and let them choose, per the AgentDiagnostic contract ("Go
// observes and reports, the agent decides and acts").
func (e *RemoteMigrateGateError) AgentDirective() string {
	// #6575: the data-behind stop is the one refusal in this type with a
	// single, safe, non-destructive, auto-runnable remedy — pulling commits
	// this clone is already behind on takes nothing away and needs no
	// cross-clone coordination. Saying so is the point: the default directive
	// below sends the agent to remote_migrate_gate.options, and if those
	// options were the blunt migrate-or-adopt pair the agent would surface
	// the migration this stop exists to prevent (whose follow-up push is
	// rejected non-fast-forward anyway) and a bootstrap that no-ops.
	if e.IsDataBehind() {
		d := "Pull, do NOT migrate: this clone is level with the remote on schema but is missing commits the remote " +
			"has, so the pending migration would land on a history without them and can wedge every later " +
			"`" + DataBehindRemedyCommand + "` (#6575, #6368). The remedy is `" + DataBehindRemedyCommand + "`, which is in " +
			"remote_migrate_gate.options as the single option."
		if e.DataDiverged {
			d += " This clone also has commits of its own, so that pull MERGES rather than fast-forwards and may " +
				"report conflicts to resolve before it completes — surface a conflict outcome to the operator " +
				"rather than forcing past it."
		} else {
			d += " This clone has no commits of its own, so that pull is a pure fast-forward and discards nothing."
		}
		if e.Shared {
			d += " After the pull the migration still needs the operator's explicit consent (" + SharedConsentCommandForced +
				" — this store is remote-backed, so the bare verb does not unlock it; see SharedConsentCommandForced): " +
				"this database is shared with co-resident bd clients and migrating promotes the schema for all of " +
				"them at once (#5920). Do NOT auto-run it, and do NOT run it before the pull has completed."
		} else {
			d += " After the pull, re-running the original command migrates on its own; nothing else is needed."
		}
		return d
	}
	switch e.Decision {
	case gateDecisionAdopt:
		return "Adopt, do NOT migrate: the remote is already migrated, so migrating here would fork it (#4259). " +
			"Adoption re-clones and replaces the local database (unpushed local work is lost), so it is a destructive " +
			"operator decision — surface remote_migrate_gate.options to the operator, do NOT auto-run bd bootstrap."
	case gateDecisionAdoptFastForward:
		return "Adopt, do NOT migrate: the remote is already migrated, and this clone's local history is a strict " +
			"ancestor of the remote's with a clean working set, so adopting is loss-free (no unpushed commits, no " +
			"uncommitted changes discarded) — unlike a typical adopt. Still surface remote_migrate_gate.options to " +
			"the operator rather than auto-running bd bootstrap; the current adopt mechanism is unchanged until an " +
			"automatic fast-forward lands (#4259 follow-up)."
	case gateDecisionForkSkew:
		return "Do NOT migrate: this clone and the remote already applied different content for migration(s) " +
			FormatMigrationVersions(e.SkewVersions) + " — the schema has forked (#4259) and migrating cannot un-fork it. " +
			"Resolving requires picking a canonical clone and re-bootstrapping the others, discarding their unpushed " +
			"work — a data-loss decision. Surface remote_migrate_gate.options to the operator; do NOT auto-run anything."
	case gateDecisionSharedNoRemote:
		return "Coordination decision required: this database is shared with co-resident bd clients, and migrating " +
			"promotes the schema for all of them at once — every client still on an older bd refuses the database " +
			"until it is upgraded (#5920). Other clients' binary versions are not observable from this process, so " +
			"do NOT auto-run the migration: surface remote_migrate_gate.options to the operator and let them confirm " +
			"the fleet is upgraded first. Reads keep working on the current schema meanwhile."
	default:
		return "Coordination decision required: only ONE clone may migrate a shared remote; " +
			"a second clone migrating independently forks the schema unrecoverably (#4259). " +
			"Do NOT auto-run a migration — surface remote_migrate_gate.options to the operator and let them choose."
	}
}

// GateOption is one conditional remediation path for the remote-migrate gate.
// It is intentionally conditional (When) and carries its Risk, so an agent
// cannot treat any single command as the unconditional fix.
type GateOption struct {
	ID       string   `json:"id"`
	When     string   `json:"when"`
	Commands []string `json:"commands"`
	Risk     string   `json:"risk"`
}

// Options returns the two mutually-exclusive remediation paths — migrate (as the
// single designated migrator) or adopt (re-clone the already-migrated DB) — each
// gated on its precondition and annotated with its risk. The migrate command is
// present but reachable only through its "single designated migrator" condition,
// never as a top-level hint.
func (e *RemoteMigrateGateError) Options() []GateOption {
	// On a shared store every remedy below lands on the database the whole
	// server is serving, so each one's risk gains a consequence that has
	// nothing to do with local data (gastownhall/beads#5920). Adopting is the
	// case that reads most wrong without it: its risk is otherwise annotated
	// purely in terms of what this machine loses.
	sharedRisk := ""
	if e.Shared {
		sharedRisk = "; on this shared server it also promotes the schema for every co-resident client, and clients still on an older bd will refuse the database until upgraded"
	}
	adopt := GateOption{
		ID:       "adopt",
		When:     "another machine has already migrated and pushed",
		Commands: []string{"bd bootstrap"},
		Risk:     "re-clones and replaces the local database; push or export unpushed work first or it is lost" + sharedRisk,
	}
	// #6575: exactly ONE option, and it is the pull. The blunt pair below is
	// actively wrong in this state — `bd migrate --force` is the bug, its
	// `bd dolt push` is rejected non-fast-forward while the clone is still
	// behind, and `bd bootstrap` no-ops against an existing workspace — so
	// offering them here would hand an agent two dead ends and no exit.
	// Unlike every other option in this type it carries no precondition the
	// operator has to confirm: pulling commits this clone is already behind on
	// is not a coordination decision.
	if e.IsDataBehind() {
		risk := "none — a pure fast-forward; this clone has no commits of its own, so nothing local is merged or discarded"
		when := "always, for this stop: this clone is behind the remote and must pull before it may migrate"
		if e.DataDiverged {
			risk = "this clone also has commits of its own, so the pull MERGES; it can report conflicts that must be resolved before it completes (bd dolt pull --strategy ours|theirs on embedded storage, bd conflicts resolve on a server store). Nothing is discarded without that resolution"
		}
		pull := GateOption{
			ID:       "pull-first",
			When:     when,
			Commands: []string{DataBehindRemedyCommand},
			Risk:     risk,
		}
		if e.Shared {
			// The pull itself is still safe; what the shared store changes is
			// what happens AFTER it, so the consent step becomes a second
			// option rather than a footnote on the first (#5920). It carries
			// the FORCED verb because this stop is always remote-backed and
			// the bare verb's consent is read only on the no-remote arm — an
			// option whose command cannot succeed in the state that printed it
			// is worse than no option (see SharedConsentCommandForced).
			return []GateOption{pull, {
				ID:       "migrate-shared-after-pulling",
				When:     "the pull above has completed AND every co-resident bd client of this server is upgraded to this binary (confirmed with the operator)",
				Commands: []string{SharedConsentCommandForced},
				Risk:     "co-resident clients still on an older bd will refuse this database until upgraded; running it before the pull completes is the wedge this stop prevents. Its --force consents to migrating a remote-backed shared store (the bare verb's consent is read only with no remote configured); it does not make migrating while behind safe",
			}}
		}
		return []GateOption{pull}
	}
	switch e.Decision {
	case gateDecisionAdopt:
		// Remote is confirmed ahead: migrate is not a valid path, only adopt.
		adopt.When = "the remote is already migrated (confirmed by the gate) — adopt it"
		return []GateOption{adopt}
	case gateDecisionAdoptFastForward:
		// Remote is confirmed ahead AND this clone is a strict ancestor with a
		// clean working set: adopting is loss-free, unlike the plain-adopt case.
		// "Risk: none" is a statement about LOCAL data, and it stays true —
		// but on a shared store it is not the whole risk, and an agent reading
		// only this field would treat the adopt as free.
		ffRisk := "none — this clone's local history is a strict ancestor of the remote's, so nothing local is discarded"
		if e.Shared {
			ffRisk = "nothing local is discarded (this clone's history is a strict ancestor of the remote's), but on this shared server the adopted schema is promoted for every co-resident client, and clients still on an older bd will refuse the database until upgraded"
		}
		return []GateOption{{
			ID:       "adopt-fast-forward",
			When:     "the remote is already migrated and this clone can fast-forward to it losslessly (no unpushed commits, clean working set)",
			Commands: []string{"bd bootstrap"},
			Risk:     ffRisk,
		}}
	case gateDecisionForkSkew:
		// Already forked: neither migrate nor a plain adopt is unconditionally
		// safe; the operator must choose a canonical clone first.
		return []GateOption{{
			ID:       "reconcile-fork",
			When:     "the schema has already forked (different content for migration(s) " + FormatMigrationVersions(e.SkewVersions) + "); choose ONE clone as canonical",
			Commands: []string{"bd export --all -o backup.jsonl", "bd bootstrap"},
			Risk:     "re-bootstrapping the non-canonical clones discards their unpushed work; export it first",
		}}
	case gateDecisionSharedNoRemote:
		// No remote means no adopt path — the database is the single copy every
		// client shares. The only question is whether the fleet is ready.
		return []GateOption{{
			ID:       "migrate-shared",
			When:     "every co-resident bd client of this server is upgraded to this binary (confirmed with the operator), run from a workspace already set up against this server",
			Commands: []string{SharedConsentCommand},
			Risk:     "co-resident clients still on an older bd will refuse this database until upgraded",
		}}
	default:
		return []GateOption{
			{
				ID:       "migrate",
				When:     "you are the single designated migrator (only ONE machine, confirmed with the operator) and no other clone has migrated yet",
				Commands: []string{"bd migrate --force", "bd dolt push"},
				Risk:     "if another clone also migrates independently, the schema forks unrecoverably (#4259)",
			},
			adopt,
		}
	}
}

// IsRemoteMigrateGateError reports whether err (or any error it wraps) is a
// *RemoteMigrateGateError.
func IsRemoteMigrateGateError(err error) bool {
	var e *RemoteMigrateGateError
	return errors.As(err, &e)
}

// CheckRemoteMigrateGate refuses to auto-apply pending schema migrations when the
// database already has a recorded schema version, has pending migrations, and has
// a remote configured — unless the designated-migrator escape hatch is set. It
// returns nil (allow) for a fresh database, an already-current database, or one
// with no remote. Call it before MigrateUp/MigrateUpWithLock on every read/write
// store open. Embedded mode uses this form: its dolt_remotes table already
// reflects remotes persisted in .dolt/config on a fresh open.
func CheckRemoteMigrateGate(ctx context.Context, db DBConn) error {
	return checkRemoteMigrateGate(ctx, db, "", nil, nil, false)
}

// CheckRemoteMigrateGateWithAdopt is CheckRemoteMigrateGate plus the injected
// fast-forward ancestry callbacks (mybd-ae1i piece 2): when the remote is
// ahead with no skew, adopt additionally lets the smart router check whether
// this clone can adopt losslessly (smartAdoptFastForward) instead of only
// ever directing a destructive re-clone. adopt may be nil — the router then
// behaves exactly as CheckRemoteMigrateGate. Embedded mode uses this form
// alongside CheckRemoteMigrateGate's no-remote-name default.
func CheckRemoteMigrateGateWithAdopt(ctx context.Context, db DBConn, adopt *FastForwardAdopter) error {
	return checkRemoteMigrateGate(ctx, db, "", nil, adopt, false)
}

// CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt is
// CheckRemoteMigrateGateWithAdopt plus an explicit sync remote name for the
// smart gate's cached remote-ref read and an on-disk fallback remote probe.
//
// The remote name only chooses which remote-tracking ref the opt-in smart
// router compares against; the blunt gate still trips when any Dolt remote
// exists. extraHasRemote covers the window where dolt_remotes reads empty
// even though a remote is configured: a freshly (auto-)started dolt
// sql-server re-registers CLI remotes from .dolt/config only later, during the
// post-open sync (GH#2315), so an SQL-only check would see no remote on the
// first write open after an upgrade. It is consulted only when the database
// has a pending migration AND the SQL table shows no remote, so the
// (subprocess-backed) filesystem probe stays off the common open path; a nil
// extraHasRemote disables the fallback.
//
// This is the non-shared form of what server mode now calls through
// CheckSharedStoreMigrateGate, and it is what internal/storage/dolt's
// smart-gate tests exercise to pin the embedded-semantics routing that the
// shared form deliberately narrows. Two thinner wrappers of the same shape
// were deleted with the swap to the shared gate: they had no callers left.
func CheckRemoteMigrateGateForRemoteWithRemoteCheckAndAdopt(ctx context.Context, db DBConn, remoteName string, extraHasRemote func() bool, adopt *FastForwardAdopter) error {
	return checkRemoteMigrateGate(ctx, db, remoteName, extraHasRemote, adopt, false)
}

// CheckSharedStoreMigrateGate is the gate for a database that is served to
// co-resident bd clients — an internal/storage/dolt sql-server store, or the
// proxied/`bd serve` unit-of-work provider. It adds two refusals on top of the
// remote-backed #4259 flow (gastownhall/beads#5920):
//
//   - With NO remote, where the ordinary gate allows a silent in-place
//     migration, it refuses unless the operator consented (the migrate verb,
//     --force, or AllowRemoteMigrateEnv). Promoting the schema cursor on a
//     shared server locks out every co-resident client still on an older bd.
//   - With a remote, it suppresses the smart gate's two auto-EXECUTE arms
//     (first-mover auto-migrate and auto-fast-forward). Both were written for
//     a clone this process owns; on a shared server neither can observe the
//     other clients, and the first-mover arm firing is exactly the #5920
//     mechanism. The refusal directives are unchanged.
//
// A fresh database (current == 0) still migrates: creating a database is
// consent for its schema.
func CheckSharedStoreMigrateGate(ctx context.Context, db DBConn, remoteName string, extraHasRemote func() bool, adopt *FastForwardAdopter) error {
	return checkRemoteMigrateGate(ctx, db, remoteName, extraHasRemote, adopt, true)
}

func checkRemoteMigrateGate(ctx context.Context, db DBConn, remoteName string, extraHasRemote func() bool, adopt *FastForwardAdopter, shared bool) error {
	// CurrentVersion treats a missing schema_migrations table as version 0, so a
	// brand-new database falls through the current==0 check below — nothing to fork.
	current, err := CurrentVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("remote-migrate gate: read current version: %w", err)
	}
	if current == 0 {
		return nil // fresh database — nothing to fork
	}

	pending, err := PendingVersions(ctx, db)
	if err != nil {
		return fmt.Errorf("remote-migrate gate: read pending versions: %w", err)
	}
	if len(pending) == 0 {
		return nil // already current — nothing to migrate
	}

	hasRemote, err := anyDoltRemoteConfigured(ctx, db)
	if err != nil {
		return fmt.Errorf("remote-migrate gate: read remotes: %w", err)
	}
	// dolt_remotes can read empty even when a remote is configured: a freshly
	// (auto-)started server has not yet synced CLI remotes from .dolt/config
	// (GH#2315). Consult the caller's on-disk probe before allowing migration.
	if !hasRemote && extraHasRemote != nil {
		hasRemote = extraHasRemote()
	}
	if !hasRemote {
		if !shared {
			return nil // no remote — no cross-clone fork risk
		}
		// A shared store has co-resident clients whether or not it has a
		// remote, and migrating promotes the schema cursor for all of them at
		// once (gastownhall/beads#5920).
		return sharedNoRemoteGate(current, len(pending))
	}

	consent := forceOrEnvConsent()
	if consent.allowed {
		fmt.Fprintf(os.Stderr,
			"Warning: applying %d pending schema migration(s) to a remote-backed database (%s); only one clone should migrate, then `bd dolt push`\n",
			len(pending), consent.how)
		return nil
	}
	unrecognizedEnv := consent.unrecognizedEnv

	latest := LatestVersion()

	// Smart gate (#4516): on by default, BD_SMART_GATE=0 opts out. Once the
	// blunt gate would fire and the designated-migrator escape hatch is not
	// set, consult the remote's cached schema state and auto-resolve the one
	// provably-safe case (first-mover migrate). The undetermined/below-floor
	// verdicts fall through to the blunt #4515 block below, so opting out of
	// smart mode (or an unreadable remote ref) is always at least as safe as
	// before.
	//
	// fallbackReason/unrecognizedSmartGateEnv record WHY, for the blunt block
	// below: an operator hitting it otherwise cannot tell "couldn't read the
	// remote's cached state" apart from "below the convergence floor" apart
	// from "opted out" apart from "unparseable BD_SMART_GATE value" — every
	// path used to produce the byte-identical #4515 text (gastownhall/beads#4551
	// follow-up).
	fallbackReason := ""
	unrecognizedSmartGateEnv := ""
	// dataDiverged records which shape of the #6575 data-behind stop fired, so
	// the guidance names the pull the operator will actually get (a merge, not
	// a fast-forward). Meaningless unless fallbackReason is data-behind.
	dataDiverged := false
	if SmartGateEnabled() {
		decision, skew, ref, atLatest := routeSmartGate(ctx, db, current, latest, remoteName, adopt)
		switch decision {
		case smartAutoMigrate:
			// The safe-first-mover argument is about clones, not clients: it
			// proves nothing about the co-resident bd processes attached to
			// this same server, and auto-executing it here IS the #5920
			// mechanism. Fall through to the blunt stop, naming why.
			if shared {
				fallbackReason = fallbackReasonSharedStore
				break
			}
			smartGateAllowMigrate(len(pending), current)
			return nil
		case smartAdopt:
			return &RemoteMigrateGateError{
				CurrentVersion:  current,
				LatestVersion:   latest,
				Pending:         len(pending),
				UnrecognizedEnv: unrecognizedEnv,
				Shared:          shared,
				Decision:        gateDecisionAdopt,
			}
		case smartAdoptFastForward:
			// mybd-ae1i piece 3 (+ follow-up fix): turn a detected
			// smartAdoptFastForward verdict into action, but ONLY when the
			// fast-forward would land exactly at this binary's latest
			// migration (atLatest). Landing short of latest would leave
			// MigrateUp to apply the remainder unconditionally in place
			// right after, with no gate re-evaluation — reintroducing the
			// #4259 fork risk this gate exists to prevent. Landing past
			// latest would mean a newer binary already migrated the remote
			// further than this one understands (#4135/#4137 class).
			// Suppressed on a shared store for the same reason as the
			// first-mover arm above: the fast-forward is a WRITE that promotes
			// the schema for every co-resident client. The adopt-ff refusal
			// directive below is still accurate and still offered.
			if !shared && atLatest && canAutoFastForward(adopt) {
				// attemptFastForward re-verifies the ancestry/clean/
				// remoteMax-at-latest preconditions in this SAME db session
				// immediately before the write (TOCTOU guard) and performs
				// CALL DOLT_MERGE('--ff-only', ref) — never forcing it.
				if attemptFastForward(ctx, db, ref, adopt, latest) {
					smartGateNotifyFastForward(current, latest)
					return nil
				}
				// A re-check miss or the merge itself refused (a dirty
				// working set raced in, non-fast-forward, concurrent
				// writer): the loss-free guarantee no longer holds
				// confidently — degrade to the plain destructive adopt,
				// never adopt-ff.
				return &RemoteMigrateGateError{
					CurrentVersion:  current,
					LatestVersion:   latest,
					Pending:         len(pending),
					UnrecognizedEnv: unrecognizedEnv,
					Shared:          shared,
					Decision:        gateDecisionAdopt,
				}
			}
			// Disqualified from auto-execution — read-only store, no
			// FastForward callback wired, or the fast-forward would not
			// land exactly at latest — but routeSmartGate already proved
			// this clone a strict ancestor of ref with a clean working set,
			// so adopting is still loss-free. Give the operator the
			// accurate adopt-ff directive instead of the generic
			// destructive-adopt text.
			return &RemoteMigrateGateError{
				CurrentVersion:  current,
				LatestVersion:   latest,
				Pending:         len(pending),
				UnrecognizedEnv: unrecognizedEnv,
				Shared:          shared,
				Decision:        gateDecisionAdoptFastForward,
			}
		case smartForkSkew:
			return &RemoteMigrateGateError{
				CurrentVersion:  current,
				LatestVersion:   latest,
				Pending:         len(pending),
				UnrecognizedEnv: unrecognizedEnv,
				Shared:          shared,
				Decision:        gateDecisionForkSkew,
				SkewVersions:    skew,
			}
		case smartDataBehind, smartDataDiverged:
			// gastownhall/beads#6575: schema parity held but this clone is
			// missing commits the cached remote ref has. Stop, and carry the
			// pull-first remedy on every surface (body, note, agent directive,
			// options) — the blunt migrate-or-adopt guidance is wrong here,
			// not merely incomplete.
			//
			// This reason is reported even on a shared store, where
			// fallbackReasonSharedStore would otherwise outrank it: that one
			// says the gate WOULD have resolved and was overruled, which is
			// not true here. The gate refuses this state on its own merits,
			// and the pull-first remedy is a precondition the operator has to
			// satisfy either way; #5920's co-resident consequence is folded
			// into the data-behind guidance instead of being lost with the
			// reason it outranks.
			fallbackReason = fallbackReasonDataBehind
			// The two verdicts share the reason and the remedy command; they
			// differ in whether that command fast-forwards or merges.
			dataDiverged = decision == smartDataDiverged
		case smartBelowFloor:
			fallbackReason = fallbackReasonBelowFloor
		case smartUndetermined:
			fallbackReason = fallbackReasonUnreadableState
		}
		// An unparseable BD_SMART_GATE value defaults to enabled (same as
		// unset), so routing above still ran — but it is a more actionable
		// fact for the operator than the technical routing outcome, so it
		// takes priority as the surfaced reason. Not over shared-store: that
		// one is structural (the gate WOULD have resolved, and was overruled),
		// so fixing the env value would not change the outcome. Not over
		// data-behind either, for the same reason (gastownhall/beads#6575):
		// that stop stands whatever BD_SMART_GATE says, and it is the one
		// reason carrying a remedy the operator can act on right now, so
		// burying it behind an env-value complaint would cost them the fix.
		envState, envValue := smartGateEnvValue()
		if envState == smartGateEnvUnparseable &&
			fallbackReason != fallbackReasonSharedStore &&
			fallbackReason != fallbackReasonDataBehind {
			fallbackReason = fallbackReasonUnparseableEnv
			unrecognizedSmartGateEnv = envValue
		}
	} else {
		fallbackReason = fallbackReasonOptedOut
	}

	return &RemoteMigrateGateError{
		CurrentVersion:           current,
		LatestVersion:            latest,
		Pending:                  len(pending),
		UnrecognizedEnv:          unrecognizedEnv,
		FallbackReason:           fallbackReason,
		Shared:                   shared,
		UnrecognizedSmartGateEnv: unrecognizedSmartGateEnv,
		DataDiverged:             dataDiverged,
	}
}

// migrateConsent is the outcome of the consent ladder. how names the surface
// that granted it, so each arm's warning can attribute itself accurately
// instead of hardcoding one command.
type migrateConsent struct {
	allowed bool
	how     string
	// unrecognizedEnv carries an AllowRemoteMigrateEnv value that was set but
	// is not a boolean, so a typo'd escape hatch fails with a hint rather than
	// silently staying locked (bd-6dnrw.34).
	unrecognizedEnv string
}

// forceOrEnvConsent runs the two consent surfaces BOTH arms share: the
// programmatic `--force` twin and AllowRemoteMigrateEnv.
//
// It exists so the parsing lives in one place. The two arms had hand-copied
// ladders that could drift, which is a real hazard for a variable whose own
// doc comment insists it is "the single knob" with one semantic.
//
// Both are consulted only once the gate would otherwise fire, so an operator
// who exports the env var in a shell profile is never warned on an open with
// nothing pending.
func forceOrEnvConsent() migrateConsent {
	// Process-local by design: unlike os.Setenv(AllowRemoteMigrateEnv), the
	// programmatic twin cannot leak into child processes (git hooks, dolt
	// subprocesses). Set by `bd migrate --force` in the root PersistentPreRunE
	// before both autoMigrateOnVersionBump and the main store open.
	if forceAllowRemoteMigrate {
		return migrateConsent{allowed: true, how: "bd migrate --force"}
	}
	v := os.Getenv(AllowRemoteMigrateEnv)
	if v == "" {
		return migrateConsent{}
	}
	// Any boolean true ("1", "true", "TRUE", ...) unlocks; a set-but-
	// unparseable value is surfaced in the gate error instead of silently
	// staying locked.
	allowed, err := strconv.ParseBool(v)
	if err != nil {
		return migrateConsent{unrecognizedEnv: v}
	}
	if !allowed {
		return migrateConsent{}
	}
	return migrateConsent{allowed: true, how: AllowRemoteMigrateEnv + "=" + v}
}

// SharedConsentCommand is the command that consents to migrating a shared
// database that has no remote. It is the single source for the gate's escape
// hint, its remediation option, and the warning it prints once consent is
// given, so those three can never name different commands.
//
// On the shared GLOBAL database (`beads_global`) the same command needs
// `--global` to route there; see SharedConsentCommandGlobal.
const SharedConsentCommand = "bd migrate schema"

// SharedConsentCommandGlobal is SharedConsentCommand aimed at the shared
// global database. `bd init` and any `--global` invocation refuse against
// their own database, so the remedy they print must carry the flag or it
// migrates the wrong one.
const SharedConsentCommandGlobal = SharedConsentCommand + " --global"

// SharedConsentCommandForced is the consent command for a shared database that
// IS remote-backed. Every #6575 data-behind stop is in that state by
// construction — behind-ness is read from `remotes/<name>/<branch>`, so there
// is always a remote — and there the bare verb above is a dead end:
// sharedMigrateConsent has exactly one reader, sharedNoRemoteGate, which
// checkRemoteMigrateGate reaches only on the !hasRemote branch. With a remote
// configured the retry routes through forceOrEnvConsent instead, so typing the
// bare verb sets a consent nothing reads and the operator lands on the blunt
// shared-store refusal, whose body prescribes the `bd migrate --force` this
// stop's own body tells them not to use.
//
// The fix is deliberately the command, not the gate: widening
// sharedNoRemoteGate's verb consent to the remote-backed arm would drop the
// designated-migrator confirmation that #4259's cross-clone fork risk demands
// whenever a remote exists, which is the very thing sharedNoRemoteGate's
// comment says is intentional. `--force` is what forceOrEnvConsent honors, and
// the CLI accepts it on this verb (cmd/bd.isForcedMigrate covers both
// `bd migrate` and `bd migrate schema`), so this is the narrowest command that
// still names the schema migration AND actually unlocks the retry.
const SharedConsentCommandForced = SharedConsentCommand + " --force"

// SharedConsentCommandForcedGlobal is SharedConsentCommandForced aimed at the
// shared global database, for the same reason SharedConsentCommandGlobal
// exists.
const SharedConsentCommandForcedGlobal = SharedConsentCommandGlobal + " --force"

// sharedNoRemoteGate decides the shared-store-without-a-remote arm
// (gastownhall/beads#5920). There is no remote, so none of the #4259
// migrate-vs-adopt machinery applies: the only question is whether the
// operator consented to promoting the schema for every co-resident client.
func sharedNoRemoteGate(current, pending int) error {
	consent := forceOrEnvConsent()
	// The migrate verb is the third surface, and unlike the other two it
	// unlocks ONLY here: with a remote configured, #4259's cross-clone fork
	// risk still demands the stronger designated-migrator confirmation.
	if !consent.allowed && sharedMigrateConsent {
		consent.allowed, consent.how = true, SharedConsentCommand
	}
	if consent.allowed {
		fmt.Fprintf(os.Stderr,
			"Warning: applying %d pending schema migration(s) to a shared server database (%s); co-resident bd clients still on an older binary will refuse this database until they are upgraded (#5920)\n",
			pending, consent.how)
		return nil
	}

	return &RemoteMigrateGateError{
		CurrentVersion:  current,
		LatestVersion:   LatestVersion(),
		Pending:         pending,
		UnrecognizedEnv: consent.unrecognizedEnv,
		Decision:        gateDecisionSharedNoRemote,
		Shared:          true,
	}
}

// anyDoltRemoteConfigured reports whether the database has any Dolt remote
// registered. dolt_remotes is always present in a Dolt database; a
// "table not found" is treated as "no remotes" so a missing system table can
// never wedge every store open.
func anyDoltRemoteConfigured(ctx context.Context, db DBConn) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_remotes").Scan(&count); err != nil {
		if dberrors.IsTableNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return count > 0, nil
}
