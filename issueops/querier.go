package issueops

import (
	"context"
)

// QueryRequest describes one boolean-expression query: the question `bd query`
// asks, plus the page it wants back.
//
// THE EXPRESSION IS THE PREDICATE, and that is what makes this request unlike
// every other one here. CountRequest, ListRequest and ReadyRequest spell their
// predicate as FIELDS — one per filter, ANDed together — and a caller composes
// them by setting more of them. This request carries a SENTENCE, in the query
// mini-language `bd query` publishes, and that language has OR, NOT and
// parentheses, which no conjunction of fields can express. Parsing, evaluation
// and the choice of how to execute the result all happen inside; a caller says
// what it wants matched and never how the match is shaped.
type QueryRequest struct {
	// Expression is the query, in the language `bd query --help` documents:
	// field comparisons (`status=open`, `priority>1`, `created>7d`) combined
	// with AND, OR, NOT and parentheses.
	//
	// It is parsed INSIDE. An expression that is blank, unparseable, or that
	// names a field or operator the language does not have is ErrValidation
	// naming the fault, never an empty page: a caller that misspelled a field
	// and got zero rows back has no way to tell that from a store with nothing
	// in it.
	Expression string

	// IncludeClosed admits closed issues, which the query otherwise hides.
	//
	// THE HIDING IS CONDITIONAL, and the condition is part of this contract
	// because both front doors have always applied it: closed rows are
	// excluded UNLESS this is set, or unless the expression itself compares
	// `status`. So `status=closed` answers with closed rows on its own, and
	// `NOT status=open` does too — an expression that has an opinion about
	// status keeps it, and only an expression with none gets the default. The
	// flag `bd query --all` sets is this one.
	IncludeClosed bool

	// SortBy names the display order and Reverse inverts it, over the same
	// vocabulary ListRequest.SortBy takes. Empty leaves the rows in the order
	// the query returned them.
	//
	// WHAT THE ORDER IS APPLIED TO depends on what bounded the query, and the
	// difference is worth stating because it is observable. The order is
	// applied to the rows the QUERY bounded, then the page is cut from them —
	// the epilogue `bd list` runs (workapi.FinishPage). For a
	// filter-expressible query the database applied Limit, so the order sorts
	// the page. For a PREDICATE query nothing bounded the query — see
	// Querier.Query — so the order sorts the whole matching set and the page
	// is its head. `bd list` has the same split for the same reason: a sort
	// SQL cannot express fetches everything and trims afterwards.
	SortBy  string
	Reverse bool

	// Limit bounds the page the caller RECEIVES. Nil means the shared query
	// default; 0 means unlimited. It is a pointer so that "unset" and
	// "explicitly unlimited" stay distinguishable, which is what lets one
	// constant serve both surfaces. A negative Limit is ErrValidation rather
	// than a synonym for unlimited: two spellings of "no bound" is one more
	// than a caller can check for.
	Limit *int

	// Offset skips the first N MATCHING rows. It is honored by the
	// unit-of-work implementation and REFUSED by the store-backed one with a
	// typed *ErrUnsupported naming the operation and the backend, exactly as
	// ListRequest.Offset is; what neither does is silently return an unpaged
	// answer. A negative Offset is ErrValidation everywhere.
	//
	// THE STORE-BACKED REFUSAL IS UNIFORM, not per-expression, and that is a
	// decision rather than an oversight. That body could in principle skip
	// rows for a PREDICATE query, where the skipping happens in Go and no SQL
	// OFFSET is involved — but which shape an expression takes is decided by
	// the evaluator, not by the caller, so an Offset that worked for
	// `type=bug OR type=task` and refused `type=bug` would be a refusal a
	// caller could not predict. One answer per backend is the weaker promise
	// and the checkable one.
	//
	// IT SKIPS MATCHES, wherever it is honored — never candidate rows. A skip
	// applied before the predicate would discard rows the predicate would have
	// rejected anyway and hand back a short page, which is the failure that
	// made `bd query --offset` refuse OR queries outright.
	//
	// AN OFFSET WITH A SortBy IS ErrValidation, at every backend. The order is
	// applied to the rows the query BOUNDED (see SortBy), so under a page
	// bound each page is sorted for itself: a walk with an offset would neither
	// visit every row nor visit any row once. Refusing the combination is the
	// only answer that is true on both shapes of query; accepting it would mean
	// publishing a walk that silently is not one.
	Offset int
}

// Querier answers boolean-expression queries: the operation `bd query`
// performs, and — like Counter, Reader and ReadyCounter — a role with its own
// accessor. A new capability gets a new role interface and its own accessor;
// never append a method here.
//
// IT IS ITS OWN ROLE RATHER THAN A SHAPE OF Reader.List because the two take
// predicates that are not the same KIND of thing. A ListRequest is a
// conjunction: every field it carries narrows the answer, and there is no
// arrangement of them that expresses "type=bug OR label=urgent", let alone
// "NOT (priority<2 AND assignee=none)". Handing this expression to List would
// mean either adding a free-text member to ListRequest — a field the other
// eleven callers must ignore, on the request type this library's largest
// surface already shares — or teaching List a second execution mode. The
// question is different, so the role is.
//
// EQUALLY, IT IS NOT A RAW QUERY PASSTHROUGH. The language has no SQL in it:
// no table names, no joins, no ORDER BY, no way to name a column the
// vocabulary does not publish. It is a closed set of fields over one row
// shape, which is why a role can promise anything about it at all.
type Querier interface {
	// Query returns the page of issues the expression matches.
	//
	// THE PREDICATE IS EVALUATED AGAINST THE WHOLE CANDIDATE SET. That
	// sentence is the point of this method and the fix it shipped with.
	//
	// An expression the storage filter vocabulary can express — a comparison,
	// a chain of ANDs, a NOT over status or type, an OR over labels — is
	// executed by the database, which applies Limit itself. Every other
	// expression is a PREDICATE query: the database is asked for the base
	// filters the expression implies and the predicate is applied to the rows
	// in Go. For those, the query is issued UNBOUNDED and the predicate sees
	// every candidate row, so the page is the first Limit MATCHES of the
	// complete matching set.
	//
	// It was not always. Both front doors used to bound that query at
	// max(3*Limit, 100) rows and filter what came back, so a match beyond that
	// window was absent from the page AND unreported by has-more: an OR query
	// over a workspace of any size returned an arbitrary prefix of its answer
	// and called it complete. The window is gone. What it cost is stated
	// rather than hidden: a predicate query reads every row its base filters
	// admit, so a broad expression over a large workspace is a large read.
	//
	// HasMore is true exactly when the page is shorter than the matching set,
	// which for a predicate query it can now compute rather than guess.
	//
	// An expression that matches nothing is an empty page and a nil error —
	// Items is never nil on a successful call. There is no ErrNotFound here:
	// a question about a set has an answer even when the set is empty.
	//
	// Querying is a READ. Nothing here records a history entry, fires a
	// completion hook or changes a row, and a refusal changes nothing either.
	// Deterministic request-validation failures match ErrValidation, an
	// Offset a backend cannot serve is *ErrUnsupported, and result values are
	// unspecified when error is non-nil. Implementations never mutate
	// caller-owned request values.
	Query(ctx context.Context, req QueryRequest) (IssuePage, error)
}
