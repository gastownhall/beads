package issueops

import "context"

// ImportBatchRequest is one whole `bd import` invocation's write: every issue
// row that survived the caller's pre-filters, every memory record, and the
// optional issue_prefix reconciliation, landed in ONE transaction and ONE
// history entry. The request is the transaction: there is no handle to hold
// open, and a request-level failure rolls the whole import back.
type ImportBatchRequest struct {
	// Actor is recorded as the creator/mutator on every row the batch writes.
	Actor string
	// Issues are the parsed import rows, in file order. Row semantics are the
	// import's standing upsert contract: a new ID inserts, an existing ID is
	// conditionally rewritten (see AllowStale), and labels, comments and
	// dependencies merge idempotently.
	Issues []*Issue
	// Memories are the memory records, in file order, imported alongside the
	// issues in the same transaction. Keys are full config keys (the caller
	// applies its kv prefixes); a duplicated key writes twice and the later
	// record wins, which is the classic import's sequential behavior.
	Memories []ImportMemory
	// LabelDefinitions are the label vocabulary records ('bd label define'),
	// applied define-if-absent in the same transaction: an exact re-import is
	// a no-op, a case-insensitive collision keeps the existing definition and
	// reports a warning, and only genuine storage failures fail the batch -
	// an import replays history and must not hard-fail on it.
	LabelDefinitions []ImportLabelDefinition
	// AllowStale imports rows even when their updated_at is older than the
	// stored issue's. When false, the engine's conditional upsert rejects the
	// stale row inside the transaction and reports it in StaleRejectedIDs —
	// the in-transaction half of the import's two-half stale guard.
	AllowStale bool
	// SkipPrefixValidation admits rows whose ID prefix differs from the
	// workspace's configured issue prefix, which is what an explicit
	// `bd import` has always done.
	SkipPrefixValidation bool
	// SyncIssuePrefix, when non-empty, is the caller's authoritative issue
	// prefix (config.yaml); if it differs from the stored issue_prefix the
	// batch rewrites the stored value in the same transaction. Existing issue
	// IDs are intentionally left unchanged: this is the import/migration
	// reconciliation, not a rename.
	SyncIssuePrefix string
	// Source names where the rows came from (a file basename or "stdin") and
	// appears in the history entry's message.
	Source string
}

// ImportMemory is one memory record ('bd remember') carried by an import.
type ImportMemory struct {
	Key   string
	Value string
}

// ImportLabelDefinition is one label vocabulary record ('bd label define')
// carried by an import. Actor, when set, is recorded as the definer
// (typically the exporting workspace's created_by); empty falls back to the
// batch actor.
type ImportLabelDefinition struct {
	Label       string
	Description string
	Actor       string
}

// SkippedDependency reports one dependency edge the import dropped rather
// than failing the batch: its target is missing, invalid, or would form a
// disallowed edge.
type SkippedDependency struct {
	IssueID     string
	DependsOnID string
	Reason      string
}

// ImportBatchResult reports what one import batch landed.
type ImportBatchResult struct {
	// Created counts the rows the batch wrote — inserts and rewrites alike —
	// excluding stale-rejected rows.
	Created int
	// MemoriesImported counts the memory records written.
	MemoriesImported int
	// LabelDefinitionsImported counts the vocabulary records actually
	// defined - exact re-imports and case-collisions are not counted.
	LabelDefinitionsImported int
	// LabelDefinitionWarnings reports the vocabulary records kept out -
	// case-insensitive collisions with the existing registry, or definitions
	// refused by a concurrent define. Never a batch failure.
	LabelDefinitionWarnings []string
	// StaleRejectedIDs lists rows the in-transaction stale guard rejected: a
	// locally-newer row kept every stored column. Deduplicated by ID.
	StaleRejectedIDs []string
	// SkippedDependencies lists the edges dropped by the batch, deduplicated.
	SkippedDependencies []SkippedDependency
	// PrefixSynced reports that the stored issue_prefix was rewritten to
	// SyncIssuePrefix.
	PrefixSynced bool
}

// Importer lands one whole import batch in one transaction with one history
// entry. It is the sanctioned bulk-upsert door: Create refuses an occupied
// ID, and this role is where re-importing an exported snapshot converges
// instead of colliding.
type Importer interface {
	ImportBatch(ctx context.Context, request ImportBatchRequest) (ImportBatchResult, error)
}
