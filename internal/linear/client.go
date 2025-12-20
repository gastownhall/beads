package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// IssuesQuery is the GraphQL query for fetching issues with all required fields.
// Used by both FetchIssues and FetchIssuesSince for consistency.
const IssuesQuery = `
	query Issues($filter: IssueFilter!, $first: Int!, $after: String) {
		issues(
			first: $first
			after: $after
			filter: $filter
		) {
			nodes {
				id
				identifier
				title
				description
				url
				priority
				state {
					id
					name
					type
				}
				assignee {
					id
					name
					email
					displayName
				}
				labels {
					nodes {
						id
						name
					}
				}
				parent {
					id
					identifier
				}
				relations {
					nodes {
						id
						type
						relatedIssue {
							id
							identifier
						}
					}
				}
				createdAt
				updatedAt
				completedAt
			}
			pageInfo {
				hasNextPage
				endCursor
			}
		}
	}
`

// NewClient creates a new Linear client with the given API key and team ID.
func NewClient(apiKey, teamID string) *Client {
	return &Client{
		APIKey:   apiKey,
		TeamID:   teamID,
		Endpoint: DefaultAPIEndpoint,
		HTTPClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

// WithEndpoint returns a new client configured to use the specified endpoint.
// This is useful for testing with mock servers or connecting to self-hosted instances.
func (c *Client) WithEndpoint(endpoint string) *Client {
	return &Client{
		APIKey:     c.APIKey,
		TeamID:     c.TeamID,
		Endpoint:   endpoint,
		HTTPClient: c.HTTPClient,
	}
}

// WithHTTPClient returns a new client configured to use the specified HTTP client.
// This is useful for testing or customizing timeouts and transport settings.
func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	return &Client{
		APIKey:     c.APIKey,
		TeamID:     c.TeamID,
		Endpoint:   c.Endpoint,
		HTTPClient: httpClient,
	}
}

// Execute sends a GraphQL request to the Linear API.
// Handles rate limiting with exponential backoff.
func (c *Client) Execute(ctx context.Context, req *GraphQLRequest) (json.RawMessage, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", c.APIKey)

		resp, err := c.HTTPClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("request failed (attempt %d/%d): %w", attempt+1, MaxRetries+1, err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response (attempt %d/%d): %w", attempt+1, MaxRetries+1, err)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			delay := RetryDelay * time.Duration(1<<attempt) // Exponential backoff
			lastErr = fmt.Errorf("rate limited (attempt %d/%d), retrying after %v", attempt+1, MaxRetries+1, delay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
				continue
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("API error: %s (status %d)", string(respBody), resp.StatusCode)
		}

		var gqlResp struct {
			Data   json.RawMessage `json:"data"`
			Errors []GraphQLError  `json:"errors,omitempty"`
		}
		if err := json.Unmarshal(respBody, &gqlResp); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w (body: %s)", err, string(respBody))
		}

		if len(gqlResp.Errors) > 0 {
			errMsgs := make([]string, len(gqlResp.Errors))
			for i, e := range gqlResp.Errors {
				errMsgs[i] = e.Message
			}
			return nil, fmt.Errorf("GraphQL errors: %s", strings.Join(errMsgs, "; "))
		}

		return gqlResp.Data, nil
	}

	return nil, fmt.Errorf("max retries (%d) exceeded: %w", MaxRetries+1, lastErr)
}

// FetchIssues retrieves issues from Linear with optional filtering by state.
// state can be: "open" (unstarted/started), "closed" (completed/canceled), or "all".
func (c *Client) FetchIssues(ctx context.Context, state string) ([]Issue, error) {
	var allIssues []Issue
	var cursor string

	filter := map[string]interface{}{
		"team": map[string]interface{}{
			"id": map[string]interface{}{
				"eq": c.TeamID,
			},
		},
	}
	switch state {
	case "open":
		filter["state"] = map[string]interface{}{
			"type": map[string]interface{}{
				"in": []string{"backlog", "unstarted", "started"},
			},
		}
	case "closed":
		filter["state"] = map[string]interface{}{
			"type": map[string]interface{}{
				"in": []string{"completed", "canceled"},
			},
		}
	}

	for {
		variables := map[string]interface{}{
			"filter": filter,
			"first":  MaxPageSize,
		}
		if cursor != "" {
			variables["after"] = cursor
		}

		req := &GraphQLRequest{
			Query:     IssuesQuery,
			Variables: variables,
		}

		data, err := c.Execute(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch issues: %w", err)
		}

		var issuesResp IssuesResponse
		if err := json.Unmarshal(data, &issuesResp); err != nil {
			return nil, fmt.Errorf("failed to parse issues response: %w", err)
		}

		allIssues = append(allIssues, issuesResp.Issues.Nodes...)

		if !issuesResp.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = issuesResp.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// FetchIssuesSince retrieves issues from Linear that have been updated since the given time.
// This enables incremental sync by only fetching issues modified after the last sync.
// The state parameter can be: "open", "closed", or "all".
func (c *Client) FetchIssuesSince(ctx context.Context, state string, since time.Time) ([]Issue, error) {
	var allIssues []Issue
	var cursor string

	// Build the filter with team and updatedAt constraint.
	// Linear uses ISO8601 format for date comparisons.
	sinceStr := since.UTC().Format(time.RFC3339)
	filter := map[string]interface{}{
		"team": map[string]interface{}{
			"id": map[string]interface{}{
				"eq": c.TeamID,
			},
		},
		"updatedAt": map[string]interface{}{
			"gte": sinceStr,
		},
	}

	// Add state filter if specified
	switch state {
	case "open":
		filter["state"] = map[string]interface{}{
			"type": map[string]interface{}{
				"in": []string{"backlog", "unstarted", "started"},
			},
		}
	case "closed":
		filter["state"] = map[string]interface{}{
			"type": map[string]interface{}{
				"in": []string{"completed", "canceled"},
			},
		}
	}

	for {
		variables := map[string]interface{}{
			"filter": filter,
			"first":  MaxPageSize,
		}
		if cursor != "" {
			variables["after"] = cursor
		}

		req := &GraphQLRequest{
			Query:     IssuesQuery,
			Variables: variables,
		}

		data, err := c.Execute(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch issues since %s: %w", sinceStr, err)
		}

		var issuesResp IssuesResponse
		if err := json.Unmarshal(data, &issuesResp); err != nil {
			return nil, fmt.Errorf("failed to parse issues response: %w", err)
		}

		allIssues = append(allIssues, issuesResp.Issues.Nodes...)

		if !issuesResp.Issues.PageInfo.HasNextPage {
			break
		}
		cursor = issuesResp.Issues.PageInfo.EndCursor
	}

	return allIssues, nil
}

// GetTeamStates fetches the workflow states for the configured team.
func (c *Client) GetTeamStates(ctx context.Context) ([]State, error) {
	query := `
		query TeamStates($teamId: String!) {
			team(id: $teamId) {
				id
				states {
					nodes {
						id
						name
						type
					}
				}
			}
		}
	`

	req := &GraphQLRequest{
		Query: query,
		Variables: map[string]interface{}{
			"teamId": c.TeamID,
		},
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch team states: %w", err)
	}

	var teamResp TeamResponse
	if err := json.Unmarshal(data, &teamResp); err != nil {
		return nil, fmt.Errorf("failed to parse team states response: %w", err)
	}

	if teamResp.Team.States == nil {
		return nil, fmt.Errorf("no states found for team")
	}

	return teamResp.Team.States.Nodes, nil
}

// CreateIssue creates a new issue in Linear.
func (c *Client) CreateIssue(ctx context.Context, title, description string, priority int, stateID string, labelIDs []string) (*Issue, error) {
	query := `
		mutation CreateIssue($input: IssueCreateInput!) {
			issueCreate(input: $input) {
				success
				issue {
					id
					identifier
					title
					description
					url
					priority
					state {
						id
						name
						type
					}
					createdAt
					updatedAt
				}
			}
		}
	`

	input := map[string]interface{}{
		"teamId":      c.TeamID,
		"title":       title,
		"description": description,
	}

	if priority > 0 {
		input["priority"] = priority
	}

	if stateID != "" {
		input["stateId"] = stateID
	}

	if len(labelIDs) > 0 {
		input["labelIds"] = labelIDs
	}

	req := &GraphQLRequest{
		Query: query,
		Variables: map[string]interface{}{
			"input": input,
		},
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create issue: %w", err)
	}

	var createResp IssueCreateResponse
	if err := json.Unmarshal(data, &createResp); err != nil {
		return nil, fmt.Errorf("failed to parse create response: %w", err)
	}

	if !createResp.IssueCreate.Success {
		return nil, fmt.Errorf("issue creation reported as unsuccessful")
	}

	return &createResp.IssueCreate.Issue, nil
}

// UpdateIssue updates an existing issue in Linear.
func (c *Client) UpdateIssue(ctx context.Context, issueID string, updates map[string]interface{}) (*Issue, error) {
	query := `
		mutation UpdateIssue($id: String!, $input: IssueUpdateInput!) {
			issueUpdate(id: $id, input: $input) {
				success
				issue {
					id
					identifier
					title
					description
					url
					priority
					state {
						id
						name
						type
					}
					updatedAt
				}
			}
		}
	`

	req := &GraphQLRequest{
		Query: query,
		Variables: map[string]interface{}{
			"id":    issueID,
			"input": updates,
		},
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to update issue: %w", err)
	}

	var updateResp IssueUpdateResponse
	if err := json.Unmarshal(data, &updateResp); err != nil {
		return nil, fmt.Errorf("failed to parse update response: %w", err)
	}

	if !updateResp.IssueUpdate.Success {
		return nil, fmt.Errorf("issue update reported as unsuccessful")
	}

	return &updateResp.IssueUpdate.Issue, nil
}

// FetchIssueByIdentifier retrieves a single issue from Linear by its identifier (e.g., "TEAM-123").
// Returns nil if the issue is not found.
func (c *Client) FetchIssueByIdentifier(ctx context.Context, identifier string) (*Issue, error) {
	query := `
		query IssueByIdentifier($filter: IssueFilter!) {
			issues(filter: $filter, first: 1) {
				nodes {
					id
					identifier
					title
					description
					url
					priority
					state {
						id
						name
						type
					}
					assignee {
						id
						name
						email
						displayName
					}
					labels {
						nodes {
							id
							name
						}
					}
					createdAt
					updatedAt
					completedAt
				}
			}
		}
	`

	// Build filter to search by identifier number and team prefix
	// Linear identifiers look like "TEAM-123", we filter by number
	// and validate the full identifier in the results
	variables := map[string]interface{}{
		"filter": map[string]interface{}{
			"team": map[string]interface{}{
				"id": map[string]interface{}{
					"eq": c.TeamID,
				},
			},
		},
	}

	// Extract the issue number from identifier (e.g., "123" from "TEAM-123")
	parts := strings.Split(identifier, "-")
	if len(parts) >= 2 {
		if number, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			// Add number filter for more precise matching
			variables["filter"].(map[string]interface{})["number"] = map[string]interface{}{
				"eq": number,
			}
		}
	}

	req := &GraphQLRequest{
		Query:     query,
		Variables: variables,
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch issue by identifier: %w", err)
	}

	var issuesResp IssuesResponse
	if err := json.Unmarshal(data, &issuesResp); err != nil {
		return nil, fmt.Errorf("failed to parse issues response: %w", err)
	}

	// Find the exact match by identifier (in case of partial matches)
	for _, issue := range issuesResp.Issues.Nodes {
		if issue.Identifier == identifier {
			return &issue, nil
		}
	}

	return nil, nil // Issue not found
}

// BuildStateCache fetches and caches team states.
func BuildStateCache(ctx context.Context, client *Client) (*StateCache, error) {
	states, err := client.GetTeamStates(ctx)
	if err != nil {
		return nil, err
	}

	cache := &StateCache{
		States:     states,
		StatesByID: make(map[string]State),
	}

	for _, s := range states {
		cache.StatesByID[s.ID] = s
		if cache.OpenStateID == "" && (s.Type == "unstarted" || s.Type == "backlog") {
			cache.OpenStateID = s.ID
		}
	}

	return cache, nil
}

// FindStateForBeadsStatus returns the best Linear state ID for a Beads status.
func (sc *StateCache) FindStateForBeadsStatus(status types.Status) string {
	targetType := StatusToLinearStateType(status)

	for _, s := range sc.States {
		if s.Type == targetType {
			return s.ID
		}
	}

	if len(sc.States) > 0 {
		return sc.States[0].ID
	}

	return ""
}

// ExtractLinearIdentifier extracts the Linear issue identifier (e.g., "TEAM-123") from a Linear URL.
func ExtractLinearIdentifier(url string) string {
	// Linear URLs look like: https://linear.app/team/issue/TEAM-123/title
	// We want to extract "TEAM-123"
	parts := strings.Split(url, "/")
	for i, part := range parts {
		if part == "issue" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// CanonicalizeLinearExternalRef returns a stable Linear issue URL without the slug.
// Example: https://linear.app/team/issue/TEAM-123/title -> https://linear.app/team/issue/TEAM-123
// Returns ok=false if the URL isn't a recognizable Linear issue URL.
func CanonicalizeLinearExternalRef(externalRef string) (canonical string, ok bool) {
	if externalRef == "" || !IsLinearExternalRef(externalRef) {
		return "", false
	}

	parsed, err := url.Parse(externalRef)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}

	segments := strings.Split(parsed.Path, "/")
	for i, segment := range segments {
		if segment == "issue" && i+1 < len(segments) && segments[i+1] != "" {
			path := "/" + strings.Join(segments[1:i+2], "/")
			return fmt.Sprintf("%s://%s%s", parsed.Scheme, parsed.Host, path), true
		}
	}

	return "", false
}

// IsLinearExternalRef checks if an external_ref URL is a Linear issue URL.
func IsLinearExternalRef(externalRef string) bool {
	return strings.Contains(externalRef, "linear.app/") && strings.Contains(externalRef, "/issue/")
}

// FetchTeams retrieves all teams accessible with the current API key.
// This is useful for discovering the team ID needed for configuration.
func (c *Client) FetchTeams(ctx context.Context) ([]Team, error) {
	query := `
		query {
			teams {
				nodes {
					id
					name
					key
				}
			}
		}
	`

	req := &GraphQLRequest{
		Query: query,
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch teams: %w", err)
	}

	var teamsResp TeamsResponse
	if err := json.Unmarshal(data, &teamsResp); err != nil {
		return nil, fmt.Errorf("failed to parse teams response: %w", err)
	}

	return teamsResp.Teams.Nodes, nil
}
