package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestRefScopeHostCanonicalizationAndIsolation(t *testing.T) {
	scope := NewRefScope("https://git.example.com:443/subpath/api/v1/", "Acme", "Widgets")

	tests := []struct {
		ref    string
		want   int
		wantOK bool
	}{
		{"https://git.example.com/subpath/acme/widgets/issues/42", 42, true},
		{"https://GIT.EXAMPLE.COM:443/subpath/ACME/Widgets/issues/42", 42, true},
		{"https://git.example.com/subpath/api/v1/repos/acme/widgets/issues/99", 99, true},
		{"gitea:acme/widgets#7", 7, true},
		{"gitea:15", 15, true},
		// Cross-repo / cross-host / non-issue URLs must be rejected.
		{"gitea:other/widgets#7", 0, false},
		{"https://git.example.com/subpath/acme/other/issues/42", 0, false},
		{"https://other.example.com/subpath/acme/widgets/issues/42", 0, false},
		{"https://git.example.com/subpath/acme/widgets/pulls/42", 0, false},
		{"https://git.example.com/acme/widgets/issues/42", 0, false},
	}

	for _, tt := range tests {
		got, ok := scope.IssueNumberFromRef(tt.ref)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("IssueNumberFromRef(%q) = (%d, %v), want (%d, %v)", tt.ref, got, ok, tt.want, tt.wantOK)
		}
	}

	// Default HTTP port :80 canonicalization.
	httpScope := NewRefScope("http://localhost:80", "o", "r")
	if n, ok := httpScope.IssueNumberFromRef("http://localhost/o/r/issues/3"); !ok || n != 3 {
		t.Fatalf("http :80 canonicalization failed: (%d, %v)", n, ok)
	}
}

func TestPushLinksIdempotentAndDryRun(t *testing.T) {
	var createdCalls int
	existing := map[int][]int{
		2: {1}, // #2 already blocked by #1
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/2/dependencies"):
			_, _ = w.Write([]byte(`[{"id":101,"number":1}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/3/dependencies"):
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/3/dependencies"):
			createdCalls++
			var body struct {
				Owner string `json:"owner"`
				Repo  string `json:"repo"`
				Index int    `json:"index"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Owner != "acme" || body.Repo != "widgets" || body.Index != 1 {
				t.Fatalf("unexpected dependency payload: %+v", body)
			}
			existing[3] = append(existing[3], body.Index)
			_, _ = fmt.Fprintf(w, `{"id":101,"number":%d}`, body.Index)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	resolver := NewLinkResolver(NewClient("tok", srv.URL, "acme", "widgets"))
	desired := []DependencyLink{
		{FromNumber: 2, ToNumber: 1, LinkType: giteaLinkBlockedBy, FromBeadsID: "bd-2", ToBeadsID: "bd-1"},
		{FromNumber: 3, ToNumber: 1, LinkType: giteaLinkBlockedBy, FromBeadsID: "bd-3", ToBeadsID: "bd-1"},
		{FromNumber: 3, ToNumber: 1, LinkType: giteaLinkBlockedBy, FromBeadsID: "bd-3", ToBeadsID: "bd-1"}, // duplicate
	}

	// Dry-run plans #3 -> #1 without POSTing.
	var planned []DependencyLink
	dryRes := resolver.PushLinks(context.Background(), desired, PushLinkOptions{
		DryRun: true,
		OnPlan: func(l DependencyLink) { planned = append(planned, l) },
	})
	if dryRes.Created != 1 || len(dryRes.Errors) != 0 || createdCalls != 0 || len(planned) != 1 {
		t.Fatalf("dry-run result = %+v, createdCalls=%d, planned=%+v", dryRes, createdCalls, planned)
	}

	// Live push creates only #3 -> #1.
	res := resolver.PushLinks(context.Background(), desired, PushLinkOptions{})
	if res.Created != 1 || len(res.Errors) != 0 || createdCalls != 1 {
		t.Fatalf("live result = %+v, createdCalls=%d", res, createdCalls)
	}
}

func TestPushLinksHandlesDisabledDependenciesAndMissingSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/acme/widgets/issues/2/dependencies":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"dependencies disabled"}`))
		case "/api/v1/repos/acme/widgets/issues/3/dependencies":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		case "/api/v1/repos/acme/widgets/issues/3":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"issue not found"}`))
		case "/api/v1/repos/acme/widgets/issues/4/dependencies":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"dependencies disabled"}`))
		case "/api/v1/repos/acme/widgets/issues/4":
			_, _ = w.Write([]byte(`{"id":40,"number":4,"title":"Exists"}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	resolver := NewLinkResolver(NewClient("tok", srv.URL, "acme", "widgets"))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{FromNumber: 2, ToNumber: 1, LinkType: giteaLinkBlockedBy},
		{FromNumber: 3, ToNumber: 1, LinkType: giteaLinkBlockedBy},
		{FromNumber: 4, ToNumber: 1, LinkType: giteaLinkBlockedBy},
	}, PushLinkOptions{})

	if res.DependenciesDisabledSkipped != 2 {
		t.Errorf("DependenciesDisabledSkipped = %d, want 2", res.DependenciesDisabledSkipped)
	}
	if res.SourceMissing != 1 {
		t.Errorf("SourceMissing = %d, want 1", res.SourceMissing)
	}
	if len(res.Errors) != 0 {
		t.Errorf("Errors = %v, want empty", res.Errors)
	}
}

func TestBlockedByLinkFromBeadsDependency(t *testing.T) {
	scope := NewRefScope("https://codeberg.org", "acme", "widgets")
	ref1 := "https://codeberg.org/acme/widgets/issues/1"
	ref2 := "https://codeberg.org/acme/widgets/issues/2"
	otherRef := "https://codeberg.org/other/repo/issues/9"

	link, ok := scope.BlockedByLinkFromBeadsDependency(
		&types.Issue{ID: "bd-2", ExternalRef: &ref2},
		&types.IssueWithDependencyMetadata{
			Issue:          types.Issue{ID: "bd-1", ExternalRef: &ref1},
			DependencyType: types.DepBlocks,
		},
	)
	if !ok || link.FromNumber != 2 || link.ToNumber != 1 {
		t.Fatalf("BlockedByLinkFromBeadsDependency = (%+v, %v)", link, ok)
	}

	if _, ok := scope.BlockedByLinkFromBeadsDependency(
		&types.Issue{ID: "bd-2", ExternalRef: &ref2},
		&types.IssueWithDependencyMetadata{
			Issue:          types.Issue{ID: "bd-9", ExternalRef: &otherRef},
			DependencyType: types.DepBlocks,
		},
	); ok {
		t.Fatal("cross-repo dependency should not produce a repo-scoped link")
	}
}
