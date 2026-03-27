package targetprocess

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func init() {
	tracker.Register("targetprocess", func() tracker.IssueTracker {
		return &Tracker{}
	})
}

// Tracker implements tracker.IssueTracker for Targetprocess.
type Tracker struct {
	client       *Client
	store        storage.Storage
	baseURL      string
	projectID    int
	pullWhere    string
	statusMap    map[string]string
	typeMap      map[string]string
	fieldMapper  tracker.FieldMapper
	entityStates map[string][]EntityState
}

func (t *Tracker) Name() string         { return "targetprocess" }
func (t *Tracker) DisplayName() string  { return "Targetprocess" }
func (t *Tracker) ConfigPrefix() string { return "targetprocess" }

func (t *Tracker) Init(ctx context.Context, store storage.Storage) error {
	t.store = store

	baseURL := t.getConfig(ctx, "targetprocess.url", "TARGETPROCESS_URL")
	if baseURL == "" {
		return fmt.Errorf("Targetprocess URL not configured (set targetprocess.url or TARGETPROCESS_URL)")
	}
	t.baseURL = strings.TrimRight(baseURL, "/")

	projectIDStr := t.getConfig(ctx, "targetprocess.project_id", "TARGETPROCESS_PROJECT_ID")
	if projectIDStr == "" {
		return fmt.Errorf("Targetprocess project ID not configured (set targetprocess.project_id or TARGETPROCESS_PROJECT_ID)")
	}
	projectID, err := strconv.Atoi(projectIDStr)
	if err != nil || projectID <= 0 {
		return fmt.Errorf("invalid Targetprocess project ID %q", projectIDStr)
	}
	t.projectID = projectID

	accessToken := t.getConfig(ctx, "targetprocess.access_token", "TARGETPROCESS_ACCESS_TOKEN")
	token := t.getConfig(ctx, "targetprocess.token", "TARGETPROCESS_TOKEN")
	username := t.getConfig(ctx, "targetprocess.username", "TARGETPROCESS_USERNAME")
	password := t.getConfig(ctx, "targetprocess.password", "TARGETPROCESS_PASSWORD")

	if accessToken == "" && token == "" && (username == "" || password == "") {
		return fmt.Errorf("Targetprocess credentials not configured (set targetprocess.access_token, targetprocess.token, or targetprocess.username/targetprocess.password)")
	}

	t.pullWhere = t.getConfig(ctx, "targetprocess.pull_where", "TARGETPROCESS_PULL_WHERE")
	t.statusMap = t.readMappingConfigByPrefix(ctx, "targetprocess.status_map.")
	t.typeMap = t.readMappingConfigByPrefix(ctx, "targetprocess.type_map.")
	t.fieldMapper = newFieldMapper(t.statusMap, t.typeMap)
	t.client = NewClient(t.baseURL, accessToken, token, username, password)
	t.entityStates = make(map[string][]EntityState)

	return nil
}

func (t *Tracker) Validate() error {
	if t.client == nil {
		return fmt.Errorf("Targetprocess tracker not initialized")
	}
	return t.client.Validate(context.Background())
}

func (t *Tracker) Close() error { return nil }

func (t *Tracker) FetchIssues(ctx context.Context, opts tracker.FetchOptions) ([]tracker.TrackerIssue, error) {
	// Pull uses API v2 for richer querying and paging; fetch-by-id and writes stay on v1.
	userStories, err := t.client.FetchEntitiesV2(ctx, "UserStory", t.projectID, opts.State, opts.Since, t.pullWhere, opts.Limit)
	if err != nil {
		return nil, err
	}

	remainingLimit := 0
	if opts.Limit > 0 {
		remainingLimit = opts.Limit - len(userStories)
		if remainingLimit < 0 {
			remainingLimit = 0
		}
	}

	bugs, err := t.client.FetchEntitiesV2(ctx, "Bug", t.projectID, opts.State, opts.Since, t.pullWhere, remainingLimit)
	if err != nil {
		return nil, err
	}

	result := make([]tracker.TrackerIssue, 0, len(userStories)+len(bugs))
	for i := range userStories {
		result = append(result, t.assignableToTrackerIssue(&userStories[i]))
	}
	for i := range bugs {
		result = append(result, t.assignableToTrackerIssue(&bugs[i]))
	}

	return result, nil
}

func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*tracker.TrackerIssue, error) {
	id, err := parseAssignableID(identifier)
	if err != nil {
		return nil, err
	}

	assignable, err := t.client.FetchAssignable(ctx, id)
	if err != nil {
		return nil, err
	}
	if assignable == nil {
		return nil, nil
	}

	ti := t.assignableToTrackerIssue(assignable)
	return &ti, nil
}

func (t *Tracker) CreateIssue(ctx context.Context, issue *types.Issue) (*tracker.TrackerIssue, error) {
	fields := t.FieldMapper().IssueToTracker(issue)
	entityType, _ := t.FieldMapper().TypeToTracker(issue.IssueType).(string)
	if entityType == "" {
		entityType = "UserStory"
	}

	fields["Project"] = map[string]int{"Id": t.projectID}
	if stateID, ok := t.resolveStateID(ctx, entityType, issue.Status); ok {
		fields["EntityState"] = map[string]int{"Id": stateID}
	}

	created, err := t.client.CreateIssue(ctx, entityType, fields)
	if err != nil {
		return nil, err
	}

	ti := t.assignableToTrackerIssue(created)
	return &ti, nil
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	id, err := parseAssignableID(externalID)
	if err != nil {
		return nil, err
	}

	fields := t.FieldMapper().IssueToTracker(issue)
	entityType, _ := t.FieldMapper().TypeToTracker(issue.IssueType).(string)
	if entityType == "" {
		entityType = "UserStory"
	}

	if stateID, ok := t.resolveStateID(ctx, entityType, issue.Status); ok {
		fields["EntityState"] = map[string]int{"Id": stateID}
	}

	updated, err := t.client.UpdateAssignable(ctx, id, fields)
	if err != nil {
		return nil, err
	}

	ti := t.assignableToTrackerIssue(updated)
	return &ti, nil
}

func (t *Tracker) FieldMapper() tracker.FieldMapper {
	return t.fieldMapper
}

func (t *Tracker) IsExternalRef(ref string) bool {
	return IsExternalRef(ref, t.baseURL)
}

func (t *Tracker) ExtractIdentifier(ref string) string {
	return ExtractIdentifier(ref)
}

func (t *Tracker) BuildExternalRef(issue *tracker.TrackerIssue) string {
	return BuildExternalRef(t.baseURL, issue)
}

func (t *Tracker) assignableToTrackerIssue(assignable *Assignable) tracker.TrackerIssue {
	identifier := strconv.Itoa(assignable.ID)
	ti := tracker.TrackerIssue{
		ID:          identifier,
		Identifier:  identifier,
		URL:         BuildExternalRef(t.baseURL, &tracker.TrackerIssue{Identifier: identifier}),
		Title:       assignable.Name,
		Description: assignable.Description,
		Priority:    t.fieldMapper.(*fieldMapper).priorityFromAssignable(assignable),
		Labels:      tagsToLabels(assignable.Tags),
		Raw:         assignable,
	}

	if entityType := assignableEntityType(assignable); entityType != "" {
		ti.Type = entityType
	}
	if assignable.EntityState != nil {
		ti.State = assignable.EntityState.Name
	}
	if assignable.AssignedUser != nil {
		ti.Assignee = firstNonEmpty(assignable.AssignedUser.Login, assignable.AssignedUser.Email, strings.TrimSpace(assignable.AssignedUser.FirstName+" "+assignable.AssignedUser.LastName), assignable.AssignedUser.Name)
		ti.AssigneeEmail = assignable.AssignedUser.Email
		ti.AssigneeID = strconv.Itoa(assignable.AssignedUser.ID)
	}
	if ts, err := parseTimestamp(assignable.CreateDate); err == nil {
		ti.CreatedAt = ts
	}
	if ts, err := parseTimestamp(assignable.ModifyDate); err == nil {
		ti.UpdatedAt = ts
	}

	ti.Metadata = map[string]interface{}{
		"targetprocess.id": assignable.ID,
	}
	if assignable.Project != nil {
		ti.Metadata["targetprocess.project_id"] = assignable.Project.ID
	}
	if entityType := assignableEntityType(assignable); entityType != "" {
		ti.Metadata["targetprocess.entity_type"] = entityType
	}

	return ti
}

func (t *Tracker) resolveStateID(ctx context.Context, entityType string, status types.Status) (int, bool) {
	states, err := t.cachedEntityStates(ctx, entityType)
	if err != nil {
		return 0, false
	}
	if len(states) == 0 {
		return 0, false
	}

	if targetStateName, ok := t.statusMap[string(status)]; ok {
		for _, state := range states {
			if strings.EqualFold(state.Name, targetStateName) {
				return state.ID, true
			}
		}
	}

	matchScore := func(state EntityState) int {
		lower := strings.ToLower(state.Name)
		switch status {
		case types.StatusClosed:
			if state.IsFinal {
				return 100
			}
			if strings.Contains(lower, "done") || strings.Contains(lower, "close") || strings.Contains(lower, "complete") || strings.Contains(lower, "resolved") {
				return 90
			}
		case types.StatusBlocked:
			if strings.Contains(lower, "block") {
				return 100
			}
			if strings.Contains(lower, "hold") {
				return 80
			}
		case types.StatusDeferred:
			if strings.Contains(lower, "defer") || strings.Contains(lower, "hold") || strings.Contains(lower, "postpone") {
				return 100
			}
			if state.IsInitial {
				return 50
			}
		case types.StatusInProgress:
			if strings.Contains(lower, "progress") || strings.Contains(lower, "active") || strings.Contains(lower, "start") || strings.Contains(lower, "review") || strings.Contains(lower, "test") || strings.Contains(lower, "fix") {
				return 100
			}
			if !state.IsFinal && !state.IsInitial {
				return 60
			}
		default:
			if state.IsInitial {
				return 100
			}
			if strings.Contains(lower, "open") || strings.Contains(lower, "new") || strings.Contains(lower, "planned") || strings.Contains(lower, "request") || strings.Contains(lower, "backlog") {
				return 90
			}
			if !state.IsFinal {
				return 50
			}
		}
		return 0
	}

	bestScore := 0
	bestID := 0
	for _, state := range states {
		score := matchScore(state)
		if score > bestScore {
			bestScore = score
			bestID = state.ID
		}
	}

	return bestID, bestScore > 0
}

func (t *Tracker) cachedEntityStates(ctx context.Context, entityType string) ([]EntityState, error) {
	if states, ok := t.entityStates[entityType]; ok {
		return states, nil
	}

	states, err := t.client.FetchEntityStates(ctx, entityType)
	if err != nil {
		return nil, err
	}
	t.entityStates[entityType] = states
	return states, nil
}

func (t *Tracker) getConfig(ctx context.Context, key, envVar string) string {
	val, err := t.store.GetConfig(ctx, key)
	if err == nil && val != "" {
		return val
	}
	if envVar != "" {
		if envVal := os.Getenv(envVar); envVal != "" {
			return envVal
		}
	}
	return ""
}

func (t *Tracker) readMappingConfigByPrefix(ctx context.Context, prefix string) map[string]string {
	mappings := make(map[string]string)
	allConfig, err := t.store.GetAllConfig(ctx)
	if err != nil {
		return mappings
	}
	for key, value := range allConfig {
		if strings.HasPrefix(key, prefix) && value != "" {
			mappings[strings.TrimPrefix(key, prefix)] = value
		}
	}
	return mappings
}

func parseTimestamp(raw string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.9999999",
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
	}

	for _, layout := range layouts {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts, nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported timestamp %q", raw)
}

func parseAssignableID(identifier string) (int, error) {
	id, err := strconv.Atoi(identifier)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid Targetprocess issue ID %q", identifier)
	}
	return id, nil
}
