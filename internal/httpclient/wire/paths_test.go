// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/paths_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestIssuePathEscapesTheIDIntoOneSegment(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want string
	}{
		{"ordinary id", "ga-93nh1", "/v0/beads/issues/ga-93nh1"},
		// A slash would otherwise split the id across two segments and address
		// a route that does not exist.
		{"slash", "a/b", "/v0/beads/issues/a%2Fb"},
		// The one PathEscape leaves alone: a colon inside the id is
		// indistinguishable from a custom-method suffix once it is on the wire.
		{"colon", "ga-1:claim", "/v0/beads/issues/ga-1%3Aclaim"},
		{"space and unicode", "a bé", "/v0/beads/issues/a%20b%C3%A9"},
		{"newline", "a\nb", "/v0/beads/issues/a%0Ab"},
		{"percent", "50%", "/v0/beads/issues/50%25"},
		// path.Join resolves these, so as a whole segment they must travel
		// escaped or they climb out of the collection.
		{"dot", ".", "/v0/beads/issues/%2E"},
		{"dotdot", "..", "/v0/beads/issues/%2E%2E"},
		{"dot inside an id is ordinary", "ga-1.2", "/v0/beads/issues/ga-1.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IssuePath(tc.id)
			if err != nil {
				t.Fatalf("IssuePath(%q): %v", tc.id, err)
			}
			if got != tc.want {
				t.Errorf("IssuePath(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

func TestEscapedSegmentsRoundTripBackToTheID(t *testing.T) {
	// The escaping is only correct if the server decodes it to the id we meant.
	for _, id := range []string{"ga-93nh1", "a/b", "ga-1:claim", "a bé", "..", ".", "50%", "a\nb"} {
		path, err := IssuePath(id)
		if err != nil {
			t.Fatalf("IssuePath(%q): %v", id, err)
		}
		seg := path[strings.LastIndex(path, "/")+1:]
		got, err := url.PathUnescape(seg)
		if err != nil {
			t.Fatalf("unescape %q: %v", seg, err)
		}
		if got != id {
			t.Errorf("segment for %q decodes to %q", id, got)
		}
	}
}

func TestEmptySegmentsAreRefusedRatherThanJoined(t *testing.T) {
	// An empty segment would collapse a single-resource read into a read of the
	// whole collection, which is the same request with a very different answer.
	if _, err := IssuePath(""); err == nil {
		t.Error("IssuePath(\"\") returned no error")
	}
	if _, err := SettingPath(""); err == nil {
		t.Error("SettingPath(\"\") returned no error")
	}
	if _, err := MemoryPath(""); err == nil {
		t.Error("MemoryPath(\"\") returned no error")
	}
	if _, err := IssueMethodPath("", MethodClaim); err == nil {
		t.Error("IssueMethodPath(\"\", :claim) returned no error")
	}
	if _, err := IssueCommentsPath(""); err == nil {
		t.Error("IssueCommentsPath(\"\") returned no error")
	}
	if _, err := IssueRelatedPath(""); err == nil {
		t.Error("IssueRelatedPath(\"\") returned no error")
	}
}

// TestIssueSubresourcePathsEscapeTheAnchorIntoOneSegment covers the two paths
// whose collection name comes AFTER the id, which is the shape no other path on
// this surface has.
//
// The literal suffix is what makes the escaping load-bearing in a second way: an
// unescaped slash in the id would not merely address a missing route, it would
// make `/comments` land under a DIFFERENT anchor than the caller named — and the
// server's wildcard matches one segment, so the request would 404 rather than
// write to the wrong thread. Both failure modes are closed by the same escape.
func TestIssueSubresourcePathsEscapeTheAnchorIntoOneSegment(t *testing.T) {
	for _, tc := range []struct {
		name          string
		id            string
		comments, rel string
	}{
		{"ordinary id", "ga-93nh1", "/v0/beads/issues/ga-93nh1/comments", "/v0/beads/issues/ga-93nh1/related"},
		{"slash", "a/b", "/v0/beads/issues/a%2Fb/comments", "/v0/beads/issues/a%2Fb/related"},
		{"colon", "ga-1:claim", "/v0/beads/issues/ga-1%3Aclaim/comments", "/v0/beads/issues/ga-1%3Aclaim/related"},
		{"dotdot", "..", "/v0/beads/issues/%2E%2E/comments", "/v0/beads/issues/%2E%2E/related"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IssueCommentsPath(tc.id)
			if err != nil {
				t.Fatalf("IssueCommentsPath(%q): %v", tc.id, err)
			}
			if got != tc.comments {
				t.Errorf("IssueCommentsPath(%q) = %q, want %q", tc.id, got, tc.comments)
			}
			got, err = IssueRelatedPath(tc.id)
			if err != nil {
				t.Fatalf("IssueRelatedPath(%q): %v", tc.id, err)
			}
			if got != tc.rel {
				t.Errorf("IssueRelatedPath(%q) = %q, want %q", tc.id, got, tc.rel)
			}
		})
	}
}

func TestIssueMethodPathBuildsTheSuffixIntoTheFinalSegment(t *testing.T) {
	cases := []struct {
		method string
		want   string
	}{
		{MethodClaim, "/v0/beads/issues/ga-1:claim"},
		{MethodClose, "/v0/beads/issues/ga-1:close"},
		{MethodReopen, "/v0/beads/issues/ga-1:reopen"},
		{MethodRelease, "/v0/beads/issues/ga-1:release"},
	}
	for _, tc := range cases {
		got, err := IssueMethodPath("ga-1", tc.method)
		if err != nil {
			t.Fatalf("IssueMethodPath(ga-1, %s): %v", tc.method, err)
		}
		if got != tc.want {
			t.Errorf("IssueMethodPath(ga-1, %s) = %q, want %q", tc.method, got, tc.want)
		}
	}
}

func TestIssueMethodPathKeepsTheSuffixUnambiguous(t *testing.T) {
	// The server splits the custom method off the segment its wildcard matched,
	// so exactly one literal colon may survive: the suffix's.
	got, err := IssueMethodPath("ga-1:close", MethodClaim)
	if err != nil {
		t.Fatalf("IssueMethodPath: %v", err)
	}
	if got != "/v0/beads/issues/ga-1%3Aclose:claim" {
		t.Fatalf("got %q", got)
	}
	if strings.Count(got[strings.LastIndex(got, "/"):], ":") != 1 {
		t.Errorf("more than one literal colon in the final segment: %q", got)
	}
}

func TestIssueMethodPathRefusesAnUnpublishedCustomMethod(t *testing.T) {
	if _, err := IssueMethodPath("ga-1", ":delete"); err == nil {
		t.Error("an unpublished custom method returned no error")
	}
}

func TestCollectionPathsMatchTheDocument(t *testing.T) {
	// The dependency and memory operations are collection-level: their custom
	// methods are literal segments, not suffixes on a wildcard.
	want := map[string]string{
		PathDependencies:         "/v0/beads/dependencies",
		PathDependenciesBlocking: "/v0/beads/dependencies/blocking",
		PathDependenciesTree:     "/v0/beads/dependencies/tree",
		PathDependenciesCycles:   "/v0/beads/dependencies/cycles",
		PathDependenciesAdd:      "/v0/beads/dependencies:add",
		PathDependenciesRemove:   "/v0/beads/dependencies:remove",
		PathMemories:             "/v0/beads/memories",
		PathIssuesSweep:          "/v0/beads/issues:sweep",
		PathIssuesDelete:         "/v0/beads/issues:delete",
		PathIssuesBatchCreate:    "/v0/beads/issues:batchCreate",
		PathIssuesQuery:          "/v0/beads/issues:query",
		PathReadyCount:           "/v0/beads/ready:count",
		PathIssuesCount:          "/v0/beads/issues:count",
		PathDependenciesCount:    "/v0/beads/dependencies:count",
	}
	for got, expected := range want {
		if got != expected {
			t.Errorf("path constant = %q, want %q", got, expected)
		}
	}
}

func TestMemoryAndSettingKeysAreEscapedIntoOneSegment(t *testing.T) {
	// Memory keys are the widest segment on this surface: the document allows
	// spaces, dots and unicode.
	got, err := MemoryPath("notes/2026 planning.md")
	if err != nil {
		t.Fatalf("MemoryPath: %v", err)
	}
	if got != "/v0/beads/memories/notes%2F2026%20planning.md" {
		t.Errorf("MemoryPath = %q", got)
	}
	got, err = SettingPath("types.infra")
	if err != nil {
		t.Fatalf("SettingPath: %v", err)
	}
	if got != "/v0/beads/config/types.infra" {
		t.Errorf("SettingPath = %q", got)
	}
}

func TestTheURLTheServerSeesKeepsTheEscapingAndTheMountRoot(t *testing.T) {
	// The end-to-end statement the escaping exists to make: whatever the id, the
	// server's router sees one segment and decodes it back to the id, under
	// whatever prefix the base URL mounts the surface at.
	var gotPath, gotRaw, gotID string
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRaw = r.URL.EscapedPath(), r.URL.RawQuery
		gotID = strings.TrimPrefix(r.URL.Path, "/mount/v0/beads/issues/")
		_, _ = w.Write([]byte(`{}`))
	})
	mounted := c.BaseURL().JoinPath("/mount/")
	c, err := New(mounted, nil, Options{HTTPClient: c.hc})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	path, err := IssueMethodPath("a/b:x", MethodClaim)
	if err != nil {
		t.Fatalf("IssueMethodPath: %v", err)
	}
	var out map[string]any
	if err := c.Do(ctx(t), Request{
		Op:     OpClaimIssue,
		Method: http.MethodPost,
		Path:   path,
		Query:  url.Values{"actor": {"me"}},
	}, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if want := "/mount/v0/beads/issues/a%2Fb%3Ax:claim"; gotPath != want {
		t.Errorf("escaped path = %q, want %q", gotPath, want)
	}
	if gotID != "a/b:x:claim" {
		t.Errorf("decoded final segment = %q, want %q", gotID, "a/b:x:claim")
	}
	if gotRaw != "actor=me" {
		t.Errorf("query = %q, want actor=me", gotRaw)
	}
}
