// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/lifecycle_create_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The SINGLE create's refuse-not-drop gate, and the counterpart of
// TestBatchCreateRefusesEveryMemberTheWireExcludes.
//
// The two operations flatten the same types.Issue and they flatten DIFFERENT
// slices of it — createIssue publishes twenty members where batchCreateIssues'
// item publishes eight — so neither gate can stand in for the other. What they
// do share is the ROLE's own ignore list, which is one fact about one role and
// is one table here.
//
// The four directions:
//
//	carried -> wire     the twenty members the client claims to carry are
//	                    members apigen.CreateIssueRequest really publishes.
//	source -> partition every populatable field of types.Issue is carried,
//	                    ignored by the role, refused by the role, or refused by
//	                    the wire. There is no fifth arm, so a member added
//	                    upstream cannot arrive unclassified.
//	behavior            every wire-refused member really fails, citing its
//	                    ledger row, WITHOUT dialing.
//	request             the request's own excluded member — IDPrefix — refuses
//	                    the same way wherever it would act.
func TestCreateRefusesEveryMemberTheWireExcludes(t *testing.T) {
	t.Run("carried members are the wire's own", func(t *testing.T) {
		published := bodyMembers(t, reflect.TypeOf(apigen.CreateIssueRequest{}))
		for field, wireMember := range createCarriedIssueMembers {
			if _, ok := reflect.TypeOf(issueops.Issue{}).FieldByName(field); !ok {
				t.Errorf("the carried table names Issue.%s, which does not exist", field)
			}
			if !published[wireMember] {
				t.Errorf("Issue.%s claims wire member %q, which CreateIssueRequest does not publish", field, wireMember)
			}
		}
		// The complement is the REQUEST's own members rather than the issue's,
		// so this direction stops at "published"; the whole-body bijection is
		// the createIssue writeShape's, which claims every member of this body
		// exactly once.
		for _, wireMember := range createCarriedIssueMembers {
			if strings.TrimSpace(wireMember) == "" {
				t.Error("the carried table names an empty wire member")
			}
		}
	})

	refused := createWireRefusedIssueMembers(t)

	t.Run("the partition covers every member", func(t *testing.T) {
		if len(refused) == 0 {
			t.Fatal("no member is wire-refused; the partition tables have swallowed the population this gate exists for")
		}
		// The creation stamp and the classification plumbing are the two
		// populations the operation's own description says it cannot set, and a
		// create that dropped either is a row the caller believes they wrote.
		// CreatedBy stays here because only a value NAMING THE ACTOR rides the
		// server's stamp — see "a CreatedBy naming the actor" below.
		for _, name := range []string{"CreatedAt", "CreatedBy", "SpecID", "StorageClass", "MolType", "Pinned"} {
			if !slices.Contains(refused, name) {
				t.Errorf("Issue.%s is not in the refused population; a create that dropped it is data loss", name)
			}
		}
		// And the mirror image: the members this operation DOES publish that
		// the batch's narrower item does not. A regression that copied the
		// batch's table onto this operation would refuse all six.
		for _, name := range []string{"ID", "Status", "Notes", "Metadata", "Ephemeral", "Sender"} {
			if slices.Contains(refused, name) {
				t.Errorf("Issue.%s is refused, but createIssue publishes it — this is the operation that fixed the batch's gap", name)
			}
		}
		for field := range roleIgnoredCreateIssueMembers {
			if _, dup := createCarriedIssueMembers[field]; dup {
				t.Errorf("Issue.%s is both carried and ignored", field)
			}
		}
	})

	t.Run("every refused member fails without dialing", func(t *testing.T) {
		for _, name := range refused {
			t.Run(name, func(t *testing.T) {
				issue := &issueops.Issue{Title: "t"}
				setNonZero(t, reflect.ValueOf(issue).Elem().FieldByName(name))

				w := &stubWire{}
				_, err := createRole(t, w).Create(t.Context(), issueops.CreateRequest{
					Actor: "writer", Issue: issue,
				})
				assertRefusedBy(t, err, "W-CreateRequest.Issue")
				if !strings.Contains(err.Error(), name) {
					t.Errorf("the refusal does not name the member: %v", err)
				}
				if len(w.calls) != 0 {
					t.Errorf("the refused member reached the wire: %v", w.calls)
				}
			})
		}
	})

	t.Run("the role's own two are ErrValidation", func(t *testing.T) {
		for name := range createRoleRefusedIssueMembers {
			issue := &issueops.Issue{Title: "t"}
			setNonZero(t, reflect.ValueOf(issue).Elem().FieldByName(name))

			w := &stubWire{}
			_, err := createRole(t, w).Create(t.Context(), issueops.CreateRequest{Actor: "writer", Issue: issue})
			if !errors.Is(err, issueops.ErrValidation) {
				t.Errorf("Issue.%s error = %v, want ErrValidation — a local backend refuses it too", name, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("Issue.%s reached the wire: %v", name, w.calls)
			}
		}
	})

	t.Run("a CreatedBy naming the actor rides the server's stamp", func(t *testing.T) {
		// Every create shape stamps created_by from the actor, so the one value
		// that stamp writes is carried BY it — and it is what every CLI create
		// sends. Any other value would be silently replaced, which is why the
		// sweep above still refuses CreatedBy: "sentinel" is never the actor.
		w := &stubWire{}
		if _, err := createRole(t, w).Create(t.Context(), issueops.CreateRequest{
			Actor: "writer",
			Issue: &issueops.Issue{Title: "t", CreatedBy: "writer"},
		}); err != nil {
			t.Fatalf("Create with CreatedBy == Actor = %v, want it carried by the server's stamp", err)
		}
		if w.lastCreate.Actor != "writer" {
			t.Errorf("sent actor = %q, want the creator the stamp will write", w.lastCreate.Actor)
		}
	})

	t.Run("the request's own excluded member refuses where it would act", func(t *testing.T) {
		w := &stubWire{}
		_, err := createRole(t, w).Create(t.Context(), issueops.CreateRequest{
			Actor:    "writer",
			IDPrefix: "other",
			Issue:    &issueops.Issue{ID: "other-1", Title: "t"},
		})
		assertRefusedBy(t, err, "W-CreateRequest.IDPrefix")
		if len(w.calls) != 0 {
			t.Errorf("the prefix override reached the wire: %v", w.calls)
		}
	})

	t.Run("the request's own excluded member is inert where it would not act", func(t *testing.T) {
		// The role reads IDPrefix only to check an explicit, unforced id. `bd
		// create` sends the workspace's prefix on EVERY create, so refusing it
		// on a minted or forced id would refuse every create in a workspace
		// whose config.yaml names a prefix.
		for name, req := range map[string]issueops.CreateRequest{
			"a minted id": {Issue: &issueops.Issue{Title: "t"}},
			"a forced id": {Issue: &issueops.Issue{ID: "other-1", Title: "t"}, ForceIDPrefix: true},
		} {
			t.Run(name, func(t *testing.T) {
				w := &stubWire{}
				request := req
				request.Actor = "writer"
				request.IDPrefix = "other"
				if _, err := createRole(t, w).Create(t.Context(), request); err != nil {
					t.Fatalf("Create = %v, want the inert override sent without", err)
				}
				if got := derefOr(w.lastCreate.Id, ""); got != request.Issue.ID {
					t.Errorf("sent id = %q, want %q", got, request.Issue.ID)
				}
				if forced := w.lastCreate.ForceIdPrefix != nil && *w.lastCreate.ForceIdPrefix; forced != request.ForceIDPrefix {
					t.Errorf("sent force_id_prefix = %v, want %v", forced, request.ForceIDPrefix)
				}
			})
		}
	})

	t.Run("a metadata blob that is not JSON is ErrValidation, not a marshal fault", func(t *testing.T) {
		// The blob's CONTENT is the role's business and travels verbatim; its
		// well-formedness is this layer's, because bytes that are not JSON
		// cannot go on a JSON wire and would otherwise fail inside
		// json.Marshal — a transport fault where the contract promises a
		// deterministic validation failure.
		for name, req := range map[string]issueops.CreateRequest{
			"on the issue": {
				Issue: &issueops.Issue{Title: "t", Metadata: []byte(`{"k":`)},
			},
			"on an edge": {
				Issue:        &issueops.Issue{Title: "t"},
				Dependencies: []issueops.CreateDependency{{TargetID: "bd-9", Type: types.DepBlocks, Metadata: `not json`}},
			},
		} {
			t.Run(name, func(t *testing.T) {
				w := &stubWire{}
				request := req
				request.Actor = "writer"
				if _, err := createRole(t, w).Create(t.Context(), request); !errors.Is(err, issueops.ErrValidation) {
					t.Errorf("Create with a malformed metadata blob = %v, want ErrValidation", err)
				}
				if len(w.calls) != 0 {
					t.Errorf("the malformed blob reached the wire: %v", w.calls)
				}
			})
		}
	})

	t.Run("the edge's one", func(t *testing.T) {
		// ONE, not the batch's three: createIssue's edge publishes `reverse`
		// and `metadata`, because a create has an id for a target to point back
		// at. Only the thread has no member on any operation.
		w := &stubWire{}
		_, err := createRole(t, w).Create(t.Context(), issueops.CreateRequest{
			Actor: "writer",
			Issue: &issueops.Issue{Title: "t"},
			Dependencies: []issueops.CreateDependency{
				{TargetID: "bd-9", Type: types.DepBlocks, ThreadID: "th-1"},
			},
		})
		assertRefusedBy(t, err, "W-CreateDependency.ThreadID")
		if len(w.calls) != 0 {
			t.Errorf("the thread id reached the wire: %v", w.calls)
		}
	})

	t.Run("the request has exactly the members this gate accounts for", func(t *testing.T) {
		// The CONTAINER above types.Issue, which no other gate can see: the
		// createIssue writeShape classifies these fields against the body, but
		// a field added to CreateRequest tomorrow and then carried by a new
		// wire member would leave this gate's own reasoning stale without
		// anything saying so.
		got := populatableFields(reflect.TypeOf(issueops.CreateRequest{}))
		sort.Strings(got)
		want := []string{
			"Actor", "DefaultPriority", "Dependencies", "ForceIDPrefix", "IDPrefix",
			"InheritLabelsFromParent", "Issue", "ParentID", "WaitsFor",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("CreateRequest carries %v, want %v.\n"+
				"A new member reaches no wire member and no refusal: carry it in createBody, or refuse it with a ledger row.", got, want)
		}
	})
}

// createRoleRefusedIssueMembers are the two members the ROLE refuses in its own
// right, so the refusal is ErrValidation on every backend rather than a
// divergence: "Issue.Comments and Issue.Dependencies must be empty; supply edges
// through the request's own Dependencies field" (issueops.CreateRequest.Issue).
var createRoleRefusedIssueMembers = map[string]string{
	"Comments":     "the role refuses a create carrying comments",
	"Dependencies": "the role refuses an inline edge list; edges ride the request's own Dependencies",
}

// createWireRefusedIssueMembers is the complement the gate drives: every
// populatable field of types.Issue that is neither carried by this operation's
// vocabulary, nor ignored by the role, nor refused by the role in its own right.
func createWireRefusedIssueMembers(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, name := range populatableFields(reflect.TypeOf(issueops.Issue{})) {
		if _, ok := createCarriedIssueMembers[name]; ok {
			continue
		}
		if _, ok := roleIgnoredCreateIssueMembers[name]; ok {
			continue
		}
		if _, ok := createRoleRefusedIssueMembers[name]; ok {
			continue
		}
		out = append(out, name)
	}
	return out
}

func createRole(t *testing.T, w *stubWire) issueops.Lifecycle {
	t.Helper()
	lifecycle, err := stubStore(t, w).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	return lifecycle
}
