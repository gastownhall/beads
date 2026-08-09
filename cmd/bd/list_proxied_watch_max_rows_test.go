//go:build cgo

package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestProxiedServerListWatchCountsTheOffsetIntoMaxRows pins the cap boundary on
// the one `bd list` mode no contract can reach.
//
// --watch consumes the FILTER as a value and re-runs it on a ticker, so it has
// no shared page epilogue above it to take the offset off the query
// (issueops.Reader's doc names it as one of the two exceptions). That used to
// make it the only route where --offset and --max-rows disagreed with every
// other one: the seam rendered LIMIT n OFFSET k, so EnforceMaxRowsCap counted
// the survivors of the skip, and the request the paged route refuses came back
// here as a page. The combination became reachable when `bd list --max-rows`
// stopped being rejected under --proxied-server.
//
// It is also the only place it can be driven. The reader contract never hands a
// seam an offset — both implementations widen the filter and skip in the
// epilogue — and the direct route refuses --offset outside --proxied-server.
func TestProxiedServerListWatchCountsTheOffsetIntoMaxRows(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "lwcap")

	// Five matching rows, so a window bounded at four has a row behind it. A
	// smaller result set could not reach the cap and the case would pass on a
	// route that had no cap at all.
	const scope = "watch-cap-scope"
	for i := range 5 {
		bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Watch cap row %d", i), "--type", "task", "--label", scope)
	}

	// The paged route's answer to the identical request, READ rather than
	// asserted from a number written here: the promise is that the two routes
	// agree, so this is the thing the watch route is compared against.
	paged := bdProxiedListFail(t, bd, p, "--label", scope, "--limit", "10", "--offset", "2", "--max-rows", "3")
	if !strings.Contains(paged, "too many rows") {
		t.Fatalf("bd list --limit 10 --offset 2 --max-rows 3 must refuse before this case can ask --watch to agree with it; got:\n%s", paged)
	}

	t.Run("fires_where_the_page_route_fires", func(t *testing.T) {
		stdout, stderr, err, timedOut := bdProxiedRunDeadline(t, bd, p.dir, 90*time.Second,
			"list", "--watch", "--label", scope, "--limit", "10", "--offset", "2", "--max-rows", "3")
		out := stdout + stderr
		if timedOut {
			t.Fatalf("bd list --watch --offset 2 --max-rows 3 kept watching; the page route refuses the same request with:\n%s\nwatch output:\n%s",
				strings.TrimSpace(paged), out)
		}
		if err == nil {
			t.Fatalf("bd list --watch --offset 2 --max-rows 3 exited 0; want the cap to fire:\n%s", out)
		}
		if !strings.Contains(out, "too many rows") || !strings.Contains(out, "--max-rows=3") {
			t.Errorf("expected the cap to fire naming its source, got:\n%s", out)
		}
		// The count is the whole finding: four rows TOUCHED, not the two that
		// survive the skip. A route that counted survivors would report a
		// number at or below the cap, or not fire at all.
		if !strings.Contains(out, "4 found") {
			t.Errorf("expected the refusal to count the rows the query touched (4 found), got:\n%s", out)
		}
	})

	// The complement, and it is not optional: a route that refused every capped
	// request behind an offset would pass the case above, and an offset that
	// manufactures a refusal is the same bug from the other side. The window
	// here is 12 rows against a cap of 20, so nothing may fire.
	t.Run("keeps_watching_under_a_cap_the_window_fits_inside", func(t *testing.T) {
		stdout, stderr, reached := bdProxiedRunUntilStderr(t, bd, p.dir, "Watching for changes", 60*time.Second,
			"list", "--watch", "--label", scope, "--limit", "10", "--offset", "2", "--max-rows", "20")
		out := stdout + stderr
		if strings.Contains(out, "too many rows") {
			t.Fatalf("the cap fired on a 12-row window under --max-rows 20:\n%s", out)
		}
		if !reached {
			t.Fatalf("bd list --watch never reached its poll loop:\n%s", out)
		}
	})
}
