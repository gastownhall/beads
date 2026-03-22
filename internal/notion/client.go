package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultBaseURL       = "https://api.notion.com/v1"
	DefaultNotionVersion = "2026-03-11"
	DefaultTimeout       = 30 * time.Second
	maxResponseBytes     = 20 * 1024 * 1024
	maxQueryPages        = 50
	maxPageSize          = 100
)

type Client struct {
	Token         string
	BaseURL       string
	NotionVersion string
	HTTPClient    *http.Client
}

func NewClient(token string) *Client {
	return &Client{
		Token:         token,
		BaseURL:       DefaultBaseURL,
		NotionVersion: DefaultNotionVersion,
		HTTPClient:    &http.Client{Timeout: DefaultTimeout},
	}
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	clone := *c
	clone.HTTPClient = httpClient
	return &clone
}

func (c *Client) WithBaseURL(baseURL string) *Client {
	clone := *c
	clone.BaseURL = strings.TrimSuffix(baseURL, "/")
	return &clone
}

func (c *Client) GetCurrentUser(ctx context.Context) (*User, error) {
	body, _, err := c.doRequest(ctx, http.MethodGet, "/users/me", nil)
	if err != nil {
		return nil, err
	}
	var user User
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("parse current user response: %w", err)
	}
	return &user, nil
}

func (c *Client) RetrieveDataSource(ctx context.Context, dataSourceID string) (*DataSource, error) {
	body, _, err := c.doRequest(ctx, http.MethodGet, "/data_sources/"+url.PathEscape(dataSourceID), nil)
	if err != nil {
		return nil, err
	}
	var ds DataSource
	if err := json.Unmarshal(body, &ds); err != nil {
		return nil, fmt.Errorf("parse data source response: %w", err)
	}
	return &ds, nil
}

func (c *Client) RetrieveDatabase(ctx context.Context, databaseID string) (*Database, error) {
	body, _, err := c.doRequest(ctx, http.MethodGet, "/databases/"+url.PathEscape(databaseID), nil)
	if err != nil {
		return nil, err
	}
	var db Database
	if err := json.Unmarshal(body, &db); err != nil {
		return nil, fmt.Errorf("parse database response: %w", err)
	}
	return &db, nil
}

func (c *Client) CreateDatabase(ctx context.Context, parentPageID, title string) (*Database, error) {
	parentPageID = strings.TrimSpace(parentPageID)
	if parentPageID == "" {
		return nil, fmt.Errorf("parent page ID is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = DefaultDatabaseTitle
	}
	request := map[string]interface{}{
		"parent": map[string]interface{}{
			"type":    "page_id",
			"page_id": parentPageID,
		},
		"title":     richTextRequest(title),
		"is_inline": false,
		"initial_data_source": map[string]interface{}{
			"title":      richTextRequest(title),
			"properties": BuildInitialDataSourceProperties(),
		},
	}
	body, _, err := c.doRequest(ctx, http.MethodPost, "/databases", request)
	if err != nil {
		return nil, err
	}
	var db Database
	if err := json.Unmarshal(body, &db); err != nil {
		return nil, fmt.Errorf("parse create database response: %w", err)
	}
	return &db, nil
}

func (c *Client) QueryDataSource(ctx context.Context, dataSourceID string) ([]Page, error) {
	var pages []Page
	var cursor string
	for pageNum := 0; pageNum < maxQueryPages; pageNum++ {
		request := map[string]interface{}{
			"page_size":   maxPageSize,
			"result_type": "page",
			"in_trash":    false,
		}
		if cursor != "" {
			request["start_cursor"] = cursor
		}

		body, _, err := c.doRequest(ctx, http.MethodPost, "/data_sources/"+url.PathEscape(dataSourceID)+"/query", request)
		if err != nil {
			return nil, err
		}
		var resp QueryDataSourceResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parse data source query response: %w", err)
		}
		pages = append(pages, resp.Results...)
		if !resp.HasMore || resp.NextCursor == "" {
			return pages, nil
		}
		cursor = resp.NextCursor
	}
	return nil, fmt.Errorf("query pagination exceeded %d pages", maxQueryPages)
}

func (c *Client) CreatePage(ctx context.Context, dataSourceID string, properties map[string]interface{}) (*Page, error) {
	request := map[string]interface{}{
		"parent": map[string]interface{}{
			"type":           "data_source_id",
			"data_source_id": dataSourceID,
		},
		"properties": properties,
	}
	body, _, err := c.doRequest(ctx, http.MethodPost, "/pages", request)
	if err != nil {
		return nil, err
	}
	var page Page
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("parse create page response: %w", err)
	}
	return &page, nil
}

func (c *Client) UpdatePage(ctx context.Context, pageID string, properties map[string]interface{}) (*Page, error) {
	request := map[string]interface{}{"properties": properties}
	body, _, err := c.doRequest(ctx, http.MethodPatch, "/pages/"+url.PathEscape(pageID), request)
	if err != nil {
		return nil, err
	}
	var page Page
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("parse update page response: %w", err)
	}
	return &page, nil
}

func (c *Client) ArchivePage(ctx context.Context, pageID string, inTrash bool) (*Page, error) {
	body, _, err := c.doRequest(ctx, http.MethodPatch, "/pages/"+url.PathEscape(pageID), map[string]interface{}{"in_trash": inTrash})
	if err != nil {
		return nil, err
	}
	var page Page
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("parse archive page response: %w", err)
	}
	return &page, nil
}

type DataSourceResolver interface {
	RetrieveDataSource(ctx context.Context, dataSourceID string) (*DataSource, error)
	RetrieveDatabase(ctx context.Context, databaseID string) (*Database, error)
}

type ResolvedDataSource struct {
	InputID      string
	DataSourceID string
	DataSource   *DataSource
	Database     *Database
	ViewURL      string
}

func ResolveDataSourceReference(ctx context.Context, client DataSourceResolver, ref string) (*ResolvedDataSource, error) {
	if client == nil {
		return nil, fmt.Errorf("notion client is nil")
	}
	identifier := ExtractNotionIdentifier(ref)
	if identifier == "" {
		return nil, fmt.Errorf("could not extract a Notion ID from %q", ref)
	}
	if ds, err := client.RetrieveDataSource(ctx, identifier); err == nil {
		return &ResolvedDataSource{
			InputID:      identifier,
			DataSourceID: ds.ID,
			DataSource:   ds,
			ViewURL:      strings.TrimSpace(ref),
		}, nil
	} else {
		db, dbErr := client.RetrieveDatabase(ctx, identifier)
		if dbErr != nil {
			return nil, fmt.Errorf("resolve %q as data source: %w; as database: %v", ref, err, dbErr)
		}
		if len(db.DataSources) == 0 || strings.TrimSpace(db.DataSources[0].ID) == "" {
			return nil, fmt.Errorf("database %s has no child data sources", db.ID)
		}
		resolvedID := strings.TrimSpace(db.DataSources[0].ID)
		resolvedDS, err := client.RetrieveDataSource(ctx, resolvedID)
		if err != nil {
			return nil, fmt.Errorf("retrieve child data source %s: %w", resolvedID, err)
		}
		return &ResolvedDataSource{
			InputID:      identifier,
			DataSourceID: resolvedID,
			DataSource:   resolvedDS,
			Database:     db,
			ViewURL:      strings.TrimSpace(ref),
		}, nil
	}
}

func (c *Client) doRequest(ctx context.Context, method, path string, requestBody interface{}) ([]byte, http.Header, error) {
	if c == nil {
		return nil, nil, fmt.Errorf("notion client is nil")
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, nil, fmt.Errorf("Notion token not configured")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}

	var bodyReader io.Reader
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(payload)
	}

	requestURL := path
	if !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
		requestURL = strings.TrimSuffix(c.BaseURL, "/") + path
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Notion-Version", c.NotionVersion)
	req.Header.Set("Accept", "application/json")
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, resp.Header, nil
	}

	var apiErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Message != "" {
		return nil, nil, fmt.Errorf("Notion API error %s (%d): %s", apiErr.Code, resp.StatusCode, apiErr.Message)
	}
	return nil, nil, fmt.Errorf("Notion API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
