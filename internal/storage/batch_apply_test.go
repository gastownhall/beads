package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These pin the DATABASE-FREE half of issueops.BatchApplier: what a request
// means, which refs it can resolve, which guards it may carry, and how a
// waits-for gate is spelled. Every one of them is a promise the leaf makes and
// a conformance case would otherwise need three live backends to observe — so
// they run here, in milliseconds, and the contract is left to assert what only
// a real backend can show.

// planApplyItem builds one item of the given kind with a minimal valid payload.
func planApplyCreate(key string) issueops.ApplyItem {
	return issueops.ApplyItem{
		Kind:   issueops.ItemCreate,
		Create: &issueops.CreateItem{Key: key, Issue: &issueops.Issue{Title: "t"}},
	}
}

func planApplyUpdate(target issueops.Ref, expected *int64) issueops.ApplyItem {
	return issueops.ApplyItem{
		Kind:   issueops.ItemUpdate,
		Update: &issueops.UpdateItem{Target: target, ExpectedVersion: expected},
	}
}

func planApplyClose(target issueops.Ref, expected *int64) issueops.ApplyItem {
	return issueops.ApplyItem{
		Kind:  issueops.ItemClose,
		Close: &issueops.CloseItem{Target: target, ExpectedVersion: expected},
	}
}

func planApplyDep(source, target issueops.Ref, depType types.DependencyType, metadata string) issueops.ApplyItem {
	return issueops.ApplyItem{
		Kind:   issueops.ItemDepAdd,
		DepAdd: &issueops.DepAddItem{Source: source, Target: target, Type: depType, Metadata: metadata},
	}
}

func planApplyRequest(items ...issueops.ApplyItem) issueops.ApplyBatchRequest {
	return issueops.ApplyBatchRequest{Actor: "planner", Items: items}
}

func int64Ptr(v int64) *int64 { return &v }

// TestPlanApplyBatchRefusesAnUnusableRequest pins the request-level shape rules:
// every one of them is knowable from the request alone, so a request refused
// here has provably touched no database.
func TestPlanApplyBatchRefusesAnUnusableRequest(t *testing.T) {
	hundredAndOne := make([]issueops.ApplyItem, 101)
	for i := range hundredAndOne {
		hundredAndOne[i] = planApplyCreate("")
	}

	for _, test := range []struct {
		name    string
		request issueops.ApplyBatchRequest
		want    string
	}{
		{"no actor", issueops.ApplyBatchRequest{Items: []issueops.ApplyItem{planApplyCreate("")}}, "requires an actor"},
		{"no items", issueops.ApplyBatchRequest{Actor: "planner"}, "at least one item"},
		{"too many items", issueops.ApplyBatchRequest{Actor: "planner", Items: hundredAndOne}, "at most 100 items"},
		{"no payload", planApplyRequest(issueops.ApplyItem{Kind: issueops.ItemCreate}), "exactly one payload"},
		{
			"two payloads",
			planApplyRequest(issueops.ApplyItem{
				Kind:   issueops.ItemCreate,
				Create: &issueops.CreateItem{Issue: &issueops.Issue{}},
				Close:  &issueops.CloseItem{Target: issueops.Ref{ID: "bd-1"}},
			}),
			"exactly one payload",
		},
		{
			"kind and payload disagree",
			planApplyRequest(issueops.ApplyItem{
				Kind:  issueops.ItemCreate,
				Close: &issueops.CloseItem{Target: issueops.Ref{ID: "bd-1"}},
			}),
			"carries another kind's payload",
		},
		{
			"unknown kind",
			planApplyRequest(issueops.ApplyItem{
				Kind:   "reopen",
				Create: &issueops.CreateItem{Issue: &issueops.Issue{}},
			}),
			`unknown kind "reopen"`,
		},
		{
			"create with no issue",
			planApplyRequest(issueops.ApplyItem{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{}}),
			"requires an issue",
		},
		{
			"create carrying inline dependencies",
			planApplyRequest(issueops.ApplyItem{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
				Issue: &issueops.Issue{Dependencies: []*types.Dependency{{IssueID: "a", DependsOnID: "b"}}},
			}}),
			"edges are their own items",
		},
		{
			"duplicate create key",
			planApplyRequest(planApplyCreate("root"), planApplyCreate("root")),
			`reuses key "root"`,
		},
		{
			"ref names neither member",
			planApplyRequest(planApplyUpdate(issueops.Ref{}, nil)),
			"must name a key or an id",
		},
		{
			"ref names both members",
			planApplyRequest(planApplyUpdate(issueops.Ref{Key: "k", ID: "bd-1"}, nil)),
			"exactly one",
		},
		{
			"edge with no type",
			planApplyRequest(planApplyDep(issueops.Ref{ID: "bd-1"}, issueops.Ref{ID: "bd-2"}, "", "")),
			"requires a dependency type",
		},
		{
			"edge metadata that is not JSON",
			planApplyRequest(planApplyDep(issueops.Ref{ID: "bd-1"}, issueops.Ref{ID: "bd-2"}, types.DepBlocks, "not json")),
			"not well-formed JSON",
		},
		{
			"metadata_ref with an empty key",
			planApplyRequest(issueops.ApplyItem{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
				Issue:        &issueops.Issue{},
				MetadataRefs: map[string]issueops.Ref{"": {ID: "bd-1"}},
			}}),
			"empty key",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanApplyBatch(test.request)
			if err == nil {
				t.Fatalf("PlanApplyBatch accepted %s", test.name)
			}
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("error = %v, want it to match ErrValidation so every front door classifies it", err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want it to name %q", err, test.want)
			}
		})
	}
}

// TestPlanApplyBatchAcceptsTheBoundary pins the OTHER side of the item cap.
// Without it the cap check could be off by one in the refusing direction and
// nothing above would notice.
func TestPlanApplyBatchAcceptsTheBoundary(t *testing.T) {
	items := make([]issueops.ApplyItem, issueops.MaxApplyBatchItems)
	for i := range items {
		items[i] = planApplyCreate("")
	}
	if _, err := PlanApplyBatch(planApplyRequest(items...)); err != nil {
		t.Fatalf("PlanApplyBatch(%d items) = %v, want the cap to be inclusive", issueops.MaxApplyBatchItems, err)
	}
}

// TestPlanApplyBatchSelfDependencyIsItsOwnRefusal pins that an edge from a row
// to itself is ErrSelfDependency rather than a cycle, the way every other
// dependency path types it — a scheduling self-edge would otherwise trip the
// cycle probe and report the wrong refusal.
func TestPlanApplyBatchSelfDependencyIsItsOwnRefusal(t *testing.T) {
	for _, test := range []struct {
		name string
		ref  issueops.Ref
	}{
		{"by id", issueops.Ref{ID: "bd-1"}},
		{"by key", issueops.Ref{Key: "root"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanApplyBatch(planApplyRequest(
				planApplyCreate("root"),
				planApplyDep(test.ref, test.ref, types.DepBlocks, ""),
			))
			if !errors.Is(err, issueops.ErrSelfDependency) {
				t.Fatalf("error = %v, want ErrSelfDependency", err)
			}
		})
	}
}

// TestPlanApplyBatchResolvesKeysBackwardOnly pins the ref rule the whole item
// vocabulary rests on, and — the part that earns the type — that the TWO
// failures are told apart. A key declared later is an ORDERING mistake a caller
// fixes by moving an item; a key nothing declares is a typo. Collapsing them
// would send a caller hunting for the wrong thing.
func TestPlanApplyBatchResolvesKeysBackwardOnly(t *testing.T) {
	t.Run("backward is accepted", func(t *testing.T) {
		plan, err := PlanApplyBatch(planApplyRequest(
			planApplyCreate("root"),
			planApplyUpdate(issueops.Ref{Key: "root"}, nil),
		))
		if err != nil {
			t.Fatalf("PlanApplyBatch = %v, want a backward key to resolve", err)
		}
		if got := plan.KeyIndex["root"]; got != 0 {
			t.Fatalf("KeyIndex[root] = %d, want 0", got)
		}
	})

	for _, test := range []struct {
		name          string
		items         []issueops.ApplyItem
		member        string
		index         int
		declaredLater bool
	}{
		{
			"forward target",
			[]issueops.ApplyItem{planApplyUpdate(issueops.Ref{Key: "root"}, nil), planApplyCreate("root")},
			"target", 0, true,
		},
		{
			"a target naming its own item's key",
			[]issueops.ApplyItem{
				planApplyCreate("root"),
				planApplyDep(issueops.Ref{Key: "later"}, issueops.Ref{ID: "bd-2"}, types.DepBlocks, ""),
				planApplyCreate("later"),
			},
			"source", 1, true,
		},
		{
			"unknown target",
			[]issueops.ApplyItem{planApplyClose(issueops.Ref{Key: "ghost"}, nil)},
			"target", 0, false,
		},
		{
			"forward edge source",
			[]issueops.ApplyItem{
				planApplyDep(issueops.Ref{Key: "root"}, issueops.Ref{ID: "bd-2"}, types.DepBlocks, ""),
				planApplyCreate("root"),
			},
			"source", 0, true,
		},
		{
			"unknown edge target",
			[]issueops.ApplyItem{
				planApplyCreate("root"),
				planApplyDep(issueops.Ref{Key: "root"}, issueops.Ref{Key: "ghost"}, types.DepBlocks, ""),
			},
			"target", 1, false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanApplyBatch(planApplyRequest(test.items...))
			var refErr *issueops.RefError
			if !errors.As(err, &refErr) {
				t.Fatalf("error = %v, want *issueops.RefError", err)
			}
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatal("a *RefError must match ErrValidation, so a front door classifies it without knowing the type")
			}
			if refErr.Index != test.index || refErr.Member != test.member {
				t.Fatalf("RefError{Index: %d, Member: %q}, want {Index: %d, Member: %q}",
					refErr.Index, refErr.Member, test.index, test.member)
			}
			if refErr.DeclaredLater != test.declaredLater {
				t.Fatalf("RefError.DeclaredLater = %v, want %v: a key declared later and a key nothing declares "+
					"are different diagnoses", refErr.DeclaredLater, test.declaredLater)
			}
		})
	}
}

// TestPlanApplyBatchMetadataRefsReachAnyDirection pins the ONE exception to the
// backward rule, and it is the measured shape rather than a generalization: a
// plan whose first step records the id of a row a later step mints (the retry
// that stamps the original's id onto its replacement). Every id exists before
// any splice runs, so the direction cannot matter for a VALUE.
func TestPlanApplyBatchMetadataRefsReachAnyDirection(t *testing.T) {
	forward := issueops.ApplyItem{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
		Key:          "first",
		Issue:        &issueops.Issue{Title: "first"},
		MetadataRefs: map[string]issueops.Ref{"gc.retry_of": {Key: "second"}, "gc.self": {Key: "first"}},
	}}
	if _, err := PlanApplyBatch(planApplyRequest(forward, planApplyCreate("second"))); err != nil {
		t.Fatalf("PlanApplyBatch = %v, want a forward and a self metadata_ref to resolve", err)
	}

	unknown := issueops.ApplyItem{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
		Issue:        &issueops.Issue{Title: "first"},
		MetadataRefs: map[string]issueops.Ref{"gc.retry_of": {Key: "ghost"}},
	}}
	_, err := PlanApplyBatch(planApplyRequest(unknown))
	var refErr *issueops.RefError
	if !errors.As(err, &refErr) {
		t.Fatalf("error = %v, want *issueops.RefError for a metadata_ref naming no item", err)
	}
	if refErr.Member != "metadata_ref gc.retry_of" {
		t.Fatalf("RefError.Member = %q, want it to name WHICH ref failed", refErr.Member)
	}
	if refErr.DeclaredLater {
		t.Fatal("a metadata_ref has no forward/backward distinction to report, so DeclaredLater must be false")
	}
}

// TestPlanApplyBatchRefusesAVersionGuardOnARowThisRequestWrote is the F4 rule,
// and it is a REQUEST-SHAPE refusal rather than a race: the token is minted by
// the write, so mid-request there is no value a caller could send. Letting it
// through would answer every such request with ErrVersionMismatch and send the
// caller looking for a concurrent writer that does not exist.
func TestPlanApplyBatchRefusesAVersionGuardOnARowThisRequestWrote(t *testing.T) {
	for _, test := range []struct {
		name  string
		items []issueops.ApplyItem
	}{
		{
			"guard on a row an earlier item created",
			[]issueops.ApplyItem{planApplyCreate("root"), planApplyUpdate(issueops.Ref{Key: "root"}, int64Ptr(7))},
		},
		{
			"guard on a row an earlier item updated",
			[]issueops.ApplyItem{
				planApplyUpdate(issueops.Ref{ID: "bd-1"}, nil),
				planApplyUpdate(issueops.Ref{ID: "bd-1"}, int64Ptr(7)),
			},
		},
		{
			"guard on a row an earlier item closed",
			[]issueops.ApplyItem{
				planApplyClose(issueops.Ref{ID: "bd-1"}, nil),
				planApplyUpdate(issueops.Ref{ID: "bd-1"}, int64Ptr(7)),
			},
		},
		{
			"close guard on a row an earlier item updated",
			[]issueops.ApplyItem{
				planApplyUpdate(issueops.Ref{ID: "bd-1"}, nil),
				planApplyClose(issueops.Ref{ID: "bd-1"}, int64Ptr(7)),
			},
		},
		{
			"guard on a row an earlier item created by explicit id",
			[]issueops.ApplyItem{
				{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Issue: &issueops.Issue{ID: "bd-1"}}},
				planApplyClose(issueops.Ref{ID: "bd-1"}, int64Ptr(7)),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanApplyBatch(planApplyRequest(test.items...))
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
			if !strings.Contains(err.Error(), "cannot be known mid-request") {
				t.Fatalf("error = %q, want it to say why the guard is unanswerable", err)
			}
		})
	}
}

// TestPlanApplyBatchAcceptsAVersionGuardTheRequestCanAnswer is the positive
// half, and it covers the three shapes the rule must NOT refuse. The dep_add
// row is the one that is a decision rather than an omission: an edge write is a
// change to the GRAPH, and this role promises nothing about whether it moves
// either endpoint's version token, so a later guard on that source is left to
// the substrate where a real mismatch is an honest ErrVersionMismatch.
func TestPlanApplyBatchAcceptsAVersionGuardTheRequestCanAnswer(t *testing.T) {
	for _, test := range []struct {
		name  string
		items []issueops.ApplyItem
	}{
		{
			"guard on a row this request has not written",
			[]issueops.ApplyItem{
				planApplyUpdate(issueops.Ref{ID: "bd-1"}, nil),
				planApplyUpdate(issueops.Ref{ID: "bd-2"}, int64Ptr(7)),
			},
		},
		{
			"guard on the first item",
			[]issueops.ApplyItem{planApplyUpdate(issueops.Ref{ID: "bd-1"}, int64Ptr(7))},
		},
		{
			"guard after an edge write on the same source",
			[]issueops.ApplyItem{
				planApplyDep(issueops.Ref{ID: "bd-1"}, issueops.Ref{ID: "bd-2"}, types.DepBlocks, ""),
				planApplyUpdate(issueops.Ref{ID: "bd-1"}, int64Ptr(7)),
			},
		},
		{
			"a key and an id that read alike are two rows",
			[]issueops.ApplyItem{
				planApplyCreate("bd-1"),
				planApplyUpdate(issueops.Ref{ID: "bd-1"}, int64Ptr(7)),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := PlanApplyBatch(planApplyRequest(test.items...)); err != nil {
				t.Fatalf("PlanApplyBatch = %v, want the guard accepted", err)
			}
		})
	}
}

// TestPlanApplyBatchOtherGuardsAreNeverStaticallyRefused pins the difference
// between the version token and the other two: a caller CAN know it wants the
// status its own earlier item set, and as-modified evaluation gives it exactly
// that. Refusing those the way the version guard is refused would delete the
// role's most useful shape.
func TestPlanApplyBatchOtherGuardsAreNeverStaticallyRefused(t *testing.T) {
	status := issueops.StatusInProgress
	assignee := "worker"
	items := []issueops.ApplyItem{
		planApplyCreate("root"),
		{Kind: issueops.ItemUpdate, Update: &issueops.UpdateItem{
			Target:           issueops.Ref{Key: "root"},
			ExpectedStatus:   &status,
			ExpectedAssignee: &assignee,
		}},
	}
	if _, err := PlanApplyBatch(planApplyRequest(items...)); err != nil {
		t.Fatalf("PlanApplyBatch = %v, want an as-modified status/assignee guard accepted", err)
	}
}

// TestPlanApplyBatchNormalizesWaitsForGates pins the migration-0059 invariant:
// a stored waits-for row must be SELF-DESCRIBING, because readers that predate
// the gate do not default a missing one. Empty metadata on a waits-for edge is
// a row those readers get wrong, so it is never what is stored.
func TestPlanApplyBatchNormalizesWaitsForGates(t *testing.T) {
	allChildren := fmt.Sprintf(`{"gate":%q}`, types.WaitsForAllChildren)

	for _, test := range []struct {
		name     string
		depType  types.DependencyType
		metadata string
		want     string
	}{
		{"absent gate", types.DepWaitsFor, "", allChildren},
		{"blank gate", types.DepWaitsFor, "   ", allChildren},
		{"empty object", types.DepWaitsFor, "{}", allChildren},
		{"explicit all-children", types.DepWaitsFor, allChildren, allChildren},
		{
			"any-children survives",
			types.DepWaitsFor,
			fmt.Sprintf(`{"gate":%q}`, types.WaitsForAnyChildren),
			fmt.Sprintf(`{"gate":%q}`, types.WaitsForAnyChildren),
		},
		{
			"the spawner and also-blocks members survive",
			types.DepWaitsFor,
			`{"spawner_id":"bd-9","also_blocks":true}`,
			fmt.Sprintf(`{"gate":%q,"spawner_id":"bd-9","also_blocks":true}`, types.WaitsForAllChildren),
		},
		{"a blocking edge is untouched", types.DepBlocks, "", ""},
		{"other metadata passes through", types.DepRelated, `{"score":0.5}`, `{"score":0.5}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanApplyBatch(planApplyRequest(
				planApplyDep(issueops.Ref{ID: "bd-1"}, issueops.Ref{ID: "bd-2"}, test.depType, test.metadata),
			))
			if err != nil {
				t.Fatalf("PlanApplyBatch = %v", err)
			}
			got := plan.Items[0].DepAdd.Metadata
			if test.depType != types.DepWaitsFor {
				if got != test.want {
					t.Fatalf("metadata = %q, want %q unchanged", got, test.want)
				}
				return
			}
			var gotMeta, wantMeta types.WaitsForMeta
			if err := json.Unmarshal([]byte(got), &gotMeta); err != nil {
				t.Fatalf("normalized metadata %q is not a gate object: %v", got, err)
			}
			if err := json.Unmarshal([]byte(test.want), &wantMeta); err != nil {
				t.Fatalf("want %q is not a gate object: %v", test.want, err)
			}
			if gotMeta != wantMeta {
				t.Fatalf("normalized metadata = %+v, want %+v", gotMeta, wantMeta)
			}
			if gotMeta.Gate == "" {
				t.Fatal("a stored waits-for row with no gate is exactly what migration 0059 forbids")
			}
		})
	}
}

// TestPlanApplyBatchRefusesAnUnknownWaitsForGate pins the other half: a gate
// this workspace could never evaluate is refused rather than stored.
func TestPlanApplyBatchRefusesAnUnknownWaitsForGate(t *testing.T) {
	for _, metadata := range []string{`{"gate":"first-tuesday"}`, `{"gate":`} {
		_, err := PlanApplyBatch(planApplyRequest(
			planApplyDep(issueops.Ref{ID: "bd-1"}, issueops.Ref{ID: "bd-2"}, types.DepWaitsFor, metadata),
		))
		if !errors.Is(err, issueops.ErrValidation) {
			t.Fatalf("PlanApplyBatch(%q) = %v, want ErrValidation", metadata, err)
		}
	}
}

// TestPlanApplyBatchDoesNotMutateTheCallerRequest is the promise every role
// makes, and the plan is the one place this role could break it: it normalizes
// gate metadata, and the item payloads it copies are the caller's POINTERS. The
// case would go green on a plan that wrote the normalized gate straight through
// the pointer, so it compares the whole request rather than the one member.
func TestPlanApplyBatchDoesNotMutateTheCallerRequest(t *testing.T) {
	request := issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{
			planApplyCreate("root"),
			planApplyDep(issueops.Ref{Key: "root"}, issueops.Ref{ID: "bd-2"}, types.DepWaitsFor, ""),
		},
	}
	before := issueops.ApplyBatchRequest{
		Actor: request.Actor,
		Items: []issueops.ApplyItem{
			planApplyCreate("root"),
			planApplyDep(issueops.Ref{Key: "root"}, issueops.Ref{ID: "bd-2"}, types.DepWaitsFor, ""),
		},
	}

	plan, err := PlanApplyBatch(request)
	if err != nil {
		t.Fatalf("PlanApplyBatch = %v", err)
	}
	if plan.Items[1].DepAdd.Metadata == "" {
		t.Fatal("the plan did not normalize the gate at all; this case would be vacuous")
	}
	if !reflect.DeepEqual(request.Items[0].Create, before.Items[0].Create) {
		t.Fatalf("PlanApplyBatch wrote through to the caller's create item: %+v", request.Items[0].Create)
	}
	if !reflect.DeepEqual(request.Items[1].DepAdd, before.Items[1].DepAdd) {
		t.Fatalf("PlanApplyBatch wrote the normalized gate through to the caller's edge item: %+v",
			request.Items[1].DepAdd)
	}
}

// TestPlanApplyBatchCarriesTheRequestForward pins that the plan is the whole
// request rather than a filtered one: a field dropped here is a field no
// backend can honor, and nothing else in the tree would notice.
func TestPlanApplyBatchCarriesTheRequestForward(t *testing.T) {
	plan, err := PlanApplyBatch(issueops.ApplyBatchRequest{
		Actor:                 "planner",
		Provenance:            "bd: apply plan.md",
		ForceIDPrefix:         true,
		SkipPerEdgeCycleCheck: true,
		Items:                 []issueops.ApplyItem{planApplyCreate("root")},
	})
	if err != nil {
		t.Fatalf("PlanApplyBatch = %v", err)
	}
	if plan.Actor != "planner" || plan.Provenance != "bd: apply plan.md" ||
		!plan.ForceIDPrefix || !plan.SkipPerEdgeCycleCheck {
		t.Fatalf("plan = %+v, want every request field carried", plan)
	}
	if len(plan.Items) != 1 {
		t.Fatalf("plan.Items = %d, want 1", len(plan.Items))
	}
}
