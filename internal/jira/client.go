package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Issue represents a Jira issue from the REST API.
type Issue struct {
	ID     string      `json:"id"`
	Key    string      `json:"key"`
	Self   string      `json:"self"`
	Fields IssueFields `json:"fields"`
}

// IssueFields contains the fields of a Jira issue.
type IssueFields struct {
	Summary     string           `json:"summary"`
	Description json.RawMessage  `json:"description"` // ADF (Atlassian Document Format) or plain text
	Status      *StatusField     `json:"status"`
	Priority    *PriorityField   `json:"priority"`
	IssueType   *IssueTypeField  `json:"issuetype"`
	Project     *ProjectField    `json:"project"`
	Assignee    *UserField       `json:"assignee"`
	Labels      []string         `json:"labels"`
	Created     string           `json:"created"`
	Updated     string           `json:"updated"`
	Resolution  *ResolutionField `json:"resolution"`
}

// StatusField represents a Jira issue status.
type StatusField struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PriorityField represents a Jira issue priority.
type PriorityField struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// IssueTypeField represents a Jira issue type.
type IssueTypeField struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProjectField represents a Jira project.
type ProjectField struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// UserField represents a Jira user.
type UserField struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

// ResolutionField represents a Jira resolution.
type ResolutionField struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SearchResult represents a Jira JQL search response.
type SearchResult struct {
	StartAt    int     `json:"startAt"`
	MaxResults int     `json:"maxResults"`
	Total      int     `json:"total"`
	Issues     []Issue `json:"issues"`
}

// Client provides HTTP access to a Jira instance.
type Client struct {
	URL        string
	Username   string
	APIToken   string
	HTTPClient *http.Client
}

// NewClient creates a new Jira client.
func NewClient(url, username, apiToken string) *Client {
	return &Client{
		URL:      strings.TrimSuffix(url, "/"),
		Username: username,
		APIToken: apiToken,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// FetchIssueTimestamp fetches the updated timestamp for a single Jira issue.
func (c *Client) FetchIssueTimestamp(ctx context.Context, jiraKey string) (time.Time, error) {
	var zero time.Time

	apiURL := fmt.Sprintf("%s/rest/api/3/issue/%s?fields=updated", c.URL, url.PathEscape(jiraKey))

	body, err := c.doRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return zero, fmt.Errorf("fetch issue %s: %w", jiraKey, err)
	}

	var result struct {
		Fields struct {
			Updated string `json:"updated"`
		} `json:"fields"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return zero, fmt.Errorf("parse Jira response: %w", err)
	}

	updated, err := ParseTimestamp(result.Fields.Updated)
	if err != nil {
		return zero, fmt.Errorf("parse Jira timestamp: %w", err)
	}

	return updated, nil
}

// searchFields is the default set of fields to request in search/get queries.
const searchFields = "summary,description,status,priority,issuetype,project,assignee,labels,created,updated,resolution"

// SearchIssues queries Jira using JQL and returns all matching issues, handling pagination.
func (c *Client) SearchIssues(ctx context.Context, jql string) ([]Issue, error) {
	var allIssues []Issue
	startAt := 0
	maxResults := 100

	for {
		params := url.Values{
			"jql":        {jql},
			"fields":     {searchFields},
			"startAt":    {fmt.Sprintf("%d", startAt)},
			"maxResults": {fmt.Sprintf("%d", maxResults)},
		}

		apiURL := fmt.Sprintf("%s/rest/api/3/search?%s", c.URL, params.Encode())

		body, err := c.doRequest(ctx, "GET", apiURL, nil)
		if err != nil {
			return nil, fmt.Errorf("search issues: %w", err)
		}

		var result SearchResult
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parse search response: %w", err)
		}

		allIssues = append(allIssues, result.Issues...)

		if startAt+len(result.Issues) >= result.Total {
			break
		}
		startAt += len(result.Issues)
	}

	return allIssues, nil
}

// GetIssue fetches a single Jira issue by key (e.g., "PROJ-123").
func (c *Client) GetIssue(ctx context.Context, key string) (*Issue, error) {
	apiURL := fmt.Sprintf("%s/rest/api/3/issue/%s?fields=%s", c.URL, url.PathEscape(key), searchFields)

	body, err := c.doRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("get issue %s: %w", key, err)
	}

	var issue Issue
	if err := json.Unmarshal(body, &issue); err != nil {
		return nil, fmt.Errorf("parse issue response: %w", err)
	}

	return &issue, nil
}

// CreateIssue creates a new issue in Jira.
// fields should include "project", "summary", "issuetype", and optionally other fields.
func (c *Client) CreateIssue(ctx context.Context, fields map[string]interface{}) (*Issue, error) {
	payload := map[string]interface{}{"fields": fields}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal create request: %w", err)
	}

	apiURL := fmt.Sprintf("%s/rest/api/3/issue", c.URL)

	body, err := c.doRequest(ctx, "POST", apiURL, data)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	// Create response only returns id, key, self. Fetch the full issue.
	var created struct {
		ID   string `json:"id"`
		Key  string `json:"key"`
		Self string `json:"self"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return nil, fmt.Errorf("parse create response: %w", err)
	}

	return c.GetIssue(ctx, created.Key)
}

// UpdateIssue updates an existing Jira issue by key.
func (c *Client) UpdateIssue(ctx context.Context, key string, fields map[string]interface{}) error {
	payload := map[string]interface{}{"fields": fields}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal update request: %w", err)
	}

	apiURL := fmt.Sprintf("%s/rest/api/3/issue/%s", c.URL, url.PathEscape(key))

	_, err = c.doRequest(ctx, "PUT", apiURL, data)
	if err != nil {
		return fmt.Errorf("update issue %s: %w", key, err)
	}

	return nil
}

// doRequest executes an authenticated HTTP request and returns the response body.
func (c *Client) doRequest(ctx context.Context, method, apiURL string, body []byte) ([]byte, error) {
	if c.URL == "" {
		return nil, fmt.Errorf("jira URL not configured")
	}
	if c.APIToken == "" {
		return nil, fmt.Errorf("jira API token not configured")
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	c.setAuth(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bd-jira-sync/1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// PUT returns 204 No Content on success
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("jira API returned %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// setAuth sets the appropriate authentication header on the request.
func (c *Client) setAuth(req *http.Request) {
	isCloud := strings.Contains(c.URL, "atlassian.net")
	if (isCloud || c.Username != "") && c.Username != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.APIToken))
		req.Header.Set("Authorization", "Basic "+auth)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.APIToken)
	}
}

// DescriptionToPlainText extracts plain text from Jira's ADF (Atlassian Document Format).
// Jira v3 API returns descriptions as ADF JSON, not plain text.
func DescriptionToPlainText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	// Try to parse as ADF document
	var doc struct {
		Type    string `json:"type"`
		Content []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}

	if err := json.Unmarshal(raw, &doc); err != nil {
		// Not JSON - treat as plain text string
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return string(raw)
	}

	if doc.Type != "doc" {
		// Not ADF - try plain string
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return string(raw)
	}

	// Extract text from ADF nodes
	var parts []string
	for _, block := range doc.Content {
		var line []string
		for _, inline := range block.Content {
			if inline.Text != "" {
				line = append(line, inline.Text)
			}
		}
		if len(line) > 0 {
			parts = append(parts, strings.Join(line, ""))
		}
	}

	return strings.Join(parts, "\n")
}

// PlainTextToADF converts plain text to Jira's ADF (Atlassian Document Format).
func PlainTextToADF(text string) json.RawMessage {
	if text == "" {
		return nil
	}

	paragraphs := strings.Split(text, "\n")
	var content []interface{}
	for _, para := range paragraphs {
		if para == "" {
			content = append(content, map[string]interface{}{
				"type":    "paragraph",
				"content": []interface{}{},
			})
			continue
		}
		content = append(content, map[string]interface{}{
			"type": "paragraph",
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": para,
				},
			},
		})
	}

	doc := map[string]interface{}{
		"type":    "doc",
		"version": 1,
		"content": content,
	}

	data, _ := json.Marshal(doc)
	return data
}
