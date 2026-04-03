// Package gitlab provides client and data types for the GitLab REST API.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// NewClient creates a new GitLab client with the given token, base URL, and project ID.
func NewClient(token, baseURL, projectID string) *Client {
	return &Client{
		Token:     token,
		BaseURL:   baseURL,
		ProjectID: projectID,
		HTTPClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

// WithGroupID returns a new client configured to fetch issues at the group level.
// When GroupID is set, FetchIssues and FetchIssuesSince use /groups/:id/issues
// instead of /projects/:id/issues. Issue creation still uses the project endpoint.
func (c *Client) WithGroupID(groupID string) *Client {
	return &Client{
		Token:      c.Token,
		BaseURL:    c.BaseURL,
		ProjectID:  c.ProjectID,
		GroupID:    groupID,
		HTTPClient: c.HTTPClient,
	}
}

// WithHTTPClient returns a new client configured to use the specified HTTP client.
// This is useful for testing or customizing timeouts and transport settings.
func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	return &Client{
		Token:      c.Token,
		BaseURL:    c.BaseURL,
		ProjectID:  c.ProjectID,
		GroupID:    c.GroupID,
		HTTPClient: httpClient,
	}
}

// WithEndpoint returns a new client configured to use a custom API endpoint.
// This is useful for testing with mock servers or self-hosted GitLab instances.
func (c *Client) WithEndpoint(endpoint string) *Client {
	return &Client{
		Token:      c.Token,
		BaseURL:    endpoint,
		ProjectID:  c.ProjectID,
		GroupID:    c.GroupID,
		HTTPClient: c.HTTPClient,
	}
}

// projectPath returns the URL-encoded project path for API calls.
// This handles both numeric IDs (e.g., "123") and path-based IDs (e.g., "group/project").
func (c *Client) projectPath() string {
	return url.PathEscape(c.ProjectID)
}

// issuesBasePath returns the API path prefix for listing issues.
// When GroupID is set, returns /groups/:id/issues (group-level).
// Otherwise, returns /projects/:id/issues (project-level).
func (c *Client) issuesBasePath() string {
	if c.GroupID != "" {
		return "/groups/" + url.PathEscape(c.GroupID) + "/issues"
	}
	return "/projects/" + c.projectPath() + "/issues"
}

// buildURL constructs a full API URL from path and optional query parameters.
func (c *Client) buildURL(path string, params map[string]string) string {
	u := c.BaseURL + DefaultAPIEndpoint + path

	if len(params) > 0 {
		values := url.Values{}
		for k, v := range params {
			values.Set(k, v)
		}
		u += "?" + values.Encode()
	}

	return u
}

// doRequest performs an HTTP request with authentication and retry logic.
func (c *Client) doRequest(ctx context.Context, method, urlStr string, body interface{}) ([]byte, http.Header, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	var lastErr error
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		// Reset body reader at top of loop so retries after network errors
		// don't send empty bodies (the reader may be at EOF).
		if body != nil {
			jsonBody, err := json.Marshal(body)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to marshal request body: %w", err)
			}
			reqBody = bytes.NewReader(jsonBody)
		}

		req, err := http.NewRequestWithContext(ctx, method, urlStr, reqBody)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("PRIVATE-TOKEN", c.Token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed (attempt %d/%d): %w", attempt+1, MaxRetries+1, err)
			continue
		}

		// Limit response body to 50MB to prevent OOM from malformed responses.
		const maxResponseSize = 50 * 1024 * 1024
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		_ = resp.Body.Close() // Best effort: HTTP body close; connection may be reused regardless
		if err != nil {
			lastErr = fmt.Errorf("failed to read response (attempt %d/%d): %w", attempt+1, MaxRetries+1, err)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, resp.Header, nil
		}

		// Retry on rate-limiting and server errors with exponential backoff.
		retriable := resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusInternalServerError ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout

		if retriable {
			delay := RetryDelay * time.Duration(1<<attempt)
			useServerDelay := false

			// Use Retry-After header if present (no jitter — respect server-mandated delay)
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil {
					delay = time.Duration(seconds) * time.Second
					useServerDelay = true
				}
			}

			// Only add jitter to our own exponential backoff, not server-mandated delays
			if !useServerDelay {
				if half := int64(delay / 2); half > 0 {
					delay += time.Duration(rand.Int64N(half)) //nolint:gosec // G404: jitter for retry backoff does not need crypto rand
				}
			}

			lastErr = fmt.Errorf("transient error %d (attempt %d/%d)", resp.StatusCode, attempt+1, MaxRetries+1)
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(delay):
				continue
			}
		}

		return nil, nil, fmt.Errorf("API error: %s (status %d)", string(respBody), resp.StatusCode)
	}

	return nil, nil, fmt.Errorf("max retries (%d) exceeded: %w", MaxRetries+1, lastErr)
}

// applyFilter adds IssueFilter fields as query parameters to the params map.
// ProjectID filtering is done client-side (not supported by GitLab API on group endpoints).
func applyFilter(params map[string]string, filter *IssueFilter) {
	if filter == nil {
		return
	}
	if filter.Labels != "" {
		params["labels"] = filter.Labels
	}
	if filter.Milestone != "" {
		params["milestone"] = filter.Milestone
	}
	if filter.Assignee != "" {
		params["assignee_username"] = filter.Assignee
	}
}

// filterByProject removes issues that don't match the filter's ProjectID.
// Returns the input slice unmodified if filter is nil or ProjectID is 0.
func filterByProject(issues []Issue, filter *IssueFilter) []Issue {
	if filter == nil || filter.ProjectID == 0 {
		return issues
	}
	filtered := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.ProjectID == filter.ProjectID {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

// FetchIssues retrieves issues from GitLab with optional filtering by state and IssueFilter.
// state can be: "opened", "closed", or "all".
func (c *Client) FetchIssues(ctx context.Context, state string, filters ...*IssueFilter) ([]Issue, error) {
	var filter *IssueFilter
	if len(filters) > 0 {
		filter = filters[0]
	}

	var allIssues []Issue
	page := 1

	for {
		// Check for context cancellation at start of each iteration
		select {
		case <-ctx.Done():
			return allIssues, ctx.Err()
		default:
		}

		params := map[string]string{
			"per_page": strconv.Itoa(MaxPageSize),
			"page":     strconv.Itoa(page),
		}
		if state != "" && state != "all" {
			params["state"] = state
		}
		applyFilter(params, filter)

		urlStr := c.buildURL(c.issuesBasePath(), params)
		respBody, headers, err := c.doRequest(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch issues: %w", err)
		}

		var issues []Issue
		if err := json.Unmarshal(respBody, &issues); err != nil {
			return nil, fmt.Errorf("failed to parse issues response: %w", err)
		}

		allIssues = append(allIssues, issues...)

		// Check for next page
		nextPage := headers.Get("X-Next-Page")
		if nextPage == "" {
			break
		}
		page++

		// Guard against infinite pagination loops from malformed responses
		if page > MaxPages {
			return nil, fmt.Errorf("pagination limit exceeded: stopped after %d pages", MaxPages)
		}
	}

	return filterByProject(allIssues, filter), nil
}

// FetchIssuesSince retrieves issues from GitLab that have been updated since the given time.
// This enables incremental sync by only fetching issues modified after the last sync.
func (c *Client) FetchIssuesSince(ctx context.Context, state string, since time.Time, filters ...*IssueFilter) ([]Issue, error) {
	var filter *IssueFilter
	if len(filters) > 0 {
		filter = filters[0]
	}

	var allIssues []Issue
	page := 1

	sinceStr := since.UTC().Format(time.RFC3339)

	for {
		// Check for context cancellation at start of each iteration
		select {
		case <-ctx.Done():
			return allIssues, ctx.Err()
		default:
		}

		params := map[string]string{
			"per_page":      strconv.Itoa(MaxPageSize),
			"page":          strconv.Itoa(page),
			"updated_after": sinceStr,
		}
		if state != "" && state != "all" {
			params["state"] = state
		}
		applyFilter(params, filter)

		urlStr := c.buildURL(c.issuesBasePath(), params)
		respBody, headers, err := c.doRequest(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch issues since %s: %w", sinceStr, err)
		}

		var issues []Issue
		if err := json.Unmarshal(respBody, &issues); err != nil {
			return nil, fmt.Errorf("failed to parse issues response: %w", err)
		}

		allIssues = append(allIssues, issues...)

		// Check for next page
		nextPage := headers.Get("X-Next-Page")
		if nextPage == "" {
			break
		}
		page++

		// Guard against infinite pagination loops from malformed responses
		if page > MaxPages {
			return nil, fmt.Errorf("pagination limit exceeded: stopped after %d pages", MaxPages)
		}
	}

	return filterByProject(allIssues, filter), nil
}

// CreateIssue creates a new issue in GitLab.
func (c *Client) CreateIssue(ctx context.Context, title, description string, labels []string) (*Issue, error) {
	body := map[string]interface{}{
		"title":       title,
		"description": description,
	}
	if len(labels) > 0 {
		body["labels"] = labels
	}

	urlStr := c.buildURL("/projects/"+c.projectPath()+"/issues", nil)
	respBody, _, err := c.doRequest(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create issue: %w", err)
	}

	var issue Issue
	if err := json.Unmarshal(respBody, &issue); err != nil {
		return nil, fmt.Errorf("failed to parse create response: %w", err)
	}

	return &issue, nil
}

// UpdateIssue updates an existing issue in GitLab.
func (c *Client) UpdateIssue(ctx context.Context, iid int, updates map[string]interface{}) (*Issue, error) {
	urlStr := c.buildURL("/projects/"+c.projectPath()+"/issues/"+strconv.Itoa(iid), nil)
	respBody, _, err := c.doRequest(ctx, http.MethodPut, urlStr, updates)
	if err != nil {
		return nil, fmt.Errorf("failed to update issue: %w", err)
	}

	var issue Issue
	if err := json.Unmarshal(respBody, &issue); err != nil {
		return nil, fmt.Errorf("failed to parse update response: %w", err)
	}

	return &issue, nil
}

// GetIssueLinks retrieves issue links for the specified issue IID.
func (c *Client) GetIssueLinks(ctx context.Context, iid int) ([]IssueLink, error) {
	urlStr := c.buildURL("/projects/"+c.projectPath()+"/issues/"+strconv.Itoa(iid)+"/links", nil)
	respBody, _, err := c.doRequest(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue links: %w", err)
	}

	var links []IssueLink
	if err := json.Unmarshal(respBody, &links); err != nil {
		return nil, fmt.Errorf("failed to parse issue links response: %w", err)
	}

	return links, nil
}

// FetchIssueByIID retrieves a single issue by its project-scoped IID.
func (c *Client) FetchIssueByIID(ctx context.Context, iid int) (*Issue, error) {
	urlStr := c.buildURL("/projects/"+c.projectPath()+"/issues/"+strconv.Itoa(iid), nil)
	respBody, _, err := c.doRequest(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch issue %d: %w", iid, err)
	}

	var issue Issue
	if err := json.Unmarshal(respBody, &issue); err != nil {
		return nil, fmt.Errorf("failed to parse issue response: %w", err)
	}

	return &issue, nil
}

// FetchMilestones retrieves milestones from the project with optional state filter.
// state can be: "active", "closed", or "" (all).
func (c *Client) FetchMilestones(ctx context.Context, state string) ([]Milestone, error) {
	params := map[string]string{
		"per_page": strconv.Itoa(MaxPageSize),
	}
	if state != "" {
		params["state"] = state
	}

	urlStr := c.buildURL("/projects/"+c.projectPath()+"/milestones", params)
	respBody, _, err := c.doRequest(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch milestones: %w", err)
	}

	var milestones []Milestone
	if err := json.Unmarshal(respBody, &milestones); err != nil {
		return nil, fmt.Errorf("failed to parse milestones response: %w", err)
	}

	return milestones, nil
}

// CreateMilestone creates a new milestone in GitLab.
func (c *Client) CreateMilestone(ctx context.Context, title, description string) (*Milestone, error) {
	body := map[string]interface{}{
		"title":       title,
		"description": description,
	}

	urlStr := c.buildURL("/projects/"+c.projectPath()+"/milestones", nil)
	respBody, _, err := c.doRequest(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create milestone: %w", err)
	}

	var milestone Milestone
	if err := json.Unmarshal(respBody, &milestone); err != nil {
		return nil, fmt.Errorf("failed to parse milestone response: %w", err)
	}

	return &milestone, nil
}

// UpdateMilestone updates an existing milestone in GitLab.
func (c *Client) UpdateMilestone(ctx context.Context, milestoneID int, updates map[string]interface{}) (*Milestone, error) {
	urlStr := c.buildURL("/projects/"+c.projectPath()+"/milestones/"+strconv.Itoa(milestoneID), nil)
	respBody, _, err := c.doRequest(ctx, http.MethodPut, urlStr, updates)
	if err != nil {
		return nil, fmt.Errorf("failed to update milestone: %w", err)
	}

	var milestone Milestone
	if err := json.Unmarshal(respBody, &milestone); err != nil {
		return nil, fmt.Errorf("failed to parse milestone response: %w", err)
	}

	return &milestone, nil
}

// GraphQL support for work item hierarchy (Issue → Task parent-child).

// graphqlRequest executes a GraphQL query against the GitLab instance.
func (c *Client) graphqlRequest(ctx context.Context, query string, variables map[string]interface{}) (json.RawMessage, error) {
	body := map[string]interface{}{"query": query}
	if len(variables) > 0 {
		body["variables"] = variables
	}

	// GraphQL endpoint is at /api/graphql (not under /api/v4/)
	urlStr := c.BaseURL + "/api/graphql"
	respBody, _, err := c.doRequest(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return nil, fmt.Errorf("GraphQL request failed: %w", err)
	}

	var result struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse GraphQL response: %w", err)
	}
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("GraphQL error: %s", result.Errors[0].Message)
	}
	return result.Data, nil
}

// WorkItem represents a GitLab work item from the GraphQL API.
type WorkItem struct {
	ID    string `json:"id"`  // Global ID (gid://gitlab/WorkItem/123)
	IID   string `json:"iid"` // Project-scoped ID
	Title string `json:"title"`
	Type  string `json:"type"` // Work item type name
}

// CreateTaskWorkItem creates a Task-type work item via GraphQL, optionally with a parent.
// parentGID is the global ID of the parent work item (e.g., "gid://gitlab/WorkItem/456").
// If parentGID is empty, creates a standalone task.
func (c *Client) CreateTaskWorkItem(ctx context.Context, projectPath, title, description, parentGID string) (*WorkItem, error) {
	hierarchyPart := ""
	if parentGID != "" {
		hierarchyPart = fmt.Sprintf(`, hierarchyWidget: { parentId: %q }`, parentGID)
	}

	query := fmt.Sprintf(`mutation {
		workItemCreate(input: {
			projectPath: %q,
			title: %q,
			description: %q,
			workItemTypeId: "gid://gitlab/WorkItems::Type/5"%s
		}) {
			errors
			workItem { id iid title workItemType { name } webUrl }
		}
	}`, projectPath, title, description, hierarchyPart)

	data, err := c.graphqlRequest(ctx, query, nil)
	if err != nil {
		return nil, err
	}

	var resp struct {
		WorkItemCreate struct {
			Errors   []string `json:"errors"`
			WorkItem *struct {
				ID     string `json:"id"`
				IID    string `json:"iid"`
				Title  string `json:"title"`
				WebURL string `json:"webUrl"`
				Type   struct {
					Name string `json:"name"`
				} `json:"workItemType"`
			} `json:"workItem"`
		} `json:"workItemCreate"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse work item response: %w", err)
	}
	if len(resp.WorkItemCreate.Errors) > 0 {
		return nil, fmt.Errorf("work item creation failed: %s", resp.WorkItemCreate.Errors[0])
	}
	if resp.WorkItemCreate.WorkItem == nil {
		return nil, fmt.Errorf("work item creation returned nil")
	}

	wi := resp.WorkItemCreate.WorkItem
	return &WorkItem{
		ID:    wi.ID,
		IID:   wi.IID,
		Title: wi.Title,
		Type:  wi.Type.Name,
	}, nil
}

// GetWorkItemGID looks up the global ID of a work item by its project-scoped IID.
func (c *Client) GetWorkItemGID(ctx context.Context, projectPath string, iid int) (string, error) {
	query := fmt.Sprintf(`{
		project(fullPath: %q) {
			workItems(iid: "%d", first: 1) {
				nodes { id }
			}
		}
	}`, projectPath, iid)

	data, err := c.graphqlRequest(ctx, query, nil)
	if err != nil {
		return "", err
	}

	var resp struct {
		Project struct {
			WorkItems struct {
				Nodes []struct {
					ID string `json:"id"`
				} `json:"nodes"`
			} `json:"workItems"`
		} `json:"project"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("failed to parse work item GID response: %w", err)
	}
	if len(resp.Project.WorkItems.Nodes) == 0 {
		return "", fmt.Errorf("work item with IID %d not found", iid)
	}
	return resp.Project.WorkItems.Nodes[0].ID, nil
}

// ListProjects retrieves projects accessible to the authenticated user.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	params := map[string]string{
		"membership": "true",
		"per_page":   "100",
	}
	urlStr := c.buildURL("/projects", params)
	respBody, _, err := c.doRequest(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	var projects []Project
	if err := json.Unmarshal(respBody, &projects); err != nil {
		return nil, fmt.Errorf("failed to parse projects response: %w", err)
	}

	return projects, nil
}

// CreateIssueLink creates a link between two issues in the SAME project.
// Cross-project links are not supported by this function; attempting to link
// issues from different projects will result in an error from the GitLab API.
// linkType can be: "relates_to", "blocks", or "is_blocked_by".
func (c *Client) CreateIssueLink(ctx context.Context, sourceIID, targetIID int, linkType string) (*IssueLink, error) {
	body := map[string]interface{}{
		"target_project_id": c.ProjectID,
		"target_issue_iid":  targetIID,
		"link_type":         linkType,
	}

	urlStr := c.buildURL("/projects/"+c.projectPath()+"/issues/"+strconv.Itoa(sourceIID)+"/links", nil)
	respBody, _, err := c.doRequest(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create issue link: %w", err)
	}

	var link IssueLink
	if err := json.Unmarshal(respBody, &link); err != nil {
		return nil, fmt.Errorf("failed to parse issue link response: %w", err)
	}

	return &link, nil
}
