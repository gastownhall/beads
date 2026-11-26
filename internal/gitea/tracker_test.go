package gitea

import (
	"context"
	"encoding/json"
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

	var requestsB int
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	// Private env confirmation with trailing /api/v1/ must match unstripped raw URL.
	t.Setenv("GITEA_URL", serverA.URL+"/api/v1/")
	if err := (&Tracker{}).Init(context.Background(), endpointTestStore{endpoint: serverA.URL + "/api/v1"}); err != nil {
		t.Fatalf("private confirmed Init with /api/v1: %v", err)
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
				out := make([]*Issue, 0, len(issues))
				for _, iss := range issues {
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
