package graphops

import "context"

// The Type roles. DescriptorReader is the ordered catalog and a keyed lookup —
// a different question from Reader (the catalog is the Scope's contract
// inventory, not its content) and PROTECTED like it: every call asserts the
// witness inside its transaction, and the catalog is bounded by the store's
// catalog limit. TypeInstaller is the post-mint install: a fenced mutation
// under the exclusive gate, with an install event per descriptor. Before mint
// there is no install — the built-in catalog is installed by
// ScopeBootstrapper.Mint — and a caller entitled to read the catalog is not
// thereby entitled to change it, which is why these are two roles.

// DescriptorRequest names one descriptor by Type URL.
type DescriptorRequest struct {
	// URL is the canonical Type ID. A non-canonical spelling is
	// ErrValidation.
	URL string
}

// InstallRequest carries the descriptors to install or converge.
type InstallRequest struct {
	// Descriptors are the constructed descriptors, each already valid under
	// the closed-shape laws. Order is not significant.
	Descriptors []TypeDescriptor
}

// InstallResult reports an install by Type URL.
//
// DECISION: the shape. B2 names the type without members; an installer that
// is idempotent by fingerprint has exactly two outcomes per descriptor — it
// landed, or a byte-identical contract was already there — and both are
// reported so a caller can prove idempotence without a second read. A
// descriptor whose URL is installed under a DIFFERENT fingerprint is refused
// (the pinned contract closure at an ID is immutable), so it is an error, not
// a third list.
type InstallResult struct {
	// Installed lists the Type URLs whose descriptors were written, with an
	// install event each, in code-unit order.
	Installed []string
	// Unchanged lists the Type URLs already installed at the same
	// fingerprint, in code-unit order. No event is appended for them.
	Unchanged []string
}

// DescriptorReader is the catalog question.
type DescriptorReader interface {
	// Descriptors returns every installed descriptor in ascending code-unit
	// order of Type URL. The catalog is bounded; a Scope whose catalog
	// exceeds the store's bound is a configuration error the store reports,
	// never a truncated answer.
	Descriptors(ctx context.Context) ([]TypeDescriptor, error)
	// Descriptor returns one installed descriptor by Type URL, or
	// ErrNotFound.
	Descriptor(ctx context.Context, req DescriptorRequest) (TypeDescriptor, error)
}

// TypeInstaller is the catalog mutation.
type TypeInstaller interface {
	// Install writes each descriptor that is not already installed at the
	// same fingerprint, appends an install event per write, and publishes
	// the mutation as every replicated mutation is published. A descriptor
	// already installed at another fingerprint under the same URL refuses
	// the whole request with ErrValidation; nothing is written. Requires a
	// minted Scope (ErrNoScope otherwise) and this workspace's authority.
	Install(ctx context.Context, req InstallRequest) (InstallResult, error)
}
