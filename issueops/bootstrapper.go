package issueops

import "context"

// BootstrapRequest asks an UNIDENTIFIED substrate to record the identity every
// later open verifies against.
//
// THE REQUEST CARRIES THE IDENTITY RATHER THAN MINTING IT. Choosing a prefix
// from a directory name, generating a project UUID and deciding whether to adopt
// an identity another rig already chose are all decisions made where the
// workspace is — a working directory, a git remote, a metadata.json — and none
// of them is reachable from a substrate. What is reachable from a substrate is
// the write, the refusal that guards it and the read that observes both, which
// is the whole of what this role and InitVerifier are.
type BootstrapRequest struct {
	// Prefix is the workspace's issue prefix, the string every id this
	// workspace mints begins with. It is REQUIRED: "" is ErrValidation, because
	// a substrate with no prefix cannot name an issue and every later open
	// reports it as uninitialized.
	//
	// A TRAILING HYPHEN IS STRIPPED, and nothing else about the value is
	// touched. The settings plane has always normalized this key that way, so
	// stating it here is what makes Result.Prefix — and the answer a later
	// VerifyIdentity gives — the same string on every backend rather than
	// whichever one that plane happened to store.
	Prefix string
	// ProjectID is the workspace's project identity, the value cross-project
	// verification compares on every connection to catch a workspace opened
	// against another project's database. It is REQUIRED: "" is ErrValidation.
	//
	// This role does not generate one. A caller that has no identity to record
	// yet mints it (configfile.GenerateProjectID); a caller adopting a
	// substrate another rig or a provisioning server already identified must
	// not be here at all — that is InitVerifier's question, and asking this one
	// is the refusal below.
	ProjectID string
}

// BootstrapResult reports the identity the substrate now carries.
//
// It echoes rather than adds, deliberately. There is no "created" flag: this
// role writes or refuses, so a nil error IS the flag, and a second statement of
// it is a second thing to keep true.
type BootstrapResult struct {
	// Prefix is the prefix as STORED, which is the request's with any trailing
	// hyphen removed. A caller that goes on to print the prefix, or to build
	// ids with it, uses this rather than what it sent.
	Prefix string
	// ProjectID is the identity as stored, byte for byte what was requested.
	ProjectID string
}

// Bootstrapper describes SEEDING A SUBSTRATE'S IDENTITY: the one-time write
// that turns a database bd can connect to into a workspace bd can use.
//
// WHY ONLY THIS SLICE OF `bd init` IS A ROLE. Everything else init does —
// creating .beads/, writing metadata.json and config.yaml, initializing a git
// repository, installing hooks, rendering agent instructions, starting a Dolt
// server — CREATES the substrate or the workspace around it. A role is reached
// through an accessor ON a store, so it cannot be what makes the store exist;
// and fs/git provisioning has exactly one implementation, so a three-backend
// conformance contract over it would be three names for one body. What is left
// once those are taken out is the part that happens INSIDE the database and
// that all three backends must agree about, and that part is the identity.
//
// THE VERSION-CONTROL REMOTE IS NOT IDENTITY and is not here, even though one
// of the two front doors used to write it in the same call. A remote is where
// this database syncs to; the identity is what this database IS. They have
// different lifetimes, different failure modes and different permissions, and
// putting them in one call is what let a remote that could not be created fail
// a bootstrap that had already succeeded.
//
// A BOOTSTRAP OVER AN ALREADY-IDENTIFIED SUBSTRATE IS REFUSED. See Bootstrap.
//
// There is NO HTTP SURFACE, and that is a decision rather than an omission —
// the same shape VersionReconciler's is, for a different reason. A server can
// only serve a database it is already bound to, and binding it required the
// identity this role writes; so the one caller that could reach such an
// endpoint would be asking a running server to identify a workspace that is by
// construction already identified, which is precisely the refusal below.
// Bootstrapping over an unauthenticated surface is also a different risk class
// from reading one: `bd serve`'s document is reads plus a claim plus one
// destructive sweep, and an operation that stamps a shared database's identity
// belongs to whatever provisions the database, not to a request. The identity a
// remote caller can act on is already published — ContextResponse.project_id on
// GET /v0/beads/context is InitVerifier's answer, on the wire, today.
//
// Deterministic request-validation failures match ErrValidation; result values
// are unspecified when error is non-nil.
type Bootstrapper interface {
	// Bootstrap records the request's identity on a substrate that has none.
	//
	// AN ALREADY-IDENTIFIED SUBSTRATE IS REFUSED, with ErrAlreadyIdentified
	// carried by an *AlreadyIdentifiedError naming what was found. "Already
	// identified" means EITHER marker is present — a prefix, a project id, or
	// both — and the reason it is either rather than both is that a substrate
	// several rigs share carries a prefix one of them chose, and overwriting it
	// renames every id the others are about to mint. A partial identity is
	// refused for the same reason it is reported: bd cannot tell a half-written
	// bootstrap from a deliberately half-provisioned database, and guessing
	// wrong destroys the one it did not mean.
	//
	// THE REFUSAL WRITES NOTHING, and on every backend it is decided from the
	// same read the write would have followed, in one transaction where the
	// substrate has transactions. Re-identifying a workspace is therefore an
	// explicit operation somebody has to ask for, rather than something that
	// happens because init ran twice.
	//
	// IT WRITES THE IDENTITY AND NOTHING ELSE — the prefix and the project id,
	// the same two markers the refusal reads and VerifyIdentity answers with.
	//
	// The per-clone bookkeeping `bd init` also seeds is deliberately NOT here.
	// The repository and clone fingerprints, the synced-at marker and the
	// recorded binary version are rewritten on EVERY init, adopt or not — a
	// fresh clone of an already-identified database needs its own fingerprints
	// precisely because it bootstrapped nothing — so folding them into a
	// one-time, refusable write would mean either a refusal that skips them or
	// a write that is no longer one-time. The recorded version has a second
	// reason: it is VersionReconciler's key, and two roles writing one key is
	// the accretion the governing rule exists to prevent.
	//
	// A LATER VerifyIdentity IS THE PROMISE. After a successful Bootstrap it
	// answers with this result's Prefix and ProjectID, from a fresh connection
	// or a fresh unit of work. That is what "bootstrapped" means here, and it
	// is why the read is InitVerifier's rather than a second method: the two
	// are asked by different callers with different permissions, and the caller
	// that may only read must not be handed the writer.
	//
	// THE TWO MARKERS LAND IN ONE TRANSACTION on every backend, because every
	// backend's body takes one — which is also what makes the refusal a refusal
	// rather than a suggestion two racing inits both pass. This role therefore
	// never leaves a HALF-identified substrate behind; it only FINDS one,
	// written by something older or by a hand, and the refusal names what it
	// found so its caller can tell that apart from a re-init.
	//
	// AT MOST ONE VERSION-CONTROL ENTRY is recorded, and a backend that records
	// none is conforming. The two markers live on planes that travel, so a
	// bootstrap is history-worthy where a backend commits at all; what the role
	// forbids is one entry per key.
	Bootstrap(ctx context.Context, req BootstrapRequest) (BootstrapResult, error)
}
