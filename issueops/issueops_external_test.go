package issueops_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/issueops"
)

func TestPublicDeclarationsHaveUsefulZeroValues(t *testing.T) {
	var _ issueops.Lifecycle = operationsProbe{}
	var create issueops.CreateRequest
	var update issueops.UpdateRequest
	var closeRequest issueops.CloseRequest
	var reopen issueops.ReopenRequest
	if create.Issue != nil || update.ExpectedVersion != nil || closeRequest.ExpectedVersion != nil || reopen.ExpectedVersion != nil {
		t.Fatal("zero-value request unexpectedly carries a value")
	}
}

func TestFieldDistinguishesOmittedFromExplicitZero(t *testing.T) {
	var omitted issueops.Field[int]
	explicit := issueops.Field[int]{Set: true, Value: 0}
	if omitted.Set || !explicit.Set || explicit.Value != 0 {
		t.Fatalf("field semantics lost: %#v %#v", omitted, explicit)
	}

	clear := issueops.Field[*string]{Set: true, Value: nil}
	if !clear.Set || clear.Value != nil {
		t.Fatalf("explicit pointer clear lost: %#v", clear)
	}
}

func TestPatchesRepresentExplicitEmptyReplacements(t *testing.T) {
	labels := issueops.LabelPatch{
		Replace: issueops.Field[[]string]{Set: true, Value: []string{}},
	}
	if !labels.Replace.Set || labels.Replace.Value == nil || len(labels.Replace.Value) != 0 {
		t.Fatalf("explicit empty label replacement lost: %#v", labels.Replace)
	}

	metadata := issueops.MetadataPatch{
		Replace: issueops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{}`)},
	}
	if !metadata.Replace.Set || string(metadata.Replace.Value) != `{}` {
		t.Fatalf("explicit empty metadata replacement lost: %#v", metadata.Replace)
	}
}

func TestPersistencePatchHasOneTypedMode(t *testing.T) {
	patchType := reflect.TypeFor[issueops.IssuePatch]()
	if _, found := patchType.FieldByName("Ephemeral"); found {
		t.Fatal("IssuePatch still exposes independent Ephemeral")
	}
	if _, found := patchType.FieldByName("NoHistory"); found {
		t.Fatal("IssuePatch still exposes independent NoHistory")
	}
	persistence, found := patchType.FieldByName("Persistence")
	if !found {
		t.Fatal("IssuePatch.Persistence is missing")
	}
	if persistence.Type != reflect.TypeFor[issueops.Field[issueops.PersistenceMode]]() {
		t.Fatalf("IssuePatch.Persistence type = %v, want Field[PersistenceMode]", persistence.Type)
	}
}

func TestPublicPersistenceModeValues(t *testing.T) {
	cases := []struct {
		got  issueops.PersistenceMode
		want string
	}{
		{issueops.PersistenceModePersistent, "persistent"},
		{issueops.PersistenceModeEphemeral, "ephemeral"},
		{issueops.PersistenceModeNoHistory, "no_history"},
	}
	for _, tc := range cases {
		if string(tc.got) != tc.want {
			t.Errorf("persistence mode = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestClaimRequestSupportsPostClaimPatch(t *testing.T) {
	version := int64(7)
	request := issueops.UpdateRequest{
		Actor:           "worker",
		IssueID:         "bd-123",
		Claim:           true,
		ExpectedVersion: &version,
		Patch: issueops.IssuePatch{
			Status:   issueops.Field[issueops.Status]{Set: true, Value: issueops.StatusBlocked},
			Assignee: issueops.Field[string]{Set: true, Value: "dispatcher"},
		},
	}
	if !request.Claim || request.Patch.Status.Value != issueops.StatusBlocked || request.Patch.Assignee.Value != "dispatcher" || *request.ExpectedVersion != version {
		t.Fatalf("claim request lost its explicit post-claim patch or version guard: %#v", request)
	}
}

func TestForceAssigneeTransferHasSafeZeroValue(t *testing.T) {
	var guarded issueops.UpdateRequest
	forced := issueops.UpdateRequest{ForceAssigneeTransfer: true}
	if guarded.ForceAssigneeTransfer || !forced.ForceAssigneeTransfer {
		t.Fatalf("force-assignee-transfer declaration lost bool zero-value semantics: %#v %#v", guarded, forced)
	}
}

func TestPublicConstantsKeepCanonicalValues(t *testing.T) {
	statuses := []struct {
		got  issueops.Status
		want beads.Status
	}{
		{issueops.StatusOpen, beads.StatusOpen},
		{issueops.StatusInProgress, beads.StatusInProgress},
		{issueops.StatusBlocked, beads.StatusBlocked},
		{issueops.StatusDeferred, beads.StatusDeferred},
		{issueops.StatusClosed, beads.StatusClosed},
	}
	for _, check := range statuses {
		if check.got != check.want {
			t.Fatalf("status constant = %q, want %q", check.got, check.want)
		}
	}

	for name, got := range map[string]string{
		"pinned":       string(issueops.StatusPinned),
		"hooked":       string(issueops.StatusHooked),
		"all-children": issueops.WaitsForAllChildren,
		"any-children": issueops.WaitsForAnyChildren,
	} {
		if got != name {
			t.Errorf("public constant = %q, want %q", got, name)
		}
	}
}

func TestPublicErrorsKeepCanonicalIdentity(t *testing.T) {
	checks := []struct {
		name      string
		got, want error
	}{
		{"not found", issueops.ErrNotFound, beads.ErrNotFound},
		{"field too long", issueops.ErrFieldTooLong, beads.ErrFieldTooLong},
		{"already claimed", issueops.ErrAlreadyClaimed, beads.ErrAlreadyClaimed},
		{"not claimable", issueops.ErrNotClaimable, beads.ErrNotClaimable},
		{"close blocked", issueops.ErrCloseBlocked, beads.ErrCloseBlocked},
		{"version mismatch", issueops.ErrVersionMismatch, beads.ErrVersionMismatch},
		{"self dependency", issueops.ErrSelfDependency, beads.ErrSelfDependency},
		{"dependency cycle", issueops.ErrDependencyCycle, beads.ErrDependencyCycle},
	}
	for _, check := range checks {
		if !errors.Is(check.got, check.want) {
			t.Errorf("%s error identity lost: %v", check.name, check.got)
		}
	}

	for name, err := range map[string]error{
		"validation":        issueops.ErrValidation,
		"not initialized":   issueops.ErrNotInitialized,
		"prefix mismatch":   issueops.ErrPrefixMismatch,
		"assignee mismatch": issueops.ErrAssigneeMismatch,
		"status mismatch":   issueops.ErrStatusMismatch,
		"already exists":    issueops.ErrAlreadyExists,
	} {
		if err == nil {
			t.Errorf("%s sentinel is nil", name)
		}
	}

	classified := errors.Join(issueops.ErrValidation, issueops.ErrPrefixMismatch)
	if !errors.Is(classified, issueops.ErrValidation) {
		t.Error("deterministic validation failure lost ErrValidation classification")
	}
	if !errors.Is(classified, issueops.ErrPrefixMismatch) {
		t.Error("deterministic validation failure lost its specific classification")
	}
}

func TestOpenChildrenCloseContractShape(t *testing.T) {
	err := &issueops.CloseOpenChildrenError{
		IssueID:      "bd-parent",
		OpenChildren: 3,
	}
	if !errors.Is(err, issueops.ErrCloseOpenChildren) {
		t.Fatalf("CloseOpenChildrenError does not match ErrCloseOpenChildren: %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "bd-parent") || !strings.Contains(got, "3") {
		t.Errorf("CloseOpenChildrenError = %q, want issue and count", got)
	}

	result := issueops.CloseResult{OpenChildren: 3}
	if result.OpenChildren != 3 {
		t.Fatalf("CloseResult.OpenChildren = %d, want 3", result.OpenChildren)
	}
}

func TestPublicDependencyConflictTypesRemainCanonical(t *testing.T) {
	var typeConflict error = &issueops.DependencyTypeConflictError{}
	var hierarchyConflict error = &issueops.DependencyHierarchyConflictError{}
	if typeConflict == nil || hierarchyConflict == nil {
		t.Fatal("dependency conflict aliases lost their error implementations")
	}
	if reflect.TypeFor[*issueops.DependencyTypeConflictError]() != reflect.TypeFor[*beads.DependencyTypeConflictError]() {
		t.Error("DependencyTypeConflictError lost canonical type identity")
	}
	if reflect.TypeFor[*issueops.DependencyHierarchyConflictError]() != reflect.TypeFor[*beads.DependencyHierarchyConflictError]() {
		t.Error("DependencyHierarchyConflictError lost canonical type identity")
	}
}

type operationsProbe struct{}

func (operationsProbe) Create(context.Context, issueops.CreateRequest) (issueops.CreateResult, error) {
	return issueops.CreateResult{}, nil
}
func (operationsProbe) Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error) {
	return issueops.UpdateResult{}, nil
}
func (operationsProbe) Close(context.Context, issueops.CloseRequest) (issueops.CloseResult, error) {
	return issueops.CloseResult{}, nil
}
func (operationsProbe) Reopen(context.Context, issueops.ReopenRequest) (issueops.ReopenResult, error) {
	return issueops.ReopenResult{}, nil
}
