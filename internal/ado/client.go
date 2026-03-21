package ado

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PullFilters configures which work items to pull from ADO.
// All filter values are validated before use in WIQL queries.
type PullFilters struct {
	AreaPath      string   // Filter to area path (uses UNDER for hierarchy), validated
	IterationPath string   // Filter to iteration path (uses UNDER for hierarchy), validated
	WorkItemTypes []string // Filter to specific work item types, validated
	States        []string // Filter to specific states, validated
}

var (
	// areaPathPattern validates ADO area/iteration path values.
	areaPathPattern = regexp.MustCompile(`^[a-zA-Z0-9 ._\\/-]+$`)
	// statePattern validates ADO state names.
	statePattern = regexp.MustCompile(`^[a-zA-Z0-9 _]+$`)
	// orgPattern validates ADO organization names.
	orgPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	// projectPattern validates ADO project names (allows spaces, quotes, etc.).
	projectPattern = regexp.MustCompile(`^[a-zA-Z0-9 ._'-]+$`)
)

// Validate checks all filter values against their allowlist patterns.
func (f *PullFilters) Validate() error {
	if f.AreaPath != "" && !areaPathPattern.MatchString(f.AreaPath) {
		return fmt.Errorf("invalid area path: %q (must match %s)", f.AreaPath, areaPathPattern.String())
	}
	if f.IterationPath != "" && !areaPathPattern.MatchString(f.IterationPath) {
		return fmt.Errorf("invalid iteration path: %q", f.IterationPath)
	}
	for _, t := range f.WorkItemTypes {
		if !areaPathPattern.MatchString(t) {
			return fmt.Errorf("invalid work item type: %q", t)
		}
	}
	for _, s := range f.States {
		if !statePattern.MatchString(s) {
			return fmt.Errorf("invalid state filter: %q", s)
		}
	}
	return nil
}

// ValidateOrg checks the organization name against the allowlist pattern.
func ValidateOrg(org string) error {
	if !orgPattern.MatchString(org) {
		return fmt.Errorf("invalid organization name: must match %s", orgPattern.String())
	}
	return nil
}

// ValidateProject checks the project name against the allowlist pattern.
func ValidateProject(project string) error {
	if !projectPattern.MatchString(project) {
		return fmt.Errorf("invalid project name: must match %s", projectPattern.String())
	}
	return nil
}

// Client communicates with the Azure DevOps REST API.
type Client struct {
	PAT        SecretString
	BaseURL    string // Custom URL for on-prem; empty = cloud default
	Org        string
	Project    string
	HTTPClient *http.Client
}

// NewClient creates a new Azure DevOps REST API client for the given organization
// and project, authenticating with the provided Personal Access Token. The returned
// client uses DefaultTimeout for HTTP requests and DefaultBaseURL for the API endpoint.
func NewClient(pat SecretString, org, project string) *Client {
	return &Client{
		PAT:     pat,
		Org:     org,
		Project: project,
		HTTPClient: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

// WithHTTPClient returns a copy of the client configured to use the specified HTTP client.
func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	return &Client{
		PAT:        c.PAT,
		BaseURL:    c.BaseURL,
		Org:        c.Org,
		Project:    c.Project,
		HTTPClient: httpClient,
	}
}

// validateURLScheme rejects non-HTTPS URLs unless the host is localhost or 127.0.0.1.
func validateURLScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return fmt.Errorf("HTTPS required for ADO connections (got %s); use https:// or localhost for testing", rawURL)
	}
	return nil
}

// WithBaseURL returns a copy of the client configured to use a custom API base URL.
// This is useful for on-prem Azure DevOps Server or testing with mock servers.
// Returns an error if the URL uses plain HTTP for non-localhost hosts.
func (c *Client) WithBaseURL(baseURL string) (*Client, error) {
	if err := validateURLScheme(baseURL); err != nil {
		return nil, err
	}
	return &Client{
		PAT:        c.PAT,
		BaseURL:    strings.TrimSuffix(baseURL, "/"),
		Org:        c.Org,
		Project:    c.Project,
		HTTPClient: c.HTTPClient,
	}, nil
}

// apiBase returns the project-scoped _apis URL prefix.
func (c *Client) apiBase() string {
	if c.BaseURL != "" {
		return c.BaseURL + "/" + url.PathEscape(c.Project) + "/_apis"
	}
	return DefaultBaseURL + "/" + url.PathEscape(c.Org) + "/" + url.PathEscape(c.Project) + "/_apis"
}

// orgBase returns the org-level _apis URL prefix (not project-scoped).
func (c *Client) orgBase() string {
	if c.BaseURL != "" {
		return c.BaseURL + "/_apis"
	}
	return DefaultBaseURL + "/" + url.PathEscape(c.Org) + "/_apis"
}

// doRequest performs an HTTP request with authentication and retry logic.
// contentType controls the Content-Type header; pass empty string for GET requests.
// isIdempotent reports whether the given request is safe to retry.
// GET requests are always safe. POST to the WIQL endpoint is a read-only
// query and is also safe. Mutations (POST/PATCH to other endpoints) are
// NOT safe to retry because the server may have already applied them.
func isIdempotent(method, urlStr string) bool {
	if method == http.MethodGet {
		return true
	}
	if method == http.MethodPost && strings.Contains(urlStr, "/wit/wiql") {
		return true
	}
	return false
}

func (c *Client) doRequest(ctx context.Context, method, urlStr, contentType string, body interface{}) ([]byte, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	cred := base64.StdEncoding.EncodeToString([]byte(":" + c.PAT.Expose()))
	canRetry := isIdempotent(method, urlStr)

	maxAttempts := 0
	if canRetry {
		maxAttempts = MaxRetries
	}

	var lastErr error
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, urlStr, reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Authorization", "Basic "+cred)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.HTTPClient.Do(req) //nolint:gosec // G704: URL is from admin-configured ADO endpoint, not untrusted input
		if err != nil {
			lastErr = fmt.Errorf("request failed (attempt %d/%d): %w", attempt+1, maxAttempts+1, err)
			if attempt < maxAttempts {
				delay := RetryDelay * time.Duration(1<<uint(attempt))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
			}
			continue
		}

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response (attempt %d/%d): %w", attempt+1, maxAttempts+1, err)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, nil
		}

		// Permanent failures — no retry.
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return nil, fmt.Errorf("API error: %s (status %d)", string(respBody), resp.StatusCode)
		}

		// Retry on 429 and 5xx server errors (idempotent requests only).
		retriable := resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= 500

		if retriable && attempt < maxAttempts {
			delay := RetryDelay * time.Duration(1<<uint(attempt))
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil {
					delay = time.Duration(seconds) * time.Second
				}
			}
			lastErr = fmt.Errorf("transient error %d (attempt %d/%d)", resp.StatusCode, attempt+1, maxAttempts+1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
				continue
			}
		}

		return nil, fmt.Errorf("API error: %s (status %d)", string(respBody), resp.StatusCode)
	}

	return nil, fmt.Errorf("max retries (%d) exceeded: %w", maxAttempts+1, lastErr)
}

// addAPIVersion appends the api-version query parameter to a URL string.
func addAPIVersion(urlStr string) string {
	if strings.Contains(urlStr, "?") {
		return urlStr + "&api-version=" + APIVersion
	}
	return urlStr + "?api-version=" + APIVersion
}

// listResponse is a generic envelope for ADO list API responses.
type listResponse struct {
	Count int             `json:"count"`
	Value json.RawMessage `json:"value"`
}

// escapeWIQL escapes a string for safe inclusion in a WIQL query literal.
func escapeWIQL(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	return strings.ReplaceAll(s, "'", "''")
}

// buildPatchOps converts a field map into sorted JSON Patch operations.
func buildPatchOps(fields map[string]interface{}) []PatchOperation {
	var ops []PatchOperation
	for field, value := range fields {
		ops = append(ops, PatchOperation{
			Op:    "add",
			Path:  "/fields/" + field,
			Value: value,
		})
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Path < ops[j].Path })
	return ops
}

// FetchWorkItems retrieves work items by ID in batches of MaxBatchSize.
func (c *Client) FetchWorkItems(ctx context.Context, ids []int) ([]WorkItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	var all []WorkItem
	for start := 0; start < len(ids); start += MaxBatchSize {
		end := start + MaxBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]

		parts := make([]string, len(chunk))
		for i, id := range chunk {
			parts[i] = strconv.Itoa(id)
		}

		urlStr := addAPIVersion(c.apiBase() + "/wit/workitems?ids=" + strings.Join(parts, ",") + "&$expand=All")
		respBody, err := c.doRequest(ctx, http.MethodGet, urlStr, "", nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch work items: %w", err)
		}

		var envelope listResponse
		if err := json.Unmarshal(respBody, &envelope); err != nil {
			return nil, fmt.Errorf("failed to parse work items response: %w", err)
		}

		var items []WorkItem
		if err := json.Unmarshal(envelope.Value, &items); err != nil {
			return nil, fmt.Errorf("failed to parse work items value: %w", err)
		}
		all = append(all, items...)
	}

	return all, nil
}

// buildPullWIQL constructs a safe WIQL query from validated filter fields.
// All values are escaped via escapeWIQL before interpolation.
func (c *Client) buildPullWIQL(since *time.Time, filters *PullFilters) string {
	clauses := []string{
		fmt.Sprintf("[System.TeamProject] = '%s'", escapeWIQL(c.Project)),
		"[System.IsDeleted] = false",
	}
	if since != nil {
		clauses = append(clauses, fmt.Sprintf(
			"[System.ChangedDate] >= '%s'",
			since.UTC().Format("2006-01-02T15:04:05Z"),
		))
	}
	if filters != nil {
		if filters.AreaPath != "" {
			clauses = append(clauses, fmt.Sprintf(
				"[System.AreaPath] UNDER '%s'", escapeWIQL(filters.AreaPath),
			))
		}
		if filters.IterationPath != "" {
			clauses = append(clauses, fmt.Sprintf(
				"[System.IterationPath] UNDER '%s'", escapeWIQL(filters.IterationPath),
			))
		}
		if len(filters.WorkItemTypes) > 0 {
			quoted := make([]string, len(filters.WorkItemTypes))
			for i, t := range filters.WorkItemTypes {
				quoted[i] = "'" + escapeWIQL(t) + "'"
			}
			clauses = append(clauses, fmt.Sprintf(
				"[System.WorkItemType] IN (%s)", strings.Join(quoted, ", "),
			))
		}
		if len(filters.States) > 0 {
			quoted := make([]string, len(filters.States))
			for i, s := range filters.States {
				quoted[i] = "'" + escapeWIQL(s) + "'"
			}
			clauses = append(clauses, fmt.Sprintf(
				"[System.State] IN (%s)", strings.Join(quoted, ", "),
			))
		}
	}
	return "SELECT [System.Id] FROM WorkItems WHERE " +
		strings.Join(clauses, " AND ") +
		" ORDER BY [System.ChangedDate] ASC"
}

// fetchWorkItemsByWIQL executes the given WIQL query and fetches full work items.
func (c *Client) fetchWorkItemsByWIQL(ctx context.Context, query string) ([]WorkItem, error) {
	urlStr := addAPIVersion(c.apiBase() + "/wit/wiql")
	reqBody := WIQLRequest{Query: query}
	respBody, err := c.doRequest(ctx, http.MethodPost, urlStr, "application/json", reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to execute WIQL query: %w", err)
	}

	var result WIQLResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse WIQL response: %w", err)
	}

	if len(result.WorkItems) == 0 {
		return nil, nil
	}

	ids := make([]int, len(result.WorkItems))
	for i, ref := range result.WorkItems {
		ids[i] = ref.ID
	}

	return c.FetchWorkItems(ctx, ids)
}

// FetchWorkItemsSince retrieves work items changed since the given time using WIQL.
// Pass nil for filters to fetch all work item types and states.
func (c *Client) FetchWorkItemsSince(ctx context.Context, since time.Time, filters *PullFilters) ([]WorkItem, error) {
	if filters != nil {
		if err := filters.Validate(); err != nil {
			return nil, fmt.Errorf("invalid pull filters: %w", err)
		}
	}
	query := c.buildPullWIQL(&since, filters)
	return c.fetchWorkItemsByWIQL(ctx, query)
}

// FetchAllWorkItems retrieves all work items matching the given filters.
// Used for initial sync or reconciliation.
func (c *Client) FetchAllWorkItems(ctx context.Context, filters *PullFilters) ([]WorkItem, error) {
	if filters != nil {
		if err := filters.Validate(); err != nil {
			return nil, fmt.Errorf("invalid pull filters: %w", err)
		}
	}
	query := c.buildPullWIQL(nil, filters)
	return c.fetchWorkItemsByWIQL(ctx, query)
}

// CreateWorkItem creates a new work item of the given type with the specified fields.
func (c *Client) CreateWorkItem(ctx context.Context, typeName string, fields map[string]interface{}) (*WorkItem, error) {
	ops := buildPatchOps(fields)
	urlStr := addAPIVersion(c.apiBase() + "/wit/workitems/$" + url.PathEscape(typeName))
	respBody, err := c.doRequest(ctx, http.MethodPost, urlStr, "application/json-patch+json", ops)
	if err != nil {
		return nil, fmt.Errorf("failed to create work item: %w", err)
	}

	var item WorkItem
	if err := json.Unmarshal(respBody, &item); err != nil {
		return nil, fmt.Errorf("failed to parse create response: %w", err)
	}
	return &item, nil
}

// UpdateWorkItem updates an existing work item's fields.
func (c *Client) UpdateWorkItem(ctx context.Context, id int, fields map[string]interface{}) (*WorkItem, error) {
	ops := buildPatchOps(fields)
	urlStr := addAPIVersion(fmt.Sprintf("%s/wit/workitems/%d", c.apiBase(), id))
	respBody, err := c.doRequest(ctx, http.MethodPatch, urlStr, "application/json-patch+json", ops)
	if err != nil {
		return nil, fmt.Errorf("failed to update work item: %w", err)
	}

	var item WorkItem
	if err := json.Unmarshal(respBody, &item); err != nil {
		return nil, fmt.Errorf("failed to parse update response: %w", err)
	}
	return &item, nil
}

// AddWorkItemLink adds a relation link from sourceID to the target work item URL.
// The comment parameter sets the relation comment attribute; pass "" for no comment.
func (c *Client) AddWorkItemLink(ctx context.Context, sourceID int, targetURL, linkType, comment string) error {
	ops := []PatchOperation{
		{
			Op:   "add",
			Path: "/relations/-",
			Value: map[string]interface{}{
				"rel": linkType,
				"url": targetURL,
				"attributes": map[string]interface{}{
					"comment": comment,
				},
			},
		},
	}
	urlStr := addAPIVersion(fmt.Sprintf("%s/wit/workitems/%d", c.apiBase(), sourceID))
	_, err := c.doRequest(ctx, http.MethodPatch, urlStr, "application/json-patch+json", ops)
	if err != nil {
		return fmt.Errorf("failed to add work item link: %w", err)
	}
	return nil
}

// RemoveWorkItemLink removes a relation link by index from the given work item.
func (c *Client) RemoveWorkItemLink(ctx context.Context, sourceID, relationIndex int) error {
	ops := []PatchOperation{
		{
			Op:   "remove",
			Path: fmt.Sprintf("/relations/%d", relationIndex),
		},
	}
	urlStr := addAPIVersion(fmt.Sprintf("%s/wit/workitems/%d", c.apiBase(), sourceID))
	_, err := c.doRequest(ctx, http.MethodPatch, urlStr, "application/json-patch+json", ops)
	if err != nil {
		return fmt.Errorf("failed to remove work item link: %w", err)
	}
	return nil
}

// ListProjects returns all team projects in the organization.
// This is an org-level endpoint, not project-scoped.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	urlStr := addAPIVersion(c.orgBase() + "/projects")
	respBody, err := c.doRequest(ctx, http.MethodGet, urlStr, "", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	var envelope listResponse
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse projects response: %w", err)
	}

	var projects []Project
	if err := json.Unmarshal(envelope.Value, &projects); err != nil {
		return nil, fmt.Errorf("failed to parse projects value: %w", err)
	}
	return projects, nil
}

// GetWorkItemTypes returns the work item types available in the project.
func (c *Client) GetWorkItemTypes(ctx context.Context) ([]WorkItemType, error) {
	urlStr := addAPIVersion(c.apiBase() + "/wit/workitemtypes")
	respBody, err := c.doRequest(ctx, http.MethodGet, urlStr, "", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get work item types: %w", err)
	}

	var envelope listResponse
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse work item types response: %w", err)
	}

	var types []WorkItemType
	if err := json.Unmarshal(envelope.Value, &types); err != nil {
		return nil, fmt.Errorf("failed to parse work item types value: %w", err)
	}
	return types, nil
}

// GetWorkItemStates returns the states for a given work item type.
func (c *Client) GetWorkItemStates(ctx context.Context, typeName string) ([]WorkItemState, error) {
	urlStr := addAPIVersion(c.apiBase() + "/wit/workitemtypes/" + url.PathEscape(typeName) + "/states")
	respBody, err := c.doRequest(ctx, http.MethodGet, urlStr, "", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get work item states: %w", err)
	}

	var envelope listResponse
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse work item states response: %w", err)
	}

	var states []WorkItemState
	if err := json.Unmarshal(envelope.Value, &states); err != nil {
		return nil, fmt.Errorf("failed to parse work item states value: %w", err)
	}
	return states, nil
}
