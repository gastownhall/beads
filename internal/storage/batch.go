// Package storage defines the interface for issue storage backends.
package storage

// OrphanHandling specifies how to handle issues with missing parent references.
type OrphanHandling string

const (
	// OrphanStrict fails import on missing parent (safest)
	OrphanStrict OrphanHandling = "strict"
	// OrphanResurrect auto-resurrects missing parents from database history
	OrphanResurrect OrphanHandling = "resurrect"
	// OrphanSkip skips orphaned issues with warning
	OrphanSkip OrphanHandling = "skip"
	// OrphanAllow imports orphans without validation (default, works around bugs)
	OrphanAllow OrphanHandling = "allow"
)

// BatchCreateOptions contains options for batch issue creation.
// This is a backend-agnostic type that can be used by any storage implementation.
type BatchCreateOptions struct {
	// OrphanHandling specifies how to handle issues with missing parent references
	OrphanHandling OrphanHandling
	// SkipPrefixValidation skips prefix validation for existing IDs (used during import)
	SkipPrefixValidation bool
	// ConflictSkip makes batch creation insert-if-new instead of UPSERT: an
	// issue whose ID already exists is left untouched rather than overwritten.
	// Used only by the auto-import upgrade-recovery fallback (GH#3955), so
	// that if the emptiness guard in maybeAutoImportJSONL ever regresses
	// again (cf. PR #3630), auto-import degrades to a harmless no-op instead
	// of clobbering live rows. Explicit `bd import` keeps UPSERT semantics.
	ConflictSkip bool
	// RejectStaleUpserts makes the issue-row UPSERT conditional on updated_at:
	// the incoming row only rewrites the stored columns when it is strictly
	// newer. A stored row that is strictly newer is rejected outright
	// (OnStaleRejected fires, aux data skipped); an equal-timestamp row keeps
	// every stored column but still merges its aux data — updated_at has
	// second granularity, so a tie may be two distinct same-second updates
	// and the local row wins it (bd-hj85c). This is the transactional half
	// of the import stale guard (bd-pkim8): cmd/bd's filterStaleImportIssues
	// reads local updated_at before the batch write, so a local update
	// committing in between would otherwise be silently overwritten (the
	// PR 4204 race). Set by `bd import` unless --allow-stale; create paths
	// leave it false.
	RejectStaleUpserts bool
	// SkipDependencyValidationErrors skips dependency validation failures that
	// legacy imports tolerated, such as cycles or self-dependencies.
	SkipDependencyValidationErrors bool
	// OnSkippedDependency records dependency edges skipped during batch create.
	OnSkippedDependency func(issueID, dependsOnID, reason string)
	// OnStaleRejected records issues whose row the RejectStaleUpserts guard
	// kept (stored row strictly newer than the incoming one). Rejected issues
	// also skip label/comment/dependency persistence, so callers can count
	// them as skipped rather than created. May fire more than once per issue
	// if the enclosing transaction retries; callers should dedup by ID.
	OnStaleRejected func(issueID string)
}
