// Package gitlab provides client and data types for the GitLab REST API.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// WithHTTPClient returns a new client configured to use the specified HTTP client.
// This is useful for testing or customizing timeouts and transport settings.
func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	return &Client{
		Token:      c.Token,
		BaseURL:    c.BaseURL,
		ProjectID:  c.ProjectID,
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
		HTTPClient: c.HTTPClient,
	}
}

// projectPath returns the URL-encoded project path for API calls.
// This handles both numeric IDs (e.g., "123") and path-based IDs (e.g., "group/project").
func (c *Client) projectPath() string {
	return url.PathEscape(c.ProjectID)
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

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response (attempt %d/%d): %w", attempt+1, MaxRetries+1, err)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			delay := RetryDelay * time.Duration(1<<attempt)
			lastErr = fmt.Errorf("rate limited (attempt %d/%d)", attempt+1, MaxRetries+1)
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(delay):
				// Reset body reader for retry
				if body != nil {
					jsonBody, _ := json.Marshal(body)
					reqBody = bytes.NewReader(jsonBody)
				}
				continue
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, nil, fmt.Errorf("API error: %s (status %d)", string(respBody), resp.StatusCode)
		}

		return respBody, resp.Header, nil
	}

	return nil, nil, fmt.Errorf("max retries (%d) exceeded: %w", MaxRetries+1, lastErr)
}

// FetchIssues retrieves issues from GitLab with optional filtering by state.
// state can be: "opened", "closed", or "all".
func (c *Client) FetchIssues(ctx context.Context, state string) ([]Issue, error) {
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

		urlStr := c.buildURL("/projects/"+c.projectPath()+"/issues", params)
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

	return allIssues, nil
}

// FetchIssuesSince retrieves issues from GitLab that have been updated since the given time.
// This enables incremental sync by only fetching issues modified after the last sync.
func (c *Client) FetchIssuesSince(ctx context.Context, state string, since time.Time) ([]Issue, error) {
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

		urlStr := c.buildURL("/projects/"+c.projectPath()+"/issues", params)
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

	return allIssues, nil
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

// CreateIssueLink creates a link between two issues.
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
