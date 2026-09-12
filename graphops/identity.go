package graphops

import "context"

// The identity roles: what the Scope IS, how it comes to exist, and how its
// authority is administered. IdentityReader is EXEMPT from the authority
// assertion — it reports state, including the state "this workspace is not
// the authority" — and reads three rows. ScopeBootstrapper mints, once.
// Admin is the local administrative composition root: promotion, rotation,
// the ledger lane, and the restore marker. Transitions are multi-phase and
// recovered by evidence on the next load; each one produces the witness and
// carries its own preconditions in place of the head check. None of these
// roles is ever reachable from a server: httpapi has no field for them.

// MintRequest asks for a Scope to be minted under a URL.
//
// DECISION: the shape. Mint installs the built-in catalog, which is the
// implementation's (it arrives with the type-generation work the design
// defers), so the request carries the one fact only the operator knows.
type MintRequest struct {
	// ScopeURL is the canonical Scope URL to mint under, already normalized
	// (NormalizeScopeURL) and admissible as a persisted identity
	// (ValidatePersistedScopeURL) — a non-canonical spelling or the reserved
	// local-test segment is ErrValidation, and a URL this store has refused
	// (rotated away) is ErrURLReused.
	ScopeURL string
}

// PromoteRequest asks this workspace to hold the Scope's authority.
//
// DECISION: the shape, from the verb's three cases (spec A4): a lease naming
// this workspace self-regrants with no input; a lease naming another holder
// is taken only with Steal; no lease row is refused unless RotateURL names
// the new Scope URL to bootstrap under.
type PromoteRequest struct {
	// Steal is the operator's assertion that a lease held by another holder
	// names the same database and may be taken. A foreign holder's expiry
	// alone never grants a takeover.
	Steal bool
	// RotateURL, when set, rotates the Scope to this canonical URL
	// (ValidatePersistedScopeURL) in the same transition: the old URL is
	// refused forever, the new lease row is created under the new one. ""
	// leaves the URL alone.
	RotateURL string
}

// RotateRequest asks for the Scope URL to change.
type RotateRequest struct {
	// NewURL is the canonical Scope URL (ValidatePersistedScopeURL) to serve
	// under from now on. The current URL is refused forever (refuse_url), the
	// new one recorded (rotate), in one transaction; no stored intra-Scope
	// reference changes.
	NewURL string
}

// LedgerRange selects a contiguous run of ledger events.
//
// DECISION: the shape; 0 on either side means unbounded on that side.
type LedgerRange struct {
	// FromSeq is the first sequence number wanted; 0 means from the mint
	// event.
	FromSeq uint64
	// ToSeq is the last sequence number wanted; 0 means through the head.
	ToSeq uint64
}

// LedgerApplyResult reports what a ledger apply changed.
//
// DECISION: the shape, from what the lane does (spec A4): events appended,
// allocations that became reserved because no row backs them, the head the
// store now records, and the counter it was set to (last_seq + 1).
type LedgerApplyResult struct {
	// Applied is the number of events appended.
	Applied int
	// Reserved is the number of allocations that became reserved.
	Reserved int
	// HeadSeq and HeadHash are the ledger head after the apply.
	HeadSeq  uint64
	HeadHash string
	// NextSeq is the counter's value after the apply.
	NextSeq uint64
}

// IdentityReader reports the Scope and this workspace's claim on it.
type IdentityReader interface {
	// Read returns the Scope row and the witness's claim in three
	// statements: Scope row, ledger head, lease. A workspace with no Scope
	// row answers ErrNoScope; every other state — no witness, a foreign
	// holder, an expired lease, a pending transition, unverified — is
	// REPORTED in the claim rather than refused, because this role exists
	// to say what is.
	Read(ctx context.Context) (ScopeIdentity, error)
	// LedgerDurability is the provider's declaration for ruling 11: whether
	// its ledger is restored with its state, survives independently, or does
	// not survive a restore.
	LedgerDurability(ctx context.Context) (LedgerDurability, error)
}

// ScopeBootstrapper mints the Scope, once.
type ScopeBootstrapper interface {
	// Mint, under the exclusive gate, with no Scope row: inserts the
	// singleton Scope row, seeds the sequence counter, appends the mint
	// event, installs the built-in catalog with an install event each, takes
	// the lease, publishes, and finalizes the witness — one transaction, one
	// scoped commit. A Scope row already present is ErrScopeExists; a URL
	// this store has refused is ErrURLReused. Reached only by the link-mode
	// serve verb's staged startup.
	Mint(ctx context.Context, req MintRequest) (ScopeIdentity, error)
}

// Admin administers the Scope's authority from the local composition root.
type Admin interface {
	// Promote makes this workspace the holder: a self-regrant when the lease
	// already names it (no epoch change), a CAS of the epoch with a promote
	// event when Steal takes another holder's lease, or — with RotateURL — a
	// bootstrap under a new URL when no lease row exists. Runs under the
	// shared gate beside a live server. A lost CAS race is ErrNotAuthority.
	Promote(ctx context.Context, req PromoteRequest) (ScopeIdentity, error)
	// Rotate refuses the current Scope URL forever and records the new one,
	// in one transaction, then writes the tracked configuration in the
	// transition's config_written phase. Refused while an environment
	// override of the Scope URL is in force. The new URL must not be one
	// this store has refused (ErrURLReused).
	Rotate(ctx context.Context, req RotateRequest) (ScopeIdentity, error)
	// LedgerSnapshot returns the events in the range, contiguous and in
	// order, under a manifest describing exactly them. Exempt from the head
	// check.
	LedgerSnapshot(ctx context.Context, rng LedgerRange) (LedgerManifest, []LedgerEvent, error)
	// LedgerApply restores anti-reuse history, not graph content, under the
	// exclusive gate: the manifest must cover the events (LedgerManifest.
	// Covers), its lineage must match the Scope row's, and the store's head
	// must equal the manifest's predecessor — a gap, a fork or a foreign
	// lineage is ErrValidation and nothing is written. An applied allocate
	// whose row is absent becomes a reserved allocation; the Scope lineage
	// is replayed; the counter is set to last_seq + 1; then the lease is
	// regranted.
	LedgerApply(ctx context.Context, manifest LedgerManifest, events []LedgerEvent) (LedgerApplyResult, error)
	// MarkUnverified sets the witness's unverified marker after a database
	// restore, so every protected operation refuses until a restore verb
	// clears it. A no-op without a witness.
	MarkUnverified(ctx context.Context) error
	// ClearUnverified clears the marker once continuity has been shown or
	// the Scope rotated.
	ClearUnverified(ctx context.Context) error
}
