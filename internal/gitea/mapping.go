package gitea

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// MappingConfig configures how Gitea/Forgejo fields map to beads fields.
type MappingConfig struct {
	PriorityMap    map[string]int    // priority label value -> beads priority (0-4)
	StateMap       map[string]string // Gitea/Forgejo state -> beads status
	LabelTypeMap   map[string]string // type label value -> beads issue type
	ClosedStateMap map[string]string // optional lowercase label -> beads status override when state == "closed"
}

// DefaultMappingConfig returns the default mapping configuration.
func DefaultMappingConfig() *MappingConfig {
	priorityMap := make(map[string]int, len(PriorityMapping))
	for k, v := range PriorityMapping {
		priorityMap[k] = v
	}
	labelTypeMap := make(map[string]string, len(typeMapping))
	for k, v := range typeMapping {
		labelTypeMap[k] = v
	}
	return &MappingConfig{
		PriorityMap: priorityMap,
		StateMap: map[string]string{
			"open":   StatusMapping["open"],
			"closed": StatusMapping["closed"],
		},
		LabelTypeMap:   labelTypeMap,
		ClosedStateMap: map[string]string{},
	}
}

func priorityFromLabels(labels []string, config *MappingConfig) int {
	if config == nil {
		config = DefaultMappingConfig()
	}
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "priority" {
			if p, ok := config.PriorityMap[strings.ToLower(value)]; ok {
				return p
			}
		}
	}
	return 2
}

func statusFromLabelsAndState(labels []string, state string, config *MappingConfig) string {
	if config == nil {
		config = DefaultMappingConfig()
	}
	if strings.EqualFold(state, "closed") {
		if len(config.ClosedStateMap) > 0 {
			for _, label := range labels {
				if override, ok := config.ClosedStateMap[strings.ToLower(strings.TrimSpace(label))]; ok && override != "" {
					return override
				}
			}
		}
		return "closed"
	}

	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "status" {
			switch strings.ToLower(value) {
			case "in_progress":
				return "in_progress"
			case "blocked":
				return "blocked"
			case "deferred":
				return "deferred"
			case "done", "closed":
				return "closed"
			case "open":
				return "open"
			}
		}
	}

	if s, ok := config.StateMap[strings.ToLower(state)]; ok && s != "" {
		return s
	}
	return "open"
}

func typeFromLabels(labels []string, config *MappingConfig) string {
	if config == nil {
		config = DefaultMappingConfig()
	}
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "type" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
		if prefix == "" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
	}
	return "task"
}

func giteaSourceSystem(repoID int64, owner, repo string, number int) string {
	if repoID > 0 {
		return fmt.Sprintf("gitea:%d:%d", repoID, number)
	}
	if owner != "" && repo != "" {
		return fmt.Sprintf("gitea:%s/%s:%d", strings.ToLower(owner), strings.ToLower(repo), number)
	}
	return fmt.Sprintf("gitea:%d", number)
}

func fallbackRef(gt *Issue) string {
	if gt == nil {
		return ""
	}
	if gt.HTMLURL != "" {
		return gt.HTMLURL
	}
	if gt.Repository != nil && gt.Repository.FullName != "" {
		return fmt.Sprintf("gitea:%s#%d", gt.Repository.FullName, gt.Number)
	}
	return fmt.Sprintf("gitea:%d", gt.Number)
}

// GiteaIssueToBeads converts a Gitea/Forgejo Issue to a beads Issue and dependencies.
func GiteaIssueToBeads(gt *Issue, config *MappingConfig) *IssueConversion {
	if gt == nil {
		return nil
	}
	if config == nil {
		config = DefaultMappingConfig()
	}

	extRef := fallbackRef(gt)
	var repoID int64
	var owner, repo string
	if gt.Repository != nil {
		repoID = gt.Repository.ID
		owner = gt.Repository.Owner
		repo = gt.Repository.Name
		if (owner == "" || repo == "") && strings.Contains(gt.Repository.FullName, "/") {
			parts := strings.SplitN(gt.Repository.FullName, "/", 2)
			owner, repo = parts[0], parts[1]
		}
	}
	sourceSystem := giteaSourceSystem(repoID, owner, repo, gt.Number)

	labelNames := gt.LabelNames()
	issue := &types.Issue{
		Title:        gt.Title,
		Description:  gt.Body,
		ExternalRef:  &extRef,
		SourceSystem: sourceSystem,
		IssueType:    types.IssueType(typeFromLabels(labelNames, config)),
		Priority:     priorityFromLabels(labelNames, config),
		Status:       types.Status(statusFromLabelsAndState(labelNames, gt.State, config)),
		Labels:       filterNonScopedLabels(labelNames),
	}

	if gt.Assignee != nil {
		issue.Assignee = gt.Assignee.Handle()
	}
	if gt.CreatedAt != nil {
		issue.CreatedAt = *gt.CreatedAt
	}
	if gt.UpdatedAt != nil {
		issue.UpdatedAt = *gt.UpdatedAt
	}
	if gt.ClosedAt != nil && issue.Status == types.StatusClosed {
		issue.ClosedAt = gt.ClosedAt
	}

	var metadata map[string]interface{}
	if gt.PullRequest != nil && gt.PullRequest.Merged {
		metadata = map[string]interface{}{
			"gitea.pr_merged": true,
		}
	}
	if gt.Milestone != nil && gt.Milestone.Title != "" {
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		metadata["gitea.milestone"] = gt.Milestone.Title
	}
	if len(metadata) > 0 {
		if encoded, err := encodeMetadata(metadata); err == nil {
			issue.Metadata = encoded
		}
	}

	deps := make([]DependencyInfo, 0, len(gt.BlockedBy))
	for i := range gt.BlockedBy {
		dep := &gt.BlockedBy[i]
		if dep.Number <= 0 || dep.Number == gt.Number {
			continue
		}
		var depRepoID int64
		if dep.Repository != nil {
			depRepoID = dep.Repository.ID
		}
		deps = append(deps, DependencyInfo{
			FromNumber: gt.Number,
			ToNumber:   dep.Number,
			FromRepoID: repoID,
			ToRepoID:   depRepoID,
			FromRef:    extRef,
			ToRef:      fallbackRef(dep),
			Type:       string(types.DepBlocks),
		})
	}

	return &IssueConversion{
		Issue:        issue,
		Dependencies: deps,
	}
}

func shouldCloseRemote(issue *types.Issue, config *MappingConfig) bool {
	if issue == nil {
		return false
	}
	if issue.Status == types.StatusClosed {
		return true
	}
	if config == nil || len(config.ClosedStateMap) == 0 {
		return false
	}
	for _, label := range issue.Labels {
		mapped, ok := config.ClosedStateMap[strings.ToLower(strings.TrimSpace(label))]
		if ok && mapped != "" && strings.EqualFold(mapped, string(issue.Status)) {
			return true
		}
	}
	return false
}

func desiredGiteaState(issue *types.Issue, config *MappingConfig) string {
	if shouldCloseRemote(issue, config) {
		return "closed"
	}
	return "open"
}

// BeadsIssueToGiteaFields converts a beads Issue to Gitea/Forgejo API fields.
func BeadsIssueToGiteaFields(issue *types.Issue, config *MappingConfig) map[string]interface{} {
	fields := map[string]interface{}{
		"title": issue.Title,
		"body":  issue.Description,
	}

	closeRemote := shouldCloseRemote(issue, config)

	var labels []string
	if issue.IssueType != "" {
		labels = append(labels, "type::"+string(issue.IssueType))
	}
	if priorityLabel := priorityToLabel(issue.Priority); priorityLabel != "" {
		labels = append(labels, "priority::"+priorityLabel)
	}
	if !closeRemote {
		switch issue.Status {
		case types.StatusInProgress:
			labels = append(labels, "status::in_progress")
		case types.StatusBlocked:
			labels = append(labels, "status::blocked")
		case types.StatusDeferred:
			labels = append(labels, "status::deferred")
		}
	}
	labels = append(labels, issue.Labels...)
	if labels == nil {
		labels = []string{}
	}
	fields["labels"] = labels

	if closeRemote {
		fields["state"] = "closed"
	} else {
		fields["state"] = "open"
	}

	return fields
}

// PushFieldsEqual reports whether pushing local to remote would be a no-op.
func PushFieldsEqual(local *types.Issue, remote *Issue, config *MappingConfig) bool {
	if local == nil || remote == nil {
		return false
	}
	if local.Title != remote.Title {
		return false
	}
	if local.Description != remote.Body {
		return false
	}

	desiredState := desiredGiteaState(local, config)
	if !strings.EqualFold(desiredState, remote.State) {
		return false
	}

	desiredLabels, _ := BeadsIssueToGiteaFields(local, config)["labels"].([]string)
	return labelSetsEqual(desiredLabels, remote.LabelNames())
}

const pushContentHashVersion = "1"

// PushContentHash returns a stable hex fingerprint of the fields a push sends to Gitea/Forgejo.
func PushContentHash(local *types.Issue, config *MappingConfig) string {
	if local == nil {
		return ""
	}
	desiredState := desiredGiteaState(local, config)
	desiredLabels, _ := BeadsIssueToGiteaFields(local, config)["labels"].([]string)
	sortedLabels := append([]string(nil), desiredLabels...)
	slices.Sort(sortedLabels)

	h := sha256.New()
	for _, s := range []string{pushContentHashVersion, local.Title, local.Description, desiredState, strings.Join(sortedLabels, "\x00")} {
		_, _ = h.Write([]byte(fmt.Sprintf("%d:%s", len(s), s)))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func labelSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}

func priorityToLabel(priority int) string {
	switch priority {
	case 0:
		return "critical"
	case 1:
		return "high"
	case 2:
		return "medium"
	case 3:
		return "low"
	case 4:
		return "none"
	default:
		return "medium"
	}
}

// filterNonScopedLabels returns non-scoped labels and is guaranteed non-nil so
// ApplyIssueUpdate replaces cleared labels instead of preserving stale ones.
func filterNonScopedLabels(labels []string) []string {
	filtered := []string{}
	for _, label := range labels {
		prefix, _ := parseLabelPrefix(label)
		if prefix == "priority" || prefix == "status" || prefix == "type" {
			continue
		}
		filtered = append(filtered, label)
	}
	return filtered
}
