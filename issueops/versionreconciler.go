package issueops

import "context"

// RecordedVersionRequest asks for the workspace's recorded version markers. It
// carries no fields today and exists so that adding one is an additive change
// to a type callers already name, rather than a signature change on the
// interface — the reason ListSettingsRequest is spelled the same way.
type RecordedVersionRequest struct{}

// RecordedVersionResult is what the workspace currently records about the bd
// binaries that have opened it.
//
// BOTH FIELDS ARE CLONE-LOCAL. They live in the dolt-ignored local metadata
// plane, so they are not history, they do not sync, a fresh clone of a
// workspace has neither of them, and two machines sharing one workspace answer
// this question differently. That is the whole reason the pair exists as its
// own role rather than as two more keys on WorkspaceConfig, whose values are
// durable workspace settings that DO travel.
type RecordedVersionResult struct {
	// Recorded is the version of the last bd binary that reconciled this
	// workspace, or "" for a workspace no binary has reconciled yet.
	//
	// "" IS A NORMAL ANSWER, not an error and not a missing row to classify:
	// the local metadata plane is ephemeral by construction and every reader of
	// it has to treat absence as "unknown". A caller that needs to know whether
	// a reconciliation has ever happened compares against "" and gets a
	// truthful answer on all three backends.
	Recorded string
	// HighWaterMark is the HIGHEST version ever recorded here, which is not the
	// same fact as Recorded and is why there are two fields rather than one.
	//
	// Recorded moves both up and — when something outside this role writes it —
	// down; the high-water mark only ever moves up. A workspace that was opened
	// by a newer binary and then by an older one keeps the newer number here,
	// and that is what lets ReconcileVersion refuse a downgrade it could not
	// otherwise see. It is "" exactly when nothing has ever been recorded.
	HighWaterMark string
}

// VersionReconcileRequest asks the workspace to record the running binary's
// version.
type VersionReconcileRequest struct {
	// CLIVersion is the running binary's release version. It is REQUIRED: an
	// empty value is ErrValidation rather than a no-op, because the one thing
	// this role must never do is record "this workspace was last opened by
	// nothing" over a real marker and lose the downgrade guard with it.
	//
	// It is compared as dotted decimal, component by component, with a
	// non-numeric component reading as 0. So "1.2.0" and "1.2.0-rc1" are the
	// SAME version to this role, "1.2" is lower than "1.2.1", and a version
	// string with no digits at all is 0.0.0. That is the comparison both front
	// doors have always used and it is stated here rather than tightened,
	// because tightening it would start refusing pre-release binaries that
	// reconcile cleanly today.
	CLIVersion string
}

// VersionReconcileResult reports what the reconciliation decided.
//
// THE THREE OUTCOMES ARE DISTINGUISHABLE WITHOUT AN ERROR, and that is the
// shape this role needs rather than a convenience. Its caller is a startup
// hook with no user waiting on an answer, so it must be able to tell "I moved
// the marker" from "there was nothing to do" from "I declined" while treating
// all three as success — and keep a real failure, which IS an error, separate
// from all of them.
type VersionReconcileResult struct {
	// Previous is the marker as it was found: "" on a workspace nothing has
	// reconciled yet.
	Previous string
	// Current is the marker as it stands after the call. It equals the
	// request's CLIVersion when Migrated is set, and equals Previous otherwise
	// — including on a refusal, where it is the value that STAYED.
	Current string
	// Migrated reports that the marker moved, and it is the only outcome that
	// writes anything. It is false for a workspace already at this version:
	// reconciling twice is not two migrations.
	Migrated bool
	// Downgrade reports a REFUSAL, and a refusal is not an error.
	//
	// It is set when the running binary is older than the marker, or older than
	// the high-water mark, and it means nothing was written and the newer
	// number still stands. A caller may report it, and both front doors do at
	// debug level, but nothing about the command it precedes changes: an older
	// binary is allowed to use a workspace a newer one has opened. What it is
	// NOT allowed to do is quietly relabel that workspace as older than it is,
	// because the next upgrade would then re-run the version-bump work the
	// newer binary already did.
	Downgrade bool
}

// VersionReconciler describes the workspace's CLONE-LOCAL VERSION MARKERS: the
// pair of dolt-ignored values recording which bd binary last opened this
// workspace and the highest one that ever has. Like Lifecycle, Reader, Counter,
// WorkspaceConfig, StatsReporter, CycleDetector, EdgeReader and ReadyCounter, it
// is a role with its own accessor, and a new capability gets a new role rather
// than a method appended here.
//
// IT BELONGS TO NO COMMAND, and it is the only role in this program that
// does not. It fires from PersistentPreRun on EVERY startup, on both routes,
// before the command the user actually typed has run — which is precisely why
// it went unnoticed for so long: `bd version` prints two strings, opens no
// store and reaches nothing here, so the state machinery was hiding behind a
// command that does not use it.
//
// THAT ORIGIN IS A CONSTRAINT ON EVERY PROMISE BELOW, not a piece of history.
// A role that runs before every command pays for itself on every command, so:
//
//   - It does exactly two round trips on the path that changes nothing (read
//     the two markers) and never more than four. It loads no configuration,
//     opens no second connection and takes no lock.
//   - It NEVER refuses the command in front of it. A downgrade is reported as
//     an outcome rather than raised as an error (see Downgrade), and a genuine
//     failure — a substrate that cannot be written, a canceled context, a
//     connection that dropped — is an error the CALLER is expected to log and
//     walk past. Both front doors do exactly that, at debug level, and a role
//     whose caller must ignore its errors has to say so where the promise is
//     made rather than leave each caller to decide.
//   - It repairs nothing. It does not migrate a schema, rebuild a database or
//     rewrite a marker it cannot parse. Everything named "auto-migrate" around
//     its CLI call site is the CLI's, and the two markers are all this role
//     touches.
//
// WHY IT IS NOT WorkspaceConfig WITH TWO MORE KEYS. Those are two different
// planes with two different lifetimes. A workspace setting is durable, travels
// with the database and is the same on every machine that has it; these two
// markers are dolt-ignored, do not survive a clone, and are deliberately
// different per machine — a stale marker on one laptop says nothing about
// anyone else's. Publishing them through the settings plane would put a value
// that cannot be shared behind an interface whose whole promise is that its
// values are.
//
// BOTH METHODS BELONG TO THE ONE ROLE. They are two shapes of one question,
// which is what the governing rule permits a role to be born with: reconciling
// IS a statement about what a later read returns, and the read is the only way
// to observe that the write landed — the reconcile result reports what it
// decided, not what the workspace holds. They also cannot be separated in
// practice, because the direct front door needs the read alone: it probes a
// read-only handle first so an already-current workspace never pays for a
// writable open.
//
// There is NO HTTP surface, and that is a decision rather than an omission.
// Reconciliation is an effect of a process starting, not of a request
// arriving; a client cannot usefully ask a server to record the CLIENT's
// version, because the marker describes the binary holding the database, and
// the one version fact a client can act on — the serving binary's release —
// is already published as ContextResponse.bd_version on GET /v0/beads/context.
// An endpoint here would publish a per-clone number that a remote caller can
// neither compare against its own nor do anything with.
//
// Deterministic request-validation failures match ErrValidation; result values
// are unspecified when error is non-nil.
type VersionReconciler interface {
	// RecordedVersion returns the two markers as they stand. It writes
	// nothing, records no history, and answers "" for a workspace that has
	// never been reconciled rather than reporting a missing row.
	RecordedVersion(ctx context.Context, req RecordedVersionRequest) (RecordedVersionResult, error)

	// ReconcileVersion records the running binary's version, and is the ONLY
	// way this pair is written.
	//
	// It moves the marker forward and raises the high-water mark to match when
	// the running binary is newer than both (Migrated); it writes nothing when
	// the marker already names this version (neither flag set); and it writes
	// nothing and reports Downgrade when the running binary is older than
	// either marker. The two markers move TOGETHER or not at all in the sense
	// that matters to a reader: the mark is never lowered, and a successful
	// migration always leaves the mark greater than or equal to the marker.
	//
	// A LATER RecordedVersion IS THE PROMISE. After a Migrated result, it
	// answers with the version just recorded, from a fresh connection or a
	// fresh unit of work; after a refusal or a validation failure, it answers
	// with exactly what it answered before the call. That is the whole of what
	// "reconciled" means here, and it is why the read shares this role.
	//
	// AN EMPTY CLIVersion IS ErrValidation and writes nothing.
	//
	// RECONCILIATION RECORDS NO HISTORY. The markers are clone-local and
	// dolt-ignored, so a reconciliation adds no entry to the workspace's log on
	// any backend — a fact worth stating because this runs on every startup and
	// a role that appended one would fill a workspace's history with the
	// version of the binary that read it.
	ReconcileVersion(ctx context.Context, req VersionReconcileRequest) (VersionReconcileResult, error)
}
