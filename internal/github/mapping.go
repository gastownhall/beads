// Package github provides client and data types for the GitHub REST API.
package github

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// MappingConfig configures how GitHub fields map to beads fields.
type MappingConfig struct {
	PriorityMap  map[string]int    // priority label value -> beads priority (0-4)
	StateMap     map[string]string // GitHub state -> beads status
	LabelTypeMap map[string]string // type label value -> beads issue type
}

// DefaultMappingConfig returns the default mapping configuration.
// Uses exported mapping constants from types.go as the single source of truth.
func DefaultMappingConfig() *MappingConfig {
	// Copy PriorityMapping to avoid external modification
	priorityMap := make(map[string]int, len(PriorityMapping))
	for k, v := range PriorityMapping {
		priorityMap[k] = v
	}

	// Copy typeMapping to avoid external modification
	labelTypeMap := make(map[string]string, len(typeMapping))
	for k, v := range typeMapping {
		labelTypeMap[k] = v
	}

	return &MappingConfig{
		PriorityMap: priorityMap,
		// StateMap maps GitHub states to beads statuses
		StateMap: map[string]string{
			"open":   StatusMapping["open"],
			"closed": StatusMapping["closed"],
		},
		LabelTypeMap: labelTypeMap,
	}
}

// priorityFromLabels extracts priority from GitHub labels.
// Returns default priority (2 = medium) if no priority label found.
func priorityFromLabels(labels []string, config *MappingConfig) int {
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "priority" {
			if p, ok := config.PriorityMap[strings.ToLower(value)]; ok {
				return p
			}
		}
	}
	return 2 // Default to medium
}

// statusFromLabelsAndState determines beads status from GitHub labels and state.
// GitHub's closed state takes precedence over status labels.
func statusFromLabelsAndState(labels []string, state string, config *MappingConfig) string {
	// Closed state always wins
	if state == "closed" {
		return "closed"
	}

	// Check for status label
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "status" {
			normalized := strings.ToLower(value)
			if normalized == "in_progress" {
				return "in_progress"
			}
			if normalized == "blocked" {
				return "blocked"
			}
			if normalized == "deferred" {
				return "deferred"
			}
		}
	}

	// Default: map GitHub state to beads status
	if s, ok := config.StateMap[state]; ok {
		return s
	}
	return "open"
}

// typeFromLabels extracts issue type from GitHub labels.
// Checks both scoped (type::bug) and bare (bug) labels.
// Returns "task" if no type label found.
func typeFromLabels(labels []string, config *MappingConfig) string {
	for _, label := range labels {
		prefix, value := parseLabelPrefix(label)
		if prefix == "type" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
		// Also check bare labels (no prefix)
		if prefix == "" {
			if t, ok := config.LabelTypeMap[strings.ToLower(value)]; ok {
				return t
			}
		}
	}
	return "task" // Default to task
}

// GitHubIssueToBeads converts a GitHub Issue to a beads Issue.
func GitHubIssueToBeads(gh *Issue, config *MappingConfig) *IssueConversion {
	htmlURL := gh.HTMLURL
	sourceSystem := fmt.Sprintf("github:%s:%d", gh.HTMLURL, gh.Number)
	// Use a cleaner source system format if HTMLURL is not available
	if htmlURL == "" {
		sourceSystem = fmt.Sprintf("github:%d:%d", gh.ID, gh.Number)
	}

	labelNames := gh.LabelNames()

	issue := &types.Issue{
		ID:           ExtractBDIDFromGitHubSyncBlock(gh.Body),
		Title:        gh.Title,
		Description:  StripGitHubSyncBlock(gh.Body),
		ExternalRef:  &htmlURL,
		SourceSystem: sourceSystem,
		IssueType:    types.IssueType(typeFromLabels(labelNames, config)),
		Priority:     priorityFromLabels(labelNames, config),
		Status:       types.Status(statusFromLabelsAndState(labelNames, gh.State, config)),
		Labels:       filterNonScopedLabels(labelNames),
	}

	// Set assignee from GitHub user
	if gh.Assignee != nil {
		issue.Assignee = gh.Assignee.Login
	}

	// Set timestamps
	if gh.CreatedAt != nil {
		issue.CreatedAt = *gh.CreatedAt
	}
	if gh.UpdatedAt != nil {
		issue.UpdatedAt = *gh.UpdatedAt
	}

	return &IssueConversion{
		Issue:        issue,
		Dependencies: []DependencyInfo{},
	}
}

// BeadsIssueToGitHubFields converts a beads Issue to GitHub API update fields.
func BeadsIssueToGitHubFields(issue *types.Issue, config *MappingConfig) map[string]interface{} {
	body := issue.Description
	if !strings.Contains(body, "<!-- bd-github-sync: start -->") {
		body = RenderGitHubIssueBody(issue, nil, nil)
	}
	fields := map[string]interface{}{
		"title": issue.Title,
		"body":  body,
	}

	// Build labels from type, priority, and status
	var labels []string

	// Add type label
	if issue.IssueType != "" {
		labels = append(labels, "type::"+string(issue.IssueType))
	}

	// Add priority label
	priorityLabel := priorityToLabel(issue.Priority)
	if priorityLabel != "" {
		labels = append(labels, "priority::"+priorityLabel)
	}

	// Add status label (if not open or closed - those are handled by state)
	if issue.Status == types.StatusInProgress {
		labels = append(labels, "status::in_progress")
	} else if issue.Status == types.StatusBlocked {
		labels = append(labels, "status::blocked")
	} else if issue.Status == types.StatusDeferred {
		labels = append(labels, "status::deferred")
	}

	// Add any existing non-scoped labels
	labels = append(labels, issue.Labels...)

	fields["labels"] = labels

	// Set state for closed issues
	if issue.Status == types.StatusClosed {
		fields["state"] = "closed"
	} else {
		fields["state"] = "open"
	}

	return fields
}

func RenderGitHubIssueBody(issue *types.Issue, dependencies, dependents []*types.IssueWithDependencyMetadata) string {
	if issue == nil {
		return ""
	}

	base := StripGitHubSyncBlock(issue.Description)
	var b strings.Builder
	b.WriteString(strings.TrimSpace(base))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("<!-- bd-github-sync: start -->\n")
	if strings.TrimSpace(issue.ID) != "" {
		b.WriteString(fmt.Sprintf("<!-- bd: %s sync=github-v1 -->\n", strings.TrimSpace(issue.ID)))
	}
	b.WriteString("## Beads\n\n")
	if strings.TrimSpace(issue.ID) != "" {
		b.WriteString(fmt.Sprintf("Source: `%s`\n", strings.TrimSpace(issue.ID)))
	}
	appendGitHubTasklistSection(&b, "Dependencies", dependencies, true)
	appendGitHubTasklistSection(&b, "Blocked by this issue", dependents, false)
	b.WriteString("<!-- bd-github-sync: end -->")
	return strings.TrimSpace(b.String())
}

func appendGitHubTasklistSection(b *strings.Builder, title string, issues []*types.IssueWithDependencyMetadata, incoming bool) {
	if len(issues) == 0 {
		return
	}
	b.WriteString("\n")
	b.WriteString("### ")
	b.WriteString(title)
	b.WriteString("\n")
	for _, item := range issues {
		if item == nil {
			continue
		}
		label := githubDependencyLabel(item.DependencyType, incoming)
		b.WriteString("- [ ] ")
		b.WriteString(githubTaskTarget(&item.Issue))
		if text := githubTaskTitle(item.Title); text != "" {
			b.WriteString(" — ")
			b.WriteString(text)
		}
		if item.ID != "" || label != "" {
			b.WriteString(" (")
			parts := make([]string, 0, 2)
			if item.ID != "" {
				parts = append(parts, fmt.Sprintf("`%s`", item.ID))
			}
			if label != "" {
				parts = append(parts, label)
			}
			b.WriteString(strings.Join(parts, ", "))
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
}

func githubDependencyLabel(depType types.DependencyType, incoming bool) string {
	switch depType {
	case types.DepBlocks:
		if incoming {
			return "blocked by"
		}
		return "blocks"
	case types.DepParentChild:
		if incoming {
			return "parent"
		}
		return "child"
	case types.DepWaitsFor:
		if incoming {
			return "waits for"
		}
		return "waited on by"
	case types.DepConditionalBlocks:
		if incoming {
			return "conditionally blocked by"
		}
		return "conditionally blocks"
	}
	return string(depType)
}

func githubTaskTarget(issue *types.Issue) string {
	if issue == nil {
		return "`unknown`"
	}
	if issue.ExternalRef != nil {
		if n := githubIssueNumberFromRef(*issue.ExternalRef); n != "" {
			return "#" + n
		}
	}
	if strings.TrimSpace(issue.ID) != "" {
		return fmt.Sprintf("`%s`", strings.TrimSpace(issue.ID))
	}
	return "`unknown`"
}

func githubIssueNumberFromRef(ref string) string {
	marker := "/issues/"
	idx := strings.LastIndex(ref, marker)
	if idx == -1 {
		return ""
	}
	rest := ref[idx+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return ""
	}
	return rest[:end]
}

func githubTaskTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	if i := strings.IndexAny(title, "\r\n"); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	return title
}

// StripGitHubSyncBlock removes the generated Beads sync footer from a GitHub
// issue body before storing it as the local issue description.
func StripGitHubSyncBlock(body string) string {
	const start = "<!-- bd-github-sync: start -->"
	const end = "<!-- bd-github-sync: end -->"
	for {
		startIdx := strings.Index(body, start)
		if startIdx == -1 {
			break
		}
		endIdx := strings.Index(body[startIdx:], end)
		if endIdx == -1 {
			break
		}
		endIdx = startIdx + endIdx + len(end)
		body = body[:startIdx] + body[endIdx:]
	}
	return strings.TrimSpace(body)
}

func ExtractBDIDFromGitHubSyncBlock(body string) string {
	idx := strings.Index(body, "<!-- bd:")
	if idx == -1 {
		return ""
	}
	rest := body[idx+len("<!-- bd:"):]
	end := strings.Index(rest, "-->")
	if end == -1 {
		return ""
	}
	fields := strings.Fields(strings.TrimSpace(rest[:end]))
	for _, field := range fields {
		field = strings.Trim(field, ",")
		if strings.HasPrefix(field, "id=") {
			return strings.Trim(strings.TrimPrefix(field, "id="), "`\"'")
		}
		if strings.HasPrefix(field, "sync=") {
			continue
		}
		return strings.Trim(field, "`\"'")
	}
	return ""
}

// priorityToLabel converts beads priority (0-4) to GitHub priority label value.
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

// filterNonScopedLabels returns only labels without scoped prefixes.
// Removes priority::*, status::*, and type::* labels.
func filterNonScopedLabels(labels []string) []string {
	var filtered []string
	for _, label := range labels {
		prefix, _ := parseLabelPrefix(label)
		// Skip scoped labels that we handle specially
		if prefix == "priority" || prefix == "status" || prefix == "type" {
			continue
		}
		filtered = append(filtered, label)
	}
	return filtered
}
