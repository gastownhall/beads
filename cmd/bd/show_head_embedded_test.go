//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// Direct-route half of the --head route tests; fixtures and the proxied
// half are in show_head_proxied_integration_test.go.

// TestEmbeddedShowHead drives the direct route (show.go) through the built
// binary.
func TestEmbeddedShowHead(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hd")
	issue := bdCreate(t, bd, dir, showHeadCreateArgs()...)
	for _, text := range showHeadComments {
		if out, err := bdRunWithFlockRetry(t, bd, dir, "comments", "add", issue.ID, text); err != nil {
			t.Fatalf("bd comments add %q failed: %v\n%s", text, err, out)
		}
	}

	t.Run("direct_head", func(t *testing.T) {
		assertShowHead(t, "direct", bdShowRaw(t, bd, dir, issue.ID, "--head"), issue.ID, 3)
	})

	t.Run("direct_head_with_comments_tail", func(t *testing.T) {
		assertShowHead(t, "direct --comments-tail 1", bdShowRaw(t, bd, dir, issue.ID, "--head", "--comments-tail", "1"), issue.ID, 1)
		assertShowHead(t, "direct --comments-tail 0", bdShowRaw(t, bd, dir, issue.ID, "--head", "--comments-tail", "0"), issue.ID, 0)
	})

	t.Run("direct_full_view_untouched", func(t *testing.T) {
		assertShowHeadOff(t, "direct", bdShowRaw(t, bd, dir, issue.ID))
	})

	t.Run("direct_short_issue_withholds_nothing", func(t *testing.T) {
		short := bdCreate(t, bd, dir, "Short one", "--type", "task", "--notes", "one line of state")
		out := bdShowRaw(t, bd, dir, short.ID, "--head")
		if !strings.Contains(out, "… head view: nothing withheld") || !strings.Contains(out, "one line of state") {
			t.Errorf("a short issue did not render whole with the nothing-withheld line:\n%s", out)
		}
	})

	t.Run("direct_refuses_other_views", func(t *testing.T) {
		for _, other := range showHeadRefused {
			out := bdShowFail2(t, bd, dir, issue.ID, "--head", other)
			if !strings.Contains(out, "--head") || !strings.Contains(out, showHeadRefusal(other)) {
				t.Errorf("--head %s did not fail as a usage error:\n%s", other, out)
			}
		}
	})
}
