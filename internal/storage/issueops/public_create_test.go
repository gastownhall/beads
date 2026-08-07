package issueops

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestPreparePublicCreateRequestNormalizesAcceptedFieldsAndIgnoresDerivedFields(t *testing.T) {
	createdAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.FixedZone("offset", -7*60*60))
	request := publicops.CreateRequest{Actor: "actor", Issue: &publicops.Issue{
		ID: "bd-public-create", Title: "title", Status: "custom", Priority: 2, IssueType: "custom-type",
		Metadata: json.RawMessage(`{"key":true}`), ContentHash: "caller-hash", RowVersion: 42,
		LeaseExpiresAt: &createdAt, CompactionLevel: 4, IDPrefix: "ignored", PrefixOverride: "ignored", IsLitePartial: true,
	}}

	prepared, err := PreparePublicCreateRequest(request, PublicCreateContext{
		IssuePrefix: "bd", CustomStatuses: []string{"custom"}, CustomTypes: []string{"custom-type"},
	})
	if err != nil {
		t.Fatalf("PreparePublicCreateRequest() error = %v", err)
	}
	if prepared.Issue.ContentHash == "caller-hash" || prepared.Issue.RowVersion != 0 || prepared.Issue.LeaseExpiresAt != nil || prepared.Issue.CompactionLevel != 0 || prepared.Issue.IDPrefix != "" || prepared.Issue.PrefixOverride != "" || prepared.Issue.IsLitePartial {
		t.Fatalf("derived fields survived preparation: %#v", prepared.Issue)
	}
}

func TestPreparePublicCreateRequestCarriesSourceRepo(t *testing.T) {
	prepared, err := PreparePublicCreateRequest(publicops.CreateRequest{Actor: "actor", Issue: &publicops.Issue{
		ID: "bd-source-repo", Title: "title", IssueType: types.TypeTask, Priority: 2,
		SourceSystem: "github", SourceRepo: "other/repo",
	}}, PublicCreateContext{IssuePrefix: "bd"})
	if err != nil {
		t.Fatalf("PreparePublicCreateRequest() error = %v", err)
	}
	if prepared.Issue.SourceRepo != "other/repo" || prepared.Issue.SourceSystem != "github" {
		t.Fatalf("prepared source = %q/%q, want github/other/repo", prepared.Issue.SourceSystem, prepared.Issue.SourceRepo)
	}
}

func TestPublicCreateIssueFieldClassificationIsComplete(t *testing.T) {
	accepted := map[string]bool{
		"ID": true, "Title": true, "Description": true, "Design": true, "AcceptanceCriteria": true, "Notes": true, "SpecID": true,
		"Status": true, "Priority": true, "IssueType": true, "Assignee": true, "Owner": true, "EstimatedMinutes": true,
		"CreatedAt": true, "CreatedBy": true, "UpdatedAt": true, "StartedAt": true, "ClosedAt": true, "CloseReason": true, "ClosedBySession": true,
		"DueAt": true, "DeferUntil": true, "ExternalRef": true, "SourceSystem": true, "SourceRepo": true, "Metadata": true, "Labels": true,
		"Sender": true, "Ephemeral": true, "NoHistory": true, "WispType": true, "StorageClass": true, "Pinned": true, "IsTemplate": true,
		"BondedFrom": true, "AwaitType": true, "AwaitID": true, "Timeout": true, "Waiters": true, "SourceFormula": true, "SourceLocation": true,
		"MolType": true, "WorkType": true, "EventKind": true, "Actor": true, "Target": true, "Payload": true,
	}
	ignored := map[string]bool{
		"ContentHash": true, "LeaseExpiresAt": true, "HeartbeatAt": true, "LeaseGrantedNode": true, "RowVersion": true,
		"CompactionLevel": true, "CompactedAt": true, "CompactedAtCommit": true, "OriginalSize": true,
		"IDPrefix": true, "PrefixOverride": true, "IsLitePartial": true,
		// WispPlaneOverride is import-plumbing (the export stream's explicit
		// plane marker, bd-r9uce); a public create routes by the flags it
		// accepts (Ephemeral/NoHistory), so the override is dropped here.
		"WispPlaneOverride": true,
	}
	rejected := map[string]bool{"Dependencies": true, "Comments": true}
	issueType := reflect.TypeFor[types.Issue]()
	for field := range issueType.NumField() {
		name := issueType.Field(field).Name
		if accepted[name] || ignored[name] || rejected[name] {
			continue
		}
		t.Errorf("types.Issue field %q is unclassified at the public create boundary", name)
	}
	for name := range accepted {
		if _, ok := issueType.FieldByName(name); !ok {
			t.Errorf("accepted field %q is not a types.Issue field", name)
		}
	}
	for name := range ignored {
		if _, ok := issueType.FieldByName(name); !ok {
			t.Errorf("ignored field %q is not a types.Issue field", name)
		}
	}
	for name := range rejected {
		if _, ok := issueType.FieldByName(name); !ok {
			t.Errorf("rejected field %q is not a types.Issue field", name)
		}
	}
}

func TestPreparePublicCreateRequestRejectsDependencyDuplicatesAndSelfEdges(t *testing.T) {
	for _, tc := range []struct {
		name     string
		request  publicops.CreateRequest
		conflict bool
	}{
		{"same type duplicate", publicops.CreateRequest{Actor: "a", Issue: &publicops.Issue{ID: "bd-new", Title: "x", IssueType: types.TypeTask, Priority: 2}, ParentID: "bd-parent", Dependencies: []publicops.CreateDependency{{TargetID: "bd-parent", Type: types.DepParentChild}}}, false},
		{"different type duplicate", publicops.CreateRequest{Actor: "a", Issue: &publicops.Issue{ID: "bd-new", Title: "x", IssueType: types.TypeTask, Priority: 2}, ParentID: "bd-parent", Dependencies: []publicops.CreateDependency{{TargetID: "bd-parent", Type: types.DepBlocks}}}, true},
		{"explicit self edge", publicops.CreateRequest{Actor: "a", Issue: &publicops.Issue{ID: "bd-new", Title: "x", IssueType: types.TypeTask, Priority: 2}, Dependencies: []publicops.CreateDependency{{TargetID: "bd-new", Type: types.DepBlocks}}}, false},
		{"generated ID duplicate", publicops.CreateRequest{Actor: "a", Issue: &publicops.Issue{Title: "x", IssueType: types.TypeTask, Priority: 2}, ParentID: "bd-parent", Dependencies: []publicops.CreateDependency{{TargetID: "bd-parent", Type: types.DepParentChild}}}, false},
		{"duplicate reverse edge", publicops.CreateRequest{Actor: "a", Issue: &publicops.Issue{ID: "bd-new", Title: "x", IssueType: types.TypeTask, Priority: 2}, Dependencies: []publicops.CreateDependency{{TargetID: "bd-target", Type: types.DepBlocks, Reverse: true}, {TargetID: "bd-target", Type: types.DepBlocks, Reverse: true}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PreparePublicCreateRequest(tc.request, PublicCreateContext{IssuePrefix: "bd"})
			if !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
			var conflict *domain.DependencyTypeConflictError
			if errors.As(err, &conflict) != tc.conflict {
				t.Fatalf("conflict identity = %v, want %v (%v)", errors.As(err, &conflict), tc.conflict, err)
			}
		})
	}
}

func TestPreparePublicCreateRequestAllowsOppositeDependencyDirections(t *testing.T) {
	request := publicops.CreateRequest{
		Actor: "a",
		Issue: &publicops.Issue{ID: "bd-new", Title: "x", IssueType: types.TypeTask, Priority: 2},
		Dependencies: []publicops.CreateDependency{
			{TargetID: "bd-target", Type: types.DepBlocks},
			{TargetID: "bd-target", Type: types.DepBlocks, Reverse: true},
		},
	}

	if _, err := PreparePublicCreateRequest(request, PublicCreateContext{IssuePrefix: "bd"}); err != nil {
		t.Fatalf("PreparePublicCreateRequest() error = %v, want nil", err)
	}
}

func TestValidatePublicCreateRequestChecksDependenciesBeforeContextPreparation(t *testing.T) {
	request := publicops.CreateRequest{
		Actor:        "a",
		Issue:        &publicops.Issue{Title: "x"},
		ParentID:     "bd-parent",
		Dependencies: []publicops.CreateDependency{{TargetID: "bd-parent", Type: types.DepParentChild}},
	}

	err := ValidatePublicCreateRequest(request)
	if !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("ValidatePublicCreateRequest() error = %v, want ErrValidation", err)
	}
}

func TestValidatePublicCreateRequestAllowsEmptyWaitsForGate(t *testing.T) {
	err := ValidatePublicCreateRequest(publicops.CreateRequest{
		Actor:    "a",
		Issue:    &publicops.Issue{Title: "x"},
		WaitsFor: &publicops.WaitsFor{SpawnerID: "bd-spawner"},
	})
	if err != nil {
		t.Fatalf("ValidatePublicCreateRequest() error = %v, want nil", err)
	}
}

// TestValidatePublicCreateRequestRejectsUnknownWaitsForGate is the other half
// of the clause above: the leaf says an empty Gate defaults to
// WaitsForAllChildren and that "otherwise only the exported waits-for gate
// constants are valid" (issueops/issueops.go:149-152). Only the permissive
// half was pinned, so an unknown gate could have started defaulting silently —
// which is a readiness primitive quietly answering the wrong question rather
// than refusing.
//
// This lives at the shared validator rather than in the conformance contract
// because all three Lifecycle backends reach it: the two stores through
// ExecuteCreate and the unit-of-work one through its own Create
// (internal/storage/uow/issue_operations.go:64).
func TestValidatePublicCreateRequestRejectsUnknownWaitsForGate(t *testing.T) {
	for _, gate := range []string{"bogus", "ALL_CHILDREN", " all_children"} {
		t.Run(gate, func(t *testing.T) {
			err := ValidatePublicCreateRequest(publicops.CreateRequest{
				Actor:    "a",
				Issue:    &publicops.Issue{Title: "x"},
				WaitsFor: &publicops.WaitsFor{SpawnerID: "bd-spawner", Gate: gate},
			})
			if !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidatePublicCreateRequest() with gate %q error = %v, want ErrValidation", gate, err)
			}
		})
	}
}

// TestValidatePublicCreateRequestAcceptsTheExportedWaitsForGates keeps the
// refusal above honest: the two exported constants must still pass, so the
// check cannot be tightened into rejecting everything.
func TestValidatePublicCreateRequestAcceptsTheExportedWaitsForGates(t *testing.T) {
	for _, gate := range []string{string(publicops.WaitsForAllChildren), string(publicops.WaitsForAnyChildren)} {
		t.Run(gate, func(t *testing.T) {
			err := ValidatePublicCreateRequest(publicops.CreateRequest{
				Actor:    "a",
				Issue:    &publicops.Issue{Title: "x"},
				WaitsFor: &publicops.WaitsFor{SpawnerID: "bd-spawner", Gate: gate},
			})
			if err != nil {
				t.Fatalf("ValidatePublicCreateRequest() with gate %q error = %v, want nil", gate, err)
			}
		})
	}
}

func TestValidatePublicCreateRequestRejectsInvalidImportedRelations(t *testing.T) {
	checks := []struct {
		name    string
		request publicops.CreateRequest
	}{
		{
			name: "malformed dependency metadata",
			request: publicops.CreateRequest{
				Actor:        "actor",
				Issue:        &publicops.Issue{Title: "title"},
				Dependencies: []publicops.CreateDependency{{TargetID: "bd-target", Type: types.DepRelated, Metadata: "{"}},
			},
		},
		{
			name: "overlong dependency thread",
			request: publicops.CreateRequest{
				Actor:        "actor",
				Issue:        &publicops.Issue{Title: "title"},
				Dependencies: []publicops.CreateDependency{{TargetID: "bd-target", Type: types.DepRelated, ThreadID: strings.Repeat("t", types.MaxFieldLen+1)}},
			},
		},
	}

	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			err := ValidatePublicCreateRequest(check.request)
			if !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidatePublicCreateRequest() error = %v, want ErrValidation", err)
			}
		})
	}
}

func TestValidatePublicCreateRequestRejectsOverlongRelationshipIDs(t *testing.T) {
	overlong := strings.Repeat("x", types.MaxFieldLen+1)
	checks := []struct {
		name    string
		request publicops.CreateRequest
		field   string
	}{
		{
			name:    "parent ID",
			request: publicops.CreateRequest{Actor: "actor", Issue: &publicops.Issue{Title: "title"}, ParentID: overlong},
			field:   "parent",
		},
		{
			name: "dependency target ID",
			request: publicops.CreateRequest{
				Actor:        "actor",
				Issue:        &publicops.Issue{Title: "title"},
				Dependencies: []publicops.CreateDependency{{TargetID: overlong, Type: types.DepRelated}},
			},
			field: "dependency target",
		},
		{
			name: "waits-for spawner ID",
			request: publicops.CreateRequest{
				Actor:    "actor",
				Issue:    &publicops.Issue{Title: "title"},
				WaitsFor: &publicops.WaitsFor{SpawnerID: overlong},
			},
			field: "waits-for spawner",
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			err := ValidatePublicCreateRequest(check.request)
			if !errors.Is(err, storage.ErrValidation) || !errors.Is(err, types.ErrFieldTooLong) {
				t.Fatalf("ValidatePublicCreateRequest() error = %v, want ErrValidation and ErrFieldTooLong", err)
			}
			if !strings.Contains(err.Error(), check.field) {
				t.Fatalf("ValidatePublicCreateRequest() error = %v, want field %q", err, check.field)
			}
		})
	}
}

type sqlStateError string

func (e sqlStateError) Error() string    { return string(e) }
func (e sqlStateError) SQLState() string { return string(e) }

func TestValidatePublicCreateRequestRejectsOverlongLabelsBeforeOpeningUOW(t *testing.T) {
	err := ValidatePublicCreateRequest(publicops.CreateRequest{
		Actor: "actor",
		Issue: &publicops.Issue{Title: "title", Labels: []string{strings.Repeat("x", types.MaxFieldLen+1)}},
	})
	if !errors.Is(err, storage.ErrValidation) || !errors.Is(err, types.ErrFieldTooLong) {
		t.Fatalf("ValidatePublicCreateRequest() error = %v, want ErrValidation and ErrFieldTooLong", err)
	}
}

func TestValidatePublicCreateRequestChecksAssigneeBeforeOwner(t *testing.T) {
	err := ValidatePublicCreateRequest(publicops.CreateRequest{Actor: "actor", Issue: &publicops.Issue{
		Assignee: strings.Repeat("a", types.MaxFieldLen+1),
		Owner:    strings.Repeat("o", types.MaxFieldLen+1),
	}})
	if !errors.Is(err, types.ErrFieldTooLong) || !strings.Contains(err.Error(), "assignee") {
		t.Fatalf("field-length error = %v, want assignee first", err)
	}
}

func TestClassifyPublicCreateErrorPreservesDeterministicIdentities(t *testing.T) {
	hierarchy := &domain.DependencyHierarchyConflictError{}
	for _, err := range []error{types.ErrFieldTooLong, domain.ErrDependencyCycle, hierarchy} {
		classified := ClassifyPublicCreateError(err)
		if !errors.Is(classified, storage.ErrValidation) || !errors.Is(classified, err) {
			t.Fatalf("ClassifyPublicCreateError(%v) = %v, want validation and original identity", err, classified)
		}
	}
	classified := ClassifyPublicCreateError(sqlStateError("23505"))
	if !errors.Is(classified, storage.ErrAlreadyExists) || !errors.Is(classified, sqlStateError("23505")) {
		t.Fatalf("ClassifyPublicCreateError(23505) = %v, want ErrAlreadyExists and SQLSTATE", classified)
	}
}

// TestPreparePublicCreateRequestHonorsTheCallerSuppliedIDPrefix pins
// CreateRequest.IDPrefix: the caller's prefix overrides the substrate's, and an
// empty one leaves the substrate's in force.
//
// The override exists because config.yaml's `issue-prefix` beats the
// database's and no implementation can see config.yaml. Without it the two
// `bd create` routes disagreed about which ids a workspace may mint: the
// direct route refused `bd-123` in a workspace configured for `app`, while the
// proxied route checked only the server database's prefix and created it.
func TestPreparePublicCreateRequestHonorsTheCallerSuppliedIDPrefix(t *testing.T) {
	newRequest := func(id, prefix string) publicops.CreateRequest {
		return publicops.CreateRequest{
			Actor:    "actor",
			Issue:    &publicops.Issue{ID: id, Title: "title", Priority: 2, IssueType: "task"},
			IDPrefix: prefix,
		}
	}
	// The substrate says "bd"; the caller says the workspace is "app".
	context := PublicCreateContext{IssuePrefix: "bd"}

	if _, err := PreparePublicCreateRequest(newRequest("app-1", "app"), context); err != nil {
		t.Errorf("an id matching the CALLER's prefix was refused: %v", err)
	}
	if _, err := PreparePublicCreateRequest(newRequest("bd-1", "app"), context); err == nil {
		t.Error("an id matching only the SUBSTRATE's prefix was accepted; the caller's prefix must win")
	}

	// Empty override: the substrate's prefix is the whole rule, which is the
	// ordinary case and must not regress.
	if _, err := PreparePublicCreateRequest(newRequest("bd-1", ""), context); err != nil {
		t.Errorf("an id matching the substrate's prefix was refused with no override: %v", err)
	}
	if _, err := PreparePublicCreateRequest(newRequest("app-1", ""), context); err == nil {
		t.Error("an id outside the substrate's prefix was accepted with no override")
	}

	// ForceIDPrefix still outranks both, which is what --force means.
	forced := newRequest("zz-1", "app")
	forced.ForceIDPrefix = true
	if _, err := PreparePublicCreateRequest(forced, context); err != nil {
		t.Errorf("ForceIDPrefix did not bypass the caller-supplied prefix: %v", err)
	}
}
