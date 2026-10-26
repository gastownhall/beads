package gitlab

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
	factory := tracker.Get("gitlab")
	if factory == nil {
		t.Fatal("gitlab tracker not registered")
	}
	tr := factory()
	if tr.Name() != "gitlab" {
		t.Errorf("Name() = %q, want %q", tr.Name(), "gitlab")
	}
	if tr.DisplayName() != "GitLab" {
		t.Errorf("DisplayName() = %q, want %q", tr.DisplayName(), "GitLab")
	}
	if tr.ConfigPrefix() != "gitlab" {
		t.Errorf("ConfigPrefix() = %q, want %q", tr.ConfigPrefix(), "gitlab")
	}
}

func TestIsExternalRef(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://gitlab.com/group/project/-/issues/42", true},
		{"https://my-gitlab.example.com/team/repo/-/issues/123", true},
		{"https://gitlab.com/group/project/-/work_items/42", true},
		{"https://linear.app/team/issue/PROJ-123", false},
		{"https://github.com/org/repo/issues/1", false},
		{"", false},
		// Shorthand format produced by BuildExternalRef
		{"gitlab:681509", true},
		{"gitlab:1", true},
		{"gitlab:abc", false},
		{"gitlab:123/extra", false},
		{"gitlab:0", false},
	}
	for _, tt := range tests {
		if got := tr.IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

func TestIsExternalRefSelfHostedURL(t *testing.T) {
	tr := &Tracker{client: NewClient("tok", "https://nova.teachx.ai", "42")}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://nova.teachx.ai/group/project/-/work_items/24", true},
		{"https://nova.teachx.ai/group/project/-/issues/24", true},
		{"https://NOVA.teachx.ai/group/project/-/issues/24", true},
		// Scheme is not compared, only host: an http:// ref on an https://
		// instance is claimed.
		{"http://nova.teachx.ai/group/project/-/issues/24", true},
		{"https://other.example.com/group/project/-/work_items/24", false},
		{"https://github.com/org/repo/issues/1", false},
		{"https://nova.teachx.ai/group/project/-/merge_requests/24", false},
		{"", false},
		// Regression pins for the pre-existing arms, not the host check: the
		// milestone row is claimed by the "milestones" substring and the
		// shorthand returns at glShorthandPattern, both before the host check
		// runs. They still pass if onConfiguredHost is deleted.
		{"https://nova.teachx.ai/group/project/-/milestones/5", true},
		{"gitlab:24", true},
	}
	for _, tt := range tests {
		if got := tr.IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}

	// A GitLab served from a sub-path only claims URLs under that path.
	sub := &Tracker{client: NewClient("tok", "https://corp.example.com/git", "42")}
	if !sub.IsExternalRef("https://corp.example.com/git/g/p/-/issues/3") {
		t.Error("sub-path host URL should be recognized")
	}
	if sub.IsExternalRef("https://corp.example.com/wiki/g/p/-/issues/3") {
		t.Error("URL outside the configured sub-path should be rejected")
	}

	// Port is part of the host comparison, so gitlab.url has to name the host
	// the way GitLab spells it in web_url. A non-default port must be present
	// in the ref; conversely, a base that spells a port the web URLs omit
	// matches nothing and the ref falls through to the legacy arms.
	port := &Tracker{client: NewClient("tok", "https://gl.corp:8443", "42")}
	if !port.IsExternalRef("https://gl.corp:8443/g/p/-/issues/3") {
		t.Error("URL on the configured host:port should be recognized")
	}
	if port.IsExternalRef("https://gl.corp/g/p/-/issues/3") {
		t.Error("URL omitting the configured non-default port should be rejected")
	}
}

func TestExtractIdentifier(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want string
	}{
		{"https://gitlab.com/group/project/-/issues/42", "42"},
		{"https://gitlab.example.com/team/repo/-/issues/123", "123"},
		{"https://gitlab.com/group/project/-/work_items/42", "42"},
		{"https://gitlab.com/group/project/-/milestones/4", "milestone:4"},
		{"not-a-url", ""},
		// Shorthand format
		{"gitlab:681509", "681509"},
		{"gitlab:1", "1"},
		{"gitlab:abc", ""},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestTrackerMatchKey(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{"issue url", "https://gitlab.com/group/project/-/issues/42", "gl://gitlab.com/group/project#42"},
		{"work item shares issue key", "https://gitlab.com/group/project/-/work_items/42", "gl://gitlab.com/group/project#42"},
		{"legacy issues url", "https://gitlab.com/group/project/issues/42", "gl://gitlab.com/group/project#42"},
		{"legacy work item url", "https://gitlab.com/group/project/work_items/42", "gl://gitlab.com/group/project#42"},
		{"host case ignored", "https://GITLAB.com/group/project/-/issues/42", "gl://gitlab.com/group/project#42"},
		{"path case ignored", "https://gitlab.com/Group/Project/-/issues/42", "gl://gitlab.com/group/project#42"},
		{"default https port dropped", "https://gitlab.com:443/group/project/-/issues/42", "gl://gitlab.com/group/project#42"},
		{"default http port dropped", "http://gitlab.com:80/group/project/-/issues/42", "gl://gitlab.com/group/project#42"},
		{"non-default port kept", "https://gitlab.com:8443/group/project/-/issues/42", "gl://gitlab.com:8443/group/project#42"},
		{"trailing slash ignored", "https://gitlab.com/group/project/-/issues/42/", "gl://gitlab.com/group/project#42"},
		{"repeated slashes collapse", "https://gitlab.com//group//project/-/issues/42", "gl://gitlab.com/group/project#42"},
		{"query and fragment ignored", "https://gitlab.com/group/project/-/issues/42?utm=x#notes", "gl://gitlab.com/group/project#42"},
		{"nested group path", "https://gitlab.com/group/sub/project/-/issues/7", "gl://gitlab.com/group/sub/project#7"},
		{"sibling project full-path compared", "https://gitlab.com/group/b-other/-/issues/7", "gl://gitlab.com/group/b-other#7"},
		{"self-hosted host kept", "https://my-gitlab.example.com/team/repo/-/issues/7", "gl://my-gitlab.example.com/team/repo#7"},
		{"project milestone", "https://gitlab.com/group/project/-/milestones/5", "gl://gitlab.com/group/project#milestone:5"},
		{"group milestone", "https://gitlab.com/groups/mygroup/-/milestones/5", "gl://gitlab.com/groups/mygroup#milestone:5"},
		{"milestone namespaced away from issue iid", "https://gitlab.com/group/project/-/milestones/7", "gl://gitlab.com/group/project#milestone:7"},
		{"merge request takes no fallback", "https://gitlab.com/group/project/-/merge_requests/7", ""},
		{"iid zero rejected", "https://gitlab.com/group/project/-/issues/0", ""},
		{"leading zero rejected", "https://gitlab.com/group/project/-/issues/07", ""},
		{"no project path", "https://gitlab.com/-/issues/7", ""},
		{"empty separator only", "https://gitlab.com/-/issues/7/", ""},
		{"not a url", "not-a-url", ""},
		{"empty ref", "", ""},
	}
	for _, tt := range tests {
		if got := tr.MatchKey(tt.ref); got != tt.want {
			t.Errorf("%s: MatchKey(%q) = %q, want %q", tt.name, tt.ref, got, tt.want)
		}
	}

	if a, b := tr.MatchKey("https://gitlab.com/group/b/-/issues/7"), tr.MatchKey("https://gitlab.com/group/b-other/-/issues/7"); a == b {
		t.Errorf("sibling projects share match key %q", a)
	}
	if a, b := tr.MatchKey("https://gitlab.com/group/project/-/milestones/7"), tr.MatchKey("https://gitlab.com/group/project/-/issues/7"); a == b {
		t.Errorf("milestone and issue share match key %q", a)
	}

	// The "gitlab:{iid}" shorthand carries no project of its own. With the
	// configured project known without a network call (gitlab.project_path or
	// a path-form gitlab.project_id), it is qualified to that project's
	// namespace; otherwise it stays in a dedicated namespace no URL key can
	// equal, so it can never fallback-match another project's URL.
	shorthand := []struct {
		name string
		tr   *Tracker
		want string
	}{
		{"project path configured", &Tracker{projectPath: "team/repo", client: NewClient("tok", "https://gitlab.example.com", "42")}, "gl://gitlab.example.com/team/repo#7"},
		{"project path case ignored", &Tracker{projectPath: "Team/Repo", client: NewClient("tok", "https://gitlab.example.com", "42")}, "gl://gitlab.example.com/team/repo#7"},
		{"path-form project id", &Tracker{client: NewClient("tok", "https://gitlab.com", "team/repo")}, "gl://gitlab.com/team/repo#7"},
		{"numeric id without path", &Tracker{client: NewClient("tok", "https://gitlab.com", "42")}, "gitlab:7"},
		{"uninitialized tracker", &Tracker{}, "gitlab:7"},
	}
	for _, tt := range shorthand {
		if got := tt.tr.MatchKey("gitlab:7"); got != tt.want {
			t.Errorf("%s: MatchKey(gitlab:7) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestBuildExternalRef(t *testing.T) {
	tr := &Tracker{}
	ti := &tracker.TrackerIssue{
		URL:        "https://gitlab.com/group/project/-/issues/42",
		Identifier: "42",
	}
	ref := tr.BuildExternalRef(ti)
	if ref != ti.URL {
		t.Errorf("BuildExternalRef() = %q, want %q", ref, ti.URL)
	}
}

func TestFieldMapperStatus(t *testing.T) {
	m := &gitlabFieldMapper{config: DefaultMappingConfig()}

	if got := m.StatusToBeads("opened"); got != types.StatusOpen {
		t.Errorf("StatusToBeads(opened) = %q, want %q", got, types.StatusOpen)
	}
	if got := m.StatusToBeads("closed"); got != types.StatusClosed {
		t.Errorf("StatusToBeads(closed) = %q, want %q", got, types.StatusClosed)
	}
	if got := m.StatusToBeads("reopened"); got != types.StatusOpen {
		t.Errorf("StatusToBeads(reopened) = %q, want %q", got, types.StatusOpen)
	}
}

func TestFieldMapperPriority(t *testing.T) {
	m := &gitlabFieldMapper{config: DefaultMappingConfig()}

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

func TestGitLabToTrackerIssue(t *testing.T) {
	now := time.Now()
	gl := &Issue{
		ID:          100,
		IID:         42,
		Title:       "Fix pipeline",
		Description: "CI is broken",
		State:       "opened",
		WebURL:      "https://gitlab.com/group/project/-/issues/42",
		Labels:      []string{"bug", "priority::high"},
		CreatedAt:   &now,
		UpdatedAt:   &now,
		Assignee:    &User{ID: 5, Username: "bob"},
	}

	ti := gitlabToTrackerIssue(gl)

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
	if ti.Raw != gl {
		t.Error("Raw should reference original gitlab.Issue")
	}
	if len(ti.Labels) != 2 {
		t.Errorf("Labels count = %d, want 2", len(ti.Labels))
	}
}

// TestGetConfig_YamlOnlyKeyBypassesStore verifies that yaml-only keys
// (e.g. gitlab.token) bypass the Dolt store entirely. A nil store proves
// the store is never dereferenced; before the fix this would panic.
func TestGetConfig_YamlOnlyKeyBypassesStore(t *testing.T) {
	ctx := context.Background()
	tr := &Tracker{store: nil}

	t.Run("falls back to env var", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "env-token-value")
		got, err := tr.getConfig(ctx, "gitlab.token", "GITLAB_TOKEN")
		if err != nil {
			t.Fatalf("getConfig returned error: %v", err)
		}
		if got != "env-token-value" {
			t.Errorf("getConfig(gitlab.token) = %q, want %q", got, "env-token-value")
		}
	})

	t.Run("returns empty when no value is set", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "")
		got, err := tr.getConfig(ctx, "gitlab.token", "GITLAB_TOKEN")
		if err != nil {
			t.Fatalf("getConfig returned error: %v", err)
		}
		if got != "" {
			t.Errorf("getConfig(gitlab.token) = %q, want empty", got)
		}
	})
}

// TestGetConfig_YamlOnlyKeyReadsFromYaml verifies that gitlab.token is
// read from .beads/config.yaml when set there, without depending on the
// GITLAB_TOKEN env var.
func TestGetConfig_YamlOnlyKeyReadsFromYaml(t *testing.T) {
	const wantToken = "yaml-config-token-value"

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	yamlBody := "gitlab.token: \"" + wantToken + "\"\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	t.Setenv("GITLAB_TOKEN", "")
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

	if got := config.GetString("gitlab.token"); got != wantToken {
		t.Fatalf("config.GetString(gitlab.token) = %q, want %q (yaml not loaded?)", got, wantToken)
	}

	tr := &Tracker{store: nil}
	got, err := tr.getConfig(context.Background(), "gitlab.token", "GITLAB_TOKEN")
	if err != nil {
		t.Fatalf("getConfig returned error: %v", err)
	}
	if got != wantToken {
		t.Errorf("getConfig(gitlab.token) = %q, want %q (yaml value)", got, wantToken)
	}
}
