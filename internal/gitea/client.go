package gitea

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var linkNextPattern = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// NormalizeBaseURL normalizes a Gitea/Forgejo instance URL to its web root
// (trimming whitespace, trailing slashes, and any trailing "/api/v1" suffix).
func NormalizeBaseURL(rawURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if strings.HasSuffix(strings.ToLower(trimmed), DefaultAPIEndpoint) {
		trimmed = strings.TrimRight(trimmed[:len(trimmed)-len(DefaultAPIEndpoint)], "/")
	}
	return trimmed
}

// ValidateURLScheme checks that rawURL uses HTTPS or HTTP strictly on a
// loopback host (localhost, 127.0.0.1, ::1).
func ValidateURLScheme(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("Gitea/Forgejo URL is required")
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return fmt.Errorf("invalid Gitea/Forgejo URL %q: must include scheme and host", rawURL)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
		return nil
	case "http":
		host := strings.ToLower(u.Hostname())
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("Gitea/Forgejo URL must use https:// (got %s://%s; http:// is only allowed for localhost/127.0.0.1/::1)", scheme, u.Hostname())
	default:
		return fmt.Errorf("unsupported Gitea/Forgejo URL scheme %q (expected https://)", scheme)
	}
}

// NewHTTPClientWithCA returns an *http.Client trusting the PEM certificates at
// caPath (on top of the system cert pool) with TLS 1.2 minimum. If caPath is
// empty, a default HTTP client is returned.
func NewHTTPClientWithCA(caPath string) (*http.Client, error) {
	trimmed := strings.TrimSpace(caPath)
	if trimmed == "" {
		return &http.Client{Timeout: DefaultTimeout}, nil
	}
	pemData, err := os.ReadFile(trimmed) //nolint:gosec // G304: caPath is explicitly configured via gitea.tls_ca / GITEA_TLS_CA
	if err != nil {
		return nil, fmt.Errorf("read Gitea/Forgejo TLS CA %q: %w", trimmed, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("parse PEM certificates from %q", trimmed)
	}
	return &http.Client{
		Timeout: DefaultTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS12,
			},
		},
	}, nil
}

// NewClient creates a new Gitea/Forgejo client.
func NewClient(token, baseURL, owner, repo string) *Client {
	return &Client{
		Token:   token,
		BaseURL: NormalizeBaseURL(baseURL),
		Owner:   owner,
		Repo:    repo,
		HTTPClient: &http.Client{
			Timeout: DefaultTimeout,
		},
		Retry: DefaultRetryConfig(),
	}
}

// WithHTTPClient returns a copy of c using the provided HTTP client.
func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	return &Client{
		Token:      c.Token,
		BaseURL:    c.BaseURL,
		Owner:      c.Owner,
		Repo:       c.Repo,
		HTTPClient: httpClient,
		Retry:      c.Retry,
	}
}

// WithBaseURL returns a copy of c pointing at the normalized baseURL.
func (c *Client) WithBaseURL(baseURL string) *Client {
	return &Client{
		Token:      c.Token,
		BaseURL:    NormalizeBaseURL(baseURL),
		Owner:      c.Owner,
		Repo:       c.Repo,
		HTTPClient: c.HTTPClient,
		Retry:      c.Retry,
	}
}

// WithTLSCA returns a copy of c configured with the PEM CA bundle at caPath.
func (c *Client) WithTLSCA(caPath string) (*Client, error) {
	httpClient, err := NewHTTPClientWithCA(caPath)
	if err != nil {
		return nil, err
	}
	return c.WithHTTPClient(httpClient), nil
}

func (c *Client) repoPath() string {
	return "/repos/" + url.PathEscape(c.Owner) + "/" + url.PathEscape(c.Repo)
}

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

func (c *Client) doRequest(ctx context.Context, method, urlStr string, body interface{}) ([]byte, http.Header, error) {
	var jsonBody []byte
	if body != nil {
		var err error
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	retry := c.Retry
	if retry.BaseDelay <= 0 && retry.MaxRetries == 0 && retry.MaxBackoff == 0 {
		retry = DefaultRetryConfig()
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}

	var lastErr error
	var lastRateLimit *RateLimitError

	for attempt := 0; attempt <= retry.MaxRetries; attempt++ {
		var reqBody io.Reader
		if jsonBody != nil {
			reqBody = bytes.NewReader(jsonBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, urlStr, reqBody)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create request: %w", err)
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "token "+c.Token)
		}
		req.Header.Set(headerAccept, "application/json")
		if jsonBody != nil {
			req.Header.Set(headerContentType, "application/json")
		}

		resp, err := httpClient.Do(req) //nolint:gosec // G704: URL is constructed from configured Gitea/Forgejo endpoint, not untrusted input
		if err != nil {
			lastErr = fmt.Errorf("request failed (attempt %d/%d): %w", attempt+1, retry.MaxRetries+1, err)
			if attempt >= retry.MaxRetries {
				continue
			}
			delay := exponentialBackoffWithJitter(retry.BaseDelay, attempt, retry.MaxBackoff)
			if sleepErr := sleep(ctx, delay); sleepErr != nil {
				return nil, nil, sleepErr
			}
			continue
		}

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("failed to read response (attempt %d/%d): %w", attempt+1, retry.MaxRetries+1, readErr)
			if attempt >= retry.MaxRetries {
				continue
			}
			delay := exponentialBackoffWithJitter(retry.BaseDelay, attempt, retry.MaxBackoff)
			if sleepErr := sleep(ctx, delay); sleepErr != nil {
				return nil, nil, sleepErr
			}
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, resp.Header, nil
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			rlErr := parseRateLimitError(resp.Header, respBody, resp.StatusCode, urlStr)
			lastRateLimit = rlErr
			lastErr = rlErr
			if attempt >= retry.MaxRetries {
				continue
			}
			delay := rlErr.RateLimitRetryAfter()
			if delay <= 0 {
				delay = exponentialBackoffWithJitter(retry.BaseDelay, attempt, retry.MaxBackoff)
			}
			if sleepErr := sleep(ctx, delay); sleepErr != nil {
				return nil, nil, sleepErr
			}
			continue
		}

		switch resp.StatusCode {
		case http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			lastErr = fmt.Errorf("transient error %d (attempt %d/%d): %s", resp.StatusCode, attempt+1, retry.MaxRetries+1, extractAPIMessage(respBody))
			if attempt >= retry.MaxRetries {
				continue
			}
			delay := exponentialBackoffWithJitter(retry.BaseDelay, attempt, retry.MaxBackoff)
			if sleepErr := sleep(ctx, delay); sleepErr != nil {
				return nil, nil, sleepErr
			}
			continue
		}

		return nil, nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
			URL:        urlStr,
		}
	}

	if lastRateLimit != nil {
		return nil, nil, lastRateLimit
	}
	return nil, nil, fmt.Errorf("max retries (%d) exceeded: %w", retry.MaxRetries+1, lastErr)
}

func exponentialBackoffWithJitter(base time.Duration, attempt int, maxBackoff time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if attempt > 30 {
		attempt = 30
	}
	delay := base * time.Duration(1<<attempt)
	if maxBackoff > 0 && delay > maxBackoff {
		delay = maxBackoff
	}
	if half := int64(delay / 2); half > 0 {
		delay += time.Duration(rand.Int64N(half)) //nolint:gosec // G404: jitter for retry backoff does not need crypto rand
	}
	return delay
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func hasNextPage(headers http.Header, pageCount, totalLoaded int) bool {
	if link := headers.Get("Link"); link != "" {
		return linkNextPattern.MatchString(link)
	}
	if totalStr := headers.Get("X-Total-Count"); totalStr != "" {
		if total, err := strconv.Atoi(strings.TrimSpace(totalStr)); err == nil {
			return totalLoaded < total && pageCount > 0
		}
	}
	return pageCount >= MaxPageSize
}

// GetAuthenticatedUser returns the authenticated user from /api/v1/user.
func (c *Client) GetAuthenticatedUser(ctx context.Context) (*User, error) {
	respBody, _, err := c.doRequest(ctx, http.MethodGet, c.buildURL("/user", nil), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get authenticated user: %w", err)
	}
	var u User
	if err := json.Unmarshal(respBody, &u); err != nil {
		return nil, fmt.Errorf("failed to parse user response: %w", err)
	}
	return &u, nil
}

// FetchRepository returns metadata for the configured repository.
func (c *Client) FetchRepository(ctx context.Context) (*Repository, error) {
	return c.fetchRepositoryNamed(ctx, c.Owner, c.Repo)
}

func (c *Client) fetchRepositoryNamed(ctx context.Context, owner, repo string) (*Repository, error) {
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	respBody, _, err := c.doRequest(ctx, http.MethodGet, c.buildURL(path, nil), nil)
	if err != nil {
		return nil, err
	}
	var out Repository
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("failed to parse repository response: %w", err)
	}
	return &out, nil
}

// ListRepositories returns repositories accessible to the authenticated user.
func (c *Client) ListRepositories(ctx context.Context) ([]Repository, error) {
	var all []Repository
	page := 1
	for {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}
		params := map[string]string{
			"limit": strconv.Itoa(MaxPageSize),
			"page":  strconv.Itoa(page),
		}
		respBody, headers, err := c.doRequest(ctx, http.MethodGet, c.buildURL("/user/repos", params), nil)
		if err != nil {
			return nil, fmt.Errorf("failed to list repositories: %w", err)
		}
		var repos []Repository
		if err := json.Unmarshal(respBody, &repos); err != nil {
			return nil, fmt.Errorf("failed to parse repositories response: %w", err)
		}
		all = append(all, repos...)
		if !hasNextPage(headers, len(repos), len(all)) {
			break
		}
		page++
		if page > MaxPages {
			return nil, fmt.Errorf("pagination limit exceeded: stopped after %d pages", MaxPages)
		}
	}
	return all, nil
}

func applyFilterParams(params map[string]string, filter *IssueFilter) {
	if filter == nil {
		return
	}
	if filter.Labels != "" {
		params["labels"] = filter.Labels
	}
	if filter.Milestone != "" {
		params["milestones"] = filter.Milestone
	}
	if filter.Assignee != "" {
		params["assigned_by"] = filter.Assignee
	}
}

// FetchIssues retrieves issues from the configured repository, excluding pull requests.
func (c *Client) FetchIssues(ctx context.Context, state string, filters ...*IssueFilter) ([]Issue, error) {
	var filter *IssueFilter
	if len(filters) > 0 {
		filter = filters[0]
	}
	return c.fetchIssuesWithParams(ctx, state, nil, filter)
}

// FetchIssuesSince retrieves issues updated at or after since, excluding pull requests.
func (c *Client) FetchIssuesSince(ctx context.Context, state string, since time.Time, filters ...*IssueFilter) ([]Issue, error) {
	var filter *IssueFilter
	if len(filters) > 0 {
		filter = filters[0]
	}
	return c.fetchIssuesWithParams(ctx, state, &since, filter)
}

func (c *Client) fetchIssuesWithParams(ctx context.Context, state string, since *time.Time, filter *IssueFilter) ([]Issue, error) {
	if state == "" {
		state = "all"
	}
	var allIssues []Issue
	page := 1
	totalRaw := 0

	for {
		select {
		case <-ctx.Done():
			return allIssues, ctx.Err()
		default:
		}

		params := map[string]string{
			"type":  "issues",
			"state": state,
			"limit": strconv.Itoa(MaxPageSize),
			"page":  strconv.Itoa(page),
		}
		if since != nil {
			params["since"] = since.UTC().Format(time.RFC3339)
		}
		applyFilterParams(params, filter)

		respBody, headers, err := c.doRequest(ctx, http.MethodGet, c.buildURL(c.repoPath()+"/issues", params), nil)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch issues: %w", err)
		}

		var issues []Issue
		if err := json.Unmarshal(respBody, &issues); err != nil {
			return nil, fmt.Errorf("failed to parse issues response: %w", err)
		}
		totalRaw += len(issues)
		for i := range issues {
			if !issues[i].IsPullRequest() {
				allIssues = append(allIssues, issues[i])
			}
		}

		if !hasNextPage(headers, len(issues), totalRaw) {
			break
		}
		page++
		if page > MaxPages {
			return nil, fmt.Errorf("pagination limit exceeded: stopped after %d pages", MaxPages)
		}
	}

	return allIssues, nil
}

// FetchIssueByNumber retrieves a single issue by repository-scoped issue index.
// Returns nil, nil if the issue is not found (404).
func (c *Client) FetchIssueByNumber(ctx context.Context, number int) (*Issue, error) {
	path := fmt.Sprintf("%s/issues/%d", c.repoPath(), number)
	respBody, _, err := c.doRequest(ctx, http.MethodGet, c.buildURL(path, nil), nil)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to fetch issue #%d: %w", number, err)
	}
	var issue Issue
	if err := json.Unmarshal(respBody, &issue); err != nil {
		return nil, fmt.Errorf("failed to parse issue response: %w", err)
	}
	return &issue, nil
}

// ListLabels retrieves all labels defined in the configured repository.
func (c *Client) ListLabels(ctx context.Context) ([]Label, error) {
	var all []Label
	page := 1
	for {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}
		params := map[string]string{
			"limit": strconv.Itoa(MaxPageSize),
			"page":  strconv.Itoa(page),
		}
		respBody, headers, err := c.doRequest(ctx, http.MethodGet, c.buildURL(c.repoPath()+"/labels", params), nil)
		if err != nil {
			return nil, fmt.Errorf("failed to list labels: %w", err)
		}
		var labels []Label
		if err := json.Unmarshal(respBody, &labels); err != nil {
			return nil, fmt.Errorf("failed to parse labels response: %w", err)
		}
		all = append(all, labels...)
		if !hasNextPage(headers, len(labels), len(all)) {
			break
		}
		page++
		if page > MaxPages {
			return nil, fmt.Errorf("pagination limit exceeded: stopped after %d pages", MaxPages)
		}
	}
	return all, nil
}

// CreateLabel creates a repository label with the given name and hex color.
func (c *Client) CreateLabel(ctx context.Context, name, color string) (*Label, error) {
	if color == "" {
		color = deterministicLabelColor(name)
	}
	payload := map[string]interface{}{
		"name":  name,
		"color": color,
	}
	respBody, _, err := c.doRequest(ctx, http.MethodPost, c.buildURL(c.repoPath()+"/labels", nil), payload)
	if err != nil {
		return nil, fmt.Errorf("failed to create label %q: %w", name, err)
	}
	var lbl Label
	if err := json.Unmarshal(respBody, &lbl); err != nil {
		return nil, fmt.Errorf("failed to parse label response: %w", err)
	}
	return &lbl, nil
}

func deterministicLabelColor(name string) string {
	switch {
	case strings.HasPrefix(name, "priority::"):
		return "#d93f0b"
	case strings.HasPrefix(name, "type::"):
		return "#0075ca"
	case strings.HasPrefix(name, "status::"):
		return "#fbca04"
	}
	sum := sha256.Sum256([]byte(strings.ToLower(name)))
	return "#" + hex.EncodeToString(sum[:3])
}

// ResolveLabelIDs maps label names to numeric repository label IDs, creating
// any missing labels. Always returns a non-nil slice.
func (c *Client) ResolveLabelIDs(ctx context.Context, names []string) ([]int64, error) {
	ids := make([]int64, 0, len(names))
	if len(names) == 0 {
		return ids, nil
	}

	c.labelMu.Lock()
	defer c.labelMu.Unlock()

	if c.labelCache == nil {
		labels, err := c.ListLabels(ctx)
		if err != nil {
			return nil, err
		}
		c.labelCache = make(map[string]int64, len(labels))
		for _, lbl := range labels {
			key := strings.ToLower(strings.TrimSpace(lbl.Name))
			if key != "" && lbl.ID > 0 {
				c.labelCache[key] = lbl.ID
			}
		}
	}

	seen := make(map[int64]bool, len(names))
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		id, ok := c.labelCache[key]
		if !ok {
			created, err := c.CreateLabel(ctx, name, deterministicLabelColor(name))
			if err != nil {
				// Handle race/existing label conflict by refreshing label cache once.
				if labels, listErr := c.ListLabels(ctx); listErr == nil {
					for _, lbl := range labels {
						k := strings.ToLower(strings.TrimSpace(lbl.Name))
						if k != "" && lbl.ID > 0 {
							c.labelCache[k] = lbl.ID
						}
					}
					if refreshedID, exists := c.labelCache[key]; exists {
						id = refreshedID
						ok = true
					}
				}
				if !ok {
					return nil, err
				}
			} else {
				id = created.ID
				c.labelCache[key] = id
			}
		}
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// CreateIssue creates a new issue in the configured repository.
func (c *Client) CreateIssue(ctx context.Context, title, body string, labelIDs []int64) (*Issue, error) {
	reqBody := map[string]interface{}{
		"title": title,
		"body":  body,
	}
	if len(labelIDs) > 0 {
		reqBody["labels"] = labelIDs
	}
	respBody, _, err := c.doRequest(ctx, http.MethodPost, c.buildURL(c.repoPath()+"/issues", nil), reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create issue: %w", err)
	}
	var created Issue
	if err := json.Unmarshal(respBody, &created); err != nil {
		return nil, fmt.Errorf("failed to parse created issue: %w", err)
	}
	return &created, nil
}

// UpdateIssue patches an existing issue (title, body, state, etc.).
func (c *Client) UpdateIssue(ctx context.Context, number int, updates map[string]interface{}) (*Issue, error) {
	path := fmt.Sprintf("%s/issues/%d", c.repoPath(), number)
	respBody, _, err := c.doRequest(ctx, http.MethodPatch, c.buildURL(path, nil), updates)
	if err != nil {
		return nil, fmt.Errorf("failed to update issue #%d: %w", number, err)
	}
	var updated Issue
	if err := json.Unmarshal(respBody, &updated); err != nil {
		return nil, fmt.Errorf("failed to parse updated issue: %w", err)
	}
	return &updated, nil
}

// ReplaceIssueLabels replaces all labels on an issue with the provided numeric label IDs.
func (c *Client) ReplaceIssueLabels(ctx context.Context, number int, labelIDs []int64) error {
	if labelIDs == nil {
		labelIDs = []int64{}
	}
	path := fmt.Sprintf("%s/issues/%d/labels", c.repoPath(), number)
	_, err := func() ([]byte, error) {
		b, _, e := c.doRequest(ctx, http.MethodPut, c.buildURL(path, nil), map[string]interface{}{
			"labels": labelIDs,
		})
		return b, e
	}()
	if err != nil {
		return fmt.Errorf("failed to replace labels on issue #%d: %w", number, err)
	}
	return nil
}

// ListBlockedBy returns the issues that block issue number.
func (c *Client) ListBlockedBy(ctx context.Context, number int) ([]Issue, error) {
	var all []Issue
	page := 1
	path := fmt.Sprintf("%s/issues/%d/dependencies", c.repoPath(), number)
	for {
		select {
		case <-ctx.Done():
			return all, ctx.Err()
		default:
		}
		params := map[string]string{
			"limit": strconv.Itoa(MaxPageSize),
			"page":  strconv.Itoa(page),
		}
		respBody, headers, err := c.doRequest(ctx, http.MethodGet, c.buildURL(path, params), nil)
		if err != nil {
			return nil, err
		}
		var deps []Issue
		if err := json.Unmarshal(respBody, &deps); err != nil {
			return nil, fmt.Errorf("failed to parse dependencies response: %w", err)
		}
		all = append(all, deps...)
		if !hasNextPage(headers, len(deps), len(all)) {
			break
		}
		page++
		if page > MaxPages {
			return nil, fmt.Errorf("pagination limit exceeded: stopped after %d pages", MaxPages)
		}
	}
	return all, nil
}

// AddBlockedBy records that issue number is blocked by blockerOwner/blockerRepo#blockerNumber.
func (c *Client) AddBlockedBy(ctx context.Context, number int, blockerOwner, blockerRepo string, blockerNumber int) error {
	path := fmt.Sprintf("%s/issues/%d/dependencies", c.repoPath(), number)
	payload := map[string]interface{}{
		"owner": blockerOwner,
		"repo":  blockerRepo,
		"index": blockerNumber,
	}
	_, _, err := c.doRequest(ctx, http.MethodPost, c.buildURL(path, nil), payload)
	return err
}
