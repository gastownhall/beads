package issueops

import "context"

// VerifyIdentityRequest asks what identity a substrate carries. It has no
// fields today and is spelled as a type so that gaining one is an additive
// change to something callers already name — the reason
// RecordedVersionRequest and ListSettingsRequest are spelled the same way.
type VerifyIdentityRequest struct{}

// VerifyIdentityResult is the identity the substrate carries, as it stands.
//
// BOTH FIELDS ARE "" WHEN ABSENT, and "" IS A NORMAL ANSWER rather than an
// error or a missing row to classify. A substrate nothing has bootstrapped
// answers with two empty strings, and that is the answer a caller deciding
// whether to bootstrap needs; turning it into ErrNotFound would make the
// ordinary case look like a failure and force every caller to unwrap it back
// into the same two strings.
type VerifyIdentityResult struct {
	// Prefix is the substrate's issue prefix, or "" when it carries none.
	Prefix string
	// ProjectID is the substrate's project identity, or "" when it carries
	// none.
	//
	// THE TWO ARE READ AS ONE SNAPSHOT wherever the substrate publishes a
	// transaction, so a caller never sees a prefix from before a bootstrap
	// beside a project id from after one. That matters because the pair is what
	// a caller compares against a local metadata.json, and a torn read there
	// looks exactly like the cross-project mismatch the comparison exists to
	// find.
	ProjectID string
}

// InitVerifier describes READING A SUBSTRATE'S IDENTITY: the prefix and project
// id `bd init` reconciles against, adopts, or refuses to invent.
//
// IT IS A SEPARATE ROLE FROM Bootstrapper, AND NOT ITS READ HALF. The
// precedent that would have made them one — VersionReconciler, whose read and
// write are two shapes of one question — does not reach here, because the
// callers are different and so are their PERMISSIONS. bd reads this identity on
// paths where it is forbidden to write one: against a bts-provisioned team
// database, whose identity the provisioning tool owns and bd adopts; against an
// authenticating gateway, whose credential may be read-only and whose
// server-side identity a client must never overwrite. Handing those callers a
// surface with a write on it is exactly the shape that produced the writes they
// now have to suppress flag by flag. A capability a caller must not have is a
// capability it should not be able to reach.
//
// THE ONE PROMISE EVERYTHING ELSE HERE IS FOR: an ABSENT identity and an
// UNREADABLE one are different answers. Absent is "" with a nil error;
// unreadable is an error. Nothing in this role ever reports a failed read as an
// empty answer, because every caller then decides the same thing from it — an
// unprovisioned database gets bootstrapped or refused as a provisioning-contract
// violation, and a database that merely could not be reached must get neither.
// A flaky connection misread as an empty database is how a workspace acquires a
// second identity.
//
// IT WRITES NOTHING. Not the keys it reads, not a marker that it ran, and no
// version-control entry. A verifier that repaired what it found would be the
// bootstrap this role exists to keep separate.
//
// There is NO HTTP surface, for the reason recorded on Bootstrapper: the
// identity a remote caller can act on is already published as
// ContextResponse.project_id on GET /v0/beads/context, and a second spelling of
// one fact is a second thing to keep true.
//
// Deterministic request-validation failures match ErrValidation; result values
// are unspecified when error is non-nil.
type InitVerifier interface {
	// VerifyIdentity returns the substrate's identity as it stands.
	//
	// It answers "" for each marker the substrate does not carry, reports a
	// read failure as an ERROR rather than as absence, and writes nothing.
	VerifyIdentity(ctx context.Context, req VerifyIdentityRequest) (VerifyIdentityResult, error)
}
