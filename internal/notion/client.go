package notion

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
)

const (
	DefaultBaseURL       = "https://api.notion.com/v1"
	DefaultNotionVersion = "2026-03-11"
	DefaultTimeout       = 30 * time.Second
	maxResponseBytes     = 20 * 1024 * 1024
	maxQueryPages        = 50
	maxPageSize          = 100
	// maxRequestAttempts bounds retries of a rate-limited or transiently failing
	// request, including the first try.
	maxRequestAttempts = 5
	// maxRetryDelay clamps a server-supplied Retry-After so an interactive CLI
	// cannot be parked for minutes by one header.
	maxRetryDelay = 30 * time.Second
)

type Client struct {
	Token         string
	BaseURL       string
	NotionVersion string
	HTTPClient    *http.Client

	// MaxQueryPages bounds pagination in QueryDataSource. Zero means
	// maxQueryPages. Raise it for a data source larger than
	// maxQueryPages*maxPageSize rows, which otherwise cannot be synced at all.
	MaxQueryPages int

	// sleep is the retry delay hook, swapped out in tests so backoff coverage
	// does not spend real seconds.
	sleep func(time.Duration)
}

func NewClient(token string) *Client {
	return &Client{
		Token:         token,
		BaseURL:       DefaultBaseURL,
		NotionVersion: DefaultNotionVersion,
		HTTPClient:    &http.Client{Timeout: DefaultTimeout},
	}
}

// WithMaxQueryPages overrides the pagination bound for QueryDataSource.
// Non-positive values fall back to the default.
func (c *Client) WithMaxQueryPages(pages int) *Client {
	clone := *c
	clone.MaxQueryPages = pages
	return &clone
}

func (c *Client) maxQueryPages() int {
	if c.MaxQueryPages > 0 {
		return c.MaxQueryPages
	}
	return maxQueryPages
}

func (c *Client) sleepFor(d time.Duration) {
	if c.sleep != nil {
		c.sleep(d)
		return
	}
	time.Sleep(d)
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
	body, err := c.doRequest(ctx, http.MethodGet, "/users/me", nil)
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
	body, err := c.doRequest(ctx, http.MethodGet, "/data_sources/"+url.PathEscape(dataSourceID), nil)
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
	body, err := c.doRequest(ctx, http.MethodGet, "/databases/"+url.PathEscape(databaseID), nil)
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
	body, err := c.doRequest(ctx, http.MethodPost, "/databases", request)
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
	limit := c.maxQueryPages()
	for pageNum := 0; pageNum < limit; pageNum++ {
		request := map[string]interface{}{
			"page_size":   maxPageSize,
			"result_type": "page",
		}
		if cursor != "" {
			request["start_cursor"] = cursor
		}

		body, err := c.doRequest(ctx, http.MethodPost, "/data_sources/"+url.PathEscape(dataSourceID)+"/query", request)
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
	// Naming the ceiling in rows, not pages, is the difference between a caller
	// knowing what to do and filing a bug: the number they can compare against
	// their data source is limit*maxPageSize.
	return nil, fmt.Errorf(
		"query pagination exceeded %d pages (~%d rows): this data source is larger than the "+
			"configured bound, so no sync can complete. Raise it via Client.WithMaxQueryPages, "+
			"or reduce the number of rows in the data source",
		limit, limit*maxPageSize)
}

func (c *Client) CreatePage(ctx context.Context, dataSourceID string, properties map[string]interface{}) (*Page, error) {
	request := map[string]interface{}{
		"parent": map[string]interface{}{
			"type":           "data_source_id",
			"data_source_id": dataSourceID,
		},
		"properties": properties,
	}
	body, err := c.doRequest(ctx, http.MethodPost, "/pages", request)
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
	body, err := c.doRequest(ctx, http.MethodPatch, "/pages/"+url.PathEscape(pageID), request)
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
	body, err := c.doRequest(ctx, http.MethodPatch, "/pages/"+url.PathEscape(pageID), map[string]interface{}{"in_trash": inTrash})
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

func (c *Client) doRequest(ctx context.Context, method, path string, requestBody interface{}) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("notion client is nil")
	}
	if strings.TrimSpace(c.Token) == "" {
		return nil, fmt.Errorf("Notion token not configured")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}

	// Marshal once and rebuild the reader per attempt: a retry cannot reuse a
	// drained body.
	var payload []byte
	if requestBody != nil {
		var err error
		payload, err = json.Marshal(requestBody)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
	}

	requestURL := path
	if !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
		requestURL = strings.TrimSuffix(c.BaseURL, "/") + path
	}

	// Notion enforces ~3 requests/second per connection and answers 429 with a
	// Retry-After header. Without honouring it, any caller that paginates a
	// large data source trips the limit and the whole sync dies on a transient
	// condition the API explicitly tells us how to wait out.
	var lastErr error
	for attempt := 0; attempt < maxRequestAttempts; attempt++ {
		var bodyReader io.Reader
		if payload != nil {
			bodyReader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Notion-Version", c.NotionVersion)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		body, status, retryAfter, err := c.doAttempt(httpClient, req)
		if err != nil {
			return nil, err
		}
		if status >= 200 && status < 300 {
			return body, nil
		}

		if !retryableStatus(status, method) || attempt == maxRequestAttempts-1 {
			return nil, notionAPIError(status, body)
		}
		lastErr = notionAPIError(status, body)

		// Retry-After is authoritative when present; otherwise exponential,
		// clamped so a long server-side hint cannot stall a CLI indefinitely.
		delay := time.Duration(1<<attempt) * time.Second
		if retryAfter > 0 {
			delay = retryAfter
		}
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		c.sleepFor(delay)
	}
	return nil, lastErr
}

// doAttempt performs one request and fully reads the response, so the caller can
// decide about retrying without holding an open body.
func (c *Client) doAttempt(httpClient *http.Client, req *http.Request) ([]byte, int, time.Duration, error) {
	resp, err := httpClient.Do(req) //nolint:gosec // G704: URL is constructed from configured Notion API base, not user input
	if err != nil {
		return nil, 0, 0, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read response: %w", err)
	}
	return body, resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

// retryableStatus reports whether a status is worth another attempt. 429 and 529
// always are. Other 5xx only for methods without side effects — replaying a
// failed POST that may have been applied server-side is how duplicates are made.
func retryableStatus(status int, method string) bool {
	if status == http.StatusTooManyRequests || status == 529 {
		return true
	}
	if method != http.MethodGet && method != http.MethodDelete {
		return false
	}
	return status == http.StatusInternalServerError ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// parseRetryAfter reads the delay-seconds form. The HTTP-date form is not
// accepted rather than guessed at: a misparsed date yielding zero would silently
// retry immediately, which is the opposite of what the header asked for.
func parseRetryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func notionAPIError(status int, body []byte) error {
	var apiErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Message != "" {
		return fmt.Errorf("Notion API error %s (%d): %s", apiErr.Code, status, apiErr.Message)
	}
	return fmt.Errorf("Notion API error (%d): %s", status, strings.TrimSpace(string(body)))
}
