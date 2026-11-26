// Package main provides the bd CLI commands.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/endpointtrust"
	"github.com/steveyegge/beads/internal/gitea"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestGiteaConfigFromEnv(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("GITEA_URL", "https://codeberg.org")
	t.Setenv("GITEA_TOKEN", "test-gitea-token")
	t.Setenv("GITEA_REPO", "acme/widgets")
	t.Setenv("GITEA_TLS_CA", "/tmp/ca.pem")

	cfg := getGiteaConfig()
	if cfg.URL != "https://codeberg.org" {
		t.Errorf("URL = %q, want https://codeberg.org", cfg.URL)
	}
	if cfg.Token != "test-gitea-token" {
		t.Errorf("Token = %q, want test-gitea-token", cfg.Token)
	}
	if cfg.Owner != "acme" || cfg.Repo != "widgets" {
		t.Errorf("Owner/Repo = %q/%q, want acme/widgets", cfg.Owner, cfg.Repo)
	}
	if cfg.TLSCA != "/tmp/ca.pem" {
		t.Errorf("TLSCA = %q, want /tmp/ca.pem", cfg.TLSCA)
	}
}

func TestGiteaConfigValueYamlTokenReadsFromYamlAndEnv(t *testing.T) {
	const wantToken = "yaml-gitea-token"

	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	isolateBeadsDirForTest(t)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("gitea.token: \""+wantToken+"\"\n"), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	t.Setenv("GITEA_TOKEN", "")
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	t.Setenv("HOME", filepath.Join(tmpDir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	t.Chdir(tmpDir)

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	if got := getGiteaConfigValue(context.Background(), "gitea.token"); got != wantToken {
		t.Errorf("getGiteaConfigValue(gitea.token) = %q, want %q", got, wantToken)
	}

	// Empty YAML value falls back to env without touching DB store.
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("reset config.yaml: %v", err)
	}
	t.Setenv("GITEA_TOKEN", "env-fallback-token")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if got := getGiteaConfigValue(context.Background(), "gitea.token"); got != "env-fallback-token" {
		t.Errorf("getGiteaConfigValue(gitea.token) env fallback = %q, want env-fallback-token", got)
	}
}

func TestValidateGiteaURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{"https://codeberg.org", false},
		{"https://forgejo.example.com/subpath", false},
		{"http://localhost:3000", false},
		{"http://127.0.0.1:3000", false},
		{"http://[::1]:3000", false},
		{"http://localhost.evil.com", true},
		{"http://gitea.example.com", true},
		{"ftp://gitea.example.com", true},
		{"", true},
	}
	for _, tt := range tests {
		err := validateGiteaURL(tt.url)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateGiteaURL(%q) err = %v, wantErr = %v", tt.url, err, tt.wantErr)
		}
	}
}

func TestGetGiteaClientChecksRawURLPinningBeforeNormalization(t *testing.T) {
	isolateBeadsDirForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()
	beadsDir := filepath.Join(work, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Chdir(work)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatal(err)
	}
	config.RecordWorkspaceBeadsDir(beadsDir)

	t.Setenv("GITEA_URL", "")
	_, err := getGiteaClient(GiteaConfig{
		URL:   "https://forgejo.example/api/v1",
		Token: "tok",
		Owner: "acme",
		Repo:  "widgets",
	})
	var firstUse *endpointtrust.FirstUseError
	if !errors.As(err, &firstUse) {
		t.Fatalf("unconfirmed raw URL error = %v, want FirstUseError", err)
	}

	// Private GITEA_URL with trailing slash matches raw unstripped /api/v1 URL.
	t.Setenv("GITEA_URL", "https://forgejo.example/api/v1/")
	client, err := getGiteaClient(GiteaConfig{
		URL:   "https://forgejo.example/api/v1",
		Token: "tok",
		Owner: "acme",
		Repo:  "widgets",
	})
	if err != nil {
		t.Fatalf("getGiteaClient with confirmed raw URL: %v", err)
	}
	if client.BaseURL != "https://forgejo.example" {
		t.Errorf("client.BaseURL = %q, want normalized https://forgejo.example", client.BaseURL)
	}
}

func TestGiteaStatusAndReposTextOutput(t *testing.T) {
	oldDBPath, oldStore, oldJSON := dbPath, store, jsonOutput
	dbPath, store, jsonOutput = "", nil, false
	t.Cleanup(func() { dbPath, store, jsonOutput = oldDBPath, oldStore, oldJSON })

	isolateBeadsDirForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user/repos" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":1,"name":"widgets","full_name":"acme/widgets","html_url":"https://forgejo.test/acme/widgets"}]`))
	}))
	defer srv.Close()

	t.Setenv("GITEA_URL", srv.URL)
	t.Setenv("GITEA_TOKEN", "abcd1234secret")
	t.Setenv("GITEA_REPO", "acme/widgets")

	var statusBuf bytes.Buffer
	giteaStatusCmd.SetOut(&statusBuf)
	t.Cleanup(func() { giteaStatusCmd.SetOut(nil) })
	if err := runGiteaStatus(giteaStatusCmd, nil); err != nil {
		t.Fatalf("runGiteaStatus: %v", err)
	}
	out := statusBuf.String()
	if !strings.Contains(out, "abcd****") || strings.Contains(out, "abcd1234secret") {
		t.Errorf("token masking broken in status output:\n%s", out)
	}
	if !strings.Contains(out, "Status: ✓ Configured") {
		t.Errorf("expected configured status in output:\n%s", out)
	}

	var reposBuf bytes.Buffer
	giteaReposCmd.SetOut(&reposBuf)
	t.Cleanup(func() { giteaReposCmd.SetOut(nil) })
	if err := runGiteaRepos(giteaReposCmd, nil); err != nil {
		t.Fatalf("runGiteaRepos: %v", err)
	}
	if !strings.Contains(reposBuf.String(), "acme/widgets") {
		t.Errorf("repos output missing acme/widgets:\n%s", reposBuf.String())
	}
}

type giteaFakeReader struct {
	issues []*types.Issue
}

func (r *giteaFakeReader) Ready(context.Context, issueops.ReadyRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, nil
}

func (r *giteaFakeReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	items := make([]*issueops.IssueWithCounts, 0, len(r.issues))
	for _, iss := range r.issues {
		items = append(items, &issueops.IssueWithCounts{Issue: iss})
	}
	return issueops.IssuePage{Items: items}, nil
}

func (r *giteaFakeReader) Get(context.Context, issueops.GetRequest) (*issueops.IssueDetails, error) {
	return nil, storage.ErrNotFound
}

type giteaSyncFakeStore struct {
	storage.DoltStorage
	issues     []*types.Issue
	deps       map[string][]*types.IssueWithDependencyMetadata
	dependents map[string][]*types.IssueWithDependencyMetadata
	config     map[string]string
	metadata   map[string]string
}

func (s *giteaSyncFakeStore) GetConfig(_ context.Context, key string) (string, error) {
	if s.config != nil {
		return s.config[key], nil
	}
	return "", nil
}

func (s *giteaSyncFakeStore) GetAllConfig(context.Context) (map[string]string, error) {
	if s.config != nil {
		return s.config, nil
	}
	return map[string]string{}, nil
}

func (s *giteaSyncFakeStore) GetLocalMetadata(_ context.Context, key string) (string, error) {
	if s.metadata != nil {
		return s.metadata[key], nil
	}
	return "", nil
}

func (s *giteaSyncFakeStore) SetLocalMetadata(_ context.Context, key, value string) error {
	if s.metadata == nil {
		s.metadata = make(map[string]string)
	}
	s.metadata[key] = value
	return nil
}

func (s *giteaSyncFakeStore) GetIssueByExternalRef(_ context.Context, externalRef string) (*types.Issue, error) {
	for _, iss := range s.issues {
		if iss != nil && iss.ExternalRef != nil && *iss.ExternalRef == externalRef {
			return iss, nil
		}
	}
	return nil, nil
}

func (s *giteaSyncFakeStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	s.issues = append(s.issues, issue)
	return nil
}

func (s *giteaSyncFakeStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return s.issues, nil
}

func (s *giteaSyncFakeStore) IssueReader() (issueops.Reader, error) {
	return &giteaFakeReader{issues: s.issues}, nil
}

func (s *giteaSyncFakeStore) GetDependenciesWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.deps[issueID], nil
}

func (s *giteaSyncFakeStore) GetDependentsWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.dependents[issueID], nil
}

func TestGiteaLinkSyncCollectionAndDisabledWarning(t *testing.T) {
	isolateBeadsDirForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets":
			_, _ = w.Write([]byte(`{"id":99,"name":"widgets","full_name":"acme/widgets"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dependencies"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"dependencies disabled"}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	t.Setenv("GITEA_URL", srv.URL)
	t.Setenv("GITEA_TOKEN", "tok")
	t.Setenv("GITEA_REPO", "acme/widgets")

	ref1 := srv.URL + "/acme/widgets/issues/1"
	ref2 := srv.URL + "/acme/widgets/issues/2"
	ref3 := srv.URL + "/acme/widgets/issues/3"
	b1 := &types.Issue{ID: "bd-1", IssueType: types.TypeTask, Status: types.StatusOpen, ExternalRef: &ref1}
	b2 := &types.Issue{ID: "bd-2", IssueType: types.TypeTask, Status: types.StatusOpen, ExternalRef: &ref2}
	b3 := &types.Issue{ID: "bd-3", IssueType: types.TypeTask, Status: types.StatusOpen, ExternalRef: &ref3}
	parent := &types.Issue{ID: "bd-parent", IssueType: types.TypeEpic, Status: types.StatusOpen}

	st := &giteaSyncFakeStore{
		issues: []*types.Issue{parent, b1, b2, b3},
		deps: map[string][]*types.IssueWithDependencyMetadata{
			b2.ID: {{Issue: *b1, DependencyType: types.DepBlocks}},
			b3.ID: {{Issue: *b1, DependencyType: types.DepBlocks}},
		},
		dependents: map[string][]*types.IssueWithDependencyMetadata{
			parent.ID: {
				{Issue: *b1, DependencyType: types.DepParentChild},
				{Issue: *b2, DependencyType: types.DepParentChild},
			},
		},
	}

	gt := &gitea.Tracker{}
	if err := gt.Init(context.Background(), st); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// --parent limits link collection to subtree (bd-2 -> bd-1 only, excluding bd-3 -> bd-1).
	data, collectWarn := collectGiteaLinkSyncData(context.Background(), st, gt.RefScope(), tracker.SyncOptions{
		ParentID: parent.ID,
	})
	if len(collectWarn) != 0 {
		t.Fatalf("collect warnings = %v", collectWarn)
	}
	if len(data.DesiredLinks) != 1 || data.DesiredLinks[0].FromNumber != 2 || data.DesiredLinks[0].ToNumber != 1 {
		t.Fatalf("DesiredLinks = %+v, want #2 -> #1 only", data.DesiredLinks)
	}

	// 403 on /dependencies becomes non-fatal warning and skipped count.
	var warnings []string
	created, skipped := pushGiteaDependencyLinks(context.Background(), gt, st, tracker.SyncOptions{
		ParentID: parent.ID,
	}, false, io.Discard, func(msg string) { warnings = append(warnings, msg) })
	if created != 0 || skipped != 1 || len(warnings) != 1 || !strings.Contains(warnings[0], "disabled") {
		t.Fatalf("pushGiteaDependencyLinks = (created=%d, skipped=%d, warnings=%v)", created, skipped, warnings)
	}
}

func TestGiteaSyncResultJSONIncludesLinksFields(t *testing.T) {
	data, err := json.Marshal(giteaSyncResult{LinksPushed: 2, LinksSkipped: 1})
	if err != nil {
		t.Fatalf("Marshal giteaSyncResult: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"links_pushed":2`) || !strings.Contains(s, `"links_skipped":1`) {
		t.Fatalf("JSON = %s, want links_pushed and links_skipped", s)
	}
}

func TestGiteaNoEphemeralDefaultTrue(t *testing.T) {
	f := giteaSyncCmd.Flags().Lookup("no-ephemeral")
	if f == nil {
		t.Fatal("missing --no-ephemeral flag")
	}
	if f.DefValue != "true" {
		t.Errorf("--no-ephemeral default = %q, want true", f.DefValue)
	}
}

func TestGiteaConfigOwnerRepoPrecedence(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	// When gitea.repo has "owner/repo", it takes precedence over gitea.owner (aligned with gitea.Tracker).
	t.Setenv("GITEA_REPO", "acme/widgets")
	t.Setenv("GITEA_OWNER", "conflicting-owner")
	t.Setenv("GITEA_PROJECT", "ignored-project")
	cfg := getGiteaConfig()
	if cfg.Owner != "acme" || cfg.Repo != "widgets" {
		t.Fatalf("getGiteaConfig() = (%q, %q), want (acme, widgets)", cfg.Owner, cfg.Repo)
	}

	// When gitea.repo has no slash, fall back to gitea.owner.
	t.Setenv("GITEA_REPO", "widgets")
	t.Setenv("GITEA_OWNER", "fallback-owner")
	cfg = getGiteaConfig()
	if cfg.Owner != "fallback-owner" || cfg.Repo != "widgets" {
		t.Fatalf("getGiteaConfig() without slash = (%q, %q), want (fallback-owner, widgets)", cfg.Owner, cfg.Repo)
	}
}

func TestGiteaSelectivePullExternalRefsAndExclusions(t *testing.T) {
	oldDBPath, oldStore, oldActive, oldJSON := dbPath, store, isStoreActive(), jsonOutput
	oldDidWrite := commandDidWrite.Load()
	t.Cleanup(func() {
		dbPath, store, jsonOutput = oldDBPath, oldStore, oldJSON
		setStoreActive(oldActive)
		commandDidWrite.Store(oldDidWrite)
	})

	isolateBeadsDirForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets":
			_, _ = w.Write([]byte(`{"id":99,"name":"widgets","full_name":"acme/widgets"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/issues/1":
			_, _ = w.Write([]byte(`{"id":101,"number":1,"title":"Issue one","state":"open","html_url":"` + srvURL + `/acme/widgets/issues/1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/issues/1/dependencies":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widgets/issues/2":
			_, _ = w.Write([]byte(`{"id":102,"number":2,"title":"PR two","state":"open","pull_request":{"merged":false},"html_url":"` + srvURL + `/acme/widgets/pulls/2"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	t.Setenv("GITEA_URL", srv.URL)
	t.Setenv("GITEA_TOKEN", "tok")
	t.Setenv("GITEA_REPO", "acme/widgets")

	giteaPullCmd.SetContext(context.Background())
	t.Cleanup(func() { giteaPullCmd.SetContext(nil) })

	for _, ref := range []string{
		srv.URL + "/acme/widgets/issues/1",
		"gitea:1",
		"gitea:acme/widgets#1",
		"1",
	} {
		t.Run("pull/"+ref, func(t *testing.T) {
			st := &giteaSyncFakeStore{
				config: map[string]string{
					"issue_prefix": "tp",
					"gitea.url":    srv.URL,
					"gitea.repo":   "acme/widgets",
				},
			}
			dbPath = ""
			store = st
			setStoreActive(true)
			jsonOutput = true

			stdioMutex.Lock()
			oldStdout, oldStderr := os.Stdout, os.Stderr
			rOut, wOut, err := os.Pipe()
			if err != nil {
				stdioMutex.Unlock()
				t.Fatalf("os.Pipe: %v", err)
			}
			rErr, wErr, err := os.Pipe()
			if err != nil {
				_ = rOut.Close()
				_ = wOut.Close()
				stdioMutex.Unlock()
				t.Fatalf("os.Pipe stderr: %v", err)
			}
			os.Stdout, os.Stderr = wOut, wErr

			runErr := giteaPullCmd.RunE(giteaPullCmd, []string{ref})

			_ = wOut.Close()
			_ = wErr.Close()
			os.Stdout, os.Stderr = oldStdout, oldStderr
			stdioMutex.Unlock()

			var outBuf bytes.Buffer
			_, _ = io.Copy(&outBuf, rOut)
			_ = rOut.Close()
			_, _ = io.Copy(io.Discard, rErr)
			_ = rErr.Close()

			if runErr != nil {
				t.Fatalf("giteaPullCmd.RunE(%q) error: %v\nstdout:\n%s", ref, runErr, outBuf.String())
			}
			var res giteaSyncResult
			if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil {
				t.Fatalf("unmarshal pull JSON %q: %v", outBuf.String(), err)
			}
			if res.Pulled != 1 || res.Created != 1 || res.Errors != 0 {
				t.Fatalf("pull(%q) = %+v, want pulled=1 created=1 errors=0", ref, res)
			}
			if len(st.issues) != 1 || st.issues[0].Title != "Issue one" {
				t.Fatalf("imported issues = %+v, want Issue one", st.issues)
			}
		})
	}

	// Cross-repo URL is rejected with errors=1, pulled=0, and PR #2 is excluded (0 imported).
	t.Run("reject_cross_repo_and_exclude_pr", func(t *testing.T) {
		st := &giteaSyncFakeStore{
			config: map[string]string{
				"issue_prefix": "tp",
				"gitea.url":    srv.URL,
				"gitea.repo":   "acme/widgets",
			},
		}
		dbPath = ""
		store = st
		setStoreActive(true)
		jsonOutput = true

		stdioMutex.Lock()
		oldStdout, oldStderr := os.Stdout, os.Stderr
		rOut, wOut, err := os.Pipe()
		if err != nil {
			stdioMutex.Unlock()
			t.Fatalf("os.Pipe: %v", err)
		}
		rErr, wErr, err := os.Pipe()
		if err != nil {
			_ = rOut.Close()
			_ = wOut.Close()
			stdioMutex.Unlock()
			t.Fatalf("os.Pipe stderr: %v", err)
		}
		os.Stdout, os.Stderr = wOut, wErr

		runErr := giteaPullCmd.RunE(giteaPullCmd, []string{
			srv.URL + "/other/repo/issues/1",
			srv.URL + "/acme/widgets/issues/2",
		})

		_ = wOut.Close()
		_ = wErr.Close()
		os.Stdout, os.Stderr = oldStdout, oldStderr
		stdioMutex.Unlock()

		var outBuf bytes.Buffer
		_, _ = io.Copy(&outBuf, rOut)
		_ = rOut.Close()
		_, _ = io.Copy(io.Discard, rErr)
		_ = rErr.Close()

		if runErr != nil {
			t.Fatalf("giteaPullCmd.RunE error: %v\nstdout:\n%s", runErr, outBuf.String())
		}
		var res giteaSyncResult
		if err := json.Unmarshal(outBuf.Bytes(), &res); err != nil {
			t.Fatalf("unmarshal pull JSON %q: %v", outBuf.String(), err)
		}
		if res.Pulled != 0 || res.Errors != 1 || res.Skipped != 1 {
			t.Fatalf("pull(cross-repo + PR) = %+v, want pulled=0 errors=1 skipped=1", res)
		}
		if len(st.issues) != 0 {
			t.Fatalf("imported %d issues, want 0", len(st.issues))
		}
	})
}

func TestGiteaPushPullJSONErrors(t *testing.T) {
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })

	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{
			name: "push_no_args",
			fn:   func() error { return giteaPushCmd.RunE(giteaPushCmd, nil) },
		},
		{
			name: "pull_no_args",
			fn:   func() error { return giteaPullCmd.RunE(giteaPullCmd, nil) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jsonOutput = true
			stdioMutex.Lock()
			oldStdout, oldStderr := os.Stdout, os.Stderr
			rOut, wOut, err := os.Pipe()
			if err != nil {
				stdioMutex.Unlock()
				t.Fatalf("os.Pipe: %v", err)
			}
			rErr, wErr, err := os.Pipe()
			if err != nil {
				_ = rOut.Close()
				_ = wOut.Close()
				stdioMutex.Unlock()
				t.Fatalf("os.Pipe stderr: %v", err)
			}
			os.Stdout, os.Stderr = wOut, wErr

			runErr := tc.fn()

			_ = wOut.Close()
			_ = wErr.Close()
			os.Stdout, os.Stderr = oldStdout, oldStderr
			stdioMutex.Unlock()

			var outBuf, errBuf bytes.Buffer
			_, _ = io.Copy(&outBuf, rOut)
			_, _ = io.Copy(&errBuf, rErr)
			_ = rOut.Close()
			_ = rErr.Close()

			if runErr == nil {
				t.Fatal("expected non-nil error")
			}
			var exitErr *exitError
			if !errors.As(runErr, &exitErr) || exitErr.Code != 1 {
				t.Fatalf("error = %v, want *exitError{Code:1}", runErr)
			}
			if errBuf.Len() != 0 {
				t.Fatalf("stderr = %q, want empty in --json mode", errBuf.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(outBuf.Bytes(), &payload); err != nil {
				t.Fatalf("invalid JSON error output %q: %v", outBuf.String(), err)
			}
			if msg, _ := payload["error"].(string); msg == "" {
				t.Fatalf("JSON payload missing error field: %v", payload)
			}
		})
	}
}
