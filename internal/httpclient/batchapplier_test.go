// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/batchapplier_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// applyRoleWithSnapshot is applyRole's twin for pinning exactly what the
// handshake advertises, the same idiom counts_test.go's
// countRoleWithSnapshot uses: passing a snapshot straight through New rather
// than accepting applyRole's bare stubStore default.
func applyRoleWithSnapshot(t *testing.T, w *stubWire, snap *apigen.ContextResponse) issueops.BatchApplier {
	t.Helper()
	applier, err := New(testTarget(t), w, snap).BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	return applier
}

// The batch-apply role's unit gates: what the client DECIDES before the dial,
// and what it refuses to believe about the answer.
//
// Everything about what a plan MEANS is the server's and is asserted end to end
// by served_batch_apply_test.go. What is here is the half a live server cannot
// show — the request the client built, the refusals it raised without dialing,
// and the answers it will not accept.

func applyRole(t *testing.T, w *stubWire) issueops.BatchApplier {
	t.Helper()
	applier, err := stubStore(t, w).BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	return applier
}

// applyOneCreate is the smallest plan that lands: one create item, nothing
// named, nothing guarded.
func applyOneCreate() issueops.ApplyBatchRequest {
	return issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{{
			Kind:   issueops.ItemCreate,
			Create: &issueops.CreateItem{Issue: &issueops.Issue{Title: "t"}},
		}},
	}
}

// TestApplyBatchRefusesEveryMemberTheWireExcludes is the refuse-not-drop gate
// for this operation, and it is the pin every W- row of the batch-apply
// population cites.
//
// The create item's issue vocabulary is EXACTLY the single create's, so the
// partition tables are shared rather than copied and this drives the same sweep
// against a different operation and a different ledger row. The patch's own
// excluded member — parent_id — is the second half.
//
// The edge item's spawner flag and thread were a third population here before
// issues.batchApply.depAddLineage: both are CARRIED now, gated on the
// capability rather than refused outright — see
// TestApplyBatchRefusesUnservedDepAddLineageBeforeDialing.
func TestApplyBatchRefusesEveryMemberTheWireExcludes(t *testing.T) {
	t.Run("the create item's carried members are the wire's own", func(t *testing.T) {
		published := bodyMembers(t, reflect.TypeOf(apigen.ApplyCreateItem{}))
		for field, wireMember := range createCarriedIssueMembers {
			if !published[wireMember] {
				t.Errorf("Issue.%s claims wire member %q, which ApplyCreateItem does not publish", field, wireMember)
			}
		}
	})

	refused := createWireRefusedIssueMembers(t)
	t.Run("every wire-refused issue member fails without dialing", func(t *testing.T) {
		if len(refused) == 0 {
			t.Fatal("no member is wire-refused; the shared partition tables have swallowed this population")
		}
		for _, name := range refused {
			t.Run(name, func(t *testing.T) {
				issue := &issueops.Issue{Title: "t"}
				setNonZero(t, reflect.ValueOf(issue).Elem().FieldByName(name))

				w := &stubWire{}
				req := applyOneCreate()
				req.Items[0].Create.Issue = issue
				_, err := applyRole(t, w).ApplyBatch(t.Context(), req)
				assertRefusedBy(t, err, "W-CreateItem.Issue")
				if !strings.Contains(err.Error(), name) {
					t.Errorf("the refusal does not name the member: %v", err)
				}
				if !strings.Contains(err.Error(), "items[0]") {
					t.Errorf("the refusal does not name the item: %v", err)
				}
				if len(w.calls) != 0 {
					t.Errorf("the refused member reached the wire: %v", w.calls)
				}
			})
		}
	})

	t.Run("a CreatedBy naming the actor rides the server's stamp", func(t *testing.T) {
		// The server stamps every create item's created_by from the actor, so
		// that one value is carried by the stamp; the sweep above refuses any
		// other.
		w := &stubWire{}
		req := applyOneCreate()
		req.Items[0].Create.Issue.CreatedBy = req.Actor
		if _, err := applyRole(t, w).ApplyBatch(t.Context(), req); err != nil {
			t.Fatalf("ApplyBatch with CreatedBy == Actor = %v, want it carried by the server's stamp", err)
		}
		if w.lastApply.Actor != req.Actor {
			t.Errorf("sent actor = %q, want the creator the stamp will write", w.lastApply.Actor)
		}
	})

	t.Run("the role's own two are ErrValidation, and here they have nowhere to go at all", func(t *testing.T) {
		for name := range createRoleRefusedIssueMembers {
			issue := &issueops.Issue{Title: "t"}
			setNonZero(t, reflect.ValueOf(issue).Elem().FieldByName(name))

			w := &stubWire{}
			req := applyOneCreate()
			req.Items[0].Create.Issue = issue
			_, err := applyRole(t, w).ApplyBatch(t.Context(), req)
			if !errors.Is(err, issueops.ErrValidation) {
				t.Errorf("Issue.%s error = %v, want ErrValidation", name, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("Issue.%s reached the wire: %v", name, w.calls)
			}
		}
	})

	t.Run("the patch's own excluded member refuses, and the four shared ones cite their existing rows", func(t *testing.T) {
		for _, test := range []struct {
			member string
			row    string
			patch  issueops.IssuePatch
		}{
			{"ParentID", "W-ApplyPatch.ParentID", issueops.IssuePatch{
				ParentID: issueops.Field[string]{Set: true, Value: "bd-parent"},
			}},
			{"SpecID", "W-IssuePatch.SpecID", issueops.IssuePatch{
				SpecID: issueops.Field[string]{Set: true, Value: "spec-1"},
			}},
			{"AwaitID", "W-IssuePatch.AwaitID", issueops.IssuePatch{
				AwaitID: issueops.Field[string]{Set: true, Value: "await-1"},
			}},
			{"ClosedBySession", "W-IssuePatch.ClosedBySession", issueops.IssuePatch{
				ClosedBySession: issueops.Field[string]{Set: true, Value: "sess-1"},
			}},
			{"Persistence", "W-IssuePatch.Persistence", issueops.IssuePatch{
				Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: types.PersistenceModeEphemeral},
			}},
		} {
			t.Run(test.member, func(t *testing.T) {
				w := &stubWire{}
				_, err := applyRole(t, w).ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
					Actor: "planner",
					Items: []issueops.ApplyItem{{
						Kind:   issueops.ItemUpdate,
						Update: &issueops.UpdateItem{Target: issueops.Ref{ID: "bd-1"}, Patch: test.patch},
					}},
				})
				assertRefusedBy(t, err, test.row)
				if len(w.calls) != 0 {
					t.Errorf("the refused member reached the wire: %v", w.calls)
				}
			})
		}
	})

	// The edge item's HasSpawner/ThreadID used to refuse unconditionally here
	// (W-DepAddItem.HasSpawner, W-DepAddItem.ThreadID). Both are now CARRIED,
	// gated by issues.batchApply.depAddLineage — see
	// TestApplyBatchRefusesUnservedDepAddLineageBeforeDialing for the pre-dial
	// capability refusal this subtest retired in favor of.

	t.Run("a spawner flag on any other edge type is the role's no-op, and is not refused", func(t *testing.T) {
		// The capability must be advertised for this to dial at all now:
		// HasSpawner/ThreadID are gated on every edge type, not only
		// waits-for — see refuseUnservedDepAddLineage.
		served := &apigen.ContextResponse{Capabilities: []string{wire.CapBatchApplyDepAddLineage}}
		w := &stubWire{}
		applier, err := New(testTarget(t), w, served).BatchApplier()
		if err != nil {
			t.Fatalf("BatchApplier(): %v", err)
		}
		_, err = applier.ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
			Actor: "planner",
			Items: []issueops.ApplyItem{{Kind: issueops.ItemDepAdd, DepAdd: &issueops.DepAddItem{
				Source: issueops.Ref{ID: "bd-1"}, Target: issueops.Ref{ID: "bd-2"}, Type: issueops.DepBlocks, HasSpawner: true,
			}}},
		})
		if err != nil {
			t.Fatalf("a spawner flag on a blocks edge refused: %v", err)
		}
		if got := w.lastApply.Items[0].DepAdd; got == nil || got.Type != string(issueops.DepBlocks) {
			t.Errorf("the encoded edge = %+v, want the caller's blocks edge", got)
		}
	})

	t.Run("owner is CARRIED here, which is where the two patch documents part company", func(t *testing.T) {
		w := &stubWire{}
		_, err := applyRole(t, w).ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
			Actor: "planner",
			Items: []issueops.ApplyItem{{
				Kind: issueops.ItemUpdate,
				Update: &issueops.UpdateItem{
					Target: issueops.Ref{ID: "bd-1"},
					Patch:  issueops.IssuePatch{Owner: issueops.Field[string]{Set: true, Value: "ada"}},
				},
			}},
		})
		if err != nil {
			t.Fatalf("an owner edit inside a plan refused: %v", err)
		}
		if got := w.lastApply.Items[0].Update.Patch["owner"]; got != "ada" {
			t.Errorf("the encoded patch carries owner = %v, want the caller's value", got)
		}
	})
}

// TestApplyBatchFailsClosedOnAnItemTheClientCannotSpell is the tagged union's
// whole risk, driven in all four directions.
//
// The document carries a required tag and four OPTIONAL payloads because it
// uses no composition keyword, so nothing in any generated type stops a
// disagreement. The dangerous one is the unknown KIND: a fifth verb added to
// issueops would arrive here as a value the client's table does not carry, and
// a client that sent the item anyway would put a plan on the wire that did less
// than the caller composed.
func TestApplyBatchFailsClosedOnAnItemTheClientCannotSpell(t *testing.T) {
	create := &issueops.CreateItem{Issue: &issueops.Issue{Title: "t"}}
	update := &issueops.UpdateItem{
		Target: issueops.Ref{ID: "bd-1"},
		Patch:  issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: "x"}},
	}

	for _, test := range []struct {
		name string
		item issueops.ApplyItem
		says string
	}{
		{
			// The fail-closed arm, and the DIAGNOSIS is what it pins rather than
			// the refusal. "reopen" is a verb the ROLE deliberately excludes
			// today, so it stands in for the fifth kind nobody has invented yet
			// — and an implementation that passed an unknown kind through to
			// the tag/payload agreement check would still refuse this item,
			// while telling the caller its PAYLOAD was wrong. That sends
			// someone reading the message to fix a payload that is correct,
			// so the refusal has to name the VOCABULARY.
			name: "a kind this client does not know",
			item: issueops.ApplyItem{Kind: issueops.ItemKind("reopen"), Create: create},
			says: `item kind "reopen" is not one of create, update, close, dep_add`,
		},
		{
			// The same kind carrying nothing, which under a pass-through would
			// be reported as a missing `reopen` payload — a member the document
			// does not publish at all.
			name: "a kind this client does not know, carrying nothing",
			item: issueops.ApplyItem{Kind: issueops.ItemKind("reopen")},
			says: `item kind "reopen" is not one of create, update, close, dep_add`,
		},
		{
			name: "a kind with no payload",
			item: issueops.ApplyItem{Kind: issueops.ItemCreate},
			says: "no create payload",
		},
		{
			name: "a payload the kind does not name",
			item: issueops.ApplyItem{Kind: issueops.ItemUpdate, Create: create},
			says: "must name the same verb",
		},
		{
			name: "two payloads",
			item: issueops.ApplyItem{Kind: issueops.ItemCreate, Create: create, Update: update},
			says: "exactly one payload",
		},
		{
			// The empty kind is the zero value of the field, which is what an
			// item built by a caller that forgot the tag looks like.
			name: "the empty kind",
			item: issueops.ApplyItem{Create: create},
			says: `item kind "" is not one of create, update, close, dep_add`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := &stubWire{}
			_, err := applyRole(t, w).ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
				Actor: "planner", Items: []issueops.ApplyItem{test.item},
			})
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
			if !strings.Contains(err.Error(), test.says) {
				t.Errorf("the refusal does not say what was wrong (%q): %v", test.says, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("the item reached the wire: %v", w.calls)
			}
		})
	}
}

// TestApplyBatchRefusesTheRequestShapesTheRoleCallsInvalid covers the bounds and
// the ref rule, all of them raised before the dial.
func TestApplyBatchRefusesTheRequestShapesTheRoleCallsInvalid(t *testing.T) {
	oversized := issueops.ApplyBatchRequest{Actor: "planner"}
	for range issueops.MaxApplyBatchItems + 1 {
		oversized.Items = append(oversized.Items, issueops.ApplyItem{
			Kind:   issueops.ItemCreate,
			Create: &issueops.CreateItem{Issue: &issueops.Issue{Title: "t"}},
		})
	}
	refTarget := func(ref issueops.Ref) issueops.ApplyBatchRequest {
		return issueops.ApplyBatchRequest{
			Actor: "planner",
			Items: []issueops.ApplyItem{{
				Kind: issueops.ItemUpdate,
				Update: &issueops.UpdateItem{
					Target: ref,
					Patch:  issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: "x"}},
				},
			}},
		}
	}

	for _, test := range []struct {
		name string
		req  issueops.ApplyBatchRequest
	}{
		{"no actor", issueops.ApplyBatchRequest{Items: applyOneCreate().Items}},
		{"no items", issueops.ApplyBatchRequest{Actor: "planner"}},
		{"one item past the cap", oversized},
		{"a ref naming neither member", refTarget(issueops.Ref{})},
		{"a ref naming both members", refTarget(issueops.Ref{Key: "k", ID: "bd-1"})},
		{"an update that writes nothing", issueops.ApplyBatchRequest{
			Actor: "planner",
			Items: []issueops.ApplyItem{{
				Kind:   issueops.ItemUpdate,
				Update: &issueops.UpdateItem{Target: issueops.Ref{ID: "bd-1"}},
			}},
		}},
		{"an edge with no type", issueops.ApplyBatchRequest{
			Actor: "planner",
			Items: []issueops.ApplyItem{{
				Kind:   issueops.ItemDepAdd,
				DepAdd: &issueops.DepAddItem{Source: issueops.Ref{ID: "bd-1"}, Target: issueops.Ref{ID: "bd-2"}},
			}},
		}},
		{"a metadata blob that is not JSON", issueops.ApplyBatchRequest{
			Actor: "planner",
			Items: []issueops.ApplyItem{{
				Kind: issueops.ItemCreate,
				Create: &issueops.CreateItem{Issue: &issueops.Issue{
					Title: "t", Metadata: json.RawMessage(`{"broken":`),
				}},
			}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := &stubWire{}
			_, err := applyRole(t, w).ApplyBatch(t.Context(), test.req)
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
			if len(w.calls) != 0 {
				t.Errorf("the request reached the wire: %v", w.calls)
			}
		})
	}

	// The cap refuses rather than SPLITTING, which is the half a refusal test
	// alone would not say: a split plan is several transactions where the
	// caller asked for one, and the end gate only ever sees one request.
	w := &stubWire{}
	_, _ = applyRole(t, w).ApplyBatch(t.Context(), oversized)
	if len(w.calls) != 0 {
		t.Errorf("the oversized plan was chunked onto the wire: %v", w.calls)
	}
}

// TestApplyBatchSendsTheGuardsAndTheLiteralsIntact is the encoding round trip.
//
// TWO CORRUPTIONS ARE THE POINT and neither is visible in a result. The row
// version is int64 END TO END: live tokens run past 5e17, where an IEEE-754
// double's ulp is already 64, so a float anywhere on this path hands the server
// a number NEAR the token that is not it — and the answer would be a
// precondition failure a caller cannot explain. And a metadata value travels as
// the caller's SOURCE LITERAL: the role compares metadata by literal, so 1 and
// 1.0 are not equal and 9007199254740993 is not 9007199254740992 — a float
// round trip changes the VERDICT of a later compare-and-set rather than a
// spelling.
func TestApplyBatchSendsTheGuardsAndTheLiteralsIntact(t *testing.T) {
	const token = int64(576460752303423489) // 2^59 + 1: a float64 cannot hold it
	const bigInt = `9007199254740993`       // 2^53 + 1
	version := token
	status := issueops.Status("in_progress")
	assignee := ""

	w := &stubWire{}
	_, err := applyRole(t, w).ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
		Actor:                 "planner",
		Provenance:            "wave 1c",
		ForceIDPrefix:         true,
		SkipPerEdgeCycleCheck: true,
		Items: []issueops.ApplyItem{
			{
				Kind: issueops.ItemCreate,
				Create: &issueops.CreateItem{
					Key: "root",
					Issue: &issueops.Issue{
						Title:    "t",
						Metadata: json.RawMessage(`{"exact":` + bigInt + `,"one":1.0}`),
					},
					MetadataRefs: map[string]issueops.Ref{"spawned": {Key: "child"}},
				},
			},
			{
				Kind: issueops.ItemUpdate,
				Update: &issueops.UpdateItem{
					Target:           issueops.Ref{Key: "root"},
					Patch:            issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: "x"}},
					ExpectedVersion:  &version,
					ExpectedStatus:   &status,
					ExpectedAssignee: &assignee,
				},
			},
			{
				Kind:  issueops.ItemClose,
				Close: &issueops.CloseItem{Target: issueops.Ref{ID: "bd-1"}, ExpectedVersion: &version},
			},
			{
				Kind: issueops.ItemDepAdd,
				DepAdd: &issueops.DepAddItem{
					Source:   issueops.Ref{Key: "root"},
					Target:   issueops.Ref{ID: "bd-1"},
					Type:     "waits-for",
					Metadata: `{"gate":"any-children","seq":` + bigInt + `}`,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}

	body := w.lastApply
	if body.Actor != "planner" {
		t.Errorf("actor = %q", body.Actor)
	}
	for member, got := range map[string]*bool{
		"force_id_prefix": body.ForceIDPrefix, "skip_per_edge_cycle_check": body.SkipPerEdgeCycleCheck,
	} {
		if got == nil || !*got {
			t.Errorf("%s = %v, want true", member, got)
		}
	}
	if body.Provenance == nil || *body.Provenance != "wave 1c" {
		t.Errorf("provenance = %v, want the caller's label", body.Provenance)
	}
	if len(body.Items) != 4 {
		t.Fatalf("the body carries %d items, want 4", len(body.Items))
	}

	// The guards, on both members that publish one.
	// The version guards reach the wire as the token's decimal STRING
	// (types.RevisionToken): a JSON number on this member is a 400, and a
	// float64 anywhere on the path would answer a number NEAR the token.
	update := body.Items[1].Update
	if update.ExpectedVersion == nil || *update.ExpectedVersion != types.RevisionToken(token) {
		t.Errorf("update expected_version = %v, want %q", update.ExpectedVersion, types.RevisionToken(token))
	}
	if update.ExpectedStatus == nil || *update.ExpectedStatus != "in_progress" {
		t.Errorf("update expected_status = %v", update.ExpectedStatus)
	}
	// The EMPTY assignee guard is "only if nobody holds it" and is a request,
	// not an absence.
	if update.ExpectedAssignee == nil {
		t.Error("update expected_assignee is absent; the empty string is a real guard")
	} else if *update.ExpectedAssignee != "" {
		t.Errorf("update expected_assignee = %q, want the empty guard", *update.ExpectedAssignee)
	}
	if got := body.Items[2].Close.ExpectedVersion; got == nil || *got != types.RevisionToken(token) {
		t.Errorf("close expected_version = %v, want %q", got, types.RevisionToken(token))
	}

	// The literals, on both paths that carry a blob.
	for name, raw := range map[string]json.RawMessage{
		"create.metadata":  json.RawMessage(body.Items[0].Create.Metadata),
		"dep_add.metadata": json.RawMessage(body.Items[3].DepAdd.Metadata),
	} {
		if !strings.Contains(string(raw), bigInt) {
			t.Errorf("%s = %s; the source literal %s did not survive — a float64 round trip answers %s",
				name, raw, bigInt, "9007199254740992")
		}
	}
	if got := string(body.Items[0].Create.Metadata); !strings.Contains(got, "1.0") {
		t.Errorf("create.metadata = %s; the literal 1.0 became 1, which the role compares as a different value", got)
	}

	// The refs, exactly one member each, and the forward metadata ref carried.
	if got := body.Items[1].Update.Target; got.Key == nil || *got.Key != "root" || got.Id != nil {
		t.Errorf("update target = %+v, want a key ref naming root and no id", got)
	}
	refs := body.Items[0].Create.MetadataRefs
	if refs == nil {
		t.Fatal("the create item dropped its metadata_refs")
	}
	if got := (*refs)["spawned"]; got.Key == nil || *got.Key != "child" {
		t.Errorf("metadata_refs[spawned] = %+v, want a key ref naming child", got)
	}
}

// TestApplyBatchAbsentGuardsStayAbsent is the other polarity, and it is the one
// a sentinel would break: 0 is a LEGAL row version — the migration-0054
// backfill wrote it — so encoding "no guard" as 0 would arm a guard on every
// unguarded item, and encoding it as -1 would arm one that can never match.
func TestApplyBatchAbsentGuardsStayAbsent(t *testing.T) {
	zero := int64(0)
	w := &stubWire{}
	_, err := applyRole(t, w).ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{
			{
				Kind: issueops.ItemUpdate,
				Update: &issueops.UpdateItem{
					Target: issueops.Ref{ID: "bd-1"},
					Patch:  issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: "x"}},
				},
			},
			{
				Kind: issueops.ItemUpdate,
				Update: &issueops.UpdateItem{
					Target:          issueops.Ref{ID: "bd-2"},
					Patch:           issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: "y"}},
					ExpectedVersion: &zero,
				},
			},
			{Kind: issueops.ItemClose, Close: &issueops.CloseItem{Target: issueops.Ref{ID: "bd-3"}}},
		},
	})
	if err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}

	unguarded := w.lastApply.Items[0].Update
	if unguarded.ExpectedVersion != nil {
		t.Errorf("an unguarded update sent expected_version = %q; absent is not zero", *unguarded.ExpectedVersion)
	}
	for member, got := range map[string]*string{
		"expected_status": unguarded.ExpectedStatus, "expected_assignee": unguarded.ExpectedAssignee,
	} {
		if got != nil {
			t.Errorf("an unguarded update sent %s = %q", member, *got)
		}
	}
	if got := w.lastApply.Items[1].Update.ExpectedVersion; got == nil || *got != "0" {
		t.Errorf("a guard ON ZERO came out as %v; 0 is a token a row really holds and it is spelled \"0\"", got)
	}
	if got := w.lastApply.Items[2].Close.ExpectedVersion; got != nil {
		t.Errorf("an unguarded close sent expected_version = %q", *got)
	}
	// The two force flags, absent rather than explicitly false.
	if w.lastApply.Items[0].Update.ForceClosePolicy != nil || w.lastApply.Items[0].Update.ForceAssigneeTransfer != nil {
		t.Error("an update that asked for no bypass sent one explicitly false")
	}
	if w.lastApply.ForceIDPrefix != nil || w.lastApply.SkipPerEdgeCycleCheck != nil || w.lastApply.Provenance != nil {
		t.Error("a request that asked for none of the three optional members sent one anyway")
	}
}

// TestApplyBatchRefusesAMisattributedResult is the positional-array guard, and
// it is the one failure on this operation that no assertion about a ROW would
// catch: both rows exist and both were written, so a result read at the wrong
// index reports one item's `changed` and `revision` as another's, and every
// row-level check still passes.
func TestApplyBatchRefusesAMisattributedResult(t *testing.T) {
	req := issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{
			{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Issue: &issueops.Issue{Title: "t"}}},
			{Kind: issueops.ItemClose, Close: &issueops.CloseItem{Target: issueops.Ref{ID: "bd-1"}}},
		},
	}

	for _, test := range []struct {
		name  string
		items []apigen.ApplyItemResult
		says  string
	}{
		{
			name:  "one result short",
			items: []apigen.ApplyItemResult{{Kind: "create", IssueId: "bd-9"}},
			says:  "1 batch-apply results for 2 items",
		},
		{
			name: "one result too many",
			items: []apigen.ApplyItemResult{
				{Kind: "create", IssueId: "bd-9"}, {Kind: "close", IssueId: "bd-1"}, {Kind: "close", IssueId: "bd-2"},
			},
			says: "3 batch-apply results for 2 items",
		},
		{
			name: "the kinds transposed",
			items: []apigen.ApplyItemResult{
				{Kind: "close", IssueId: "bd-1"}, {Kind: "create", IssueId: "bd-9"},
			},
			says: "the results are positional",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := &stubWire{applied: &apigen.ApplyBatchResponse{Keys: map[string]string{}, Items: test.items}}
			_, err := applyRole(t, w).ApplyBatch(t.Context(), req)
			if err == nil {
				t.Fatal("a misattributed result was accepted")
			}
			if !strings.Contains(err.Error(), test.says) {
				t.Errorf("error = %v, want it to say %q", err, test.says)
			}
		})
	}
}

// TestApplyBatchCrossChecksKeyedCreatesAgainstTheKeyMap closes the half the kind
// check above cannot see.
//
// A SAME-KIND SWAP IS INVISIBLE TO A KIND COMPARISON: two create items
// transposed still answer "create" at both indexes, so the length agrees, the
// kinds agree, and one row's id, `changed` and `revision` are read as the
// other's. The response carries the bytes that catch it — `keys` maps each
// NAMED create to the id it was bound to, independently of the array's order —
// so a keyed create's result must agree with its own key, and a key the request
// declared that the answer does not carry is a result that cannot be checked at
// all rather than one to pass along.
//
// THE RESIDUAL IS STATED RATHER THAN HIDDEN: an UNKEYED same-kind swap stays
// unverifiable from here. Nothing in the response distinguishes two unnamed
// creates, and nothing in the request could — that is what naming them is for.
// It sits in the same trust class as a server that answered the wrong row
// entirely, and the served assembly is provably in item order.
func TestApplyBatchCrossChecksKeyedCreatesAgainstTheKeyMap(t *testing.T) {
	keyed := issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{
			{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Key: "a", Issue: &issueops.Issue{Title: "a"}}},
			{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Key: "b", Issue: &issueops.Issue{Title: "b"}}},
		},
	}

	t.Run("two keyed creates with transposed results", func(t *testing.T) {
		w := &stubWire{applied: &apigen.ApplyBatchResponse{
			Keys: map[string]string{"a": "bd-1", "b": "bd-2"},
			Items: []apigen.ApplyItemResult{
				{Kind: "create", IssueId: "bd-2", Changed: true, Revision: "7"},
				{Kind: "create", IssueId: "bd-1", Changed: true, Revision: "9"},
			},
		}}
		_, err := applyRole(t, w).ApplyBatch(t.Context(), keyed)
		if err == nil {
			t.Fatal("a same-kind transposition was accepted; the key map says which id each named create was bound to")
		}
		if !strings.Contains(err.Error(), `key "a"`) || !strings.Contains(err.Error(), "bd-1") {
			t.Errorf("error = %v, want it to name the key and the id it was bound to", err)
		}
	})

	t.Run("a key the request declared and the answer does not carry", func(t *testing.T) {
		w := &stubWire{applied: &apigen.ApplyBatchResponse{
			Keys: map[string]string{"a": "bd-1"},
			Items: []apigen.ApplyItemResult{
				{Kind: "create", IssueId: "bd-1", Changed: true, Revision: "0"},
				{Kind: "create", IssueId: "bd-2", Changed: true, Revision: "0"},
			},
		}}
		_, err := applyRole(t, w).ApplyBatch(t.Context(), keyed)
		if err == nil {
			t.Fatal("a missing key was passed along; the binding is the one fact the request cannot carry")
		}
		// The DIAGNOSIS, not just the refusal — M8's lesson applied here. A
		// decode that ignored the map's presence bit reads the missing key as
		// the empty id and refuses as a DISAGREEMENT, which names the same key
		// while telling the caller the server answered the wrong row. It did
		// not: it answered with no binding at all.
		if !strings.Contains(err.Error(), `bound no id to key "b"`) {
			t.Errorf("error = %v, want it to say the key was left UNBOUND rather than bound to something else", err)
		}
	})

	t.Run("the agreeing answer passes, and an UNKEYED create is not checked", func(t *testing.T) {
		w := &stubWire{applied: &apigen.ApplyBatchResponse{
			Keys: map[string]string{"a": "bd-1", "b": "bd-2"},
			Items: []apigen.ApplyItemResult{
				{Kind: "create", IssueId: "bd-1", Changed: true, Revision: "0"},
				{Kind: "create", IssueId: "bd-2", Changed: true, Revision: "0"},
			},
		}}
		if _, err := applyRole(t, w).ApplyBatch(t.Context(), keyed); err != nil {
			t.Fatalf("an agreeing answer was refused: %v", err)
		}

		// The residual, asserted so it is a documented limit rather than an
		// assumption: two UNNAMED creates carry nothing to cross-check, so a
		// transposition between them passes. Naming them is what makes it
		// checkable, which is the sentence the doc comment ends on.
		unkeyed := issueops.ApplyBatchRequest{
			Actor: "planner",
			Items: []issueops.ApplyItem{
				{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Issue: &issueops.Issue{Title: "a"}}},
				{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Issue: &issueops.Issue{Title: "b"}}},
			},
		}
		w = &stubWire{applied: &apigen.ApplyBatchResponse{
			Keys: map[string]string{},
			Items: []apigen.ApplyItemResult{
				{Kind: "create", IssueId: "bd-2", Revision: "0"}, {Kind: "create", IssueId: "bd-1", Revision: "0"},
			},
		}}
		if _, err := applyRole(t, w).ApplyBatch(t.Context(), unkeyed); err != nil {
			t.Fatalf("an unkeyed pair was refused: %v — nothing in either side distinguishes them", err)
		}
	})
}

// TestApplyBatchReadsTheAnswerBackWhole is the result decode's positive half.
func TestApplyBatchReadsTheAnswerBackWhole(t *testing.T) {
	const token = int64(576460752303423489)
	dependsOn := "bd-2"
	w := &stubWire{applied: &apigen.ApplyBatchResponse{
		Keys: map[string]string{"root": "bd-9"},
		Items: []apigen.ApplyItemResult{
			{Kind: "create", IssueId: "bd-9", Changed: true, Revision: types.RevisionToken(token)},
			{Kind: "dep_add", IssueId: "bd-9", DependsOnId: &dependsOn, Changed: false, Revision: "0"},
		},
	}}
	result, err := applyRole(t, w).ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{
			{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Key: "root", Issue: &issueops.Issue{Title: "t"}}},
			{Kind: issueops.ItemDepAdd, DepAdd: &issueops.DepAddItem{
				Source: issueops.Ref{Key: "root"}, Target: issueops.Ref{ID: "bd-2"}, Type: "blocks",
			}},
		},
	})
	if err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}

	if got := result.Keys["root"]; got != "bd-9" {
		t.Errorf("Keys[root] = %q, want the minted id", got)
	}
	if len(result.Items) != 2 {
		t.Fatalf("the result carries %d items, want 2", len(result.Items))
	}
	if result.Items[0].RowVersion != token {
		t.Errorf("RowVersion = %d, want %d — the wire's decimal token parsed back losslessly",
			result.Items[0].RowVersion, token)
	}
	if !result.Items[0].Changed {
		t.Error("the create's Changed was dropped")
	}
	if result.Items[1].DependsOnID != "bd-2" {
		t.Errorf("the edge's DependsOnID = %q, want bd-2", result.Items[1].DependsOnID)
	}
	if result.Items[1].Changed {
		t.Error("an idempotent re-add was reported as Changed")
	}
	// The snapshot stops at the wire — ledger row L-apply-snapshot — and the
	// role's own leaf says why. It is asserted rather than assumed so a future
	// wave that starts hydrating has to come here and say so.
	for i, item := range result.Items {
		if item.Issue != nil {
			t.Errorf("items[%d] carries a snapshot; the wire result is lean (L-apply-snapshot)", i)
		}
	}
}

// TestApplyBatchRebuildsTheItemAndRefRefusals is the refusal decode.
//
// The request is all or nothing, so `item_*` is the ONLY place the offender
// exists — and the two shapes are told apart by `declared_later`'s PRESENCE,
// which is why the server emits it in both polarities.
func TestApplyBatchRebuildsTheItemAndRefRefusals(t *testing.T) {
	req := issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{
			{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{Issue: &issueops.Issue{Title: "t"}}},
			{Kind: issueops.ItemClose, Close: &issueops.CloseItem{Target: issueops.Ref{ID: "bd-1"}}},
		},
	}

	t.Run("an item refusal names the item and keeps its sentinel", func(t *testing.T) {
		index := 1
		kind, key, id := "close", "", "bd-1"
		w := &stubWire{errs: []error{&wire.ProblemError{
			Status: 409, Code: "not_closable", Err: issueops.ErrCloseBlocked,
			ItemIndex: &index, ItemKind: &kind, ItemKey: &key, ItemIssueID: &id,
		}}}
		_, err := applyRole(t, w).ApplyBatch(t.Context(), req)

		var itemErr *issueops.ItemError
		if !errors.As(err, &itemErr) {
			t.Fatalf("error = %v (%T), want an *ItemError naming the item that refused", err, err)
		}
		if itemErr.Index != 1 || itemErr.Kind != issueops.ItemClose || itemErr.IssueID != "bd-1" {
			t.Errorf("ItemError = %#v, want Index 1, Kind close, IssueID bd-1", itemErr)
		}
		if !errors.Is(err, issueops.ErrCloseBlocked) {
			t.Errorf("the sentinel did not survive the wrapper: %v", err)
		}
	})

	t.Run("a ref refusal is a RefError in both polarities", func(t *testing.T) {
		for _, test := range []struct {
			later  bool
			param  string
			member string
		}{
			{true, "items[1].close.target", "target"},
			{false, "items[1].dep_add.source", "source"},
			{false, "items[1].create.metadata_refs", "metadata_refs"},
		} {
			index, key, later := 1, "child", test.later
			w := &stubWire{errs: []error{&wire.ProblemError{
				Status: 400, Code: "invalid_argument", Param: test.param, Err: issueops.ErrValidation,
				ItemIndex: &index, ItemKey: &key, DeclaredLater: &later,
			}}}
			_, err := applyRole(t, w).ApplyBatch(t.Context(), req)

			var refErr *issueops.RefError
			if !errors.As(err, &refErr) {
				t.Fatalf("error = %v (%T), want a *RefError", err, err)
			}
			if refErr.DeclaredLater != test.later {
				t.Errorf("RefError.DeclaredLater = %v, want %v: the two diagnoses are what a caller acts on",
					refErr.DeclaredLater, test.later)
			}
			if refErr.Index != 1 || refErr.Key != "child" {
				t.Errorf("RefError = %#v, want Index 1 and Key child", refErr)
			}
			if refErr.Member != test.member {
				t.Errorf("RefError.Member = %q, want %q", refErr.Member, test.member)
			}
			if !errors.Is(err, issueops.ErrValidation) {
				t.Errorf("the RefError does not match ErrValidation: %v", err)
			}
		}
	})

	t.Run("a key refusal that names no item does not blame the first one", func(t *testing.T) {
		// Unreachable against this server, and hardened anyway because
		// RefError.Index has no absent state: answering 0 would name the FIRST
		// item as the offender on a refusal that named none.
		later := true
		w := &stubWire{errs: []error{&wire.ProblemError{
			Status: 400, Code: "invalid_argument", Err: issueops.ErrValidation, DeclaredLater: &later,
		}}}
		_, err := applyRole(t, w).ApplyBatch(t.Context(), req)

		var refErr *issueops.RefError
		if errors.As(err, &refErr) {
			t.Errorf("a key refusal naming no item was rebuilt as %#v; index 0 is the first item, not 'unknown'", refErr)
		}
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("error = %v, want the sentinel through", err)
		}
	})

	t.Run("a refusal that names no item travels unwrapped", func(t *testing.T) {
		w := &stubWire{errs: []error{&wire.ProblemError{
			Status: 400, Code: "invalid_argument", Param: "items", Err: issueops.ErrValidation,
		}}}
		_, err := applyRole(t, w).ApplyBatch(t.Context(), req)

		var itemErr *issueops.ItemError
		if errors.As(err, &itemErr) {
			t.Errorf("a refusal naming no item was wrapped as %#v; index 0 would be a claim about an item "+
				"this refusal says nothing about", itemErr)
		}
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("error = %v, want the sentinel through", err)
		}
	})
}

// TestTheEncodedApplyPatchDocumentUsesOnlyPublishedMembers is
// TestTheEncodedPatchDocumentUsesOnlyPublishedMembers for the plan's own patch,
// and it is a separate gate because the two documents' allowlists differ in
// BOTH directions: this one publishes owner and not parent_id.
func TestTheEncodedApplyPatchDocumentUsesOnlyPublishedMembers(t *testing.T) {
	published := bodyMembers(t, reflect.TypeOf(apigen.ApplyPatchBody{}))

	patch := issueops.IssuePatch{}
	setEvery := reflect.ValueOf(&patch).Elem()
	var wantMembers []string
	for name, how := range shapeNamed(t, "applyBatch/update/patch").carried {
		setPatchField(t, setEvery.FieldByName(name))
		wantMembers = append(wantMembers, how.member)
	}

	document, err := encodeApplyPatch(patch)
	if err != nil {
		t.Fatalf("a patch setting every carried member refused: %v", err)
	}
	for name := range document {
		if !published[name] {
			t.Errorf("the encoded apply patch carries %q, which ApplyPatchBody does not publish: %v",
				name, sortedKeys(published))
		}
	}
	slices.Sort(wantMembers)
	got := sortedKeys(toSet(document))
	if strings.Join(got, ",") != strings.Join(wantMembers, ",") {
		t.Errorf("the encoded apply patch carries\n  %v\nwant the table's carried members\n  %v", got, wantMembers)
	}
}

// TestTheApplyLabelPatchIsTheWholeEdit pins the one place this document is
// WIDER than PATCH /v0/beads/issues/{id}'s: a plan edits a label set it did not
// compose, so a REMOVAL has to be expressible without reading the set back.
func TestTheApplyLabelPatchIsTheWholeEdit(t *testing.T) {
	w := &stubWire{}
	_, err := applyRole(t, w).ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{{
			Kind: issueops.ItemUpdate,
			Update: &issueops.UpdateItem{
				Target: issueops.Ref{ID: "bd-1"},
				Patch: issueops.IssuePatch{Labels: issueops.LabelPatch{
					Replace: issueops.Field[[]string]{Set: true, Value: []string{"base"}},
					Add:     []string{"added"},
					Remove:  []string{"gone"},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("a full label patch refused: %v", err)
	}
	labels, ok := w.lastApply.Items[0].Update.Patch["labels"].(map[string]any)
	if !ok {
		t.Fatalf("the encoded patch carries labels = %#v, want the ordered edit", w.lastApply.Items[0].Update.Patch["labels"])
	}
	for member, want := range map[string][]string{
		"replace": {"base"}, "add": {"added"}, "remove": {"gone"},
	} {
		got, ok := labels[member].([]string)
		if !ok || !slices.Equal(got, want) {
			t.Errorf("labels.%s = %#v, want %v", member, labels[member], want)
		}
	}

	// A SET replacement holding no labels CLEARS every label, which is the
	// state a plain slice could not carry.
	w = &stubWire{}
	_, err = applyRole(t, w).ApplyBatch(t.Context(), issueops.ApplyBatchRequest{
		Actor: "planner",
		Items: []issueops.ApplyItem{{
			Kind: issueops.ItemUpdate,
			Update: &issueops.UpdateItem{
				Target: issueops.Ref{ID: "bd-1"},
				Patch: issueops.IssuePatch{Labels: issueops.LabelPatch{
					Replace: issueops.Field[[]string]{Set: true},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("a label clear refused: %v", err)
	}
	labels = w.lastApply.Items[0].Update.Patch["labels"].(map[string]any)
	got, ok := labels["replace"].([]string)
	if !ok || len(got) != 0 {
		t.Errorf("the clear encoded replace = %#v, want an empty array", labels["replace"])
	}
	// AND IT HAS TO MARSHAL AS ONE. A nil []string is len 0 and satisfies every
	// assertion above while serializing to JSON `null`, which this member does
	// not accept — so the clear is checked at the byte level, which is the level
	// the server reads it at.
	encoded, err := json.Marshal(labels["replace"])
	if err != nil {
		t.Fatalf("marshaling the label clear: %v", err)
	}
	if string(encoded) != "[]" {
		t.Errorf("the clear marshals as %s, want []; a JSON null on this member is not a clear", encoded)
	}
	if _, present := labels["add"]; present {
		t.Error("the clear invented an add member")
	}
}

// TestApplyBatchRefusesUnservedDepAddLineageBeforeDialing is the 2026-10
// Opus-review HIGH-1 finding: refuseUnservedDepAddLineage (batchapplier.go)
// carried no coverage at all — deleting the function and its call site
// passed every test that existed before this one.
//
// A dep_add item naming HasSpawner or ThreadID, dialed against a server that
// does not advertise CapBatchApplyDepAddLineage, must refuse BEFORE dialing
// with *storage.ErrUnsupported naming the capability. A plan that touches
// neither field must still dial normally against the SAME masked server: the
// gate scopes the two members, not the operation.
func TestApplyBatchRefusesUnservedDepAddLineageBeforeDialing(t *testing.T) {
	masked := &apigen.ContextResponse{BdVersion: "1.2.3"} // no CapBatchApplyDepAddLineage
	depAddPlan := func(item issueops.DepAddItem) issueops.ApplyBatchRequest {
		return issueops.ApplyBatchRequest{
			Actor: "planner",
			Items: []issueops.ApplyItem{{Kind: issueops.ItemDepAdd, DepAdd: &item}},
		}
	}

	for _, tc := range []struct {
		name string
		item issueops.DepAddItem
	}{
		{
			name: "HasSpawner",
			item: issueops.DepAddItem{
				Source: issueops.Ref{ID: "bd-1"}, Target: issueops.Ref{ID: "bd-2"},
				Type: "waits-for", HasSpawner: true,
			},
		},
		{
			name: "ThreadID",
			item: issueops.DepAddItem{
				Source: issueops.Ref{ID: "bd-1"}, Target: issueops.Ref{ID: "bd-2"},
				Type: "waits-for", ThreadID: "t-1",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &stubWire{}
			_, err := applyRoleWithSnapshot(t, w, masked).ApplyBatch(t.Context(), depAddPlan(tc.item))
			if err == nil {
				t.Fatal("ApplyBatch returned no error, want a pre-dial capability refusal")
			}
			var unsup *storage.ErrUnsupported
			if !errors.As(err, &unsup) {
				t.Fatalf("errors.As to *storage.ErrUnsupported failed for %v", err)
			}
			if unsup.Capability != wire.CapBatchApplyDepAddLineage {
				t.Errorf("Capability = %q, want %q", unsup.Capability, wire.CapBatchApplyDepAddLineage)
			}
			if len(w.calls) != 0 {
				t.Errorf("dialed %d times, want 0: a pre-dial refusal must never reach the wire", len(w.calls))
			}
		})
	}

	t.Run("neither field set still dials", func(t *testing.T) {
		dependsOn := "bd-2"
		w := &stubWire{applied: &apigen.ApplyBatchResponse{
			Keys: map[string]string{},
			Items: []apigen.ApplyItemResult{
				{Kind: "dep_add", IssueId: "bd-1", DependsOnId: &dependsOn, Changed: true, Revision: "0"},
			},
		}}
		_, err := applyRoleWithSnapshot(t, w, masked).ApplyBatch(t.Context(), depAddPlan(issueops.DepAddItem{
			Source: issueops.Ref{ID: "bd-1"}, Target: issueops.Ref{ID: "bd-2"}, Type: "waits-for",
		}))
		if err != nil {
			t.Fatalf("ApplyBatch with no lineage fields set: %v", err)
		}
		if len(w.calls) == 0 {
			t.Error("the plan never reached the wire, want a dial: the gate scopes HasSpawner/ThreadID, not the operation")
		}
	})
}
