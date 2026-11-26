package gitea

import (
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func encodeMetadata(m map[string]interface{}) (json.RawMessage, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

type giteaFieldMapper struct {
	config *MappingConfig
}

func (m *giteaFieldMapper) PriorityToBeads(trackerPriority interface{}) int {
	if label, ok := trackerPriority.(string); ok {
		if p, exists := m.config.PriorityMap[label]; exists {
			return p
		}
	}
	return 2
}

func (m *giteaFieldMapper) PriorityToTracker(beadsPriority int) interface{} {
	for label, p := range m.config.PriorityMap {
		if p == beadsPriority {
			return label
		}
	}
	return "medium"
}

func (m *giteaFieldMapper) StatusToBeads(trackerState interface{}) types.Status {
	if state, ok := trackerState.(string); ok {
		if status, exists := m.config.StateMap[state]; exists {
			return types.Status(status)
		}
		switch state {
		case "open":
			return types.StatusOpen
		case "closed":
			return types.StatusClosed
		}
	}
	return types.StatusOpen
}

func (m *giteaFieldMapper) StatusToTracker(beadsStatus types.Status) interface{} {
	if beadsStatus == types.StatusClosed {
		return "closed"
	}
	return "open"
}

func (m *giteaFieldMapper) TypeToBeads(trackerType interface{}) types.IssueType {
	if t, ok := trackerType.(string); ok {
		if issueType, exists := m.config.LabelTypeMap[t]; exists {
			return types.IssueType(issueType)
		}
	}
	return types.TypeTask
}

func (m *giteaFieldMapper) TypeToTracker(beadsType types.IssueType) interface{} {
	return string(beadsType)
}

func (m *giteaFieldMapper) IssueToBeads(ti *tracker.TrackerIssue) *tracker.IssueConversion {
	gt, ok := ti.Raw.(*Issue)
	if !ok || gt == nil {
		return nil
	}
	conv := GiteaIssueToBeads(gt, m.config)
	if conv == nil {
		return nil
	}

	var deps []tracker.DependencyInfo
	for _, d := range conv.Dependencies {
		fromID := d.FromRef
		if fromID == "" {
			fromID = fmt.Sprintf("gitea:%d", d.FromNumber)
		}
		toID := d.ToRef
		if toID == "" {
			toID = fmt.Sprintf("gitea:%d", d.ToNumber)
		}
		var fromKey, toKey string
		if d.FromRepoID > 0 {
			fromKey = giteaSourceSystem(d.FromRepoID, "", "", d.FromNumber)
		}
		if d.ToRepoID > 0 {
			toKey = giteaSourceSystem(d.ToRepoID, "", "", d.ToNumber)
		}
		deps = append(deps, tracker.DependencyInfo{
			FromExternalID:  fromID,
			ToExternalID:    toID,
			FromIdentityKey: fromKey,
			ToIdentityKey:   toKey,
			Type:            d.Type,
			Source:          tracker.DependencySourceRelation,
		})
	}

	return &tracker.IssueConversion{
		Issue:        conv.Issue,
		Dependencies: deps,
	}
}

func (m *giteaFieldMapper) IssueToTracker(issue *types.Issue) map[string]interface{} {
	return BeadsIssueToGiteaFields(issue, m.config)
}
