package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
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
	// maxRequestAttempts bounds retries of a rate-limited or retryable-status
	// request, including the first try. Transport-level failures — connection
	// reset, DNS, a per-attempt client timeout — return on the first attempt and
	// are not retried, so they are outside this bound.
	maxRequestAttempts = 5
	// maxRetryDelay bounds how long one attempt will wait before the next. A
	// Retry-After longer than this is refused rather than clamped down to it —
	// see doRequest.
	maxRetryDelay = 30 * time.Second
	// statusNotionOverloaded is Notion's overload status. It has no net/http
	// constant because it is not a standard HTTP status; Notion returns it from
	// its edge, which may already have handed the request to the origin.
	statusNotionOverloaded = 529
)

// retrySafety records whether replaying a request can duplicate work the server
// has already done. Each call site declares it, because the HTTP verb is not a
// reliable proxy for it on this API: Notion's query endpoint is a read-only POST,
// and no DELETE request exists at all.
type retrySafety bool

const (
	// retrySafe marks a request that applies nothing server-side, so replaying it
	// after an ambiguous failure costs only the request.
	retrySafe retrySafety = true
	// retryUnsafe marks a request that may already have been applied when the
	// failure is reported, so only a status that provably rejected it before
	// processing (429) may be replayed.
	retryUnsafe retrySafety = false
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

	// after is the retry delay hook, swapped out in tests so backoff coverage
	// does not spend real seconds. It hands back a channel rather than blocking,
	// so the wait can be selected against ctx.Done().
	after func(time.Duration) <-chan time.Time
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

// wait blocks for d, or gives up early with ctx's error if the context is
// canceled first. A plain time.Sleep would ignore cancellation for up to
// maxRetryDelay per attempt, and QueryDataSource pays that per page — so the
// worst case scales with MaxQueryPages, the bound this client lets callers
// raise.
func (c *Client) wait(ctx context.Context, d time.Duration) error {
	after := c.after
	if after == nil {
		after = time.After
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-after(d):
		return nil
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
	body, err := c.doRequest(ctx, http.MethodGet, "/users/me", nil, retrySafe)
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
	body, err := c.doRequest(ctx, http.MethodGet, "/data_sources/"+url.PathEscape(dataSourceID), nil, retrySafe)
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
	body, err := c.doRequest(ctx, http.MethodGet, "/databases/"+url.PathEscape(databaseID), nil, retrySafe)
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
	body, err := c.doRequest(ctx, http.MethodPost, "/databases", request, retryUnsafe)
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

		body, err := c.doRequest(ctx, http.MethodPost, "/data_sources/"+url.PathEscape(dataSourceID)+"/query", request, retrySafe)
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
	// their data source is limit*maxPageSize. The lever named has to be one the
	// reader can actually pull — this message reaches CLI operators, who cannot
	// call a Go method.
	return nil, fmt.Errorf(
		"query pagination exceeded %d pages (~%d rows): this data source is larger than the "+
			"configured bound, so no sync can complete. Raise it with "+
			"'bd config set notion.max_query_pages <n>' or the NOTION_MAX_QUERY_PAGES "+
			"environment variable, or reduce the number of rows in the data source",
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
	body, err := c.doRequest(ctx, http.MethodPost, "/pages", request, retryUnsafe)
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
	body, err := c.doRequest(ctx, http.MethodPatch, "/pages/"+url.PathEscape(pageID), request, retryUnsafe)
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
	body, err := c.doRequest(ctx, http.MethodPatch, "/pages/"+url.PathEscape(pageID), map[string]interface{}{"in_trash": inTrash}, retryUnsafe)
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

func (c *Client) doRequest(ctx context.Context, method, path string, requestBody interface{}, safe retrySafety) ([]byte, error) {
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
	// Retry-After header. Without honoring it, any caller that paginates a
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

		body, status, retryAfter, hasRetryAfter, err := c.doAttempt(httpClient, req)
		if err != nil {
			return nil, err
		}
		if status >= 200 && status < 300 {
			return body, nil
		}

		if !retryableStatus(status, safe) {
			return nil, notionAPIError(status, body)
		}
		lastErr = notionAPIError(status, body)

		// A Retry-After longer than we are willing to wait is refused outright
		// rather than clamped down to the ceiling. Waiting 30s when the server
		// asked for an hour just spends the remaining attempts inside the window
		// it told us to stay out of, which is how that window gets extended.
		//
		// This is evaluated before the exhaustion break below so that the same
		// message reaches the operator when the long header arrives on the final
		// attempt. It returns, so it cannot alter the loop's bound.
		if retryAfter > maxRetryDelay {
			return nil, fmt.Errorf(
				"Notion asked for a %s wait before retrying, longer than the %s this client will wait: %w",
				retryAfter, maxRetryDelay, lastErr)
		}
		if attempt == maxRequestAttempts-1 {
			break
		}

		if err := c.wait(ctx, retryDelay(attempt, retryAfter, hasRetryAfter)); err != nil {
			// The context error is what the caller acts on, but alone it drops the
			// API failure that put this call into a backoff in the first place.
			return nil, fmt.Errorf("%w (while waiting to retry after: %v)", err, lastErr)
		}
	}
	return nil, lastErr
}

// retryDelay reports how long to wait before the next attempt.
//
// A Retry-After the server actually sent is used exactly, including a literal 0,
// which means "retry now". Only the self-chosen exponential fallback is jittered:
// without it, N processes tripping the same ~3 req/s limit retry in lockstep at
// 1s/2s/4s/8s and re-collide, and QueryDataSource multiplies the exposure because
// each of up to MaxQueryPages requests runs its own ladder. Jittering a
// server-mandated delay instead of our own is what the sibling trackers
// deliberately avoid (internal/jira/client.go:419-424,
// internal/gitlab/client.go:171-176), since it would push the retry past the
// window the server named.
//
// The result is bounded by maxRetryDelay; given the refusal in doRequest, that
// clamp only ever shortens the fallback.
func retryDelay(attempt int, retryAfter time.Duration, hasRetryAfter bool) time.Duration {
	delay := retryAfter
	if !hasRetryAfter {
		delay = time.Duration(1<<attempt) * time.Second
		if half := int64(delay / 2); half > 0 {
			delay += time.Duration(rand.Int64N(half)) //nolint:gosec // G404: jitter for retry backoff does not need crypto rand
		}
	}
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	return delay
}

// doAttempt performs one request and fully reads the response, so the caller can
// decide about retrying without holding an open body.
func (c *Client) doAttempt(httpClient *http.Client, req *http.Request) (body []byte, status int, retryAfter time.Duration, hasRetryAfter bool, err error) {
	resp, err := httpClient.Do(req) //nolint:gosec // G704: URL is constructed from configured Notion API base, not user input
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("read response: %w", err)
	}
	retryAfter, hasRetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	return body, resp.StatusCode, retryAfter, hasRetryAfter, nil
}

// retryableStatus reports whether a status is worth another attempt.
//
// 429 is safe for every request: Notion rejects a rate-limited request before
// processing it, so nothing was applied server-side and a replay cannot
// duplicate anything.
//
// Every other retryable status — 529 included — can be reported after the write
// already landed, so those are replayed only for a request its call site declared
// retrySafe. Retrying a creating POST on 529 is how one bd issue becomes two
// Notion rows: the create-vs-update index is keyed on the bd ID and keeps only
// the last match, so the duplicate is invisible and every later sync updates just
// one of the pair. The read-only query POST carries no such risk, and it is the
// endpoint that pays for these retries most, issuing up to MaxQueryPages
// sequential requests per sync.
func retryableStatus(status int, safe retrySafety) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if !safe {
		return false
	}
	return status == statusNotionOverloaded ||
		status == http.StatusInternalServerError ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// parseRetryAfter reads both forms RFC 9110 §10.2.3 allows — delta-seconds and
// HTTP-date — and reports whether the header was present and understood.
//
// The flag is what keeps three otherwise indistinguishable inputs apart. An
// absent or malformed header leaves the caller on its exponential fallback, while
// a legal "Retry-After: 0" means "retry now" and must not be read as absent.
// Accepting the date form is what routes a long date-form wait into the same
// maxRetryDelay refusal as its delta-seconds twin, rather than around it: parsed
// as zero, "Retry-After: <date +1h>" would instead be retried at 1/2/4/8s inside
// the window the server asked us to stay out of.
//
// internal/linear/client.go:291 has the same two-form parse without the presence
// flag; the flag is added here because the retry loop branches on presence.
func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		// Saturate rather than let the multiply wrap, as time.Until does for the
		// date form below. A wrapped value can land negative, slip under
		// doRequest's maxRetryDelay refusal and retry at once — the opposite of
		// what the header asked for.
		if int64(seconds) > math.MaxInt64/int64(time.Second) {
			return time.Duration(math.MaxInt64), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if deadline, err := http.ParseTime(value); err == nil {
		// A date already past is the date form of "retry now".
		if delay := time.Until(deadline); delay > 0 {
			return delay, true
		}
		return 0, true
	}
	return 0, false
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
