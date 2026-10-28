//go:build cgo

// Contributed by gascity from bd-enterprise (internal/httpclient/ent_served_large_apply_timeout_test.go@1ee63dff5)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// slowBatchApplier delays the served BatchApplier before delegating, standing
// in for a large apply on a slow or contended server.
type slowBatchApplier struct {
	inner issueops.BatchApplier
	delay time.Duration
}

func (s slowBatchApplier) ApplyBatch(ctx context.Context, req issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return issueops.ApplyBatchResult{}, ctx.Err()
	}
	return s.inner.ApplyBatch(ctx, req)
}

// TestServedLargeApplyOutlivesTheDefaultClientTimeout is the product-path
// regression for a batchApply over the server's large-apply threshold that
// the server takes LONGER than wire.DefaultTimeout to answer.
// The server grants such a request httpapi.DefaultLargeApplyCeiling (5m) on a
// context detached from the client, so it commits regardless; before the fix
// the client gave up at 60s with "Client.Timeout exceeded while awaiting
// headers" and reported a failure for an apply that then landed.
//
// The client is dialed the way the product dials one — DialWith with no
// caller-supplied *http.Client, as OpenWith and backend/http's Open do — so
// it carries the production transport
// and wire.DefaultTimeout rather than the harness's bare wire.New. A ceiling
// the dial path adds (a transport-level header timeout, say) would fail this
// case the way the original one did.
//
// It deliberately spends just over a minute: the point is the PRODUCT client's
// real ceiling, which no test hook shortens. Like every served case it runs
// only in the served tier (newServedEnv skips it unless
// BEADS_TEST_EMBEDDED_DOLT=1).
func TestServedLargeApplyOutlivesTheDefaultClientTimeout(t *testing.T) {
	delay := wire.DefaultTimeout + 5*time.Second
	env := newServedEnv(t, "hbalt", func(cfg *httpapi.Config) {
		if cfg.BatchApplier == nil {
			// Failed, not skipped: in the required served tier a skip would
			// drop this regression silently. Port the delay to whatever now
			// serves batchApply instead.
			t.Fatal("the served config binds no BatchApplier role to delay")
		}
		cfg.BatchApplier = slowBatchApplier{inner: cfg.BatchApplier, delay: delay}
	})
	target := Target{BaseURL: env.base}
	conn, err := DialWith(target, nil, DialOptionsForTarget(target, DialOptions{}))
	if err != nil {
		t.Fatalf("DialWith(%s): %v", target, err)
	}
	applier, err := New(target, conn, nil).BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}

	const title = "large apply past the default client timeout"
	n := wire.LargeApplyItemThreshold + 1
	items := make([]issueops.ApplyItem, n)
	for i := range items {
		items[i] = issueops.ApplyItem{Kind: issueops.ItemCreate, Create: &issueops.CreateItem{
			Issue: &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}}
	}

	start := time.Now()
	result, err := applier.ApplyBatch(t.Context(), issueops.ApplyBatchRequest{Actor: "apply-writer", Items: items})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ApplyBatch(%d items) with the server taking %v: %v (after %v); a large apply must be dialed "+
			"under wire.LargeApplyTimeout, not wire.DefaultTimeout", n, delay, err, elapsed)
	}
	if elapsed < wire.DefaultTimeout {
		t.Fatalf("ApplyBatch returned after %v, before wire.DefaultTimeout (%v): the delay never engaged, so this "+
			"case proved nothing", elapsed, wire.DefaultTimeout)
	}
	if len(result.Items) != n {
		t.Fatalf("ApplyBatch returned %d item results, want %d", len(result.Items), n)
	}
	var rows int
	if err := env.queryScalar(t.Context(), "SELECT COUNT(*) FROM issues WHERE title = ?", []any{title}, &rows); err != nil {
		t.Fatalf("count the applied rows: %v", err)
	}
	if rows != n {
		t.Errorf("%d row(s) landed, want %d", rows, n)
	}
}
