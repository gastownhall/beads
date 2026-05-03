package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/types"
)

// projectsQuery is the GraphQL query for fetching projects.
const projectsQuery = `
	query Projects($filter: ProjectFilter!, $first: Int!, $after: String) {
		projects(
			first: $first
			after: $after
			filter: $filter
		) {
			nodes {
				id
				name
				description
				slugId
				url
				state
				progress
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

// issuesQuery is the GraphQL query for fetching issues with all required fields.
// Used by both FetchIssues and FetchIssuesSince for consistency.
const issuesQuery = `
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
		AuthMode: AuthModeAPIKey,
		HTTPClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

// NewOAuthClient creates a new Linear client that authenticates via OAuth
// client_credentials flow instead of a static API key.
func NewOAuthClient(oauthConfig OAuthConfig, teamID string) *Client {
	return &Client{
		TeamID:       teamID,
		Endpoint:     DefaultAPIEndpoint,
		AuthMode:     AuthModeOAuth,
		TokenManager: NewOAuthTokenManager(oauthConfig),
		HTTPClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

// WithEndpoint returns a new client configured to use the specified endpoint.
// This is useful for testing with mock servers or connecting to self-hosted instances.
func (c *Client) WithEndpoint(endpoint string) *Client {
	return &Client{
		APIKey:         c.APIKey,
		TeamID:         c.TeamID,
		ProjectID:      c.ProjectID,
		Endpoint:       endpoint,
		HTTPClient:     c.HTTPClient,
		AuthMode:       c.AuthMode,
		TokenManager:   c.TokenManager,
		RateLimitFloor: c.RateLimitFloor,
	}
}

// WithHTTPClient returns a new client configured to use the specified HTTP client.
// This is useful for testing or customizing timeouts and transport settings.
func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	return &Client{
		APIKey:         c.APIKey,
		TeamID:         c.TeamID,
		ProjectID:      c.ProjectID,
		Endpoint:       c.Endpoint,
		HTTPClient:     httpClient,
		AuthMode:       c.AuthMode,
		TokenManager:   c.TokenManager,
		RateLimitFloor: c.RateLimitFloor,
	}
}

// WithProjectID returns a new client configured to filter issues by the specified project.
// When set, FetchIssues and FetchIssuesSince will only return issues belonging to this project.
func (c *Client) WithProjectID(projectID string) *Client {
	return &Client{
		APIKey:         c.APIKey,
		TeamID:         c.TeamID,
		ProjectID:      projectID,
		Endpoint:       c.Endpoint,
		HTTPClient:     c.HTTPClient,
		AuthMode:       c.AuthMode,
		TokenManager:   c.TokenManager,
		RateLimitFloor: c.RateLimitFloor,
	}
}

// authHeader returns the Authorization header value for this client.
func (c *Client) authHeader() (string, error) {
	switch c.AuthMode {
	case AuthModeOAuth:
		token, err := c.TokenManager.Token()
		if err != nil {
			return "", fmt.Errorf("failed to get OAuth token: %w", err)
		}
		return "Bearer " + token, nil
	default:
		return c.APIKey, nil
	}
}

// WithRateLimitFloor returns a new client with the specified rate-limit circuit-breaker floor.
// When remaining API quota drops below this value, Execute returns ErrRateLimitExhausted.
func (c *Client) WithRateLimitFloor(floor int) *Client {
	return &Client{
		APIKey:         c.APIKey,
		TeamID:         c.TeamID,
		ProjectID:      c.ProjectID,
		Endpoint:       c.Endpoint,
		HTTPClient:     c.HTTPClient,
		AuthMode:       c.AuthMode,
		TokenManager:   c.TokenManager,
		RateLimitFloor: floor,
	}
}

// rateLimitFloor returns the effective circuit-breaker floor, using the
// default when the client has no explicit override.
func (c *Client) rateLimitFloor() int {
	if c.RateLimitFloor > 0 {
		return c.RateLimitFloor
	}
	return DefaultRateLimitFloor
}

// parseRetryAfter parses the Retry-After header value, which may be an
// integer number of seconds or an HTTP-date. Returns zero duration if
// the header is absent or unparseable.
//
// The integer form ("120") is tried first. For the HTTP-date form,
// http.ParseTime is used, which covers RFC 1123, RFC 850, and ANSI C
// formats as required by RFC 9110 §10.2.3.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if delay := time.Until(t); delay > 0 {
			return delay
		}
	}
	return 0
}

// parseRateLimitHeaders extracts rate-limit metadata from HTTP response headers.
func parseRateLimitHeaders(h http.Header) RateLimitInfo {
	info := RateLimitInfo{RequestsRemaining: -1}
	info.RetryAfter = parseRetryAfter(h.Get("Retry-After"))
	if v := h.Get("X-RateLimit-Requests-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			info.RequestsRemaining = n
		}
	}
	if v := h.Get("X-RateLimit-Requests-Reset"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			info.RequestsReset = t
		}
	}
	return info
}

// Execute sends a GraphQL request to the Linear API.
// Handles rate limiting with server-hint-aware backoff: when a 429 response
// includes a Retry-After header, that delay is preferred over the computed
// exponential backoff. A circuit breaker returns ErrRateLimitExhausted when
// remaining quota drops below the configured floor (linear.rate_limit_floor).
// OAuth clients also invalidate and retry once on 401 responses.
func (c *Client) Execute(ctx context.Context, req *GraphQLRequest) (json.RawMessage, error) {
	data, statusCode, err := c.executeOnce(ctx, req)
	if err == nil {
		return data, nil
	}

	// On 401 with OAuth, invalidate token and retry once.
	if statusCode == http.StatusUnauthorized && c.AuthMode == AuthModeOAuth {
		debug.Logf("oauth: received 401, invalidating token and retrying")
		c.TokenManager.Invalidate()
		data, _, retryErr := c.executeOnce(ctx, req)
		if retryErr != nil {
			return nil, retryErr
		}
		return data, nil
	}

	return nil, err
}

// executeOnce performs the actual HTTP request loop with rate-limit retries.
// Returns the response data, the last HTTP status code encountered, and any error.
func (c *Client) executeOnce(ctx context.Context, req *GraphQLRequest) (json.RawMessage, int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	var lastErr error
	var lastStatus int
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		authValue, err := c.authHeader()
		if err != nil {
			return nil, 0, err
		}
		httpReq.Header.Set("Authorization", authValue)

		resp, err := c.HTTPClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("request failed (attempt %d/%d): %w", attempt+1, MaxRetries+1, err)
			continue
		}

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
		_ = resp.Body.Close() // Best effort: HTTP body close; connection may be reused regardless
		if err != nil {
			lastErr = fmt.Errorf("failed to read response (attempt %d/%d): %w", attempt+1, MaxRetries+1, err)
			continue
		}

		lastStatus = resp.StatusCode
		rl := parseRateLimitHeaders(resp.Header)

		// Circuit breaker: stop early when remaining quota is critically low.
		if rl.RequestsRemaining >= 0 && rl.RequestsRemaining < c.rateLimitFloor() {
			return nil, lastStatus, &ErrRateLimitExhausted{
				Remaining: rl.RequestsRemaining,
				Floor:     c.rateLimitFloor(),
				ResetsAt:  rl.RequestsReset,
			}
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			delay := rl.RetryAfter
			if delay == 0 {
				delay = RetryDelay * time.Duration(1<<attempt) // Exponential backoff
				if half := int64(delay / 2); half > 0 {
					delay += time.Duration(rand.Int64N(half)) //nolint:gosec // G404: jitter for retry backoff does not need crypto rand
				}
			} else if delay > MaxRetryAfterDelay {
				fmt.Fprintf(os.Stderr, "linear: Retry-After %v exceeds cap %v; using cap\n", delay, MaxRetryAfterDelay)
				delay = MaxRetryAfterDelay
			}
			lastErr = fmt.Errorf("rate limited (attempt %d/%d), retrying after %v", attempt+1, MaxRetries+1, delay)
			select {
			case <-ctx.Done():
				return nil, lastStatus, ctx.Err()
			case <-time.After(delay):
				continue
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, lastStatus, fmt.Errorf("API error: %s (status %d)", string(respBody), resp.StatusCode)
		}

		var gqlResp struct {
			Data   json.RawMessage `json:"data"`
			Errors []GraphQLError  `json:"errors,omitempty"`
		}
		if err := json.Unmarshal(respBody, &gqlResp); err != nil {
			return nil, lastStatus, fmt.Errorf("failed to parse response: %w (body: %s)", err, string(respBody))
		}

		if len(gqlResp.Errors) > 0 {
			errMsgs := make([]string, len(gqlResp.Errors))
			for i, e := range gqlResp.Errors {
				errMsgs[i] = e.Message
			}
			return nil, lastStatus, fmt.Errorf("GraphQL errors: %s", strings.Join(errMsgs, "; "))
		}

		return gqlResp.Data, lastStatus, nil
	}

	return nil, lastStatus, fmt.Errorf("max retries (%d) exceeded: %w", MaxRetries+1, lastErr)
}

// FetchIssues retrieves issues from Linear with optional filtering by state.
// state can be: "open" (unstarted/started), "closed" (completed/canceled), or "all".
// If ProjectID is set on the client, only issues from that project are returned.
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

	// Add project filter if configured
	if c.ProjectID != "" {
		filter["project"] = map[string]interface{}{
			"id": map[string]interface{}{
				"eq": c.ProjectID,
			},
		}
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

	page := 0
	for {
		select {
		case <-ctx.Done():
			return allIssues, ctx.Err()
		default:
		}

		page++
		if page > MaxPages {
			return nil, fmt.Errorf("pagination limit exceeded: stopped after %d pages", MaxPages)
		}

		variables := map[string]interface{}{
			"filter": filter,
			"first":  MaxPageSize,
		}
		if cursor != "" {
			variables["after"] = cursor
		}

		req := &GraphQLRequest{
			Query:     issuesQuery,
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
// If ProjectID is set on the client, only issues from that project are returned.
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

	// Add project filter if configured
	if c.ProjectID != "" {
		filter["project"] = map[string]interface{}{
			"id": map[string]interface{}{
				"eq": c.ProjectID,
			},
		}
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

	page := 0
	for {
		select {
		case <-ctx.Done():
			return allIssues, ctx.Err()
		default:
		}

		page++
		if page > MaxPages {
			return nil, fmt.Errorf("pagination limit exceeded: stopped after %d pages", MaxPages)
		}

		variables := map[string]interface{}{
			"filter": filter,
			"first":  MaxPageSize,
		}
		if cursor != "" {
			variables["after"] = cursor
		}

		req := &GraphQLRequest{
			Query:     issuesQuery,
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

// FindIssueByDescriptionContains searches for an issue whose description
// contains the given text. This powers idempotency dedup: we embed a
// deterministic marker in the description and search for it before creating.
// Returns nil (no error) when no match is found.
func (c *Client) FindIssueByDescriptionContains(ctx context.Context, text string) (*Issue, error) {
	query := `
		query FindByDescription($filter: IssueFilter!) {
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
					createdAt
					updatedAt
				}
			}
		}
	`

	filter := map[string]interface{}{
		"team": map[string]interface{}{
			"id": map[string]interface{}{
				"eq": c.TeamID,
			},
		},
		"description": map[string]interface{}{
			"contains": text,
		},
	}

	req := &GraphQLRequest{
		Query: query,
		Variables: map[string]interface{}{
			"filter": filter,
		},
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to search issues by description: %w", err)
	}

	var issuesResp IssuesResponse
	if err := json.Unmarshal(data, &issuesResp); err != nil {
		return nil, fmt.Errorf("failed to parse description search response: %w", err)
	}

	if len(issuesResp.Issues.Nodes) > 0 {
		return &issuesResp.Issues.Nodes[0], nil
	}
	return nil, nil
}

// issueCreateMutation is the GraphQL mutation for creating a Linear issue.
const issueCreateMutation = `
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

// buildIssueCreateInput constructs the GraphQL input map for an issueCreate mutation.
func (c *Client) buildIssueCreateInput(title, description string, priority int, stateID string, labelIDs []string) map[string]interface{} {
	input := map[string]interface{}{
		"teamId":      c.TeamID,
		"title":       title,
		"description": description,
	}
	if c.ProjectID != "" {
		input["projectId"] = c.ProjectID
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
	return input
}

// CreateIssue creates a new issue in Linear.
func (c *Client) CreateIssue(ctx context.Context, title, description string, priority int, stateID string, labelIDs []string) (*Issue, error) {
	req := &GraphQLRequest{
		Query:     issueCreateMutation,
		Variables: map[string]interface{}{"input": c.buildIssueCreateInput(title, description, priority, stateID, labelIDs)},
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

// createIssueSingleAttempt executes the issueCreate mutation exactly once,
// without the retry loop used by Execute. This is intentional: retrying a
// mutation that may have already reached Linear risks creating a duplicate.
// The caller (CreateIssueIdempotent) handles retry safety by re-searching for
// the idempotency marker after any failure.
func (c *Client) createIssueSingleAttempt(ctx context.Context, title, description string, priority int, stateID string, labelIDs []string) (*Issue, error) {
	req := &GraphQLRequest{
		Query:     issueCreateMutation,
		Variables: map[string]interface{}{"input": c.buildIssueCreateInput(title, description, priority, stateID, labelIDs)},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", c.APIKey)

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
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

	var createResp IssueCreateResponse
	if err := json.Unmarshal(gqlResp.Data, &createResp); err != nil {
		return nil, fmt.Errorf("failed to parse create response: %w", err)
	}

	if !createResp.IssueCreate.Success {
		return nil, fmt.Errorf("issue creation reported as unsuccessful")
	}

	return &createResp.IssueCreate.Issue, nil
}

// CreateIssueIdempotent creates a new Linear issue with dedup protection.
// It embeds the given idempotency marker in the description and, before
// creating, queries Linear to see if an issue with that marker already exists.
// If a match is found (e.g., from a prior interrupted sync), the existing
// issue is returned without creating a duplicate.
//
// The create is performed as a single attempt (no internal retry) to avoid the
// following race: if issueCreate reaches Linear but the HTTP response is lost
// (network timeout, connection drop), a blind retry would create a second issue
// with the same marker. Instead, after any create failure, this function
// re-searches for the marker so that the caller can safely retry the entire
// CreateIssueIdempotent call and get a consistent result.
//
// Note: concurrent creates from multiple sources (e.g., two sync processes
// running simultaneously) cannot be made fully atomic without server-side
// uniqueness enforcement, which Linear does not provide. The dedup window is
// bounded by Linear's search-index propagation delay.
func (c *Client) CreateIssueIdempotent(ctx context.Context, title, description string, priority int, stateID string, labelIDs []string, marker string) (*Issue, bool, error) {
	existing, err := c.FindIssueByDescriptionContains(ctx, marker)
	if err != nil {
		return nil, false, fmt.Errorf("idempotency check failed: %w", err)
	}
	if existing != nil {
		return existing, true, nil
	}

	description = AppendIdempotencyMarker(description, marker)
	issue, err := c.createIssueSingleAttempt(ctx, title, description, priority, stateID, labelIDs)
	if err != nil {
		// The mutation may have reached Linear despite the error. Re-check for
		// the marker so callers retrying CreateIssueIdempotent get a consistent
		// result rather than creating a duplicate.
		if found, searchErr := c.FindIssueByDescriptionContains(ctx, marker); searchErr == nil && found != nil {
			return found, true, nil
		}
		return nil, false, err
	}
	return issue, false, nil
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

// BatchCreateIssues creates multiple issues in Linear using the issueBatchCreate mutation.
// Inputs are chunked into groups of BatchSize (50).
//
// On ambiguous failure (API error or success=false), this method does NOT blindly
// retry the full chunk—Linear may have partially applied the mutation. Instead it
// searches for each issue's idempotency marker (embedded in the description) to
// discover which issues were actually created, and returns an error for the rest.
func (c *Client) BatchCreateIssues(ctx context.Context, inputs []IssueCreateInput) ([]Issue, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	query := `
		mutation BatchCreateIssues($input: [IssueCreateInput!]!) {
			issueBatchCreate(input: $input) {
				success
				issues {
					id
					identifier
					title
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

	var allIssues []Issue
	for start := 0; start < len(inputs); start += BatchSize {
		end := start + BatchSize
		if end > len(inputs) {
			end = len(inputs)
		}
		chunk := inputs[start:end]

		req := &GraphQLRequest{
			Query: query,
			Variables: map[string]interface{}{
				"input": chunk,
			},
		}

		data, err := c.Execute(ctx, req)
		if err != nil {
			found, recoverErr := c.recoverAfterAmbiguousBatch(ctx, chunk)
			if recoverErr != nil {
				return allIssues, fmt.Errorf("batch create failed and recovery search also failed: %w (batch error: %v)", recoverErr, err)
			}
			allIssues = append(allIssues, found...)
			if len(found) < len(chunk) {
				return allIssues, fmt.Errorf("batch create failed; %d of %d issues unconfirmed (batch error: %v)", len(chunk)-len(found), len(chunk), err)
			}
			continue
		}

		var batchResp IssueBatchCreateResponse
		if err := json.Unmarshal(data, &batchResp); err != nil {
			return allIssues, fmt.Errorf("failed to parse batch create response: %w", err)
		}

		if !batchResp.IssueBatchCreate.Success {
			found, recoverErr := c.recoverAfterAmbiguousBatch(ctx, chunk)
			if recoverErr != nil {
				return allIssues, fmt.Errorf("batch create unsuccessful and recovery search also failed: %w", recoverErr)
			}
			allIssues = append(allIssues, found...)
			if len(found) < len(chunk) {
				return allIssues, fmt.Errorf("batch create unsuccessful; %d of %d issues unconfirmed", len(chunk)-len(found), len(chunk))
			}
			continue
		}

		allIssues = append(allIssues, batchResp.IssueBatchCreate.Issues...)
	}

	return allIssues, nil
}

// recoverAfterAmbiguousBatch searches Linear for each issue in a failed batch
// chunk to determine which were actually created. It looks for the idempotency
// marker (<!-- bd-idempotency: ... -->) embedded in each input's description.
// Returns only the issues confirmed to exist in Linear.
func (c *Client) recoverAfterAmbiguousBatch(ctx context.Context, chunk []IssueCreateInput) ([]Issue, error) {
	var found []Issue
	for _, input := range chunk {
		marker := extractIdempotencyMarker(input.Description)
		if marker == "" {
			continue
		}
		existing, err := c.FindIssueByDescriptionContains(ctx, marker)
		if err != nil {
			return found, fmt.Errorf("recovery search failed for %q: %w", input.Title, err)
		}
		if existing != nil {
			found = append(found, *existing)
		}
	}
	return found, nil
}

// extractIdempotencyMarker extracts the bd-idempotency HTML comment from a
// description string. Returns "" if no marker is found.
func extractIdempotencyMarker(description string) string {
	idx := strings.Index(description, idempotencyPrefix)
	if idx < 0 {
		return ""
	}
	end := strings.Index(description[idx:], idempotencySuffix)
	if end < 0 {
		return ""
	}
	return description[idx : idx+end+len(idempotencySuffix)]
}

// BatchUpdateIssues updates multiple issues in Linear using the issueBatchUpdate mutation.
// This applies the SAME update to all specified issue IDs per call. IDs are chunked
// into groups of BatchSize (50). If a batch call fails, it falls back to per-issue
// UpdateIssue for that chunk.
func (c *Client) BatchUpdateIssues(ctx context.Context, ids []string, updates map[string]interface{}) ([]Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	query := `
		mutation BatchUpdateIssues($ids: [UUID!]!, $input: IssueUpdateInput!) {
			issueBatchUpdate(ids: $ids, input: $input) {
				success
				issues {
					id
					identifier
					title
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

	var allIssues []Issue
	for start := 0; start < len(ids); start += BatchSize {
		end := start + BatchSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]

		req := &GraphQLRequest{
			Query: query,
			Variables: map[string]interface{}{
				"ids":   chunk,
				"input": updates,
			},
		}

		data, err := c.Execute(ctx, req)
		if err != nil {
			for _, id := range chunk {
				issue, updateErr := c.UpdateIssue(ctx, id, updates)
				if updateErr != nil {
					return allIssues, fmt.Errorf("batch update failed, single-issue fallback also failed for %s: %w (batch error: %v)", id, updateErr, err)
				}
				allIssues = append(allIssues, *issue)
			}
			continue
		}

		var batchResp IssueBatchUpdateResponse
		if err := json.Unmarshal(data, &batchResp); err != nil {
			return allIssues, fmt.Errorf("failed to parse batch update response: %w", err)
		}

		if !batchResp.IssueBatchUpdate.Success {
			for _, id := range chunk {
				issue, updateErr := c.UpdateIssue(ctx, id, updates)
				if updateErr != nil {
					return allIssues, fmt.Errorf("batch update unsuccessful, single-issue fallback also failed for %s: %w", id, updateErr)
				}
				allIssues = append(allIssues, *issue)
			}
			continue
		}

		allIssues = append(allIssues, batchResp.IssueBatchUpdate.Issues...)
	}

	return allIssues, nil
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

// FetchProjects retrieves projects from Linear with optional filtering by state.
// state can be: "planned", "started", "paused", "completed", "canceled", or "all"/"".
func (c *Client) FetchProjects(ctx context.Context, state string) ([]Project, error) {
	var allProjects []Project
	var cursor string

	filter := map[string]interface{}{
		"team": map[string]interface{}{
			"id": map[string]interface{}{
				"eq": c.TeamID,
			},
		},
	}

	if state != "all" && state != "" {
		filter["state"] = map[string]interface{}{
			"eq": state,
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
			Query:     projectsQuery,
			Variables: variables,
		}

		data, err := c.Execute(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch projects: %w", err)
		}

		var projectsResp ProjectsResponse
		if err := json.Unmarshal(data, &projectsResp); err != nil {
			return nil, fmt.Errorf("failed to parse projects response: %w", err)
		}

		allProjects = append(allProjects, projectsResp.Projects.Nodes...)

		if !projectsResp.Projects.PageInfo.HasNextPage {
			break
		}
		cursor = projectsResp.Projects.PageInfo.EndCursor
	}

	return allProjects, nil
}

// CreateProject creates a new project in Linear.
func (c *Client) CreateProject(ctx context.Context, name, description, state string) (*Project, error) {
	query := `
		mutation CreateProject($input: ProjectCreateInput!) {
			projectCreate(input: $input) {
				success
				project {
					id
					name
					description
					slugId
					url
					state
					progress
					createdAt
					updatedAt
				}
			}
		}
	`

	input := map[string]interface{}{
		"teamIds":     []string{c.TeamID},
		"name":        name,
		"description": description,
	}

	if state != "" {
		input["state"] = state
	}

	req := &GraphQLRequest{
		Query: query,
		Variables: map[string]interface{}{
			"input": input,
		},
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	var createResp ProjectCreateResponse
	if err := json.Unmarshal(data, &createResp); err != nil {
		return nil, fmt.Errorf("failed to parse create project response: %w", err)
	}

	if !createResp.ProjectCreate.Success {
		return nil, fmt.Errorf("project creation reported as unsuccessful")
	}

	return &createResp.ProjectCreate.Project, nil
}

// UpdateProject updates an existing project in Linear.
func (c *Client) UpdateProject(ctx context.Context, projectID string, updates map[string]interface{}) (*Project, error) {
	query := `
		mutation UpdateProject($id: String!, $input: ProjectUpdateInput!) {
			projectUpdate(id: $id, input: $input) {
				success
				project {
					id
					name
					description
					slugId
					url
					state
					progress
					updatedAt
				}
			}
		}
	`

	req := &GraphQLRequest{
		Query: query,
		Variables: map[string]interface{}{
			"id":    projectID,
			"input": updates,
		},
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to update project: %w", err)
	}

	var updateResp ProjectUpdateResponse
	if err := json.Unmarshal(data, &updateResp); err != nil {
		return nil, fmt.Errorf("failed to parse update project response: %w", err)
	}

	if !updateResp.ProjectUpdate.Success {
		return nil, fmt.Errorf("project update reported as unsuccessful")
	}

	return &updateResp.ProjectUpdate.Project, nil
}
