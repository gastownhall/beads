package issueops

import (
	"encoding/json"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// CloneCreateRequest returns a deep copy of request.
func CloneCreateRequest(request publicops.CreateRequest) publicops.CreateRequest {
	clone := request
	clone.Issue = clonePublicIssue(request.Issue)
	clone.Dependencies = append([]publicops.CreateDependency(nil), request.Dependencies...)
	if request.WaitsFor != nil {
		waitsFor := *request.WaitsFor
		clone.WaitsFor = &waitsFor
	}
	return clone
}

// CloneUpdateRequest returns a deep copy of request.
func CloneUpdateRequest(request publicops.UpdateRequest) publicops.UpdateRequest {
	clone := request
	clone.Patch.Labels.Add = append([]string(nil), request.Patch.Labels.Add...)
	clone.Patch.Labels.Remove = append([]string(nil), request.Patch.Labels.Remove...)
	clone.Patch.Labels.Replace.Value = append([]string(nil), request.Patch.Labels.Replace.Value...)
	clone.Patch.Metadata.Replace.Value = cloneRawMessage(request.Patch.Metadata.Replace.Value)
	clone.Patch.Metadata.Merge.Value = cloneRawMessage(request.Patch.Metadata.Merge.Value)
	clone.Patch.Metadata.Set = cloneRawMessageMap(request.Patch.Metadata.Set)
	clone.Patch.Metadata.Unset = append([]string(nil), request.Patch.Metadata.Unset...)
	clone.Patch.EstimatedMinutes.Value = cloneInt(request.Patch.EstimatedMinutes.Value)
	clone.Patch.ExternalRef.Value = cloneString(request.Patch.ExternalRef.Value)
	clone.Patch.DueAt.Value = cloneTime(request.Patch.DueAt.Value)
	clone.Patch.DeferUntil.Value = cloneTime(request.Patch.DeferUntil.Value)
	clone.ExpectedVersion = cloneInt64(request.ExpectedVersion)
	clone.ExpectedAssignee = cloneString(request.ExpectedAssignee)
	if request.ExpectedStatus != nil {
		status := *request.ExpectedStatus
		clone.ExpectedStatus = &status
	}
	return clone
}

// CloneCloseRequest returns a deep copy of request.
func CloneCloseRequest(request publicops.CloseRequest) publicops.CloseRequest {
	clone := request
	clone.ExpectedVersion = cloneInt64(request.ExpectedVersion)
	return clone
}

// CloneReopenRequest returns a deep copy of request.
func CloneReopenRequest(request publicops.ReopenRequest) publicops.ReopenRequest {
	clone := request
	clone.ExpectedVersion = cloneInt64(request.ExpectedVersion)
	return clone
}

func clonePublicIssue(issue *publicops.Issue) *publicops.Issue {
	if issue == nil {
		return nil
	}
	clone := *issue
	clone.EstimatedMinutes = cloneInt(issue.EstimatedMinutes)
	clone.ExternalRef = cloneString(issue.ExternalRef)
	clone.StartedAt = cloneTime(issue.StartedAt)
	clone.ClosedAt = cloneTime(issue.ClosedAt)
	clone.LeaseExpiresAt = cloneTime(issue.LeaseExpiresAt)
	clone.HeartbeatAt = cloneTime(issue.HeartbeatAt)
	clone.DueAt = cloneTime(issue.DueAt)
	clone.DeferUntil = cloneTime(issue.DeferUntil)
	clone.CompactedAt = cloneTime(issue.CompactedAt)
	clone.CompactedAtCommit = cloneString(issue.CompactedAtCommit)
	clone.Metadata = cloneRawMessage(issue.Metadata)
	clone.Labels = append([]string(nil), issue.Labels...)
	clone.Dependencies = cloneDependencies(issue.Dependencies)
	clone.Comments = cloneComments(issue.Comments)
	clone.BondedFrom = append([]types.BondRef(nil), issue.BondedFrom...)
	clone.Waiters = append([]string(nil), issue.Waiters...)
	clone.WispPlaneOverride = cloneBool(issue.WispPlaneOverride)
	return &clone
}

func cloneDependencies(dependencies []*types.Dependency) []*types.Dependency {
	clone := make([]*types.Dependency, len(dependencies))
	for i, dependency := range dependencies {
		if dependency == nil {
			continue
		}
		copy := *dependency
		clone[i] = &copy
	}
	return clone
}

func cloneComments(comments []*types.Comment) []*types.Comment {
	clone := make([]*types.Comment, len(comments))
	for i, comment := range comments {
		if comment == nil {
			continue
		}
		copy := *comment
		clone[i] = &copy
	}
	return clone
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneRawMessageMap(values map[string]json.RawMessage) map[string]json.RawMessage {
	if values == nil {
		return nil
	}
	clone := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		clone[key] = cloneRawMessage(value)
	}
	return clone
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
