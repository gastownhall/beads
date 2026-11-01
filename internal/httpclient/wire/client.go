// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/client.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

// ProjectIDHeader names the optional per-request workspace-identity stamp this
// client sends, spelled exactly as the server's httpapi.ProjectIDHeader and held
// to it by TestProjectIDHeaderMatchesTheServerConstant. A client that pinned an
// expected project id at `bd connect` stamps every request with it, so a server
// whose identity has drifted refuses the request (400 project_mismatch) before it
// can read or write the wrong workspace, instead of silently answering it. The
// header is redeclared here rather than imported because internal/httpapi is the
// SERVER — importing it would drag the storage engine into every client process.
const ProjectIDHeader = "Bd-Project-Id"

// WireRevisionHeader names the per-request wire-shape declaration this client
// sends on every request, spelled exactly as the server's own
// httpapi.WireRevisionHeader (held to it by
// TestTheProjectIdentityVocabularyMatchesTheServer). Redeclared rather than
// imported for ProjectIDHeader's reason: internal/httpapi is the SERVER.
const WireRevisionHeader = "Bd-Wire-Revision"

const (
	// DefaultUserAgent identifies the backend rather than the build. The build
	// version lives in package main, which nothing here may import, so the
	// activation layer overrides this with the stamped one.
	DefaultUserAgent = "bd-http-backend/v0"
	// DefaultMaxResponseBytes bounds one response body. A full page of issues
	// with long descriptions is a few megabytes; anything at this size is a URL
	// pointing at something that is not a bd serve, and reading it into memory
	// is the actual damage.
	DefaultMaxResponseBytes = 32 << 20
	// DefaultMaxRetryAfter bounds how long a 503's Retry-After may ask for. The
	// header is server-controlled input; see retryAfter.
	DefaultMaxRetryAfter = 30 * time.Second
	// DefaultTimeout is the per-request ceiling for a client this package built.
	// A caller's context still wins when it is shorter, and an injected
	// *http.Client keeps its own, except that a large apply raises a bounded
	// one for that request alone (Request.Timeout, LargeApplyTimeout).
	// Exported so a caller that must build its own *http.Client — to layer in
	// a custom RoundTripper such as the one httpstore.TransportFor returns —
	// can match this default rather than silently dropping it.
	DefaultTimeout = 60 * time.Second
	// LargeApplyTimeout is the per-request ceiling for the one request shape
	// bd serve grants MORE than DefaultTimeout: a POST issues:batchApply
	// carrying more than LargeApplyItemThreshold items. The server runs such a
	// request under its own extended budget — httpapi.DefaultLargeApplyCeiling
	// (5m, `bd serve --large-apply-ceiling`), timed from the moment it takes
	// the one-wide large-write slot — on a context DETACHED from the client's,
	// so a client that gave up at DefaultTimeout would report a failure for an
	// apply the server goes on to commit. Before that budget starts the server
	// may already have spent its whole request-read timeout (30s, which spans
	// its wait for a general slot and the body upload) and its bounded wait
	// for the large-write slot (10s); after the commit it still has to write
	// the answer back. This value covers all of that at the server's default
	// ceiling, and internal/httpapi's
	// TestTheClientLargeApplyBudgetCoversTheServer holds it to the server's
	// constants. A caller's context still wins when it is shorter, and an
	// injected *http.Client with no Timeout at all stays unbounded. An
	// operator who raises --large-apply-ceiling past about 5m15s is not
	// covered: the server does not advertise its ceiling, so the client
	// cannot learn it.
	LargeApplyTimeout = 6 * time.Minute
)

// LargeApplyItemThreshold mirrors httpapi's own largeApplyItemThreshold: the
// item count a batchApply request must EXCEED to run under the server's
// extended large-apply budget, and so to be dialed under LargeApplyTimeout
// here. Redeclared rather than imported for ProjectIDHeader's reason; held to
// the server's value by internal/httpapi's
// TestTheClientLargeApplyBudgetCoversTheServer.
const LargeApplyItemThreshold = 100

// Options tunes the client. The zero value is usable: every field falls back to
// the Default* constants above.
type Options struct {
	// HTTPClient is used as given except for CheckRedirect, which is always
	// forced (see New), and a positive Timeout, which a request the server
	// grants longer (Request.Timeout) raises for that request alone. Nil
	// builds one.
	HTTPClient *http.Client
	// UserAgent is sent on every request.
	UserAgent string
	// MaxResponseBytes bounds one response body.
	MaxResponseBytes int64
	// MaxRetryAfter bounds the Retry-After a 503 may ask for.
	MaxRetryAfter time.Duration
	// ExpectProjectID pins workspace identity, recorded at `bd connect`. The
	// handshake compares it against ContextResponse.project_id; "" skips the
	// check.
	ExpectProjectID string
	// ResponseHeaderTimeout bounds the first attempt's wait for response
	// headers on an idempotent request (GET, or Request.Idempotent), counted
	// from when the request has been written and separately from the
	// HTTPClient's overall Timeout: zero means DefaultResponseHeaderTimeout,
	// negative disables it. A body that is still streaming once the headers
	// arrived is never cut off by it. Such a request that hits it, or whose
	// connection is reset, is retried once on another connection, with no
	// header timer and only what is left of the overall Timeout, counted from
	// when the first attempt was sent. A write that is not Request.Idempotent
	// is never retried and is bounded by the overall Timeout alone (bd serve
	// answers a write after it commits). A written request whose headers do
	// not come back in time fails with *ResponseHeaderTimeoutError.
	ResponseHeaderTimeout time.Duration
	// OnStallRetry, when set, is told about every such retry. The process-wide
	// count is StallRetryCount either way.
	OnStallRetry func(StallRetry)
}

// Client is one workspace's connection to one bd serve.
//
// It is safe for concurrent use: everything below the handshake cache is
// request-scoped, and the cache takes a mutex.
type Client struct {
	base      *url.URL
	hc        *http.Client
	creds     CredentialProvider
	userAgent string
	maxBytes  int64
	maxRetry  time.Duration

	headerTimeout time.Duration
	onStall       func(StallRetry)

	handshake handshakeCache
	expectID  string

	// memo, when EnableReadMemo turned it on, answers a repeated identical
	// single-row or settings read from the response already received
	// (read_memo.go). Nil is off, the default.
	memo *readMemo
}

// New builds a client for base.
//
// creds may be nil, which is the tip OSS server's loopback-trust posture: no
// Authorization header is sent and a 401 is never retried, because there is
// nothing to rotate.
func New(base *url.URL, creds CredentialProvider, opts Options) (*Client, error) {
	if base == nil {
		return nil, errors.New("no server URL")
	}
	if err := validateBase(base); err != nil {
		return nil, err
	}

	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	} else {
		clone := *hc
		hc = &clone
	}
	// Forced on the copy, never on the caller's client. A 30x that the stdlib
	// followed would replay the Authorization header at whatever host the
	// Location named, which is the one thing a bearer token must never do.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	c := &Client{
		base:      normalizeBase(base),
		hc:        hc,
		creds:     creds,
		userAgent: opts.UserAgent,
		maxBytes:  opts.MaxResponseBytes,
		maxRetry:  opts.MaxRetryAfter,
		expectID:  opts.ExpectProjectID,
		onStall:   opts.OnStallRetry,
	}
	switch {
	case opts.ResponseHeaderTimeout == 0:
		c.headerTimeout = DefaultResponseHeaderTimeout
	case opts.ResponseHeaderTimeout > 0:
		c.headerTimeout = opts.ResponseHeaderTimeout
	}
	if c.userAgent == "" {
		c.userAgent = DefaultUserAgent
	}
	if c.maxBytes <= 0 {
		c.maxBytes = DefaultMaxResponseBytes
	}
	if c.maxRetry <= 0 {
		c.maxRetry = DefaultMaxRetryAfter
	}
	// A pinned project id becomes a header value on EVERY request, so a value
	// that is not a legal header value has to be caught here, at open, and refused
	// LOUDLY — never stamped-and-hoped or silently skipped, which would turn a
	// corrupt sidecar into either a per-request transport failure or an unenforced
	// workspace. httpguts.ValidHeaderFieldValue is the byte-level RFC 7230 rule
	// net/http itself applies; the package's own isControlRune check is layered on
	// top of it because that rule is byte-based and misses the multi-byte control
	// runes (the C1 block, U+2028/U+2029) an escape-injection would hide in.
	if c.expectID != "" &&
		(!httpguts.ValidHeaderFieldValue(c.expectID) || strings.ContainsFunc(c.expectID, isControlRune)) {
		return nil, &InvalidProjectIDError{ServerURL: c.base.Redacted(), ProjectID: c.expectID}
	}
	return c, nil
}

// BaseURL is the server mount root this client dials.
func (c *Client) BaseURL() *url.URL {
	u := *c.base
	return &u
}

// validateBase refuses the shapes that would make every later URL wrong or
// unsafe.
func validateBase(base *url.URL) error {
	switch base.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("server URL %q: scheme must be http or https", base.Redacted())
	}
	if base.Host == "" {
		return fmt.Errorf("server URL %q: no host", base.Redacted())
	}
	// Refused rather than stripped. A credential in the URL would be echoed by
	// every error that names the server, and the sidecar this URL comes from is
	// specified to hold no token at all.
	if base.User != nil {
		return errors.New("server URL carries embedded credentials; put the token in the credential source, not the URL")
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("server URL %q: a query or fragment is not a mount root", base.Redacted())
	}
	return nil
}

func normalizeBase(base *url.URL) *url.URL {
	u := *base
	// A trailing slash on the mount root would join to a doubled separator.
	for len(u.Path) > 1 && u.Path[len(u.Path)-1] == '/' {
		u.Path = u.Path[:len(u.Path)-1]
		u.RawPath = ""
	}
	if u.Path == "/" {
		u.Path = ""
	}
	return &u
}

// Request is one call on the v0 surface: the operation being dialed, where it
// lives, and what it carries.
//
// Path comes from the builders in paths.go and is already escaped. IssueID and
// DependsOnID are not sent — they are what the problem mapper needs to rebuild
// the typed conflicts whose extension members the server leaves out because the
// request already said them.
type Request struct {
	Op     string
	Method string
	Path   string
	Query  url.Values
	// Body is marshaled as JSON when non-nil.
	Body any

	IssueID     string
	DependsOnID string
	// Actor is the acting identity the body carries, for a refusal whose typed
	// error names it — the close's not_assignee — when the wire does not
	// repeat what the request already said.
	Actor string
	// Idempotent marks a read carried on a non-GET method (a POST batch get)
	// as safe to replay, the way a GET is (see Replayable). Never set it on a
	// write whose replay could double-apply. The one write that sets it is the
	// lease heartbeat (heartbeatIssue): a repeat only moves the expiry later,
	// and a replay after the lease is gone fails the holder fence.
	Idempotent bool
	// Timeout, when positive, RAISES the client's per-request ceiling for this
	// one request — never lowers it, and never bounds a client whose
	// *http.Client has no Timeout. It exists for the requests the server
	// itself grants longer than DefaultTimeout (see LargeApplyTimeout);
	// shortening a request is the caller's context's job.
	Timeout time.Duration
}

// Replayable reports whether r may be sent again after an attempt that got no
// usable answer: the safe methods, or a read carried on POST that says so
// (Idempotent). It is the one predicate both retries consult: the wire's
// retry of a stalled or reset attempt on another connection, and the store's
// bounded retry of a 503.
func (r Request) Replayable() bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return r.Idempotent
}

// Do issues r and decodes a 2xx body into out, which may be nil for a response
// the caller does not read.
//
// Non-2xx returns *ProblemError, unwrapping to the canonical sentinel for its
// code. Everything before a response — a dial failure, a refused redirect, an
// oversized body — returns its own typed error, because none of those is the
// server answering.
//
// Do does NOT pre-flight. The dispatch layer calls Preflight first, and the
// separation is deliberate: the refusal taxonomy has to refuse a post-baseline
// operation before a request is built at all, and folding the handshake in here
// would make every transport test carry one.
func (c *Client) Do(ctx context.Context, r Request, out any) error {
	var body []byte
	if r.Body != nil {
		var err error
		body, err = json.Marshal(r.Body)
		if err != nil {
			return fmt.Errorf("%s: encoding request body: %w", r.Op, err)
		}
	}

	u, err := c.resolve(r.Path, r.Query)
	if err != nil {
		return fmt.Errorf("%s: %w", r.Op, err)
	}

	var (
		res *response
		// memoKey is set only for a memoable read that MISSED, whose answer is
		// then stored under it at memoGen. A hit is never stored again: that
		// would renew its age (read_memo.go, put).
		memoKey string
		memoGen uint64
	)
	switch {
	case c.memo == nil:
	case memoable(r):
		key := u.String()
		if res, memoGen = c.memo.lookup(key); res == nil {
			memoKey = key
		}
	case r.Method != http.MethodGet:
		// A write may change anything a memoized read answered: drop them all
		// before it is sent, and again once it returns. Each clear starts a new
		// generation, so a read that overlapped the write cannot store a
		// pre-write answer behind it either.
		c.memo.clear()
		defer c.memo.clear()
	}
	if res == nil {
		var err error
		res, err = c.roundTrip(ctx, r, u, body)
		if err != nil {
			return err
		}
	}

	// The rotation window, and the whole of it. The server re-reads its token
	// file on a ~1s gate, so a client rolled mid-flight 401s once and succeeds
	// on the retry; a second 401 is a credential this server does not accept.
	if res.status == http.StatusUnauthorized && c.creds != nil {
		retry, err := c.creds.Refresh(ctx)
		if err != nil {
			// Fail closed: a credential source that errored is not a license to
			// send the stale one again, and it is certainly not a license to
			// send none.
			return fmt.Errorf("%s: refreshing the credential for bd serve at %s: %w", r.Op, c.base.Redacted(), err)
		}
		if retry {
			if res, err = c.roundTrip(ctx, r, u, body); err != nil {
				return err
			}
		}
	}

	if res.status < 200 || res.status >= 300 {
		prob := mapProblem(c.target(r), res.status, res.header, res.body, c.maxRetry)
		// A refused credential should name the ladder rung it came from (design
		// D7). The source is a client-side fact the provider knows, not something
		// the server said, so it is attached here rather than parsed from the body.
		if res.status == http.StatusUnauthorized {
			prob.CredentialSource = credentialSource(c.creds)
		}
		return prob
	}
	if out == nil {
		// Nothing decoded the answer, so nothing vouches for it: it is not
		// memoized (every memoable read in this package decodes its answer).
		return nil
	}
	if len(bytes.TrimSpace(res.body)) == 0 {
		return fmt.Errorf("%s: bd serve at %s answered %d with an empty body", r.Op, c.base.Redacted(), res.status)
	}
	decodeBody := res.body
	// A pre-#6053 server's `revision` is still a bare JSON integer; every
	// apigen response type that carries one declares it `string`, so decoding
	// straight into out would fail with an untyped json.UnmarshalTypeError on
	// exactly the servers ClientMinWireRevision exists to keep talking to. See
	// revision_tolerance.go.
	if revisionBearingResponse(out) && c.serverPredatesRevisionStrings() {
		decodeBody = tolerateLegacyRevisionNumbers(decodeBody, out)
	}
	if err := json.Unmarshal(decodeBody, out); err != nil {
		return fmt.Errorf("%s: decoding the response from bd serve at %s: %w", r.Op, c.base.Redacted(), err)
	}
	if memoKey != "" {
		// Only an answer that decoded is memoized. A 2xx with an empty or
		// undecodable body is the server misbehaving once; storing it would
		// fail every identical read for a whole TTL instead of asking again.
		c.memo.put(memoKey, res, memoGen)
	}
	return nil
}

func (c *Client) target(r Request) target {
	return target{op: r.Op, serverURL: c.base.Redacted(), issueID: r.IssueID, dependsOnID: r.DependsOnID, actor: r.Actor, expectID: c.expectID}
}

// credentialSourceReporter is the optional half of a CredentialProvider: a
// provider that can name the ladder rung its current credential came from. It is
// kept off the CredentialProvider interface itself so a no-auth or third-party
// provider that cannot report a source is not forced to — such a provider simply
// yields no name and the 401 reads as it did before.
type credentialSourceReporter interface {
	Source() string
}

// credentialSource is the provider's current credential provenance, or "" when
// there is no provider or it cannot report one.
func credentialSource(cp CredentialProvider) string {
	if r, ok := cp.(credentialSourceReporter); ok {
		return r.Source()
	}
	return ""
}

// resolve joins an already-escaped path and a query onto the base URL.
func (c *Client) resolve(path string, query url.Values) (*url.URL, error) {
	if path == "" {
		return nil, errors.New("no request path")
	}
	u := c.base.JoinPath(path)
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u, nil
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// roundTrip issues r, retrying an idempotent request once on another
// connection when the first attempt stalls before its headers or has its
// connection reset (see Options.ResponseHeaderTimeout).
//
// The request is built and stamped once and each attempt sends a copy, so the
// retry replays what the first attempt sent and the credential source is
// consulted before any clock starts. The retry gets no header timer, and both
// attempts share the one overall budget the client's Timeout gives a request,
// counted, as that Timeout is, from when the first attempt is sent: a
// credential source slow to resolve (a cold token command) is not the
// server's time. The timer cannot tell a connection that went silent from a
// serve still working on a slow read, so a retry cut short by it again would
// fail a healthy read the client used to wait out. Bounded by what is left of
// the overall Timeout instead, such a read still finishes if its headers and
// body fit in what is left, and no read waits longer than that Timeout.
func (c *Client) roundTrip(ctx context.Context, r Request, u *url.URL, body []byte) (*response, error) {
	req, err := c.newRequest(ctx, r, u, body)
	if err != nil {
		return nil, err
	}
	if !r.Replayable() {
		// Only a replayable request gets the header timer. bd serve sends a
		// write's headers after the write commits, so a slow but healthy write
		// (a large batch apply) can legitimately take longer than the header
		// timeout, and cutting it would turn it into an unknown outcome that
		// cannot be retried. A write stays bounded by the client's overall
		// timeout, and a timeout awaiting its headers is typed in
		// roundTripOnce.
		return c.roundTripOnce(ctx, r, u, req, 0, time.Time{})
	}
	sent := time.Now()
	res, err := c.roundTripOnce(ctx, r, u, req, c.headerTimeout, time.Time{})
	if err == nil {
		return res, nil
	}
	var deadline time.Time
	if timeout := c.httpClientFor(r).Timeout; timeout > 0 {
		deadline = sent.Add(timeout)
	}
	reason := stallReason(err)
	if reason == "" || ctx.Err() != nil || (!deadline.IsZero() && !time.Now().Before(deadline)) {
		return nil, err
	}
	// No idle-pool flush: canceling the stalled attempt closed its connection
	// (net/http never pools a canceled HTTP/1.1 connection, which is what bd
	// serve speaks; behind an HTTP/2 proxy the retry may share the proxy's
	// connection), so the retry rides another one. The transport may also be
	// shared by every client in the process, and its idle connections to
	// other servers are none of this request's business.
	event := StallRetry{Op: r.Op, Method: r.Method, Host: u.Host, Reason: reason, Retry: 1, Total: stallRetries.Add(1), Err: err}
	if c.onStall != nil {
		c.onStall(event)
	}
	res, err = c.roundTripOnce(ctx, r, u, req, 0, deadline)
	var hte *ResponseHeaderTimeoutError
	if errors.As(err, &hte) {
		hte.After, hte.Attempts, hte.Retried, hte.Reason = time.Since(sent), 2, true, reason
	}
	return res, err
}

// httpClientFor is the *http.Client one request is sent through: c.hc, or —
// when r.Timeout raises a bounded client's ceiling — a shallow copy of it with
// that longer Timeout. The copy shares c.hc's Transport (and so its connection
// pool and CA scope) and its forced CheckRedirect; only the whole-request
// deadline differs.
func (c *Client) httpClientFor(r Request) *http.Client {
	if r.Timeout <= 0 || c.hc.Timeout <= 0 || r.Timeout <= c.hc.Timeout {
		return c.hc
	}
	hc := *c.hc
	hc.Timeout = r.Timeout
	return &hc
}

// largeApplyTimeout is the Request.Timeout for a batchApply request carrying n
// items: LargeApplyTimeout once n crosses the server's large-apply threshold,
// zero (the client's own ceiling) otherwise.
func largeApplyTimeout(n int) time.Duration {
	if n > LargeApplyItemThreshold {
		return LargeApplyTimeout
	}
	return 0
}

// newRequest builds r's HTTP request and stamps it (see stampRequest).
// roundTripOnce sends a copy of it per attempt.
func (c *Client) newRequest(ctx context.Context, r Request, u *url.URL, body []byte) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u.String(), rdr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.Op, err)
	}
	req.Header.Set("Accept", "application/json, application/problem+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.stampRequest(ctx, r.Op, req); err != nil {
		return nil, err
	}
	return req, nil
}

// roundTripOnce sends one copy of the stamped request and reads its whole
// bounded body. headerTimeout (0: none) bounds its wait for headers once it is
// written, and deadline (zero: none) bounds the whole attempt on top of ctx
// and the client's own Timeout.
//
// Reading the body here rather than streaming it to the caller is what makes
// the 401 retry above expressible at all: both attempts are then symmetric, and
// the request body is a []byte that can be replayed. The size cap is what makes
// that safe.
func (c *Client) roundTripOnce(ctx context.Context, r Request, u *url.URL, stamped *http.Request, headerTimeout time.Duration, deadline time.Time) (*response, error) {
	attemptCtx := ctx
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	started := time.Now()
	attemptCtx, ht := startHeaderTimer(attemptCtx, headerTimeout)
	defer ht.done()
	req := stamped.Clone(attemptCtx)
	if stamped.GetBody != nil {
		// Clone shares the body reader, which an earlier attempt may have
		// consumed; every attempt sends the body from its start.
		b, err := stamped.GetBody()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Op, err)
		}
		req.Body = b
	}

	//nolint:gosec // G704: the URL is the workspace's configured server, resolved from the sidecar or --server-url; dialing it IS the feature.
	resp, err := c.httpClientFor(r).Do(req)
	if ht.stop() {
		// The header timer fired first. Headers that raced it are discarded:
		// the attempt's context is already canceled, so its body is lost.
		if resp != nil {
			_ = resp.Body.Close()
		}
		hte := &ResponseHeaderTimeoutError{Op: r.Op, Method: r.Method, Host: u.Host, After: headerTimeout, Attempts: 1, Replayable: r.Replayable()}
		return nil, &ConnectError{Op: r.Op, ServerURL: c.base.Redacted(), Err: hte}
	}
	if err != nil {
		// A timeout after the request was written ran out awaiting its
		// headers: Do returns once they arrive, so whichever bound fired (the
		// client's Timeout or this attempt's deadline) fired before them. A
		// timeout before the request was written (a dial or TLS handshake)
		// stays a plain ConnectError, because the server cannot have seen
		// the request. The caller's own context expiring is the caller's.
		if wroteAt, ok := ht.written(); ok && ctx.Err() == nil && isTimeout(err) {
			hte := &ResponseHeaderTimeoutError{Op: r.Op, Method: r.Method, Host: u.Host, After: time.Since(wroteAt), Attempts: 1, Replayable: r.Replayable(), Err: err}
			return nil, &ConnectError{Op: r.Op, ServerURL: c.base.Redacted(), Err: hte}
		}
		if de := dialFailure(r.Op, u.Host, err, time.Since(started)); de != nil && ctx.Err() == nil {
			return nil, &ConnectError{Op: r.Op, ServerURL: c.base.Redacted(), Err: de}
		}
		return nil, &ConnectError{Op: r.Op, ServerURL: c.base.Redacted(), Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, redirectRefused(r.Op, c.base.Redacted(), resp)
	}

	// One byte past the cap, so an oversized body is detected rather than
	// silently truncated into a document that happens to parse.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return nil, &ConnectError{Op: r.Op, ServerURL: c.base.Redacted(), Err: err}
	}
	if int64(len(raw)) > c.maxBytes {
		return nil, &ResponseTooLargeError{Op: r.Op, ServerURL: c.base.Redacted(), Limit: c.maxBytes}
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: raw}, nil
}

// stampRequest applies the headers every request on this surface shares: the
// backend User-Agent, the workspace-identity stamp, and the credential.
//
// It is shared by roundTrip and the stream door (stream.go) because both stamp
// the SAME three things — the Accept header and any request body are the only
// parts that differ, and the caller sets those. The Bd-Project-Id stamp rides
// EVERY request through here — the five baseline reads, both attempts of the 401
// retry, and the watch connect — because a server that has drifted to a
// different project must refuse before it reads or writes rather than answer
// silently against the wrong workspace (projectExempt is false for events:watch,
// so the stream is stamped like everything else). The value was validated at
// New, so Set cannot corrupt the header.
func (c *Client) stampRequest(ctx context.Context, op string, req *http.Request) error {
	req.Header.Set("User-Agent", c.userAgent)
	// The client's own declared wire shape (see ClientWireRevision's doc),
	// stamped on every request including the context fetch itself: a server
	// whose min_client_wire_revision has moved past what this build speaks
	// refuses with the typed wire_revision_unsupported problem rather than
	// silently answering a body this client has already said it cannot decode.
	req.Header.Set(WireRevisionHeader, strconv.Itoa(ClientWireRevision))
	if c.expectID != "" {
		req.Header.Set(ProjectIDHeader, c.expectID)
	}
	if c.creds != nil {
		if err := c.creds.Authorize(ctx, req); err != nil {
			return fmt.Errorf("%s: authorizing the request to bd serve at %s: %w", op, c.base.Redacted(), err)
		}
	}
	return nil
}

// ConnectError is a failure to get an answer at all: DNS, dial, TLS, a reset
// mid-body, or the caller's own context expiring. It is deliberately not a
// ProblemError — the server said nothing.
type ConnectError struct {
	Op        string
	ServerURL string
	Err       error
}

func (e *ConnectError) Error() string {
	return fmt.Sprintf("cannot reach bd serve at %s: %v", e.ServerURL, e.Err)
}

func (e *ConnectError) Unwrap() error { return e.Err }

// ErrRedirected reports that the server answered with a redirect, which this
// client refuses to follow.
var ErrRedirected = errors.New("bd serve answered with a redirect")

// RedirectRefusedError is a 3xx this client did not follow. Following it would
// replay the Authorization header at whatever host the Location named; a bd
// serve does not redirect, so a 30x means the URL points at something else —
// a proxy login page, a CDN, an HTTPS upgrade — and saying so beats leaking a
// token to find out.
type RedirectRefusedError struct {
	Op        string
	ServerURL string
	Status    int
	// Location is the redirect target with its query and any userinfo stripped:
	// it is server-controlled text on its way into a log line, and the scheme,
	// host and path are the whole of what diagnoses this.
	Location string
}

func (e *RedirectRefusedError) Error() string {
	if e.Location == "" {
		return fmt.Sprintf("%s: bd serve at %s answered %d; redirects are refused", e.Op, e.ServerURL, e.Status)
	}
	return fmt.Sprintf("%s: bd serve at %s answered %d redirecting to %s; redirects are refused (the URL does not point at a bd serve)",
		e.Op, e.ServerURL, e.Status, e.Location)
}

func (e *RedirectRefusedError) Unwrap() error { return ErrRedirected }

func redirectRefused(op, serverURL string, resp *http.Response) *RedirectRefusedError {
	e := &RedirectRefusedError{Op: op, ServerURL: serverURL, Status: resp.StatusCode}
	if loc, err := url.Parse(resp.Header.Get("Location")); err == nil && loc.String() != "" {
		loc.User = nil
		loc.RawQuery = ""
		loc.Fragment = ""
		e.Location = loc.Redacted()
	}
	return e
}

// ErrResponseTooLarge reports that a response body exceeded the client's cap.
var ErrResponseTooLarge = errors.New("response from bd serve is too large")

// ResponseTooLargeError is a body past Options.MaxResponseBytes. The cap is
// named in the text because raising it is the only recovery, and because the
// far likelier cause is a URL that does not point at a bd serve at all.
type ResponseTooLargeError struct {
	Op        string
	ServerURL string
	Limit     int64
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("%s: response from bd serve at %s exceeds the %d-byte limit", e.Op, e.ServerURL, e.Limit)
}

func (e *ResponseTooLargeError) Unwrap() error { return ErrResponseTooLarge }
