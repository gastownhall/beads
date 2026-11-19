package httpclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// newDetailStubStore serves one canned getIssue detail over the real wire
// client, so a test can answer as a server of any version would.
func newDetailStubStore(t *testing.T, details types.IssueDetails) *Store {
	t.Helper()
	if details.Revision == "" {
		details.Revision = types.RevisionToken(1)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == wire.PathContext:
			ctx := v0Context("proj-ude")
			ctx.Capabilities = []string{"issues.get"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ctx)
		case strings.HasPrefix(r.URL.Path, wire.PathIssues+"/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(details)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := wire.New(base, nil, wire.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return New(Target{BaseURL: base}, client, nil)
}

// AC-7 (bd-uschhs, #5850): the http combined read answers from the detail's
// unresolvable_dependency_edges. A pre-change server publishes a positive
// unresolvable_dependencies with no edge member: the exact read refuses with
// a typed *ErrUnsupported rather than answer a falsely complete empty list,
// while the detail itself keeps the count-only disclosure and names no ID.
func TestHTTPGetDependenciesWithUnresolvable(t *testing.T) {
	local := &types.IssueWithDependencyMetadata{Issue: types.Issue{ID: "ude-dep", Title: "local"}, DependencyType: types.DepBlocks}
	record := &types.Dependency{IssueID: "ude-root", DependsOnID: "external:liveop:liveop-kmf", Type: types.DepTracks}
	one := int64(1)

	t.Run("current server names its records", func(t *testing.T) {
		s := newDetailStubStore(t, types.IssueDetails{
			Issue:                       types.Issue{ID: "ude-root"},
			Dependencies:                []*types.IssueWithDependencyMetadata{local},
			UnresolvableDependencies:    &one,
			UnresolvableDependencyEdges: []*types.Dependency{record},
		})
		deps, unresolvable, err := s.GetDependenciesWithUnresolvable(t.Context(), "ude-root")
		if err != nil {
			t.Fatalf("GetDependenciesWithUnresolvable: %v", err)
		}
		if len(deps) != 1 || deps[0].ID != "ude-dep" {
			t.Errorf("resolved = %+v, want only ude-dep", deps)
		}
		if len(unresolvable) != 1 || unresolvable[0].DependsOnID != record.DependsOnID || unresolvable[0].Type != record.Type {
			t.Errorf("unresolvable = %+v, want the one served record", unresolvable)
		}
	})

	t.Run("fully local detail is complete", func(t *testing.T) {
		s := newDetailStubStore(t, types.IssueDetails{
			Issue:        types.Issue{ID: "ude-root"},
			Dependencies: []*types.IssueWithDependencyMetadata{local},
		})
		deps, unresolvable, err := s.GetDependenciesWithUnresolvable(t.Context(), "ude-root")
		if err != nil || len(deps) != 1 || len(unresolvable) != 0 {
			t.Errorf("got (%+v, %+v, %v), want (one local row, none, nil)", deps, unresolvable, err)
		}
	})

	t.Run("pre-change server count without edges", func(t *testing.T) {
		s := newDetailStubStore(t, types.IssueDetails{
			Issue:                    types.Issue{ID: "ude-root"},
			Dependencies:             []*types.IssueWithDependencyMetadata{local},
			UnresolvableDependencies: &one,
		})
		deps, unresolvable, err := s.GetDependenciesWithUnresolvable(t.Context(), "ude-root")
		var unsupported *issueops.ErrUnsupported
		if !errors.As(err, &unsupported) {
			t.Fatalf("got (%+v, %+v, %v), want a typed *ErrUnsupported", deps, unresolvable, err)
		}
		if deps != nil || unresolvable != nil {
			t.Errorf("a refusal carried data: (%+v, %+v)", deps, unresolvable)
		}

		rd, err := s.IssueReader()
		if err != nil {
			t.Fatalf("IssueReader: %v", err)
		}
		details, err := rd.Get(t.Context(), issueops.GetRequest{ID: "ude-root"})
		if err != nil {
			t.Fatalf("Reader.Get: %v", err)
		}
		if details.UnresolvableDependencies == nil || *details.UnresolvableDependencies != 1 {
			t.Errorf("unresolvable_dependencies = %v, want the served count-only 1", details.UnresolvableDependencies)
		}
		if details.UnresolvableDependencyEdges != nil {
			t.Errorf("unresolvable_dependency_edges = %+v, want none invented", details.UnresolvableDependencyEdges)
		}
	})
}
