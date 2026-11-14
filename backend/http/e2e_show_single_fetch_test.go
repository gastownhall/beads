//go:build cgo

package bdhttp_test

import (
	"slices"
	"strings"
	"testing"
)

// TestE2E_ShowFetchesTheIssueOnce (mc-5dkzp) pins the exact requests, path AND
// query, each bd show shape makes over the http backend. bd show resolves the
// id, fetches the row, then reads it for output: three reads of one URL that
// the per-command read memo answers with one request. A read that drifts to a
// URL of its own (a new parameter, or a default spelled out) still fits a
// request ceiling of two, so this is an exact list, not a budget: a change that
// makes show cheaper updates the list here too.
//
// Text show's dependents and comments sections are separate reads of the same
// row with include_dependents and include_comments, which the memo cannot
// collapse; a later change that reads them in one request updates that step.
func TestE2E_ShowFetchesTheIssueOnce(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)
	for _, tc := range []struct {
		name string
		mask func(map[string]any)
	}{
		{"current server", advertiseExternalPolicy},
		{"MC server mask", mcContextMask},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyAddr, counter := startMaskingProxy(t, addr, tc.mask)
			f := newHTTPFixture(t, bin, proxyAddr)
			target := f.create("once target", "-t", "task")
			other := f.create("once other", "-t", "task")
			f.mustRun(nil, "dep", "add", target, other)
			f.mustRun(nil, "comments", "add", target, "seed comment")

			get := func(id, query string) string {
				uri := "GET /v0/beads/issues/" + id
				if query != "" {
					uri += "?" + query
				}
				return uri
			}
			for _, step := range []struct {
				args []string
				want []string
			}{
				{[]string{"show", target, "--json"}, []string{get(target, "")}},
				{[]string{"show", "--json", "--id=" + target}, []string{get(target, "")}},
				{[]string{"show", target, "--short"}, []string{get(target, "")}},
				{[]string{"show", target, other, "--json"}, []string{get(target, ""), get(other, "")}},
				{[]string{"show", target}, []string{
					get(target, ""),
					get(target, "include_dependents=true"),
					get(target, "include_comments=true"),
				}},
			} {
				counter.reset()
				f.mustRun(nil, step.args...)
				if got := counter.uriSnapshot(); !slices.Equal(got, step.want) {
					t.Errorf("bd %s:\n got  %q\n want %q", strings.Join(step.args, " "), got, step.want)
				}
			}

			// BD_HTTP_READ_MEMO=0 turns the memo off: the same show asks
			// the server again for every read of the row.
			counter.reset()
			f.mustRun([]string{"BD_HTTP_READ_MEMO=0"}, "show", target, "--json")
			got := counter.uriSnapshot()
			if bare := countOf(got, get(target, "")); bare < 2 {
				t.Errorf("BD_HTTP_READ_MEMO=0 bd show %s --json read the row %d times (%q); the kill switch must turn the memo off", target, bare, got)
			}
		})
	}
}

func countOf(list []string, want string) int {
	n := 0
	for _, s := range list {
		if s == want {
			n++
		}
	}
	return n
}
