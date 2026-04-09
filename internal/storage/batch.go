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
	// CheckoutID is the deterministic identifier for this checkout, used to
	// look up the namespaced checkout_suffix.<id> config key.
	CheckoutID string
}
