package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/storage/uow"
)

// These tests pin Config.RequestContext: the embedder hook that runs for every
// routed request after the bearer check (when the route has one) and before it
// is dispatched, and either returns the context the handler runs with or
// refuses the request.

// callerLabelKey is the context key a test hook stores its value under, the
// way an embedder would carry a label asserted by a trusted fronting proxy.
type callerLabelKey struct{}

// ctxRecordingProvider records the caller label on every context a unit of
// work is opened with, which is the context the handler handed to its store.
type ctxRecordingProvider struct {
	fakeProvider
	mu     sync.Mutex
	labels []any
}

// newCtxRecordingProvider answers the read paths the way newReadServer's
// provider does.
func newCtxRecordingProvider() *ctxRecordingProvider {
	return &ctxRecordingProvider{fakeProvider: fakeProvider{
		issues: &fakeIssues{}, readIssues: &recordingIssues{}, readConfig: emptyConfig{},
	}}
}

func (p *ctxRecordingProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	p.mu.Lock()
	p.labels = append(p.labels, ctx.Value(callerLabelKey{}))
	p.mu.Unlock()
	return p.fakeProvider.NewUOW(ctx)
}

func (p *ctxRecordingProvider) seen() []any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]any(nil), p.labels...)
}

// labelHook is a hook that copies a request header into the context and counts
// its calls.
func labelHook(calls *atomic.Int32) func(*http.Request) (context.Context, error) {
	return func(r *http.Request) (context.Context, error) {
		calls.Add(1)
		return context.WithValue(r.Context(), callerLabelKey{}, r.Header.Get("X-Caller-Label")), nil
	}
}

// TestRequestContextNilHookIsANoOp: a Config without the hook serves exactly
// what it served before the field existed.
func TestRequestContextNilHookIsANoOp(t *testing.T) {
	prov := newCtxRecordingProvider()
	ts := newTestServer(t, Config{Provider: prov})

	resp := ts.get(t, "/v0/beads/ready")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v0/beads/ready = %d, want 200", resp.StatusCode)
	}
	if got := prov.seen(); len(got) == 0 || got[0] != nil {
		t.Errorf("labels seen by the store = %v, want one nil", got)
	}
}

// TestRequestContextValueReachesTheStore: the context the hook returns is the
// one the handler runs with, all the way down to the unit of work it opens.
func TestRequestContextValueReachesTheStore(t *testing.T) {
	var calls atomic.Int32
	prov := newCtxRecordingProvider()
	ts := newTestServer(t, Config{Provider: prov, RequestContext: labelHook(&calls)})

	resp := ts.do(t, http.MethodGet, "/v0/beads/ready", http.Header{"X-Caller-Label": {"build-bot"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v0/beads/ready = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("hook calls = %d, want 1", got)
	}
	got := prov.seen()
	if len(got) == 0 {
		t.Fatal("the handler opened no unit of work")
	}
	for _, v := range got {
		if v != "build-bot" {
			t.Errorf("label seen by the store = %v, want build-bot", v)
		}
	}
}

// TestRequestContextKeepsTheRequestDeadline: the hook receives the request
// context after the per-request deadline is applied, so a derived context
// keeps it.
func TestRequestContextKeepsTheRequestDeadline(t *testing.T) {
	var hadDeadline atomic.Bool
	ts := newTestServer(t, Config{Provider: newCtxRecordingProvider(), RequestContext: func(r *http.Request) (context.Context, error) {
		_, ok := r.Context().Deadline()
		hadDeadline.Store(ok)
		return r.Context(), nil
	}})

	if resp := ts.get(t, "/v0/beads/ready"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v0/beads/ready = %d, want 200", resp.StatusCode)
	}
	if !hadDeadline.Load() {
		t.Error("the hook saw a context without the request deadline")
	}
}

// TestRequestContextRefusal: a *RequestRefusal picks the status, code and
// detail of the standard error envelope; any other error is a 403
// request_refused whose text never reaches the wire. Either way the handler
// never runs.
func TestRequestContextRefusal(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{
			name:       "typed refusal",
			err:        &RequestRefusal{Status: http.StatusUnauthorized, Code: "caller_unknown", Message: "no caller label"},
			wantStatus: http.StatusUnauthorized,
			wantCode:   "caller_unknown",
			wantDetail: "no caller label",
		},
		{
			name:       "wrapped typed refusal",
			err:        errors.Join(errors.New("context"), &RequestRefusal{Status: http.StatusTooManyRequests, Code: "caller_throttled"}),
			wantStatus: http.StatusTooManyRequests,
			wantCode:   "caller_throttled",
		},
		{
			name:       "typed refusal with no code or valid status",
			err:        &RequestRefusal{Status: http.StatusOK},
			wantStatus: http.StatusForbidden,
			wantCode:   "request_refused",
		},
		{
			name:       "plain error",
			err:        errors.New("internal-secret-detail"),
			wantStatus: http.StatusForbidden,
			wantCode:   "request_refused",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := newCtxRecordingProvider()
			ts := newTestServer(t, Config{Provider: prov, RequestContext: func(*http.Request) (context.Context, error) {
				return nil, tc.err
			}})

			resp := ts.get(t, "/v0/beads/ready")
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
				t.Errorf("Content-Type = %q, want problem+json", ct)
			}
			body := decodeBody(t, resp)
			if body["code"] != tc.wantCode {
				t.Errorf("code = %v, want %s", body["code"], tc.wantCode)
			}
			if body["status"] != float64(tc.wantStatus) {
				t.Errorf("status member = %v, want %d", body["status"], tc.wantStatus)
			}
			if id, _ := body["request_id"].(string); id == "" {
				t.Error("the refusal carries no request_id")
			}
			detail, _ := body["detail"].(string)
			if detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", detail, tc.wantDetail)
			}
			if got := prov.seen(); len(got) != 0 {
				t.Errorf("a refused request opened %d units of work", len(got))
			}
			line := findLogLine(t, ts.stderr.String(), "path=/v0/beads/ready")
			if !strings.Contains(line, "code="+tc.wantCode) {
				t.Errorf("request log line does not record the refusal code:\n%s", line)
			}
		})
	}
}

// TestRequestContextMustDeriveFromTheRequest: a hook that returns a context not
// derived from the request's own would drop the deadline and the request
// record, so the server refuses to dispatch it rather than run without them.
func TestRequestContextMustDeriveFromTheRequest(t *testing.T) {
	prov := newCtxRecordingProvider()
	ts := newTestServer(t, Config{Provider: prov, RequestContext: func(*http.Request) (context.Context, error) {
		return context.Background(), nil
	}})

	resp := ts.get(t, "/v0/beads/ready")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s", body["code"], CodeInternal)
	}
	if got := prov.seen(); len(got) != 0 {
		t.Errorf("an undispatchable request opened %d units of work", len(got))
	}
}

// TestRequestContextRunsOnlyAfterAuthorization: an unauthorized request is
// answered 401 without consulting the hook, and an authorized one reaches it.
func TestRequestContextRunsOnlyAfterAuthorization(t *testing.T) {
	var calls atomic.Int32
	ts := newTestServer(t, Config{
		Provider:       newCtxRecordingProvider(),
		Auth:           authTokenFile(t, "real-token"),
		RequestContext: labelHook(&calls),
	})

	resp := ts.do(t, http.MethodGet, "/v0/beads/ready", bearer("wrong-token"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", resp.StatusCode)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("the hook ran %d times for an unauthorized request", got)
	}

	resp = ts.do(t, http.MethodGet, "/v0/beads/ready", bearer("real-token"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("hook calls after one authorized request = %d, want 1", got)
	}
}

// TestRequestContextRunsForAuthExemptRoutes pins the other half of the
// contract: an auth-exempt route (the liveness probe) reaches the hook with no
// credential checked, so a hook must never read "it reached me" as "it was
// authorized".
func TestRequestContextRunsForAuthExemptRoutes(t *testing.T) {
	var calls atomic.Int32
	ts := newTestServer(t, Config{
		Provider:       newCtxRecordingProvider(),
		Auth:           authTokenFile(t, "real-token"),
		RequestContext: labelHook(&calls),
	})
	if resp := ts.get(t, "/healthz"); resp.StatusCode != http.StatusOK {
		t.Fatalf("liveness without a credential = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("hook calls for an unauthenticated liveness probe = %d, want 1", got)
	}
}

// TestRequestContextNotCalledForEarlierRejections: a request the server turns
// away before authorization completes — a refused Host, an unrouted path —
// never reaches the hook.
func TestRequestContextNotCalledForEarlierRejections(t *testing.T) {
	var calls atomic.Int32
	ts := newTestServer(t, Config{RequestContext: labelHook(&calls)})

	if resp := requestWithHost(t, ts, "/v0/beads/ready", "evil.example"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("refused Host status = %d, want 400", resp.StatusCode)
	}
	if resp := ts.get(t, "/v0/no-such-route"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unrouted status = %d, want 404", resp.StatusCode)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("hook calls = %d, want 0", got)
	}
}
