package storage

import (
	"encoding/json"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// CreatePublicCreateDependencies returns the dependency snapshot described by
// a public create request after its issue ID is known.
//
// It is derived from the request rather than from the created issue because a
// reverse edge is not an outgoing edge of the new issue: it belongs to the
// target, whose own hooks still need to see it.
func CreatePublicCreateDependencies(issueID string, request issueops.CreateRequest) []*types.Dependency {
	dependencies := make([]*types.Dependency, 0, len(request.Dependencies)+2)
	if request.ParentID != "" {
		dependencies = append(dependencies, &types.Dependency{IssueID: issueID, DependsOnID: request.ParentID, Type: types.DepParentChild})
	}
	if request.WaitsFor != nil {
		gate := request.WaitsFor.Gate
		if gate == "" {
			gate = types.WaitsForAllChildren
		}
		metadata, _ := json.Marshal(types.WaitsForMeta{Gate: gate})
		dependencies = append(dependencies, &types.Dependency{IssueID: issueID, DependsOnID: request.WaitsFor.SpawnerID, Type: types.DepWaitsFor, Metadata: string(metadata)})
	}
	for _, dependency := range request.Dependencies {
		source, target := issueID, dependency.TargetID
		if dependency.Reverse {
			source, target = target, issueID
		}
		dependencies = append(dependencies, &types.Dependency{IssueID: source, DependsOnID: target, Type: dependency.Type, Metadata: dependency.Metadata, ThreadID: dependency.ThreadID})
	}
	return dependencies
}
