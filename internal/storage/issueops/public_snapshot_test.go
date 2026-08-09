package issueops

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestCloneIssueOperationRequestsDeepCopyMutableFields(t *testing.T) {
	value := 3
	ref := "ref"
	metadata := json.RawMessage(`{"key":"value"}`)
	now := time.Now().UTC()
	request := publicops.CreateRequest{
		Actor:        "actor",
		Issue:        &publicops.Issue{Labels: []string{"one"}, Metadata: metadata, EstimatedMinutes: &value, ExternalRef: &ref, StartedAt: &now, ClosedAt: &now, LeaseExpiresAt: &now, HeartbeatAt: &now, DueAt: &now, DeferUntil: &now, CompactedAt: &now, CompactedAtCommit: &ref, Dependencies: []*types.Dependency{{Metadata: "dependency"}}, Comments: []*types.Comment{{Text: "nested"}}, BondedFrom: []types.BondRef{{SourceID: "source"}}, Waiters: []string{"waiter"}},
		Dependencies: []publicops.CreateDependency{{TargetID: "target"}},
		WaitsFor:     &publicops.WaitsFor{SpawnerID: "parent"},
	}

	cloned := CloneCreateRequest(request)
	cloned.Issue.Labels[0] = "changed"
	cloned.Issue.Metadata[0] = '{'
	*cloned.Issue.EstimatedMinutes = 4
	*cloned.Issue.ExternalRef = "changed"
	cloned.Issue.Waiters[0] = "changed"
	cloned.Issue.Dependencies[0].Metadata = "changed"
	cloned.Issue.Comments[0].Text = "changed"
	cloned.Dependencies[0].TargetID = "changed"
	cloned.WaitsFor.SpawnerID = "changed"

	if request.Issue.Labels[0] != "one" || string(request.Issue.Metadata) != `{"key":"value"}` || *request.Issue.EstimatedMinutes != 3 || *request.Issue.ExternalRef != "ref" || request.Issue.Waiters[0] != "waiter" || request.Issue.Dependencies[0].Metadata != "dependency" || request.Issue.Comments[0].Text != "nested" || request.Dependencies[0].TargetID != "target" || request.WaitsFor.SpawnerID != "parent" {
		t.Fatal("CloneCreateRequest() retained mutable caller-owned state")
	}

	update := publicops.UpdateRequest{Patch: publicops.IssuePatch{Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"key": metadata}, Unset: []string{"remove"}, Replace: publicops.Field[json.RawMessage]{Set: true, Value: metadata}}}}
	update.Patch.Labels.Add = []string{"add"}
	update.Patch.Labels.Remove = []string{"remove-label"}
	update.Patch.Labels.Replace = publicops.Field[[]string]{Set: true, Value: []string{"replacement"}}
	update.Patch.EstimatedMinutes = publicops.Field[*int]{Set: true, Value: &value}
	update.Patch.ExternalRef = publicops.Field[*string]{Set: true, Value: &ref}
	update.Patch.DueAt = publicops.Field[*time.Time]{Set: true, Value: &now}
	update.Patch.DeferUntil = publicops.Field[*time.Time]{Set: true, Value: &now}
	updateClone := CloneUpdateRequest(update)
	updateClone.Patch.Metadata.Set["key"][0] = '{'
	updateClone.Patch.Metadata.Unset[0] = "changed"
	updateClone.Patch.Metadata.Replace.Value[0] = '{'
	updateClone.Patch.Labels.Add[0] = "changed"
	updateClone.Patch.Labels.Remove[0] = "changed"
	updateClone.Patch.Labels.Replace.Value[0] = "changed"
	*updateClone.Patch.EstimatedMinutes.Value = 5
	*updateClone.Patch.ExternalRef.Value = "changed"
	*updateClone.Patch.DueAt.Value = now.Add(time.Hour)
	*updateClone.Patch.DeferUntil.Value = now.Add(time.Hour)
	if string(update.Patch.Metadata.Set["key"]) != `{"key":"value"}` || update.Patch.Metadata.Unset[0] != "remove" || string(update.Patch.Metadata.Replace.Value) != `{"key":"value"}` || update.Patch.Labels.Add[0] != "add" || update.Patch.Labels.Remove[0] != "remove-label" || update.Patch.Labels.Replace.Value[0] != "replacement" || *update.Patch.EstimatedMinutes.Value != 3 || *update.Patch.ExternalRef.Value != "ref" || !update.Patch.DueAt.Value.Equal(now) || !update.Patch.DeferUntil.Value.Equal(now) {
		t.Fatal("CloneUpdateRequest() retained mutable caller-owned state")
	}
}

func TestIssueOperationCloneFunctionsKeepFrozenRequestFields(t *testing.T) {
	issueMutable := map[string]bool{"EstimatedMinutes": true, "StartedAt": true, "ClosedAt": true, "LeaseExpiresAt": true, "HeartbeatAt": true, "DueAt": true, "DeferUntil": true, "ExternalRef": true, "CompactedAt": true, "CompactedAtCommit": true, "Metadata": true, "Labels": true, "Dependencies": true, "Comments": true, "BondedFrom": true, "Waiters": true, "WispPlaneOverride": true}
	for index := 0; index < reflect.TypeOf(types.Issue{}).NumField(); index++ {
		field := reflect.TypeOf(types.Issue{}).Field(index)
		if isMutableKind(field.Type.Kind()) && !issueMutable[field.Name] {
			t.Errorf("clonePublicIssue must deep-copy newly mutable Issue field %s", field.Name)
		}
	}

	requestMutable := map[reflect.Type]map[string]bool{
		reflect.TypeOf(publicops.CreateRequest{}): {"Issue": true, "Dependencies": true, "WaitsFor": true},
		reflect.TypeOf(publicops.UpdateRequest{}): {"ExpectedVersion": true, "ExpectedAssignee": true, "ExpectedStatus": true},
		reflect.TypeOf(publicops.CloseRequest{}):  {"ExpectedVersion": true},
		reflect.TypeOf(publicops.ReopenRequest{}): {"ExpectedVersion": true},
	}
	for typeOf, allowlist := range requestMutable {
		for index := 0; index < typeOf.NumField(); index++ {
			field := typeOf.Field(index)
			if isMutableKind(field.Type.Kind()) && !allowlist[field.Name] {
				t.Errorf("request clone must deep-copy newly mutable %s field %s", typeOf, field.Name)
			}
		}
	}

	assertMutableFields(t, reflect.TypeOf(publicops.LabelPatch{}), map[string]bool{"Add": true, "Remove": true})
	assertMutableFields(t, reflect.TypeOf(publicops.MetadataPatch{}), map[string]bool{"Set": true, "Unset": true})
	assertFieldValueMutable(t, reflect.TypeOf(publicops.Field[[]string]{}), "LabelPatch.Replace")
	assertFieldValueMutable(t, reflect.TypeOf(publicops.Field[json.RawMessage]{}), "MetadataPatch.Replace")
	assertFieldValueMutable(t, reflect.TypeOf(publicops.Field[json.RawMessage]{}), "MetadataPatch.Merge")
	assertFieldValueMutable(t, reflect.TypeOf(publicops.Field[*int]{}), "IssuePatch.EstimatedMinutes")
	assertFieldValueMutable(t, reflect.TypeOf(publicops.Field[*string]{}), "IssuePatch.ExternalRef")
	assertFieldValueMutable(t, reflect.TypeOf(publicops.Field[*time.Time]{}), "IssuePatch.DueAt")
	assertFieldValueMutable(t, reflect.TypeOf(publicops.Field[*time.Time]{}), "IssuePatch.DeferUntil")
}

func isMutableKind(kind reflect.Kind) bool {
	return kind == reflect.Pointer || kind == reflect.Slice || kind == reflect.Map || kind == reflect.Interface
}

func assertMutableFields(t *testing.T, typeOf reflect.Type, allowlist map[string]bool) {
	t.Helper()
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if isMutableKind(field.Type.Kind()) && !allowlist[field.Name] {
			t.Errorf("clone must deep-copy newly mutable %s field %s", typeOf, field.Name)
		}
	}
}

func assertFieldValueMutable(t *testing.T, typeOf reflect.Type, path string) {
	t.Helper()
	field, ok := typeOf.FieldByName("Value")
	if !ok {
		t.Fatalf("%s lacks Value", typeOf)
	}
	if !isMutableKind(field.Type.Kind()) {
		t.Fatalf("%s Value is no longer mutable; update clone coverage", path)
	}
}
