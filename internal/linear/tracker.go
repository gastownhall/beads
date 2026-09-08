package linear

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

var _ tracker.BatchPushTracker = (*Tracker)(nil)

func init() {
	tracker.Register("linear", func() tracker.IssueTracker {
		return &Tracker{}
	})
}

// Tracker implements tracker.IssueTracker for Linear.
type Tracker struct {
	clients   map[string]*Client // keyed by team ID
	config    *MappingConfig
	store     storage.Storage
	teamIDs   []string // ordered list of configured team IDs
	projectID string
}

// SetTeamIDs sets the team IDs before Init(). When set, Init() uses these
// instead of reading from config. This supports the --team CLI flag.
func (t *Tracker) SetTeamIDs(ids []string) {
	t.teamIDs = ids
}

func (t *Tracker) Name() string         { return "linear" }
func (t *Tracker) DisplayName() string  { return "Linear" }
func (t *Tracker) ConfigPrefix() string { return "linear" }

func (t *Tracker) Init(ctx context.Context, store storage.Storage) error {
	t.store = store

	// Resolve authentication: OAuth client-credentials takes precedence over API key.
	oauthClientID, _ := t.getConfig(ctx, "linear.oauth_client_id", "LINEAR_OAUTH_CLIENT_ID")
	oauthClientSecret, _ := t.getConfig(ctx, "linear.oauth_client_secret", "LINEAR_OAUTH_CLIENT_SECRET")
	hasOAuth := oauthClientID != "" && oauthClientSecret != ""

	var apiKey string
	if !hasOAuth {
		apiKey, _ = t.getConfig(ctx, "linear.api_key", "LINEAR_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("Linear authentication not configured\n" +
				"Options:\n" +
				"  OAuth (for CI):  export LINEAR_OAUTH_CLIENT_ID=... LINEAR_OAUTH_CLIENT_SECRET=...\n" +
				"  API key (devs):  export LINEAR_API_KEY=... or bd config set linear.api_key \"YOUR_API_KEY\"")
		}
	}

	// Resolve team IDs: use pre-set IDs (from CLI), or fall back to config.
	if len(t.teamIDs) == 0 {
		pluralVal, _ := t.getConfig(ctx, "linear.team_ids", "LINEAR_TEAM_IDS")
		singularVal, _ := t.getConfig(ctx, "linear.team_id", "LINEAR_TEAM_ID")
		t.teamIDs = tracker.ResolveProjectIDs(nil, pluralVal, singularVal)
		if len(t.teamIDs) == 0 {
			return fmt.Errorf("Linear team ID not configured (set linear.team_id, linear.team_ids, or LINEAR_TEAM_ID)")
		}
	}

	// Read optional endpoint and project ID.
	var endpoint, projectID string
	if store != nil {
		endpoint, _ = store.GetConfig(ctx, "linear.api_endpoint")
		projectID, _ = store.GetConfig(ctx, "linear.project_id")
		if projectID != "" {
			t.projectID = projectID
		}
	}

	// Read optional rate-limit floor (LINEAR_RATE_LIMIT_FLOOR env or linear.rate_limit_floor config).
	var rateLimitFloor int
	if floorStr, _ := t.getConfig(ctx, "linear.rate_limit_floor", "LINEAR_RATE_LIMIT_FLOOR"); floorStr != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(floorStr)); err == nil && v >= 0 {
			rateLimitFloor = v
		}
	}

	// Create per-team clients upfront for O(1) routing.
	t.clients = make(map[string]*Client, len(t.teamIDs))
	for _, teamID := range t.teamIDs {
		var client *Client
		if hasOAuth {
			client = NewOAuthClient(OAuthConfig{
				ClientID:     oauthClientID,
				ClientSecret: oauthClientSecret,
			}, teamID)
		} else {
			client = NewClient(apiKey, teamID)
		}
		if endpoint != "" {
			client = client.WithEndpoint(endpoint)
		}
		if projectID != "" {
			client = client.WithProjectID(projectID)
		}
		if rateLimitFloor > 0 {
			client = client.WithRateLimitFloor(rateLimitFloor)
		}
		t.clients[teamID] = client
	}

	t.config = LoadMappingConfig(&configLoaderAdapter{ctx: ctx, store: store})
	return nil
}

func (t *Tracker) Validate() error {
	if len(t.clients) == 0 {
		return fmt.Errorf("Linear tracker not initialized")
	}
	return nil
}

func (t *Tracker) Close() error { return nil }

func (t *Tracker) FetchIssues(ctx context.Context, opts tracker.FetchOptions) ([]tracker.TrackerIssue, error) {
	state := opts.State
	if state == "" {
		state = "all"
	}

	seen := make(map[string]bool)
	var result []tracker.TrackerIssue

	for _, teamID := range t.teamIDs {
		client := t.clients[teamID]
		if client == nil {
			continue
		}

		var issues []Issue
		var err error
		if opts.Since != nil {
			issues, err = client.FetchIssuesSince(ctx, state, *opts.Since)
		} else {
			issues, err = client.FetchIssues(ctx, state)
		}
		if err != nil {
			return result, fmt.Errorf("fetching issues from team %s: %w", teamID, err)
		}

		for _, li := range issues {
			if seen[li.ID] {
				continue
			}
			seen[li.ID] = true
			result = append(result, linearToTrackerIssue(&li))
		}
	}

	return result, nil
}

func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*tracker.TrackerIssue, error) {
	// Try the primary client first (first team), then others.
	for _, teamID := range t.teamIDs {
		client := t.clients[teamID]
		if client == nil {
			continue
		}
		li, err := client.FetchIssueByIdentifier(ctx, identifier)
		if err != nil {
			continue // Issue might belong to a different team.
		}
		if li != nil {
			ti := linearToTrackerIssue(li)
			return &ti, nil
		}
	}
	return nil, nil
}

func (t *Tracker) CreateIssue(ctx context.Context, issue *types.Issue) (*tracker.TrackerIssue, error) {
	client := t.primaryClient()
	if client == nil {
		return nil, fmt.Errorf("no Linear client available")
	}

	priority := PriorityToLinear(issue.Priority, t.config)

	stateID, err := t.findStateID(ctx, client, issue.Status)
	if err != nil {
		return nil, fmt.Errorf("finding state for status %s: %w", issue.Status, err)
	}

	// Use issue.Description as-is: the sync engine's FormatDescription hook
	// (BuildLinearDescription) has already merged AcceptanceCriteria/Design/Notes
	// into the description before calling CreateIssue. Calling BuildLinearDescription
	// here a second time would duplicate those sections for issues with structured fields.
	description := issue.Description

	// Use idempotent creation when we have enough bead metadata to generate
	// a stable marker. This prevents duplicate Linear issues when sync is
	// interrupted between the API create call and the local external_ref
	// write-back.
	if issue.ID != "" && issue.CreatedBy != "" {
		marker := GenerateIdempotencyMarker(issue.ID, issue.CreatedBy, issue.CreatedAt.UnixNano())
		created, deduped, err := client.CreateIssueIdempotent(ctx, issue.Title, description, priority, stateID, nil, marker)
		if err != nil {
			return nil, err
		}
		if deduped {
			fmt.Fprintf(os.Stderr, "linear: dedup — reusing existing issue %s for bead %s\n", created.Identifier, issue.ID)
		}
		ti := linearToTrackerIssue(created)
		return &ti, nil
	}

	created, err := client.CreateIssue(ctx, issue.Title, description, priority, stateID, nil)
	if err != nil {
		return nil, err
	}

	ti := linearToTrackerIssue(created)
	return &ti, nil
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	// Route to the correct team's client based on the external ID.
	client := t.clientForExternalID(ctx, externalID)
	if client == nil {
		return nil, fmt.Errorf("cannot determine Linear team for issue %s", externalID)
	}

	mapper := t.FieldMapper()
	updates := mapper.IssueToTracker(issue)

	// Resolve and include state so status changes are pushed to Linear.
	stateID, err := t.findStateID(ctx, client, issue.Status)
	if err != nil {
		return nil, fmt.Errorf("finding state for status %s: %w", issue.Status, err)
	}
	if stateID != "" {
		updates["stateId"] = stateID
	}

	updated, err := client.UpdateIssue(ctx, externalID, updates)
	if err != nil {
		return nil, err
	}

	ti := linearToTrackerIssue(updated)
	return &ti, nil
}

// BatchPush implements tracker.BatchPushTracker. It partitions issues into
// creates and updates, uses issueBatchCreate for new issues (chunked at 50),
// and falls back to per-issue UpdateIssue for updates (since issueBatchUpdate
// applies the same fields to all IDs, which doesn't fit per-issue field diffs).
//
// Skip semantics: existing issues are fetched and compared with PushFieldsEqual
// before updating; unchanged issues are skipped. forceIDs bypasses this check.
//
// Multi-team: state IDs are resolved using the per-team workflow state cache,
// so updates to issues belonging to different teams use the correct state list.
//
// Result mapping: batch-create results are matched by title rather than array
// index, since Linear's API does not guarantee response order matches input order.
func (t *Tracker) BatchPush(ctx context.Context, issues []*types.Issue, forceIDs map[string]bool) (*tracker.BatchPushResult, error) {
	client := t.primaryClient()
	if client == nil {
		return nil, fmt.Errorf("no Linear client available")
	}

	// Build per-team state caches so that updates to issues belonging to different
	// teams resolve workflow state IDs against the correct team's state list.
	teamCaches := make(map[string]*StateCache, len(t.teamIDs))
	for _, teamID := range t.teamIDs {
		teamClient := t.clients[teamID]
		if teamClient == nil {
			continue
		}
		cache, err := BuildStateCache(ctx, teamClient)
		if err != nil {
			return nil, fmt.Errorf("building state cache for team %s: %w", teamID, err)
		}
		teamCaches[teamID] = cache
	}

	// The primary team's cache is used for creates, which always target the primary team.
	primaryCache := teamCaches[t.teamIDs[0]]
	if primaryCache == nil {
		return nil, fmt.Errorf("building state cache: no cache for primary team %s", t.teamIDs[0])
	}

	result := &tracker.BatchPushResult{}

	var toCreate []*types.Issue
	var toUpdate []*types.Issue

	for _, issue := range issues {
		extRef := ""
		if issue.ExternalRef != nil {
			extRef = *issue.ExternalRef
		}
		if extRef == "" || !IsLinearExternalRef(extRef) {
			toCreate = append(toCreate, issue)
		} else {
			toUpdate = append(toUpdate, issue)
		}
	}

	// Batch create new issues.
	if len(toCreate) > 0 {
		// Partition into unique-title (safe for batch) and duplicate-title (single-create).
		// Title-based result correlation is only safe when titles are unique in the batch.
		titleCount := make(map[string]int, len(toCreate))
		for _, issue := range toCreate {
			titleCount[issue.Title]++
		}

		var batchIssues []*types.Issue
		var singleIssues []*types.Issue
		for _, issue := range toCreate {
			if titleCount[issue.Title] > 1 {
				singleIssues = append(singleIssues, issue)
			} else {
				batchIssues = append(batchIssues, issue)
			}
		}

		// Single-create path for duplicate-title issues using idempotency markers.
		for _, issue := range singleIssues {
			priority := PriorityToLinear(issue.Priority, t.config)
			stateID, stateErr := ResolveStateIDForBeadsStatus(primaryCache, issue.Status, t.config)
			if stateErr != nil {
				result.Errors = append(result.Errors, tracker.BatchPushError{
					LocalID: issue.ID,
					Message: fmt.Sprintf("resolving state for status %s: %v", issue.Status, stateErr),
				})
				continue
			}

			marker := GenerateIdempotencyMarker(issue.ID, issue.CreatedBy, issue.CreatedAt.UnixNano())
			var labelIDs []string
			created, _, createErr := client.CreateIssueIdempotent(ctx, issue.Title, issue.Description, priority, stateID, labelIDs, marker)
			if createErr != nil {
				result.Errors = append(result.Errors, tracker.BatchPushError{
					LocalID: issue.ID,
					Message: fmt.Sprintf("single create (dup title) for %q: %v", issue.Title, createErr),
				})
				continue
			}
			result.Created = append(result.Created, tracker.BatchPushItem{
				LocalID:     issue.ID,
				ExternalRef: created.URL,
			})
		}

		// Batch-create path for unique-title issues.
		var inputs []IssueCreateInput
		titleToIssue := make(map[string]*types.Issue, len(batchIssues))
		for _, issue := range batchIssues {
			priority := PriorityToLinear(issue.Priority, t.config)
			stateID, stateErr := ResolveStateIDForBeadsStatus(primaryCache, issue.Status, t.config)
			if stateErr != nil {
				result.Errors = append(result.Errors, tracker.BatchPushError{
					LocalID: issue.ID,
					Message: fmt.Sprintf("resolving state for status %s: %v", issue.Status, stateErr),
				})
				continue
			}

			marker := GenerateIdempotencyMarker(issue.ID, issue.CreatedBy, issue.CreatedAt.UnixNano())
			desc := AppendIdempotencyMarker(issue.Description, marker)

			input := IssueCreateInput{
				TeamID:      client.TeamID,
				Title:       issue.Title,
				Description: desc,
				Priority:    priority,
				StateID:     stateID,
			}
			if client.ProjectID != "" {
				input.ProjectID = client.ProjectID
			}
			titleToIssue[issue.Title] = issue
			inputs = append(inputs, input)
		}

		if len(inputs) > 0 {
			created, createErr := client.BatchCreateIssues(ctx, inputs)
			if createErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("batch create partial error: %v", createErr))
			}
			matched := make(map[string]bool, len(created))
			for _, li := range created {
				localIssue, ok := titleToIssue[li.Title]
				if !ok {
					result.Warnings = append(result.Warnings, fmt.Sprintf("batch create: response contained unexpected title %q", li.Title))
					continue
				}
				matched[li.Title] = true
				result.Created = append(result.Created, tracker.BatchPushItem{
					LocalID:     localIssue.ID,
					ExternalRef: li.URL,
				})
			}
			for title, localIssue := range titleToIssue {
				if !matched[title] {
					result.Errors = append(result.Errors, tracker.BatchPushError{
						LocalID: localIssue.ID,
						Message: fmt.Sprintf("not returned in batch create response (title: %q)", title),
					})
				}
			}
		}
	}

	// Update existing issues individually (each has different field values).
	for _, issue := range toUpdate {
		extRef := *issue.ExternalRef
		externalID := ExtractLinearIdentifier(extRef)
		if externalID == "" {
			externalID = extRef
		}

		routeClient := t.clientForExternalID(ctx, externalID)
		if routeClient == nil {
			result.Errors = append(result.Errors, tracker.BatchPushError{
				LocalID: issue.ID,
				Message: fmt.Sprintf("cannot determine Linear team for %s", externalID),
			})
			continue
		}

		// Use the per-team state cache so that multi-team setups resolve state IDs
		// against the correct team's workflow states, not the primary team's.
		teamCache, ok := teamCaches[routeClient.TeamID]
		if !ok || teamCache == nil {
			teamCache = primaryCache // defensive fallback
		}

		// Skip issues that haven't changed since the last push, unless forced.
		// This mirrors the ContentEqual / UpdatedAt skip logic in the single-issue
		// push path (engine.go doPush) to avoid redundant API writes.
		var remoteIssue *Issue
		if !forceIDs[issue.ID] {
			fetched, lookupErr := routeClient.FetchIssueByIdentifier(ctx, externalID)
			if lookupErr == nil && fetched != nil {
				remoteIssue = fetched
				if PushFieldsEqual(issue, remoteIssue, t.config) {
					result.Skipped = append(result.Skipped, issue.ID)
					continue
				}
			}
		}

		mapper := t.FieldMapper()
		updates := mapper.IssueToTracker(issue)

		stateID, stateErr := ResolveStateIDForBeadsStatus(teamCache, issue.Status, t.config)
		if stateErr != nil {
			result.Errors = append(result.Errors, tracker.BatchPushError{
				LocalID: issue.ID,
				Message: fmt.Sprintf("resolving state for status %s: %v", issue.Status, stateErr),
			})
			continue
		}
		if stateID != "" {
			updates["stateId"] = stateID
		}

		// Prefer the UUID obtained during the skip-check fetch; fall back to a
		// fresh lookup only when the skip check was bypassed (forceIDs).
		issueUUID := externalID
		if remoteIssue != nil {
			issueUUID = remoteIssue.ID
		} else if li, lookupErr := routeClient.FetchIssueByIdentifier(ctx, externalID); lookupErr == nil && li != nil {
			issueUUID = li.ID
		}

		updated, updateErr := routeClient.UpdateIssue(ctx, issueUUID, updates)
		if updateErr != nil {
			result.Errors = append(result.Errors, tracker.BatchPushError{
				LocalID: issue.ID,
				Message: fmt.Sprintf("updating %s: %v", externalID, updateErr),
			})
			continue
		}

		result.Updated = append(result.Updated, tracker.BatchPushItem{
			LocalID:     issue.ID,
			ExternalRef: updated.URL,
		})
	}

	return result, nil
}

func (t *Tracker) FieldMapper() tracker.FieldMapper {
	return &linearFieldMapper{config: t.config}
}

// MappingConfig returns the resolved Linear mapping configuration.
func (t *Tracker) MappingConfig() *MappingConfig {
	return t.config
}

func (t *Tracker) IsExternalRef(ref string) bool {
	return IsLinearExternalRef(ref)
}

func (t *Tracker) ExtractIdentifier(ref string) string {
	return ExtractLinearIdentifier(ref)
}

func (t *Tracker) BuildExternalRef(issue *tracker.TrackerIssue) string {
	if issue.URL != "" {
		if canonical, ok := CanonicalizeLinearExternalRef(issue.URL); ok {
			return canonical
		}
		return issue.URL
	}
	return fmt.Sprintf("https://linear.app/issue/%s", issue.Identifier)
}

// ValidatePushStateMappings ensures push has explicit, non-ambiguous status
// mappings for every configured team before any mutation occurs.
func (t *Tracker) ValidatePushStateMappings(ctx context.Context) error {
	if t.config == nil || len(t.config.ExplicitStateMap) == 0 {
		return fmt.Errorf("%s", missingExplicitStateMapMessage)
	}
	for _, teamID := range t.teamIDs {
		client := t.clients[teamID]
		if client == nil {
			continue
		}
		cache, err := BuildStateCache(ctx, client)
		if err != nil {
			return fmt.Errorf("fetching workflow states for team %s: %w", teamID, err)
		}
		for _, status := range []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusBlocked, types.StatusClosed} {
			if _, err := ResolveStateIDForBeadsStatus(cache, status, t.config); err != nil {
				// Only fail for statuses the config explicitly tries to map or when
				// mappings are entirely absent. Missing blocked mappings are allowed
				// until a blocked issue is actually pushed.
				if status == types.StatusBlocked && strings.Contains(err.Error(), "has no configured Linear state") {
					continue
				}
				return err
			}
		}
	}
	return nil
}

// findStateID looks up the Linear workflow state ID for a beads status
// using the given per-team client.
func (t *Tracker) findStateID(ctx context.Context, client *Client, status types.Status) (string, error) {
	cache, err := BuildStateCache(ctx, client)
	if err != nil {
		return "", err
	}
	return ResolveStateIDForBeadsStatus(cache, status, t.config)
}

// primaryClient returns the client for the first configured team.
func (t *Tracker) primaryClient() *Client {
	if len(t.teamIDs) == 0 {
		return nil
	}
	return t.clients[t.teamIDs[0]]
}

// clientForExternalID resolves which per-team client should handle an issue
// identified by its Linear identifier (e.g., "TEAM-123").
func (t *Tracker) clientForExternalID(ctx context.Context, externalID string) *Client {
	if len(t.teamIDs) == 1 {
		return t.primaryClient()
	}

	// Try to fetch the issue from each team's client to find the owner.
	for _, teamID := range t.teamIDs {
		client := t.clients[teamID]
		if client == nil {
			continue
		}
		li, err := client.FetchIssueByIdentifier(ctx, externalID)
		if err == nil && li != nil {
			return client
		}
	}

	return t.primaryClient()
}

// TeamIDs returns the list of configured team IDs.
func (t *Tracker) TeamIDs() []string {
	return t.teamIDs
}

// PrimaryClient returns the client for the first configured team.
// Exported for CLI code that needs direct client access (e.g., push hooks).
func (t *Tracker) PrimaryClient() *Client {
	return t.primaryClient()
}

// getConfig reads a config value from storage, falling back to env var.
// For yaml-only keys (e.g. linear.api_key), reads from config.yaml first
// to match the behavior of cmd/bd/linear.go:getLinearConfig().
func (t *Tracker) getConfig(ctx context.Context, key, envVar string) (string, error) {
	// Secret keys are stored in config.yaml, not the Dolt database,
	// to avoid leaking secrets when pushing to remotes.
	if config.IsYamlOnlyKey(key) {
		if val := config.GetString(key); val != "" {
			return val, nil
		}
		if envVar != "" {
			if envVal := os.Getenv(envVar); envVal != "" {
				return envVal, nil
			}
		}
		return "", nil
	}

	val, err := t.store.GetConfig(ctx, key)
	if err == nil && val != "" {
		return val, nil
	}
	if envVar != "" {
		if envVal := os.Getenv(envVar); envVal != "" {
			return envVal, nil
		}
	}
	return "", nil
}

// linearToTrackerIssue converts a linear.Issue to a tracker.TrackerIssue.
func linearToTrackerIssue(li *Issue) tracker.TrackerIssue {
	ti := tracker.TrackerIssue{
		ID:          li.ID,
		Identifier:  li.Identifier,
		URL:         li.URL,
		Title:       li.Title,
		Description: li.Description,
		Priority:    li.Priority,
		Labels:      make([]string, 0),
		Raw:         li,
	}

	if li.State != nil {
		ti.State = li.State
	}

	if li.Labels != nil {
		for _, l := range li.Labels.Nodes {
			ti.Labels = append(ti.Labels, l.Name)
		}
	}

	if li.Assignee != nil {
		ti.Assignee = li.Assignee.Name
		ti.AssigneeEmail = li.Assignee.Email
		ti.AssigneeID = li.Assignee.ID
	}

	if li.Parent != nil {
		ti.ParentID = li.Parent.Identifier
		ti.ParentInternalID = li.Parent.ID
	}

	if li.ProjectMilestone != nil {
		ti.Metadata = map[string]interface{}{
			"linear": map[string]interface{}{
				"project_milestone": li.ProjectMilestone,
			},
		}
	}

	if t, err := time.Parse(time.RFC3339, li.CreatedAt); err == nil {
		ti.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, li.UpdatedAt); err == nil {
		ti.UpdatedAt = t
	}
	if li.CompletedAt != "" {
		if t, err := time.Parse(time.RFC3339, li.CompletedAt); err == nil {
			ti.CompletedAt = &t
		}
	}

	return ti
}

// BuildStateCacheFromTracker builds a StateCache using the tracker's primary client.
// This allows CLI code to set up PushHooks.BuildStateCache without accessing the client directly.
func BuildStateCacheFromTracker(ctx context.Context, t *Tracker) (*StateCache, error) {
	client := t.primaryClient()
	if client == nil {
		return nil, fmt.Errorf("Linear tracker not initialized")
	}
	return BuildStateCache(ctx, client)
}

// configLoaderAdapter wraps storage.Storage to implement linear.ConfigLoader.
type configLoaderAdapter struct {
	ctx   context.Context
	store storage.Storage
}

func (c *configLoaderAdapter) GetAllConfig() (map[string]string, error) {
	return c.store.GetAllConfig(c.ctx)
}
