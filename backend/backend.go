// Package backend is the public surface for OUT-OF-TREE storage backends: it
// exports, by type alias, the storage contract an external Go module must
// implement (the engine interface storage.DoltStorage and every type its
// method signatures reach), the process-init registry that plugs an
// implementation into bd, and — via the backend/conformance subpackage — the
// tests that prove an implementation behaves like the Dolt reference.
//
// Every name here is an alias or thin wrapper over the in-tree definition, so
// an external implementation and the in-tree consumers see IDENTICAL types:
// a store whose methods are written against these aliases satisfies
// storage.DoltStorage, and sentinel errors re-exported here match errors.Is
// across the module boundary. The internal packages stay the single source of
// truth; this package adds no behavior.
//
// A minimal external backend looks like:
//
//	package mybackend
//
//	import "github.com/steveyegge/beads/backend"
//
//	type Store struct{ ... }   // implements backend.DoltStorage
//
//	func init() {
//	    backend.Register("mybackend", backend.Backend{
//	        Open:         open,
//	        OpenReadOnly: openReadOnly,
//	    })
//	}
//
// and proves itself with the conformance package:
//
//	func TestConformance(t *testing.T) {
//	    // What the backend declares it does not do.
//	    conformance.RunUnsupportedContract(t, &Store{}, unsupported)
//	    // What it does: the portable raw surface.
//	    conformance.RunAll(t, func(t *testing.T) backend.DoltStorage {
//	        return newTestStore(t)
//	    })
//	}
//
// The two halves are not interchangeable. RunAll covers the portable raw
// surface — roughly half of core Storage plus five capability sub-interfaces —
// and calls NO method of VersionControl, HistoryViewer, RemoteStore, SyncStore,
// FederationStore, CompactionStore or FastStatisticsStore. Those seven are
// proved only by the allowlist: a backend that answers them with a typed
// ErrUnsupported and runs RunUnsupportedContract has satisfied everything the
// suite asks of them. The conformance package doc measures the split, and the
// role contracts in that package are the third piece, covering the issueops
// roles the accessors on Storage return.
//
// Registration is init-time wiring only; see Register. bd init / bd bootstrap
// do not provision registered backends — an external backend supplies its own
// workspace-creation path and is opened by name via metadata.json.
//
// # Stability
//
// EXPERIMENTAL. This surface is exported so external backends can be built
// and conformance-gated while the extension door stabilizes; the contract
// itself still evolves. Adding a required method to the engine interface is a
// breaking change for out-of-tree implementations — such additions are called
// out in CHANGELOG.md — and no compatibility promise beyond that is made yet.
// Pin an exact beads version and re-run the conformance suite on every bump.
package backend

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
)

// DoltStorage is the full engine interface an external backend implements:
// the core Storage interface plus every capability sub-interface. The name is
// historical — the Dolt stores were its first implementations — but the
// contract is backend-agnostic.
//
// Implementing it is not the same as being exercised by the conformance suite,
// which reaches roughly half of it; the rest is declared through the unsupported
// allowlist. See the conformance package doc for which half is which.
type DoltStorage = storage.DoltStorage

// The engine interface decomposes into the core Storage interface plus these
// capability sub-interfaces; each is aliased so an implementation can be
// organized (and documented) per capability.
type (
	Storage              = storage.Storage
	IssueLifecycleStore  = storage.IssueLifecycleStore
	VersionControl       = storage.VersionControl
	HistoryViewer        = storage.HistoryViewer
	RemoteStore          = storage.RemoteStore
	SyncStore            = storage.SyncStore
	FederationStore      = storage.FederationStore
	BulkIssueStore       = storage.BulkIssueStore
	DependencyQueryStore = storage.DependencyQueryStore
	EventQueryStore      = storage.EventQueryStore
	AnnotationStore      = storage.AnnotationStore
	ConfigMetadataStore  = storage.ConfigMetadataStore
	CompactionStore      = storage.CompactionStore
	AdvancedQueryStore   = storage.AdvancedQueryStore
	FastStatisticsStore  = storage.FastStatisticsStore
)

// Transaction types: Storage.RunInTransaction yields a Transaction;
// IssueLifecycleStore's guarded mutations run inside an
// IssueLifecycleTransaction.
type (
	Transaction               = storage.Transaction
	IssueLifecycleTransaction = storage.IssueLifecycleTransaction
)

// Value types reached by the engine interface's method signatures.
type (
	BatchCreateOptions      = storage.BatchCreateOptions
	CloseIssueOptions       = storage.CloseIssueOptions
	CloseIssueResult        = storage.CloseIssueResult
	CommentPageCursor       = storage.CommentPageCursor
	CommitInfo              = storage.CommitInfo
	Conflict                = storage.Conflict
	DependencyAddOptions    = storage.DependencyAddOptions
	DependencyRemoveOptions = storage.DependencyRemoveOptions
	DiffEntry               = storage.DiffEntry
	EventCursor             = storage.EventCursor
	FederationPeer          = storage.FederationPeer
	HistoryEntry            = storage.HistoryEntry
	MergeSlotResult         = storage.MergeSlotResult
	MergeSlotStatus         = storage.MergeSlotStatus
	OrphanHandling          = storage.OrphanHandling
	RemoteInfo              = storage.RemoteInfo
	StatusEntry             = storage.StatusEntry
	SyncResult              = storage.SyncResult
	SyncStatus              = storage.SyncStatus
	UpdateIssueOptions      = storage.UpdateIssueOptions
)

// VCStatus is the version-control status report (storage.Status). Named
// VCStatus here — matching the root beads package — because Status is the
// issue-status enum from the domain types.
type VCStatus = storage.Status

// OrphanHandling values for BatchCreateOptions.
const (
	OrphanAllow     = storage.OrphanAllow
	OrphanResurrect = storage.OrphanResurrect
	OrphanSkip      = storage.OrphanSkip
	OrphanStrict    = storage.OrphanStrict
)

// Backend describes a registered storage backend: how to open its workspace
// store. See the field docs on the aliased struct.
type Backend = backends.Backend

// Register adds a backend under name. It is process-start wiring: call it
// once during initialization (typically from a registrant package's init that
// the embedding binary blank-imports), before any concurrent store access.
// Invalid or duplicate registrations panic; the name "dolt" is reserved.
func Register(name string, backend Backend) {
	backends.Register(name, backend)
}

// Deregister removes a backend. It exists for isolated single-threaded
// contract tests; production registrants never deregister.
func Deregister(name string) bool {
	return backends.Deregister(name)
}

// Lookup returns the backend registered under name.
func Lookup(name string) (Backend, bool) {
	return backends.Lookup(name)
}

// Registered reports whether name has a backend implementation.
func Registered(name string) bool {
	return backends.Registered(name)
}

// WorkspaceIsBeadsDir reports whether name is a registered backend whose
// workspace is identified by the .beads directory alone (metadata.json plus a
// remote store), with no separately discoverable local database.
func WorkspaceIsBeadsDir(name string) bool {
	return backends.WorkspaceIsBeadsDir(name)
}

// Iter is the streaming-read cursor returned by the Iter* methods.
type Iter[T any] = storage.Iter[T]

// SliceIter adapts an in-memory slice to Iter — the simplest way for a
// backend to satisfy the Iter* methods before it streams natively.
type SliceIter[T any] = storage.SliceIter[T]

// NewSliceIter returns an Iter over items.
func NewSliceIter[T any](items []*T) *SliceIter[T] {
	return storage.NewSliceIter(items)
}

// ForEach drains it, invoking fn per element, and closes it.
func ForEach[T any](ctx context.Context, it Iter[T], fn func(*T) error) error {
	return storage.ForEach(ctx, it, fn)
}

// Collect drains it into a slice and closes it.
func Collect[T any](ctx context.Context, it Iter[T]) ([]*T, error) {
	return storage.Collect(ctx, it)
}
