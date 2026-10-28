package github

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func TestRegistered(t *testing.T) {
	factory := tracker.Get("github")
	if factory == nil {
		t.Fatal("github tracker not registered")
	}
	tr := factory()
	if tr.Name() != "github" {
		t.Errorf("Name() = %q, want %q", tr.Name(), "github")
	}
	if tr.DisplayName() != "GitHub" {
		t.Errorf("DisplayName() = %q, want %q", tr.DisplayName(), "GitHub")
	}
	if tr.ConfigPrefix() != "github" {
		t.Errorf("ConfigPrefix() = %q, want %q", tr.ConfigPrefix(), "github")
	}
}

func TestPushTargetScope(t *testing.T) {
	tr := &Tracker{client: &Client{
		BaseURL: "HTTPS://GHE.Example.Test/Api/v3///",
		Owner:   "Acme",
		Repo:    "Widgets",
	}}
	if got, want := tr.PushTargetScope(), "https://ghe.example.test/Api/v3/repos/acme/widgets"; got != want {
		t.Fatalf("PushTargetScope() = %q, want %q", got, want)
	}

	equivalent := &Tracker{client: &Client{
		BaseURL: "https://ghe.example.test/Api/v3",
		Owner:   "acme",
		Repo:    "widgets",
	}}
	if got, want := equivalent.PushTargetScope(), tr.PushTargetScope(); got != want {
		t.Fatalf("equivalent PushTargetScope() = %q, want %q", got, want)
	}

	malformed := &Tracker{client: &Client{
		BaseURL: "://GHE.Example.Test/Api/ ",
		Owner:   "ACME",
		Repo:    "WIDGETS",
	}}
	if got, want := malformed.PushTargetScope(), "://GHE.Example.Test/Api/repos/acme/widgets"; got != want {
		t.Fatalf("malformed PushTargetScope() = %q, want deterministic fallback %q", got, want)
	}

	if got := (&Tracker{}).PushTargetScope(); got != "" {
		t.Fatalf("uninitialized PushTargetScope() = %q, want empty", got)
	}
}

func TestIsExternalRef(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://github.com/org/repo/issues/42", true},
		{"https://github.com/team/project/issues/123", true},
		{"https://GitHub.com/org/repo/issues/42", true},
		// An uninitialized tracker knows no GitHub Enterprise host.
		{"https://ghe.example.test/org/repo/issues/42", false},
		{"https://gitlab.com/group/project/-/issues/42", false},
		{"https://linear.app/team/issue/PROJ-123", false},
		{"", false},
		// Shorthand format produced by BuildExternalRef
		{"github:681509", true},
		{"github:1", true},
		{"github:abc", false},
		{"github:123/extra", false},
		{"github:0", false},
	}
	for _, tt := range tests {
		if got := tr.IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

// TestIsExternalRefRecognizesConfiguredHost pins that an initialized tracker
// takes an issue URL on its configured host for GitHub's, whichever
// repository it names and in every spelling the repository scope accepts, so
// a GitHub Enterprise host without "github.com" in its name counts; github.com
// refs stay GitHub's there, so a push skips a bead linked to one instead of
// creating an issue for it.
func TestIsExternalRefRecognizesConfiguredHost(t *testing.T) {
	ghe := &Tracker{client: NewClient("token", "acme", "repo-b").WithBaseURL("https://ghe.example.test/api/v3")}
	for ref, want := range map[string]bool{
		"https://ghe.example.test/acme/repo-b/issues/42":             true,
		"https://ghe.example.test/acme/other/issues/42":              true,
		"https://GHE.Example.test.:443/acme/repo-b/issues/42":        true,
		"ghe.example.test/acme/repo-b/issues/42":                     true,
		"https://ghe.example.test/api/v3/repos/acme/other/issues/42": true,
		"https://github.com/acme/repo-b/issues/42":                   true,
		"https://GitHub.com/acme/repo-b/issues/42":                   true,
		"github:42": true,
		"https://ghe.example.test/acme/repo-b/pull/42":             false,
		"https://ghe.example.test.evil.test/acme/issues/42":        false,
		"https://ghe.example.test@evil.test/acme/repo-b/issues/42": false,
		"https://gitlab.example.test/acme/repo-b/-/issues/42":      false,
	} {
		if got := ghe.IsExternalRef(ref); got != want {
			t.Errorf("GHE IsExternalRef(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestExtractIdentifier(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want string
	}{
		{"https://github.com/org/repo/issues/42", "42"},
		{"https://github.com/team/project/issues/123", "123"},
		{"not-a-url", ""},
		// Shorthand format
		{"github:681509", "681509"},
		{"github:1", "1"},
		{"github:abc", ""},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

// TestExtractIdentifierScopedToRepository pins that an initialized GitHub
// tracker only addresses issues of its configured repository: a ref pointing
// at another repository yields "", so no sync path can match or write this
// repository's same-numbered issue by accident (gastownhall/beads#7781).
func TestExtractIdentifierScopedToRepository(t *testing.T) {
	tr := &Tracker{client: NewClient("token", "acme", "repo-b")}
	tests := []struct {
		ref  string
		want string
	}{
		{"https://github.com/acme/repo-b/issues/42", "42"},
		{"https://github.com/ACME/Repo-B/issues/42", "42"},
		{"github:42", "42"},
		// The REST URL form of the same repository, as the API returns it.
		{"https://api.github.com/repos/acme/repo-b/issues/42", "42"},
		// Respellings of the github.com host that the legacy parse accepted.
		{"https://www.github.com/acme/repo-b/issues/42", "42"},
		{"github.com/acme/repo-b/issues/42", "42"},
		{"https://GitHub.com.:443/acme/repo-b/issues/42", "42"},
		// A path below the issue still names it.
		{"https://github.com/acme/repo-b/issues/42/comments", "42"},
		{"https://api.github.com/repos/acme/repo-b/issues/42/comments", "42"},
		{"https://github.com/acme/repo-a/issues/42/comments", ""},
		{"https://github.com/acme/repo-a/issues/42", ""},
		{"https://github.com/other/repo-b/issues/42", ""},
		{"https://www.github.com/acme/repo-a/issues/42", ""},
		{"github.com/acme/repo-a/issues/42", ""},
		{"https://github.com:8443/acme/repo-b/issues/42", ""},
		// Another host, GitHub Enterprise or otherwise, same owner/repo.
		{"https://ghe.example.test/acme/repo-b/issues/42", ""},
		// Not an issue of the configured repository.
		{"https://github.com/acme/repo-b/pull/42", ""},
		{"https://github.com/acme/repo-b/issues/abc", ""},
		{"https://gitlab.com/group/project/-/issues/42", ""},
		{"github:0", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}

	// A repository named "issues" keeps its owner/repo in both URL forms.
	named := &Tracker{client: NewClient("token", "acme", "issues")}
	for _, ref := range []string{"https://github.com/acme/issues/issues/42", "https://api.github.com/repos/acme/issues/issues/42"} {
		if got := named.ExtractIdentifier(ref); got != "42" {
			t.Errorf("ExtractIdentifier(%q) for acme/issues = %q, want %q", ref, got, "42")
		}
	}

	// A github.url that spells out the default port still addresses the
	// issue URLs GitHub returns, which never carry one.
	port := &Tracker{client: NewClient("token", "acme", "repo-b").WithBaseURL("https://api.github.com:443")}
	if got := port.ExtractIdentifier("https://github.com/acme/repo-b/issues/42"); got != "42" {
		t.Errorf("ExtractIdentifier with github.url https://api.github.com:443 = %q, want %q", got, "42")
	}

	// A GitHub Enterprise tracker resolves the same spellings against its
	// own host, in both the web and the /api/v3 REST form, and no longer
	// addresses github.com refs.
	ghe := &Tracker{client: NewClient("token", "acme", "repo-b").WithBaseURL("https://ghe.example.test/api/v3")}
	for ref, want := range map[string]string{
		"https://ghe.example.test/acme/repo-b/issues/42":              "42",
		"https://ghe.example.test/api/v3/repos/acme/repo-b/issues/42": "42",
		"github:42": "42",
		"https://github.com/acme/repo-b/issues/42": "",
	} {
		if got := ghe.ExtractIdentifier(ref); got != want {
			t.Errorf("GHE ExtractIdentifier(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestBuildExternalRef(t *testing.T) {
	tr := &Tracker{}
	ti := &tracker.TrackerIssue{
		URL:        "https://github.com/org/repo/issues/42",
		Identifier: "42",
	}
	ref := tr.BuildExternalRef(ti)
	if ref != ti.URL {
		t.Errorf("BuildExternalRef() = %q, want %q", ref, ti.URL)
	}

	// Without URL, should use fallback
	ti2 := &tracker.TrackerIssue{
		Identifier: "42",
	}
	ref2 := tr.BuildExternalRef(ti2)
	if ref2 != "github:42" {
		t.Errorf("BuildExternalRef() = %q, want %q", ref2, "github:42")
	}
}

func TestFieldMapperStatus(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	if got := m.StatusToBeads("open"); got != types.StatusOpen {
		t.Errorf("StatusToBeads(open) = %q, want %q", got, types.StatusOpen)
	}
	if got := m.StatusToBeads("closed"); got != types.StatusClosed {
		t.Errorf("StatusToBeads(closed) = %q, want %q", got, types.StatusClosed)
	}
}

func TestFieldMapperPriority(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	if got := m.PriorityToBeads("critical"); got != 0 {
		t.Errorf("PriorityToBeads(critical) = %d, want 0", got)
	}
	if got := m.PriorityToBeads("high"); got != 1 {
		t.Errorf("PriorityToBeads(high) = %d, want 1", got)
	}
	if got := m.PriorityToBeads("low"); got != 3 {
		t.Errorf("PriorityToBeads(low) = %d, want 3", got)
	}
}

func TestFieldMapperType(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	if got := m.TypeToBeads("bug"); got != types.TypeBug {
		t.Errorf("TypeToBeads(bug) = %q, want %q", got, types.TypeBug)
	}
	if got := m.TypeToBeads("feature"); got != types.TypeFeature {
		t.Errorf("TypeToBeads(feature) = %q, want %q", got, types.TypeFeature)
	}
	if got := m.TypeToBeads("unknown"); got != types.TypeTask {
		t.Errorf("TypeToBeads(unknown) = %q, want %q (default)", got, types.TypeTask)
	}
}

func TestGitHubToTrackerIssue(t *testing.T) {
	now := time.Now()
	gh := &Issue{
		ID:      100,
		Number:  42,
		Title:   "Fix pipeline",
		Body:    "CI is broken",
		State:   "open",
		HTMLURL: "https://github.com/org/repo/issues/42",
		Labels: []Label{
			{Name: "bug"},
			{Name: "priority::high"},
		},
		CreatedAt: &now,
		UpdatedAt: &now,
		Assignee:  &User{ID: 5, Login: "bob"},
	}

	ti := githubToTrackerIssue(gh)

	if ti.ID != "100" {
		t.Errorf("ID = %q, want %q", ti.ID, "100")
	}
	if ti.Identifier != "42" {
		t.Errorf("Identifier = %q, want %q", ti.Identifier, "42")
	}
	if ti.Assignee != "bob" {
		t.Errorf("Assignee = %q, want %q", ti.Assignee, "bob")
	}
	if ti.AssigneeID != strconv.Itoa(5) {
		t.Errorf("AssigneeID = %q, want %q", ti.AssigneeID, "5")
	}
	if ti.Raw != gh {
		t.Error("Raw should reference original github.Issue")
	}
	if len(ti.Labels) != 2 {
		t.Errorf("Labels count = %d, want 2", len(ti.Labels))
	}
	if ti.URL != "https://github.com/org/repo/issues/42" {
		t.Errorf("URL = %q, want GitHub URL", ti.URL)
	}
}

func TestGitHubToTrackerIssue_ClosedWithTimestamp(t *testing.T) {
	now := time.Now()
	closedAt := now.Add(-time.Hour)
	gh := &Issue{
		ID:        200,
		Number:    99,
		Title:     "Closed issue",
		State:     "closed",
		CreatedAt: &now,
		UpdatedAt: &now,
		ClosedAt:  &closedAt,
		HTMLURL:   "https://github.com/org/repo/issues/99",
	}

	ti := githubToTrackerIssue(gh)

	if ti.CompletedAt == nil {
		t.Fatal("CompletedAt is nil for closed issue, want non-nil")
	}
	if !ti.CompletedAt.Equal(closedAt) {
		t.Errorf("CompletedAt = %v, want %v", ti.CompletedAt, closedAt)
	}
}

func TestFieldMapperIssueToBeads(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	now := time.Now()
	gh := &Issue{
		ID:     100,
		Number: 42,
		Title:  "Test issue",
		Body:   "Test body",
		State:  "open",
		Labels: []Label{
			{Name: "type::bug"},
			{Name: "priority::high"},
		},
		CreatedAt: &now,
		UpdatedAt: &now,
		HTMLURL:   "https://github.com/org/repo/issues/42",
	}

	ti := &tracker.TrackerIssue{
		ID:         "100",
		Identifier: "42",
		Title:      "Test issue",
		Raw:        gh,
	}

	conv := m.IssueToBeads(ti)
	if conv == nil {
		t.Fatal("IssueToBeads returned nil")
	}
	if conv.Issue == nil {
		t.Fatal("IssueToBeads returned nil issue")
	}
	if conv.Issue.Title != "Test issue" {
		t.Errorf("Issue.Title = %q, want %q", conv.Issue.Title, "Test issue")
	}
	if conv.Issue.IssueType != types.TypeBug {
		t.Errorf("Issue.IssueType = %q, want %q", conv.Issue.IssueType, types.TypeBug)
	}
}

func TestFieldMapperIssueToBeads_NilRaw(t *testing.T) {
	m := &githubFieldMapper{config: DefaultMappingConfig()}

	ti := &tracker.TrackerIssue{
		ID:  "100",
		Raw: nil, // Not a *github.Issue
	}

	conv := m.IssueToBeads(ti)
	if conv != nil {
		t.Error("IssueToBeads with nil Raw should return nil")
	}
}

// TestGetConfig_YamlOnlyKeyBypassesStore is a regression test for the bug where
// `bd github sync` errored "GitHub token not configured" even though the token
// was set in .beads/config.yaml. The github tracker's getConfig used to read
// every key from the Dolt store, but github.token is a yaml-only secret key
// (never written to the DB). The fix routes yaml-only keys through config.yaml
// and env var, skipping the store entirely.
//
// We verify the store-bypass by passing a nil store: before the fix this would
// panic on the store deref; after the fix it falls through to GITHUB_TOKEN.
func TestGetConfig_YamlOnlyKeyBypassesStore(t *testing.T) {
	ctx := context.Background()
	tr := &Tracker{store: nil}

	t.Run("falls back to env var", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "env-token-value")
		got := tr.getConfig(ctx, "github.token", "GITHUB_TOKEN")
		if got != "env-token-value" {
			t.Errorf("getConfig(github.token) = %q, want %q", got, "env-token-value")
		}
	})

	t.Run("returns empty when no value is set", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		got := tr.getConfig(ctx, "github.token", "GITHUB_TOKEN")
		if got != "" {
			t.Errorf("getConfig(github.token) = %q, want empty", got)
		}
	})
}

// TestGetConfig_YamlOnlyKeyReadsFromYaml is the positive case for the fix:
// when github.token is present in .beads/config.yaml, getConfig must return
// that value (and not depend on GITHUB_TOKEN being set). This is the primary
// behavior the PR restores; the BypassesStore test only proves the env-var
// fallback path. Together they cover both branches of the yaml-only handler.
func TestGetConfig_YamlOnlyKeyReadsFromYaml(t *testing.T) {
	const wantToken = "yaml-config-token-value"

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	yamlBody := "github.token: \"" + wantToken + "\"\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	// Isolate from the developer's environment so we read only the temp yaml.
	t.Setenv("GITHUB_TOKEN", "")
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

	// Sanity-check that the yaml actually loaded — otherwise a getConfig
	// failure could be mistaken for a tracker bug.
	if got := config.GetString("github.token"); got != wantToken {
		t.Fatalf("config.GetString(github.token) = %q, want %q (yaml not loaded?)", got, wantToken)
	}

	tr := &Tracker{store: nil}
	got := tr.getConfig(context.Background(), "github.token", "GITHUB_TOKEN")
	if got != wantToken {
		t.Errorf("getConfig(github.token) = %q, want %q (yaml value)", got, wantToken)
	}
}

func TestValidate(t *testing.T) {
	tr := &Tracker{}
	if err := tr.Validate(); err == nil {
		t.Error("Validate() on uninitialized tracker should return error")
	}

	tr.client = &Client{} // Set client
	if err := tr.Validate(); err != nil {
		t.Errorf("Validate() on initialized tracker returned error: %v", err)
	}
}
