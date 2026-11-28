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
	dep := bdCreate(t, bd, dir, showHeadDepArgs()...)
	bdDepAdd(t, bd, dir, issue.ID, dep.ID)

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

	t.Run("direct_head_json", func(t *testing.T) {
		assertShowHeadJSON(t, "direct", bdShowRaw(t, bd, dir, issue.ID, "--head", "--json"), dep.ID, 3)
	})

	t.Run("direct_head_json_with_comments_tail", func(t *testing.T) {
		assertShowHeadJSON(t, "direct --comments-tail 1", bdShowRaw(t, bd, dir, issue.ID, "--head", "--json", "--comments-tail", "1"), dep.ID, 1)
		assertShowHeadJSON(t, "direct --comments-tail 0", bdShowRaw(t, bd, dir, issue.ID, "--head", "--json", "--comments-tail", "0"), dep.ID, 0)
	})

	t.Run("direct_full_json_untouched", func(t *testing.T) {
		assertShowHeadJSONOff(t, "direct", bdShowDetails(t, bd, dir, issue.ID))
	})

	t.Run("direct_head_json_one_object_per_issue", func(t *testing.T) {
		docs := showHeadJSON(t, "direct", bdShowRaw(t, bd, dir, issue.ID, dep.ID, "--head", "--json"))
		if len(docs) != 2 {
			t.Fatalf("%d issues in the view, want 2", len(docs))
		}
		// The dependency is a short issue: its description is withheld and
		// nothing else is.
		withheld := showHeadWithheld(t, "direct", docs[1])
		if docs[1]["id"] != dep.ID || withheld["description_bytes"] != float64(len(showHeadDepBody)) || withheld["notes_bytes"] != float64(0) || withheld["comments"] != float64(0) {
			t.Errorf("the second issue's head view: id=%v withheld=%v", docs[1]["id"], withheld)
		}
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
