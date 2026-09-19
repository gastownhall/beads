package storage

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The DATABASE-FREE half of issueops.BatchApplier: what a batch request MEANS,
// which refs it can resolve, which guards it may carry, and how a waits-for
// edge's gate metadata is spelled. Every implementation of the role runs this,
// so those rules have ONE definition rather than one per backend — and they are
// pinned by unit tests in milliseconds, where a contract case would need a
// database to observe the same thing.
//
// IT IS HERE BECAUSE BOTH BODIES NEED IT, and they sit in different packages:
// the store-backed one in internal/storage/issueops and the unit-of-work one in
// internal/storage/uow, each of which already imports this package. This is
// NOT PlanCompareAndSetKey's reason — that plan type is here because
// internal/storage/domain has to name it in a repository signature, and nothing
// in domain names this one, because this role's unit-of-work leg has its own
// body and reaches domain through ordinary use cases. It is here beside
// PlanCompareAndSetKey, ValidateMetadataKey and the rest of this plane's
// meaning because that is where a reader looks for it.
//
// What is NOT here is anything that needs a row. Whether a key's create item
// lands on the durable or the ephemeral plane, whether an id exists, whether an
// edge closes a cycle — all of that belongs to the bodies, and each of the two
// runs it against its own transaction.

// ApplyBatchPlan is a validated apply-batch request whose waits-for edges carry
// normalized gate metadata. It is what an implementation works from, so no
// backend re-derives the ref rules and the caller's request is never written
// through.
//
// It shares the request's *issueops.Issue pointers rather than deep-copying
// them. The body clones each one before writing — every single-verb Execute…
// already snapshots its own request at entry — so the ID an implementation
// assigns still lands on the result and not on the caller's issue.
type ApplyBatchPlan struct {
	// Actor, Provenance, ForceIDPrefix and SkipPerEdgeCycleCheck are the
	// request's, checked.
	Actor                 string
	Provenance            string
	ForceIDPrefix         bool
	SkipPerEdgeCycleCheck bool
	// Items are the request's items in declaration order, with each waits-for
	// dep_add's Metadata replaced by its normalized form. The slice is fresh,
	// so normalization does not write through to the caller's.
	Items []issueops.ApplyItem
	// KeyIndex maps each create item's Key to the index that declares it. The
	// body resolves refs through it and the wire half reports an unknown key
	// from it, so the two cannot disagree about what a key means.
	KeyIndex map[string]int
}

// PlanApplyBatch validates an apply-batch request and normalizes its waits-for
// gate metadata. It is the whole of the role's request validation: every
// implementation calls it before touching a substrate, so a refused request
// costs no database work anywhere.
//
// THE ORDER OF THE CHECKS IS PART OF THE CONTRACT, because a request can be
// wrong in several ways at once and a caller fixing them one at a time needs
// the same answer every time. Request-level shape first, then per-item shape,
// then the ref graph, then the guards.
func PlanApplyBatch(in issueops.ApplyBatchRequest) (ApplyBatchPlan, error) {
	if in.Actor == "" {
		return ApplyBatchPlan{}, fmt.Errorf("%w: apply batch requires an actor", issueops.ErrValidation)
	}
	if len(in.Items) == 0 {
		return ApplyBatchPlan{}, fmt.Errorf("%w: apply batch requires at least one item", issueops.ErrValidation)
	}
	if len(in.Items) > issueops.MaxApplyBatchItems {
		return ApplyBatchPlan{}, fmt.Errorf("%w: apply batch accepts at most %d items, got %d",
			issueops.ErrValidation, issueops.MaxApplyBatchItems, len(in.Items))
	}

	keyIndex, err := planApplyBatchKeys(in.Items)
	if err != nil {
		return ApplyBatchPlan{}, err
	}

	plan := ApplyBatchPlan{
		Actor:                 in.Actor,
		Provenance:            in.Provenance,
		ForceIDPrefix:         in.ForceIDPrefix,
		SkipPerEdgeCycleCheck: in.SkipPerEdgeCycleCheck,
		Items:                 make([]issueops.ApplyItem, len(in.Items)),
		KeyIndex:              keyIndex,
	}
	copy(plan.Items, in.Items)

	// touched records every row an earlier item MUTATED, addressed the way the
	// request addresses it: by key for a row this request creates, by id for one
	// it names. It is what makes the ExpectedVersion rule statically checkable.
	touched := map[string]bool{}
	for i := range plan.Items {
		if err := planApplyBatchItem(&plan.Items[i], i, keyIndex, touched); err != nil {
			return ApplyBatchPlan{}, err
		}
	}
	return plan, nil
}

// planApplyBatchKeys checks every item's kind/payload agreement and collects
// the create keys. It runs as its own pass because a ref may name a key
// declared by a LATER item, which is a different diagnosis from a key nothing
// declares — and telling them apart means knowing every key before checking any
// ref.
func planApplyBatchKeys(items []issueops.ApplyItem) (map[string]int, error) {
	keyIndex := make(map[string]int, len(items))
	for i, item := range items {
		payloads := 0
		for _, present := range []bool{item.Create != nil, item.Update != nil, item.Close != nil, item.DepAdd != nil} {
			if present {
				payloads++
			}
		}
		if payloads != 1 {
			return nil, fmt.Errorf("%w: apply batch item %d must carry exactly one payload, got %d",
				issueops.ErrValidation, i, payloads)
		}
		var matches bool
		switch item.Kind {
		case issueops.ItemCreate:
			matches = item.Create != nil
		case issueops.ItemUpdate:
			matches = item.Update != nil
		case issueops.ItemClose:
			matches = item.Close != nil
		case issueops.ItemDepAdd:
			matches = item.DepAdd != nil
		default:
			return nil, fmt.Errorf("%w: apply batch item %d has unknown kind %q", issueops.ErrValidation, i, item.Kind)
		}
		if !matches {
			return nil, fmt.Errorf("%w: apply batch item %d is kind %q but carries another kind's payload",
				issueops.ErrValidation, i, item.Kind)
		}
		if item.Create == nil {
			continue
		}
		if item.Create.Issue == nil {
			return nil, fmt.Errorf("%w: apply batch item %d requires an issue", issueops.ErrValidation, i)
		}
		if len(item.Create.Issue.Comments) > 0 || len(item.Create.Issue.Dependencies) > 0 {
			return nil, fmt.Errorf("%w: apply batch item %d must not carry comments or dependencies on the issue; edges are their own items",
				issueops.ErrValidation, i)
		}
		if item.Create.Key == "" {
			continue
		}
		if prior, dup := keyIndex[item.Create.Key]; dup {
			return nil, fmt.Errorf("%w: apply batch item %d reuses key %q, already declared by item %d",
				issueops.ErrValidation, i, item.Create.Key, prior)
		}
		keyIndex[item.Create.Key] = i
	}
	return keyIndex, nil
}

// planApplyBatchItem validates one item's refs, guards and edge metadata, and
// records what it touches for the items after it.
func planApplyBatchItem(item *issueops.ApplyItem, index int, keyIndex map[string]int, touched map[string]bool) error {
	switch item.Kind {
	case issueops.ItemCreate:
		return planApplyBatchCreate(item.Create, index, keyIndex, touched)
	case issueops.ItemUpdate:
		return planApplyBatchUpdate(item.Update, index, keyIndex, touched)
	case issueops.ItemClose:
		return planApplyBatchClose(item.Close, index, keyIndex, touched)
	case issueops.ItemDepAdd:
		// The edge is COPIED before it is normalized. plan.Items is a fresh
		// slice, but its payload members are the caller's pointers, so writing
		// the normalized gate metadata in place would edit the request the
		// caller still owns — the one mutation this plan can make, and the one
		// the role promises never happens.
		edge := *item.DepAdd
		if err := planApplyBatchDepAdd(&edge, index, keyIndex); err != nil {
			return err
		}
		item.DepAdd = &edge
		return nil
	}
	return fmt.Errorf("%w: apply batch item %d has unknown kind %q", issueops.ErrValidation, index, item.Kind)
}

// planApplyBatchCreate checks a create's metadata refs and records the row it
// mints as touched.
//
// A METADATA REF MAY NAME ANY KEY, including this item's own and one declared
// later. Every id is minted before any splice runs, so the direction cannot
// matter — see issueops.CreateItem.MetadataRefs. What it may not do is name a
// key no item declares.
func planApplyBatchCreate(item *issueops.CreateItem, index int, keyIndex map[string]int, touched map[string]bool) error {
	for metaKey, ref := range item.MetadataRefs {
		if metaKey == "" {
			return fmt.Errorf("%w: apply batch item %d has a metadata_ref with an empty key", issueops.ErrValidation, index)
		}
		if err := validateApplyRef(ref, index, "metadata_ref "+metaKey); err != nil {
			return err
		}
		if ref.Key == "" {
			continue
		}
		if _, ok := keyIndex[ref.Key]; !ok {
			return &issueops.RefError{Index: index, Member: "metadata_ref " + metaKey, Key: ref.Key}
		}
	}
	if item.Key != "" {
		touched[applyTouchKeyRef(issueops.Ref{Key: item.Key})] = true
	}
	if item.Issue != nil && item.Issue.ID != "" {
		touched[applyTouchKeyRef(issueops.Ref{ID: item.Issue.ID})] = true
	}
	return nil
}

// planApplyBatchUpdate checks an update's target and guards.
func planApplyBatchUpdate(item *issueops.UpdateItem, index int, keyIndex map[string]int, touched map[string]bool) error {
	if err := validateApplyTargetRef(item.Target, index, "target", keyIndex); err != nil {
		return err
	}
	if err := checkApplyExpectedVersion(item.ExpectedVersion, item.Target, index, touched); err != nil {
		return err
	}
	touched[applyTouchKeyRef(item.Target)] = true
	return nil
}

// planApplyBatchClose checks a close's target and guard.
func planApplyBatchClose(item *issueops.CloseItem, index int, keyIndex map[string]int, touched map[string]bool) error {
	if err := validateApplyTargetRef(item.Target, index, "target", keyIndex); err != nil {
		return err
	}
	if err := checkApplyExpectedVersion(item.ExpectedVersion, item.Target, index, touched); err != nil {
		return err
	}
	touched[applyTouchKeyRef(item.Target)] = true
	return nil
}

// planApplyBatchDepAdd checks an edge's endpoints and type and normalizes its
// gate metadata.
//
// IT RECORDS NOTHING AS TOUCHED, and that is a decision rather than an
// oversight. The ExpectedVersion rule above refuses a guard on a row this
// request has already REWRITTEN, and an edge write is a change to the graph
// rather than to either endpoint's row: the role promises nothing about whether
// it moves the source's version token. A later guard on that source is
// therefore left to the substrate, where a genuine mismatch is
// ErrVersionMismatch — an honest refusal — rather than being refused up front
// as a request a caller could not have composed.
func planApplyBatchDepAdd(item *issueops.DepAddItem, index int, keyIndex map[string]int) error {
	if err := validateApplyTargetRef(item.Source, index, "source", keyIndex); err != nil {
		return err
	}
	if err := validateApplyTargetRef(item.Target, index, "target", keyIndex); err != nil {
		return err
	}
	if item.Source == item.Target {
		return fmt.Errorf("%w: apply batch item %d: %s cannot depend on itself",
			issueops.ErrSelfDependency, index, applyRefLabel(item.Source))
	}
	if !item.Type.IsValid() {
		return fmt.Errorf("%w: apply batch item %d requires a dependency type (max %d chars)",
			issueops.ErrValidation, index, types.MaxDependencyTypeLen)
	}
	metadata, err := normalizeApplyEdgeMetadata(item.Type, item.Metadata)
	if err != nil {
		return fmt.Errorf("%w: apply batch item %d: %v", issueops.ErrValidation, index, err)
	}
	item.Metadata = metadata
	return nil
}

// checkApplyExpectedVersion refuses a version guard on a row an earlier item of
// this request already mutated.
//
// IT IS A REQUEST-SHAPE RULE, not a race. The token is server-minted and
// rewritten by the write, so mid-request there is no value a caller COULD send:
// the pre-request token is stale by construction, and a row this request just
// created never had one the caller could read. Refusing statically says so;
// letting it through would answer every such request with ErrVersionMismatch
// and leave the caller looking for a concurrent writer that does not exist.
//
// ExpectedStatus and ExpectedAssignee carry no such rule, and the difference is
// that a caller CAN know what its own earlier item set them to.
func checkApplyExpectedVersion(expected *int64, target issueops.Ref, index int, touched map[string]bool) error {
	if expected == nil || !touched[applyTouchKeyRef(target)] {
		return nil
	}
	return fmt.Errorf("%w: apply batch item %d guards on a row version, but an earlier item in this request already wrote %s; "+
		"the version token is minted by the write and cannot be known mid-request",
		issueops.ErrValidation, index, applyRefLabel(target))
}

// validateApplyTargetRef checks a ref used to ADDRESS a row: exactly one member
// set, and a key that reaches BACKWARD.
func validateApplyTargetRef(ref issueops.Ref, index int, member string, keyIndex map[string]int) error {
	if err := validateApplyRef(ref, index, member); err != nil {
		return err
	}
	if ref.Key == "" {
		return nil
	}
	declaredAt, ok := keyIndex[ref.Key]
	if !ok {
		return &issueops.RefError{Index: index, Member: member, Key: ref.Key}
	}
	if declaredAt >= index {
		return &issueops.RefError{Index: index, Member: member, Key: ref.Key, DeclaredLater: true}
	}
	return nil
}

// validateApplyRef checks the exactly-one rule every ref answers to.
func validateApplyRef(ref issueops.Ref, index int, member string) error {
	switch {
	case ref.Key == "" && ref.ID == "":
		return fmt.Errorf("%w: apply batch item %d: %s must name a key or an id", issueops.ErrValidation, index, member)
	case ref.Key != "" && ref.ID != "":
		return fmt.Errorf("%w: apply batch item %d: %s names both key %q and id %q; exactly one",
			issueops.ErrValidation, index, member, ref.Key, ref.ID)
	}
	return nil
}

// applyTouchKeyRef renders a ref as the address the touched set is keyed by.
// The two namespaces are kept apart so a key and an id that happen to read the
// same string are not confused for one row.
func applyTouchKeyRef(ref issueops.Ref) string {
	if ref.Key != "" {
		return "key:" + ref.Key
	}
	return "id:" + ref.ID
}

// applyRefLabel renders a ref for a message.
func applyRefLabel(ref issueops.Ref) string {
	if ref.Key != "" {
		return fmt.Sprintf("key %q", ref.Key)
	}
	return ref.ID
}

// normalizeApplyEdgeMetadata returns the metadata an edge is STORED with.
//
// A WAITS-FOR ROW MUST BE SELF-DESCRIBING. Readers that predate the gate's
// introduction do not default a missing one, so an absent, blank or `{}`
// metadata is written as {"gate":"all-children"} — the rule
// types.NewGraphEdgeDependency already applies on every other path that writes
// one, reached here so this role cannot drift from it. A metadata that names a
// gate keeps it, along with the spawner and also-blocks members a caller may
// carry; a gate that is neither known value is refused.
//
// Every other edge type's metadata is passed through unchanged, checked only
// for being well-formed JSON when it is present at all: the blob is
// type-specific and this role does not know the types.
func normalizeApplyEdgeMetadata(depType types.DependencyType, metadata string) (string, error) {
	trimmed := strings.TrimSpace(metadata)
	if depType != types.DepWaitsFor {
		if trimmed == "" {
			return "", nil
		}
		if !json.Valid([]byte(trimmed)) {
			return "", fmt.Errorf("edge metadata is not well-formed JSON")
		}
		return metadata, nil
	}
	meta := types.WaitsForMeta{}
	if trimmed != "" && trimmed != "{}" {
		if err := json.Unmarshal([]byte(trimmed), &meta); err != nil {
			return "", fmt.Errorf("waits-for metadata is not a well-formed gate object: %w", err)
		}
	}
	if meta.Gate == "" {
		meta.Gate = types.WaitsForAllChildren
	}
	if !types.IsValidWaitsForGate(meta.Gate) {
		return "", fmt.Errorf("waits-for gate %q is neither %q nor %q",
			meta.Gate, types.WaitsForAllChildren, types.WaitsForAnyChildren)
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("serializing waits-for metadata: %w", err)
	}
	return string(raw), nil
}
