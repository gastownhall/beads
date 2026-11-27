package gitea

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/tracker/conformance"
	"github.com/steveyegge/beads/internal/types"
)

type endpointTestStore struct {
	tracker.Store
	endpoint string
}

func (s endpointTestStore) GetConfig(_ context.Context, key string) (string, error) {
	switch key {
	case "gitea.url":
		return s.endpoint, nil
	case "gitea.repo":
		return "acme/widgets", nil
	default:
		return "", nil
	}
}

func (s endpointTestStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s endpointTestStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return nil, nil
}

func TestRegistered(t *testing.T) {
	factory := tracker.Get("gitea")
	if factory == nil {
		t.Fatal("gitea tracker not registered")
	}
	tr := factory()
	if tr.Name() != "gitea" || tr.DisplayName() != "Gitea" || tr.ConfigPrefix() != "gitea" {
		t.Fatalf("unexpected metadata: Name=%q DisplayName=%q ConfigPrefix=%q", tr.Name(), tr.DisplayName(), tr.ConfigPrefix())
	}
}

func TestInitRejectsChangedOrUnpinnedEndpointBeforeRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GITEA_URL", "")
	t.Setenv("GITEA_TOKEN", "gitea-secret")
	t.Setenv("GITEA_REPO", "acme/widgets")
	t.Setenv("BEADS_DIR", "")
	work := t.TempDir()
	beadsDir := filepath.Join(work, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}

	var requestsA, requestsB int
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestsA++
		_, _ = w.Write([]byte(`{"id":10,"name":"widgets","full_name":"acme/widgets"}`))
	}))
	defer serverA.Close()
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestsB++
		w.WriteHeader(http.StatusOK)
	}))
	defer serverB.Close()

	// Unpinned shared first use must fail before hitting serverA.
	err := (&Tracker{}).Init(context.Background(), endpointTestStore{endpoint: serverA.URL})
	if err == nil || !strings.Contains(err.Error(), "credential was not sent") {
		t.Fatalf("unpinned shared first use error = %v", err)
	}
	if requestsA != 0 {
		t.Fatalf("unpinned shared endpoint received %d requests, want 0", requestsA)
	}

	// Private env confirmation with trailing /api/v1/ must match unstripped raw URL.
	t.Setenv("GITEA_URL", serverA.URL+"/api/v1/")
	if err := (&Tracker{}).Init(context.Background(), endpointTestStore{endpoint: serverA.URL + "/api/v1"}); err != nil {
		t.Fatalf("private confirmed Init with /api/v1: %v", err)
	}
	if requestsA == 0 {
		t.Fatal("confirmed Init made 0 requests to serverA, want >= 1")
	}

	// Changed shared endpoint must fail before sending credentials.
	err = (&Tracker{}).Init(context.Background(), endpointTestStore{endpoint: serverB.URL})
	if err == nil || !strings.Contains(err.Error(), "credential was not sent") {
		t.Fatalf("changed endpoint Init error = %v", err)
	}
	if requestsB != 0 {
		t.Fatalf("changed endpoint received %d requests, want 0", requestsB)
	}
}

func TestGetConfigYamlOnlyBypassesStore(t *testing.T) {
	tr := &Tracker{store: nil}
	t.Setenv("GITEA_TOKEN", "env-gitea-token")
	if got := tr.getConfig(context.Background(), "gitea.token", "GITEA_TOKEN"); got != "env-gitea-token" {
		t.Fatalf("getConfig(gitea.token) = %q, want env-gitea-token", got)
	}
}

func TestPushTargetScopeCanonicalizesDefaultPorts(t *testing.T) {
	tr1 := &Tracker{client: NewClient("tok", "HTTPS://Forgejo.Example:443/git/api/v1/", "Acme", "Widgets")}
	tr2 := &Tracker{client: NewClient("tok", "https://forgejo.example/git", "acme", "widgets")}
	if tr1.PushTargetScope() != tr2.PushTargetScope() {
		t.Fatalf("PushTargetScope mismatch: %q != %q", tr1.PushTargetScope(), tr2.PushTargetScope())
	}
	if want := "https://forgejo.example/git/repos/acme/widgets"; tr1.PushTargetScope() != want {
		t.Fatalf("PushTargetScope = %q, want %q", tr1.PushTargetScope(), want)
	}
}

func TestMatchKeyerAndIdentityKeyerAndExternalRef(t *testing.T) {
	tr := &Tracker{
		client: NewClient("tok", "https://codeberg.org:443", "acme", "widgets"),
		repoID: 99,
	}

	if !tr.IsExternalRef("https://codeberg.org/acme/widgets/issues/42") {
		t.Fatal("expected IsExternalRef true for configured host URL")
	}
	if tr.IsExternalRef("https://github.com/acme/widgets/issues/42") {
		t.Fatal("expected IsExternalRef false for foreign host")
	}
	if !tr.IsExternalRef("gitea:acme/widgets#42") || !tr.IsExternalRef("gitea:42") {
		t.Fatal("expected IsExternalRef true for gitea shorthand")
	}

	if got := tr.ExtractIdentifier("https://codeberg.org/acme/other/issues/42"); got != "" {
		t.Fatalf("cross-repo ExtractIdentifier = %q, want empty", got)
	}
	if got := tr.ExtractIdentifier("https://codeberg.org/acme/widgets/issues/42"); got != "42" {
		t.Fatalf("same-repo ExtractIdentifier = %q, want 42", got)
	}

	if k1, k2 := tr.MatchKey("gitea:42"), tr.MatchKey("https://codeberg.org/acme/widgets/issues/42"); k1 != k2 || k1 == "" {
		t.Fatalf("MatchKey mismatch: %q vs %q", k1, k2)
	}

	ref := "https://codeberg.org/acme/widgets/issues/42"
	local := &types.Issue{ID: "bd-42", ExternalRef: &ref, SourceSystem: "gitea:99:42"}
	remote := &tracker.TrackerIssue{Raw: &Issue{Number: 42, Repository: &RepositoryMeta{ID: 99}}}
	if lk, rk := tr.LocalIdentityKey(local), tr.RemoteIdentityKey(remote); lk != "gitea:99:42" || lk != rk {
		t.Fatalf("IdentityKey mismatch: local=%q remote=%q", lk, rk)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestFetchIssueExternalRefsAndExclusions(t *testing.T) {
	var fetchedPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchedPaths = append(fetchedPaths, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/repos/acme/widgets/issues/1":
			_, _ = w.Write([]byte(`{"id":101,"number":1,"title":"Real issue","state":"open","html_url":"http://127.0.0.1/acme/widgets/issues/1"}`))
		case "/api/v1/repos/acme/widgets/issues/1/dependencies":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v1/repos/acme/widgets/issues/2":
			_, _ = w.Write([]byte(`{"id":102,"number":2,"title":"PR item","state":"open","pull_request":{"merged":false},"html_url":"http://127.0.0.1/acme/widgets/pulls/2"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tr := &Tracker{
		client: NewClient("tok", srv.URL, "acme", "widgets"),
		config: DefaultMappingConfig(),
	}

	// Accepted forms: bare number, whitespace-trimmed, gitea:N, gitea:owner/repo#N, full HTML URL, API URL.
	for _, ref := range []string{
		"1",
		"  1 ",
		"gitea:1",
		"gitea:acme/widgets#1",
		"gitea:ACME/Widgets#1",
		srv.URL + "/acme/widgets/issues/1",
		srv.URL + "/api/v1/repos/acme/widgets/issues/1",
	} {
		got, err := tr.FetchIssue(context.Background(), ref)
		if err != nil {
			t.Fatalf("FetchIssue(%q) unexpected error: %v", ref, err)
		}
		if got == nil || got.Identifier != "1" || got.Title != "Real issue" {
			t.Fatalf("FetchIssue(%q) = %+v, want issue #1", ref, got)
		}
	}

	// Pull request returned from /issues/{n} is excluded (returns nil, nil) across all forms.
	for _, prRef := range []string{
		"2",
		"gitea:2",
		"gitea:acme/widgets#2",
		srv.URL + "/acme/widgets/issues/2",
	} {
		got, err := tr.FetchIssue(context.Background(), prRef)
		if err != nil {
			t.Fatalf("FetchIssue(%q) for PR unexpected error: %v", prRef, err)
		}
		if got != nil {
			t.Fatalf("FetchIssue(%q) for PR = %+v, want nil", prRef, got)
		}
	}

	// Rejected forms must fail without hitting the remote server.
	beforeCount := len(fetchedPaths)
	for _, badRef := range []string{
		"",
		"0",
		"-5",
		"not-a-number",
		srv.URL + "/other/repo/issues/1",
		"gitea:other/repo#1",
		"https://evil.example/acme/widgets/issues/1",
		srv.URL + "/acme/widgets/pulls/2",
	} {
		got, err := tr.FetchIssue(context.Background(), badRef)
		if err == nil {
			t.Fatalf("FetchIssue(%q) = %+v, want error", badRef, got)
		}
	}
	if len(fetchedPaths) != beforeCount {
		t.Fatalf("rejected refs issued %d HTTP requests, want 0 (%v)", len(fetchedPaths)-beforeCount, fetchedPaths[beforeCount:])
	}

	// Canonical :443 HTTPS URL resolution with subpath.
	canonicalClient := NewClient("tok", "https://forgejo.example/subpath", "acme", "widgets").WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/subpath/api/v1/repos/acme/widgets/issues/7" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"id":107,"number":7,"title":"Canonical 443","state":"open","html_url":"https://forgejo.example/subpath/acme/widgets/issues/7"}`)),
					Header:     make(http.Header),
				}, nil
			}
			if r.URL.Path == "/subpath/api/v1/repos/acme/widgets/issues/7/dependencies" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`[]`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})
	canonicalTracker := &Tracker{client: canonicalClient, config: DefaultMappingConfig()}
	got, err := canonicalTracker.FetchIssue(context.Background(), "https://forgejo.example:443/subpath/acme/widgets/issues/7")
	if err != nil || got == nil || got.Identifier != "7" {
		t.Fatalf("FetchIssue(:443 canonical URL) = (%+v, %v), want issue #7", got, err)
	}
	if _, err := canonicalTracker.FetchIssue(context.Background(), "https://forgejo.example:443/subpath/other/repo/issues/7"); err == nil {
		t.Fatal("expected error for :443 cross-repo URL")
	}

	// Uninitialized tracker returns a clear error instead of panicking.
	if _, err := (&Tracker{}).FetchIssue(context.Background(), "1"); err == nil {
		t.Fatal("expected error from uninitialized Tracker.FetchIssue")
	}
}

func TestResolveOwnerRepoPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		repoSpec  string
		ownerCfg  string
		project   string
		wantOwner string
		wantRepo  string
	}{
		{
			name:      "repo owner/repo overrides conflicting gitea.owner",
			repoSpec:  "acme/widgets",
			ownerCfg:  "other-owner",
			project:   "unused",
			wantOwner: "acme",
			wantRepo:  "widgets",
		},
		{
			name:      "repo without slash falls back to gitea.owner",
			repoSpec:  "widgets",
			ownerCfg:  "acme",
			project:   "ignored",
			wantOwner: "acme",
			wantRepo:  "widgets",
		},
		{
			name:      "empty repo uses gitea.owner and gitea.project",
			repoSpec:  "",
			ownerCfg:  "acme",
			project:   "widgets",
			wantOwner: "acme",
			wantRepo:  "widgets",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOwner, gotRepo := ResolveOwnerRepo(tt.repoSpec, tt.ownerCfg, tt.project)
			if gotOwner != tt.wantOwner || gotRepo != tt.wantRepo {
				t.Fatalf("ResolveOwnerRepo(%q, %q, %q) = (%q, %q), want (%q, %q)",
					tt.repoSpec, tt.ownerCfg, tt.project, gotOwner, gotRepo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

func TestCreateIssueClosedOnCreateWarning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/labels":
			_, _ = w.Write([]byte(`[{"id":1,"name":"type::task"},{"id":2,"name":"priority::medium"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/acme/widgets/issues":
			_, _ = w.Write([]byte(`{"id":100,"number":7,"title":"Closed item","state":"open","html_url":"https://gitea.test/acme/widgets/issues/7"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/repos/acme/widgets/issues/7":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"cannot close"}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	tr := &Tracker{
		client: NewClient("tok", srv.URL, "acme", "widgets"),
		config: DefaultMappingConfig(),
	}
	ti, err := tr.CreateIssue(context.Background(), &types.Issue{
		Title:     "Closed item",
		Status:    types.StatusClosed,
		IssueType: types.TypeTask,
		Priority:  2,
	})
	if err != nil {
		t.Fatalf("CreateIssue should return partial success warning, got error: %v", err)
	}
	if ti.Identifier != "7" || len(ti.Warnings) != 1 || !strings.Contains(ti.Warnings[0], "failed to close") {
		t.Fatalf("unexpected TrackerIssue: %+v", ti)
	}
}

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T, f *conformance.Fixture) conformance.Setup {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Setenv("GITEA_TOKEN", "conformance-token")
		t.Setenv("GITEA_REPO", "acme/widgets")

		var mu sync.Mutex
		nextNum := 3
		nextLabelID := int64(10)
		labels := map[string]Label{
			"bug": {ID: 1, Name: "bug", Color: "#d73a4a"},
		}
		var baseURL string
		t1 := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)

		issues := map[int]*Issue{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()

			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets":
				_, _ = w.Write([]byte(`{"id":99,"name":"widgets","full_name":"acme/widgets"}`))
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/labels":
				out := make([]Label, 0, len(labels))
				for _, l := range labels {
					out = append(out, l)
				}
				_ = json.NewEncoder(w).Encode(out)
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/acme/widgets/labels":
				var req struct {
					Name  string `json:"name"`
					Color string `json:"color"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				nextLabelID++
				lbl := Label{ID: nextLabelID, Name: req.Name, Color: req.Color}
				labels[strings.ToLower(req.Name)] = lbl
				_ = json.NewEncoder(w).Encode(lbl)
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/issues":
				q := r.URL.Query()
				if q.Get("type") != "issues" {
					t.Errorf("GET /issues type = %q, want issues", q.Get("type"))
				}
				if p := q.Get("page"); p != "" && p != "1" {
					_, _ = w.Write([]byte(`[]`))
					return
				}
				stateFilter := q.Get("state")
				var sinceFilter time.Time
				if sinceRaw := q.Get("since"); sinceRaw != "" {
					parsed, parseErr := time.Parse(time.RFC3339, sinceRaw)
					if parseErr != nil {
						t.Errorf("GET /issues invalid since %q: %v", sinceRaw, parseErr)
					} else {
						sinceFilter = parsed
					}
				}
				out := make([]*Issue, 0, len(issues))
				for _, iss := range issues {
					if stateFilter != "" && stateFilter != "all" && !strings.EqualFold(iss.State, stateFilter) {
						continue
					}
					if !sinceFilter.IsZero() && iss.UpdatedAt != nil && iss.UpdatedAt.Before(sinceFilter) {
						continue
					}
					out = append(out, iss)
				}
				_ = json.NewEncoder(w).Encode(out)
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/acme/widgets/issues":
				var req struct {
					Title  string  `json:"title"`
					Body   string  `json:"body"`
					Labels []int64 `json:"labels"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				num := nextNum
				nextNum++
				now := time.Now().UTC()
				iss := &Issue{
					ID:         int64(1000 + num),
					Number:     num,
					Title:      req.Title,
					Body:       req.Body,
					State:      "open",
					CreatedAt:  &now,
					UpdatedAt:  &now,
					HTMLURL:    fmt.Sprintf("%s/acme/widgets/issues/%d", baseURL, num),
					Repository: &RepositoryMeta{ID: 99, Owner: "acme", Name: "widgets", FullName: "acme/widgets"},
				}
				issues[num] = iss
				_ = json.NewEncoder(w).Encode(iss)
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dependencies"):
				trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/acme/widgets/issues/")
				numStr := strings.TrimSuffix(trimmed, "/dependencies")
				n, _ := strconv.Atoi(numStr)
				if n == 2 {
					_ = json.NewEncoder(w).Encode([]*Issue{issues[1]})
					return
				}
				_, _ = w.Write([]byte(`[]`))
			case strings.HasPrefix(r.URL.Path, "/api/v1/repos/acme/widgets/issues/") && strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodPut:
				trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/acme/widgets/issues/")
				numStr := strings.TrimSuffix(trimmed, "/labels")
				n, _ := strconv.Atoi(numStr)
				var req struct {
					Labels []int64 `json:"labels"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if iss := issues[n]; iss != nil {
					var updatedLabels []Label
					for _, id := range req.Labels {
						for _, lbl := range labels {
							if lbl.ID == id {
								updatedLabels = append(updatedLabels, lbl)
							}
						}
					}
					iss.Labels = updatedLabels
				}
				_, _ = w.Write([]byte(`[]`))
			case strings.HasPrefix(r.URL.Path, "/api/v1/repos/acme/widgets/issues/"):
				numStr := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/acme/widgets/issues/")
				n, _ := strconv.Atoi(numStr)
				iss := issues[n]
				if iss == nil {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method == http.MethodPatch {
					var req map[string]interface{}
					_ = json.NewDecoder(r.Body).Decode(&req)
					if title, ok := req["title"].(string); ok {
						iss.Title = title
					}
					if body, ok := req["body"].(string); ok {
						iss.Body = body
					}
					if st, ok := req["state"].(string); ok {
						iss.State = st
					}
					now := time.Now().UTC()
					iss.UpdatedAt = &now
				}
				_ = json.NewEncoder(w).Encode(iss)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)
		baseURL = srv.URL
		t.Setenv("GITEA_URL", srv.URL)

		ref1 := fmt.Sprintf("%s/acme/widgets/issues/1", srv.URL)
		ref2 := fmt.Sprintf("%s/acme/widgets/issues/2", srv.URL)
		mu.Lock()
		issues[1] = &Issue{
			ID:         1001,
			Number:     1,
			Title:      "remote",
			State:      "closed",
			Labels:     []Label{{ID: 1, Name: "bug"}},
			CreatedAt:  &t1,
			UpdatedAt:  &t1,
			HTMLURL:    ref1,
			Repository: &RepositoryMeta{ID: 99, Owner: "acme", Name: "widgets", FullName: "acme/widgets"},
		}
		issues[2] = &Issue{
			ID:         1002,
			Number:     2,
			Title:      "dependent",
			State:      "open",
			CreatedAt:  &t1,
			UpdatedAt:  &t1,
			HTMLURL:    ref2,
			Repository: &RepositoryMeta{ID: 99, Owner: "acme", Name: "widgets", FullName: "acme/widgets"},
		}
		mu.Unlock()

		store := f.StoreFactory.Open().(*conformance.Store)
		store.Config["gitea.url"] = srv.URL
		store.Config["gitea.repo"] = "acme/widgets"
		store.Issues["bd-1"] = &types.Issue{
			ID:          "bd-1",
			Title:       "local",
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			Priority:    2,
			Labels:      []string{"old"},
			ExternalRef: &ref1,
			UpdatedAt:   time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
		}

		tr := &Tracker{}
		if err := tr.Init(context.Background(), store); err != nil {
			t.Fatalf("Init: %v", err)
		}

		engine := tracker.NewEngine(tr, store, "gitea")
		var genSeq int
		engine.PullHooks = &tracker.PullHooks{
			GenerateID: func(_ context.Context, issue *types.Issue) error {
				if issue.ID == "" {
					genSeq++
					issue.ID = fmt.Sprintf("bd-gen-%d", genSeq)
				}
				return nil
			},
		}
		engine.PushHooks = &tracker.PushHooks{
			ContentEqual: func(local *types.Issue, remote *tracker.TrackerIssue) bool {
				gt, ok := remote.Raw.(*Issue)
				if !ok {
					return false
				}
				return PushFieldsEqual(local, gt, tr.MappingConfig())
			},
			PushFieldsEqual: func(local *types.Issue, remote *tracker.TrackerIssue) bool {
				gt, ok := remote.Raw.(*Issue)
				if !ok {
					return false
				}
				return PushFieldsEqual(local, gt, tr.MappingConfig())
			},
			ContentHash: func(local *types.Issue) string {
				return PushContentHash(local, tr.MappingConfig())
			},
			TargetScope: tr.PushTargetScope,
		}

		return conformance.Setup{
			Engine:        engine,
			Store:         store,
			Snapshot:      func(context.Context) (conformance.Snapshot, error) { return store.Snapshot(), nil },
			MutationCount: store.MutationCount,
			SeedExternalRefPlanes: func(context.Context) (string, string, error) {
				store.Wisps["aaa-wisp-bd-1"] = &types.Issue{
					ID:          "aaa-wisp-bd-1",
					Title:       "pushed wisp",
					Status:      types.StatusOpen,
					IssueType:   types.TypeTask,
					Priority:    2,
					Ephemeral:   true,
					ExternalRef: &ref1,
				}
				otherRef := ref1 + "-wisp-only"
				store.Wisps["wisp-only"] = &types.Issue{
					ID:          "wisp-only",
					Title:       "wisp only",
					Status:      types.StatusOpen,
					IssueType:   types.TypeTask,
					Priority:    2,
					Ephemeral:   true,
					ExternalRef: &otherRef,
				}
				return "bd-1", "wisp-only", nil
			},
			Expected: conformance.Expected{
				ExternalRef: ref1,
				ConfigKey:   "gitea.repo",
				MetadataKey: "gitea.last_sync",
			},
			Refusal: func(context.Context) (*tracker.SyncResult, error) {
				return nil, &storage.ErrUnsupported{Op: "gitea sync", Backend: "proxied-server"}
			},
			APIOnly: func(ctx context.Context, _ func() tracker.Store) error {
				double := conformance.NewHTTPDouble()
				double.Enqueue(conformance.Response{
					Status: http.StatusOK,
					Body:   `{"id":7,"username":"alice","login":"alice"}`,
				})
				c := NewClient("tok", "https://gitea.test", "acme", "widgets").WithHTTPClient(double.Client())
				u, err := c.GetAuthenticatedUser(ctx)
				if err != nil {
					return err
				}
				if u.Handle() != "alice" {
					return fmt.Errorf("Handle = %q, want alice", u.Handle())
				}
				return nil
			},
		}
	})
}

func TestPushTwiceWithMismatchedHTMLURLHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GITEA_TOKEN", "tok")
	t.Setenv("GITEA_REPO", "acme/widgets")

	var mu sync.Mutex
	var postCount, patchCount int
	issues := map[int]*Issue{}
	nextNum := 1
	labels := map[string]Label{}
	nextLabelID := int64(1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets":
			_, _ = w.Write([]byte(`{"id":99,"name":"widgets","full_name":"acme/widgets"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/labels":
			out := make([]Label, 0, len(labels))
			for _, l := range labels {
				out = append(out, l)
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/acme/widgets/labels":
			var req struct {
				Name  string `json:"name"`
				Color string `json:"color"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			lbl := Label{ID: nextLabelID, Name: req.Name, Color: req.Color}
			nextLabelID++
			labels[strings.ToLower(req.Name)] = lbl
			_ = json.NewEncoder(w).Encode(lbl)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/issues":
			out := make([]*Issue, 0, len(issues))
			for _, iss := range issues {
				out = append(out, iss)
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/acme/widgets/issues":
			postCount++
			var req struct {
				Title  string  `json:"title"`
				Body   string  `json:"body"`
				Labels []int64 `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			num := nextNum
			nextNum++
			now := time.Now().UTC()
			iss := &Issue{
				ID:         int64(1000 + num),
				Number:     num,
				Title:      req.Title,
				Body:       req.Body,
				State:      "open",
				CreatedAt:  &now,
				UpdatedAt:  &now,
				HTMLURL:    fmt.Sprintf("https://git.example.org/acme/widgets/issues/%d", num),
				Repository: &RepositoryMeta{ID: 99, Owner: "acme", Name: "widgets", FullName: "acme/widgets"},
			}
			issues[num] = iss
			_ = json.NewEncoder(w).Encode(iss)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dependencies"):
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/labels"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/repos/acme/widgets/issues/"):
			numStr := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/acme/widgets/issues/")
			n, _ := strconv.Atoi(numStr)
			iss := issues[n]
			if iss == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodPatch {
				patchCount++
				var req map[string]interface{}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if title, ok := req["title"].(string); ok {
					iss.Title = title
				}
				if body, ok := req["body"].(string); ok {
					iss.Body = body
				}
				if st, ok := req["state"].(string); ok {
					iss.State = st
				}
				now := time.Now().UTC()
				iss.UpdatedAt = &now
			}
			_ = json.NewEncoder(w).Encode(iss)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("GITEA_URL", srv.URL)

	st := conformance.NewStore()
	st.Config["gitea.url"] = srv.URL
	st.Config["gitea.repo"] = "acme/widgets"
	st.Issues["bd-1"] = &types.Issue{
		ID:          "bd-1",
		Title:       "Unlinked bead",
		Description: "Body",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		UpdatedAt:   time.Now().UTC(),
	}

	tr := &Tracker{}
	if err := tr.Init(context.Background(), st); err != nil {
		t.Fatalf("Init: %v", err)
	}
	engine := tracker.NewEngine(tr, st, "tester")
	engine.PushHooks = &tracker.PushHooks{
		ContentEqual: func(local *types.Issue, remote *tracker.TrackerIssue) bool {
			gt, ok := remote.Raw.(*Issue)
			return ok && PushFieldsEqual(local, gt, tr.MappingConfig())
		},
		PushFieldsEqual: func(local *types.Issue, remote *tracker.TrackerIssue) bool {
			gt, ok := remote.Raw.(*Issue)
			return ok && PushFieldsEqual(local, gt, tr.MappingConfig())
		},
		ContentHash: func(local *types.Issue) string {
			return PushContentHash(local, tr.MappingConfig())
		},
		TargetScope: tr.PushTargetScope,
	}

	res1, err := engine.Sync(context.Background(), tracker.SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("first push: %v", err)
	}
	if res1.Stats.Created != 1 || postCount != 1 {
		t.Fatalf("first push: created=%d postCount=%d, want 1/1", res1.Stats.Created, postCount)
	}
	if got := *st.Issues["bd-1"].ExternalRef; got != "gitea:acme/widgets#1" {
		t.Fatalf("external_ref after first push = %q, want gitea:acme/widgets#1", got)
	}

	// Second push must recognize the stored ref and skip instead of creating a duplicate.
	res2, err := engine.Sync(context.Background(), tracker.SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if res2.Stats.Created != 0 || res2.Stats.Updated != 0 || res2.Stats.Skipped != 1 || postCount != 1 {
		t.Fatalf("second push: stats=%+v postCount=%d, want skipped=1 postCount=1", res2.Stats, postCount)
	}
	if got := *st.Issues["bd-1"].ExternalRef; got != "gitea:acme/widgets#1" {
		t.Fatalf("external_ref after second push = %q, want gitea:acme/widgets#1", got)
	}

	// Editing locally and pushing a 3rd time updates #1 via PATCH without another POST.
	st.Issues["bd-1"].Title = "Updated bead"
	st.Issues["bd-1"].UpdatedAt = time.Now().UTC().Add(time.Second)
	res3, err := engine.Sync(context.Background(), tracker.SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("third push: %v", err)
	}
	if res3.Stats.Updated != 1 || postCount != 1 || patchCount != 1 {
		t.Fatalf("third push: stats=%+v postCount=%d patchCount=%d, want updated=1 post=1 patch=1", res3.Stats, postCount, patchCount)
	}

	// Pulling from the same server matches the existing bead without duplicating it.
	resPull, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if resPull.Stats.Created != 0 || len(st.Issues) != 1 {
		t.Fatalf("pull created duplicates: stats=%+v issues=%d", resPull.Stats, len(st.Issues))
	}
}

func TestClosedStateMapPullAndPushRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GITEA_TOKEN", "tok")
	t.Setenv("GITEA_REPO", "acme/widgets")

	created := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	closedAt := created.Add(time.Hour)
	var mu sync.Mutex
	var patchStates []string
	var baseURL string
	remoteIssue := &Issue{
		ID:         1001,
		Number:     1,
		Title:      "Declined feature",
		Body:       "Original body",
		State:      "closed",
		CreatedAt:  &created,
		UpdatedAt:  &closedAt,
		ClosedAt:   &closedAt,
		Labels:     []Label{{ID: 1, Name: "wontfix"}},
		Repository: &RepositoryMeta{ID: 99, Owner: "acme", Name: "widgets", FullName: "acme/widgets"},
	}
	labels := map[string]Label{"wontfix": {ID: 1, Name: "wontfix"}}
	nextLabelID := int64(2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets":
			_, _ = w.Write([]byte(`{"id":99,"name":"widgets","full_name":"acme/widgets"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{remoteIssue})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/issues/1":
			_ = json.NewEncoder(w).Encode(remoteIssue)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dependencies"):
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/labels":
			out := make([]Label, 0, len(labels))
			for _, l := range labels {
				out = append(out, l)
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/acme/widgets/labels":
			var req struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			lbl := Label{ID: nextLabelID, Name: req.Name}
			nextLabelID++
			labels[strings.ToLower(req.Name)] = lbl
			_ = json.NewEncoder(w).Encode(lbl)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/repos/acme/widgets/issues/1":
			var req map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if st, ok := req["state"].(string); ok {
				patchStates = append(patchStates, st)
				remoteIssue.State = st
			}
			if body, ok := req["body"].(string); ok {
				remoteIssue.Body = body
			}
			_ = json.NewEncoder(w).Encode(remoteIssue)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/repos/acme/widgets/issues/1/labels":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/acme/widgets/issues":
			createdNew := &Issue{
				ID:         1002,
				Number:     2,
				Title:      "New wontfix",
				State:      "open",
				HTMLURL:    baseURL + "/acme/widgets/issues/2",
				Repository: &RepositoryMeta{ID: 99, Owner: "acme", Name: "widgets", FullName: "acme/widgets"},
			}
			_ = json.NewEncoder(w).Encode(createdNew)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/repos/acme/widgets/issues/2":
			var req map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if st, ok := req["state"].(string); ok {
				patchStates = append(patchStates, st)
			}
			_ = json.NewEncoder(w).Encode(&Issue{
				ID:         1002,
				Number:     2,
				Title:      "New wontfix",
				State:      "closed",
				HTMLURL:    baseURL + "/acme/widgets/issues/2",
				Repository: &RepositoryMeta{ID: 99, Owner: "acme", Name: "widgets", FullName: "acme/widgets"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	baseURL = srv.URL
	remoteIssue.HTMLURL = srv.URL + "/acme/widgets/issues/1"
	t.Setenv("GITEA_URL", srv.URL)

	st := conformance.NewStore()
	st.Config["gitea.url"] = srv.URL
	st.Config["gitea.repo"] = "acme/widgets"
	st.Config["gitea.closed_state_map.wontfix"] = "deferred"

	tr := &Tracker{}
	if err := tr.Init(context.Background(), st); err != nil {
		t.Fatalf("Init: %v", err)
	}
	engine := tracker.NewEngine(tr, st, "tester")
	engine.PullHooks = &tracker.PullHooks{
		GenerateID: func(_ context.Context, issue *types.Issue) error {
			if issue.ID == "" {
				issue.ID = "bd-wontfix"
			}
			return nil
		},
	}

	// Pull must succeed and produce a valid deferred bead with nil ClosedAt.
	resPull, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if resPull.Stats.Created != 1 {
		t.Fatalf("pull created = %d, want 1", resPull.Stats.Created)
	}
	pulled := st.Issues["bd-wontfix"]
	if pulled == nil || pulled.Status != types.StatusDeferred || pulled.ClosedAt != nil {
		t.Fatalf("pulled issue = %+v, want Status=deferred ClosedAt=nil", pulled)
	}
	if err := pulled.ValidateWithCustom(nil, nil); err != nil {
		t.Fatalf("pulled issue failed ValidateWithCustom: %v", err)
	}

	// Updating description locally and pushing must keep remote issue state=closed.
	pulled.Description = "Updated notes"
	pulled.UpdatedAt = time.Now().UTC()
	if _, err := engine.Sync(context.Background(), tracker.SyncOptions{Push: true}); err != nil {
		t.Fatalf("push updated wontfix: %v", err)
	}
	if len(patchStates) != 1 || patchStates[0] != "closed" {
		t.Fatalf("PATCH states after push = %v, want [closed]", patchStates)
	}

	// Creating a new deferred+wontfix bead via CreateIssue must close it remotely.
	if _, err := tr.CreateIssue(context.Background(), &types.Issue{
		Title:     "New wontfix",
		Status:    types.StatusDeferred,
		IssueType: types.TypeTask,
		Priority:  2,
		Labels:    []string{"wontfix"},
	}); err != nil {
		t.Fatalf("CreateIssue(deferred+wontfix): %v", err)
	}
	if len(patchStates) != 2 || patchStates[1] != "closed" {
		t.Fatalf("PATCH states after CreateIssue = %v, want [closed closed]", patchStates)
	}
}

func TestTrackerPullSendsSinceQueryParam(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GITEA_TOKEN", "tok")
	t.Setenv("GITEA_REPO", "acme/widgets")

	var gotSince []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets":
			_, _ = w.Write([]byte(`{"id":99,"name":"widgets","full_name":"acme/widgets"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/issues":
			gotSince = append(gotSince, r.URL.Query().Get("since"))
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("GITEA_URL", srv.URL)

	st := conformance.NewStore()
	st.Config["gitea.url"] = srv.URL
	st.Config["gitea.repo"] = "acme/widgets"
	st.Metadata["gitea.last_sync"] = "2026-11-20T12:00:00Z"

	tr := &Tracker{}
	if err := tr.Init(context.Background(), st); err != nil {
		t.Fatalf("Init: %v", err)
	}
	engine := tracker.NewEngine(tr, st, "tester")
	if _, err := engine.Sync(context.Background(), tracker.SyncOptions{Pull: true}); err != nil {
		t.Fatalf("incremental pull: %v", err)
	}
	if len(gotSince) == 0 || gotSince[0] == "" {
		t.Fatalf("GET /issues received since=%v, want non-empty RFC3339 timestamp", gotSince)
	}
	if _, err := time.Parse(time.RFC3339, gotSince[0]); err != nil {
		t.Fatalf("since=%q is not valid RFC3339: %v", gotSince[0], err)
	}
}

type tlsCATestStore struct {
	tracker.Store
	endpoint string
	tlsCA    string
}

func (s tlsCATestStore) GetConfig(_ context.Context, key string) (string, error) {
	switch key {
	case "gitea.url":
		return s.endpoint, nil
	case "gitea.repo":
		return "acme/widgets", nil
	case "gitea.tls_ca":
		return s.tlsCA, nil
	default:
		return "", nil
	}
}

func (s tlsCATestStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s tlsCATestStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return nil, nil
}

func TestInitIgnoresSharedTLSCAAndHonorsPrivateTLSCA(t *testing.T) {
	home := t.TempDir()
	xdgConfig := filepath.Join(home, ".config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("GITEA_TOKEN", "tok")
	t.Setenv("GITEA_REPO", "acme/widgets")
	t.Setenv("GITEA_TLS_CA", "")
	t.Setenv("BD_GITEA_TLS_CA", "")
	t.Setenv("BEADS_DIR", "")

	work := t.TempDir()
	beadsDir := filepath.Join(work, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Shared project config tries to inject a bogus CA path.
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("gitea.tls_ca: /nonexistent/project-ca.pem\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}

	// Plain HTTP loopback server: if shared store/project gitea.tls_ca were read,
	// WithTLSCA("/nonexistent/...") would fail Init.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":10,"name":"widgets","full_name":"acme/widgets"}`))
	}))
	defer srv.Close()
	t.Setenv("GITEA_URL", srv.URL)

	st := tlsCATestStore{endpoint: srv.URL, tlsCA: "/nonexistent/store-ca.pem"}
	if err := (&Tracker{}).Init(context.Background(), st); err != nil {
		t.Fatalf("Init should ignore store and project gitea.tls_ca, got: %v", err)
	}

	// Now verify user-global ~/.config/bd/config.yaml IS honored for a self-signed TLS server.
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":11,"name":"widgets","full_name":"acme/widgets"}`))
	}))
	defer tlsSrv.Close()
	t.Setenv("GITEA_URL", tlsSrv.URL)

	caFile := filepath.Join(home, "forgejo-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	userCfgDir := filepath.Join(xdgConfig, "bd")
	if err := os.MkdirAll(userCfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"), []byte(fmt.Sprintf("gitea.tls_ca: %q\n", caFile)), 0o600); err != nil {
		t.Fatal(err)
	}
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err := (&Tracker{}).Init(context.Background(), tlsCATestStore{endpoint: tlsSrv.URL, tlsCA: "/nonexistent/store-ca.pem"}); err != nil {
		t.Fatalf("Init with user-global gitea.tls_ca failed: %v", err)
	}

	// Private env GITEA_TLS_CA overrides user-global config.
	t.Setenv("GITEA_TLS_CA", "/nonexistent/env-ca.pem")
	if err := (&Tracker{}).Init(context.Background(), tlsCATestStore{endpoint: tlsSrv.URL}); err == nil {
		t.Fatal("expected Init to honor private GITEA_TLS_CA env and fail on nonexistent path")
	}
}
