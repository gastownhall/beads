// Package main provides the bd CLI commands.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// TestGitLabConfigFromEnv verifies config is read from environment variables.
func TestGitLabConfigFromEnv(t *testing.T) {
	// Clear global state to avoid stale connections from prior tests
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	// Set environment variables
	t.Setenv("GITLAB_URL", "https://gitlab.example.com")
	t.Setenv("GITLAB_TOKEN", "test-token-123")
	t.Setenv("GITLAB_PROJECT_ID", "42")

	config := getGitLabConfig()

	if config.URL != "https://gitlab.example.com" {
		t.Errorf("URL = %q, want %q", config.URL, "https://gitlab.example.com")
	}
	if config.Token != "test-token-123" {
		t.Errorf("Token = %q, want %q", config.Token, "test-token-123")
	}
	if config.ProjectID != "42" {
		t.Errorf("ProjectID = %q, want %q", config.ProjectID, "42")
	}
}

// TestGitLabConfigValueYamlTokenReadsFromYaml verifies that the yaml-only
// secret key gitlab.token is resolved from config.yaml by the CLI-layer
// reader used to build the `bd gitlab sync` client. Before the yaml-only
// fold-in (upstream 99653e059), getGitLabConfigValue only read the Dolt store
// and env, so a config.yaml-stored token returned "" here. The store/dbPath
// globals are cleared to prove the value comes from config.yaml, not the store.
func TestGitLabConfigValueYamlTokenReadsFromYaml(t *testing.T) {
	const wantToken = "yaml-cli-token-value"

	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

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

	if got := getGitLabConfigValue(context.Background(), "gitlab.token"); got != wantToken {
		t.Errorf("getGitLabConfigValue(gitlab.token) = %q, want %q (config.yaml not consulted?)", got, wantToken)
	}
}

// TestGitLabConfigValueYamlTokenFallsBackToEnv verifies that when no
// config.yaml value is present, the yaml-only key path still falls back to the
// environment variable rather than the Dolt store.
func TestGitLabConfigValueYamlTokenFallsBackToEnv(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	tmpDir := t.TempDir()
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	t.Setenv("HOME", filepath.Join(tmpDir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("GITLAB_TOKEN", "env-cli-token-value")
	t.Chdir(tmpDir)

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	if got := getGitLabConfigValue(context.Background(), "gitlab.token"); got != "env-cli-token-value" {
		t.Errorf("getGitLabConfigValue(gitlab.token) = %q, want env fallback", got)
	}
}

func TestGitLabPushHooksMilestoneContentEqualSkipsOlderRemote(t *testing.T) {
	now := time.Now().UTC()
	ref := "https://gitlab.example.com/group/project/-/milestones/4"
	local := &types.Issue{
		ID:          "bd-epic",
		Title:       "Live milestone",
		Description: "already synced\n",
		Status:      types.StatusOpen,
		IssueType:   types.TypeEpic,
		ExternalRef: &ref,
		UpdatedAt:   now,
	}
	remote := &tracker.TrackerIssue{
		Identifier:  "35",
		URL:         ref,
		Title:       "Live milestone",
		Description: "already synced",
		State:       "active",
		UpdatedAt:   now.Add(-time.Minute),
	}

	hooks := buildGitLabPushHooks()
	if hooks == nil || hooks.ContentEqual == nil {
		t.Fatal("expected GitLab ContentEqual hook")
	}
	if !hooks.ContentEqual(local, remote) {
		t.Fatal("expected unchanged milestone content to skip even when remote updated_at is older")
	}
}

func TestGitLabPushHooksMilestoneContentChangeDoesNotSkipOlderRemote(t *testing.T) {
	now := time.Now().UTC()
	local := &types.Issue{
		ID:          "bd-epic",
		Title:       "Live milestone",
		Description: "local change",
		Status:      types.StatusOpen,
		IssueType:   types.TypeEpic,
		UpdatedAt:   now,
	}
	remote := &tracker.TrackerIssue{
		Identifier:  "35",
		Title:       "Live milestone",
		Description: "old remote",
		State:       "active",
		UpdatedAt:   now.Add(-time.Minute),
	}

	hooks := buildGitLabPushHooks()
	if hooks.ContentEqual(local, remote) {
		t.Fatal("expected changed milestone content to update when remote updated_at is older")
	}
}

func TestGitLabPushHooksPreserveTimestampSkipForIssues(t *testing.T) {
	now := time.Now().UTC()
	local := &types.Issue{
		ID:        "bd-task",
		Title:     "Task",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		UpdatedAt: now,
	}
	remote := &tracker.TrackerIssue{
		Identifier: "42",
		Title:      "Remote task",
		State:      "opened",
		UpdatedAt:  now.Add(time.Minute),
	}

	hooks := buildGitLabPushHooks()
	if !hooks.ContentEqual(local, remote) {
		t.Fatal("expected same-or-newer remote issue timestamp to preserve default skip behavior")
	}

	remote.UpdatedAt = now.Add(-time.Minute)
	if hooks.ContentEqual(local, remote) {
		t.Fatal("expected older remote issue timestamp to preserve default update behavior")
	}
}

// TestGitLabConfigValidation verifies validation catches missing required fields.
func TestGitLabConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		config    GitLabConfig
		wantError string
	}{
		{
			name:      "missing URL",
			config:    GitLabConfig{Token: "tok", ProjectID: "1"},
			wantError: "gitlab.url",
		},
		{
			name:      "missing token",
			config:    GitLabConfig{URL: "https://gitlab.com", ProjectID: "1"},
			wantError: "gitlab.token",
		},
		{
			name:      "missing project_id and group_id",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok"},
			wantError: "gitlab.project_id or gitlab.group_id",
		},
		{
			name:      "all present",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok", ProjectID: "1"},
			wantError: "",
		},
		{
			name:      "group_id only (no project_id) is valid",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok", GroupID: "mygroup"},
			wantError: "",
		},
		{
			name:      "group_id with default_project_id is valid",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok", GroupID: "mygroup", DefaultProjectID: "123"},
			wantError: "",
		},
		{
			name:      "both project_id and group_id is valid",
			config:    GitLabConfig{URL: "https://gitlab.com", Token: "tok", ProjectID: "1", GroupID: "mygroup"},
			wantError: "",
		},
		{
			name:      "plain HTTP rejected",
			config:    GitLabConfig{URL: "http://gitlab.example.com", Token: "tok", ProjectID: "1"},
			wantError: "HTTPS",
		},
		{
			name:      "localhost HTTP allowed",
			config:    GitLabConfig{URL: "http://localhost:8080", Token: "tok", ProjectID: "1"},
			wantError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGitLabConfig(tt.config)
			if tt.wantError == "" {
				if err != nil {
					t.Errorf("validateGitLabConfig() = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Error("validateGitLabConfig() = nil, want error")
				} else if !strings.Contains(err.Error(), tt.wantError) {
					t.Errorf("validateGitLabConfig() = %v, want error containing %q", err, tt.wantError)
				}
			}
		})
	}
}

// TestMaskGitLabToken verifies token masking for display.
func TestMaskGitLabToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "normal token",
			token: "glpat-xxxxxxxxxxxxxxxxxxxx",
			want:  "glpa****",
		},
		{
			name:  "short token",
			token: "abc",
			want:  "****",
		},
		{
			name:  "empty token",
			token: "",
			want:  "(not set)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskGitLabToken(tt.token)
			if got != tt.want {
				t.Errorf("maskGitLabToken(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

// TestGitLabConfigEnvVar verifies environment variable mapping.
func TestGitLabConfigEnvVar(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"gitlab.url", "GITLAB_URL"},
		{"gitlab.token", "GITLAB_TOKEN"},
		{"gitlab.project_id", "GITLAB_PROJECT_ID"},
		{"gitlab.group_id", "GITLAB_GROUP_ID"},
		{"gitlab.default_project_id", "GITLAB_DEFAULT_PROJECT_ID"},
		{"gitlab.filter_labels", "GITLAB_FILTER_LABELS"},
		{"gitlab.filter_project", "GITLAB_FILTER_PROJECT"},
		{"gitlab.filter_milestone", "GITLAB_FILTER_MILESTONE"},
		{"gitlab.filter_assignee", "GITLAB_FILTER_ASSIGNEE"},
		{"gitlab.unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := gitlabConfigToEnvVar(tt.key)
			if got != tt.want {
				t.Errorf("gitlabConfigToEnvVar(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// TestGitLabClientCreation verifies client is created with correct config.
func TestGitLabClientCreation(t *testing.T) {
	// Clear global state to avoid stale connections from prior tests
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("GITLAB_URL", "https://gitlab.test.com")
	t.Setenv("GITLAB_TOKEN", "test-token-abc")
	t.Setenv("GITLAB_PROJECT_ID", "99")

	config := getGitLabConfig()
	client := getGitLabClient(config)

	if client.BaseURL != "https://gitlab.test.com" {
		t.Errorf("client.BaseURL = %q, want %q", client.BaseURL, "https://gitlab.test.com")
	}
	if client.Token != "test-token-abc" {
		t.Errorf("client.Token = %q, want %q", client.Token, "test-token-abc")
	}
	if client.ProjectID != "99" {
		t.Errorf("client.ProjectID = %q, want %q", client.ProjectID, "99")
	}
}

// TestGitLabConfigFromEnv_GroupID verifies group config is read from environment variables.
func TestGitLabConfigFromEnv_GroupID(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("GITLAB_URL", "https://gitlab.example.com")
	t.Setenv("GITLAB_TOKEN", "test-token-123")
	t.Setenv("GITLAB_GROUP_ID", "mygroup")
	t.Setenv("GITLAB_DEFAULT_PROJECT_ID", "456")

	config := getGitLabConfig()

	if config.GroupID != "mygroup" {
		t.Errorf("GroupID = %q, want %q", config.GroupID, "mygroup")
	}
	if config.DefaultProjectID != "456" {
		t.Errorf("DefaultProjectID = %q, want %q", config.DefaultProjectID, "456")
	}
}

// TestGitLabClientCreation_WithGroupID verifies client is created with GroupID when configured.
func TestGitLabClientCreation_WithGroupID(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("GITLAB_URL", "https://gitlab.test.com")
	t.Setenv("GITLAB_TOKEN", "test-token-abc")
	t.Setenv("GITLAB_PROJECT_ID", "99")
	t.Setenv("GITLAB_GROUP_ID", "mygroup")

	config := getGitLabConfig()
	client := getGitLabClient(config)

	if client.GroupID != "mygroup" {
		t.Errorf("client.GroupID = %q, want %q", client.GroupID, "mygroup")
	}
	if client.ProjectID != "99" {
		t.Errorf("client.ProjectID = %q, want %q", client.ProjectID, "99")
	}
}

// TestGitLabCmdRegistration verifies the gitlab command and subcommands are registered.
func TestGitLabCmdRegistration(t *testing.T) {
	// Check that gitlabCmd has expected subcommands
	subcommands := gitlabCmd.Commands()

	var hasSync, hasStatus, hasProjects bool
	for _, cmd := range subcommands {
		switch cmd.Name() {
		case "sync":
			hasSync = true
		case "status":
			hasStatus = true
		case "projects":
			hasProjects = true
		}
	}

	if !hasSync {
		t.Error("gitlabCmd missing 'sync' subcommand")
	}
	if !hasStatus {
		t.Error("gitlabCmd missing 'status' subcommand")
	}
	if !hasProjects {
		t.Error("gitlabCmd missing 'projects' subcommand")
	}
}

// TestBuildCLIFilter_NoFlags verifies nil when no flags set.
func TestBuildCLIFilter_NoFlags(t *testing.T) {
	// Save and restore global flag state
	savedLabel, savedProject, savedMilestone, savedAssignee := gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee
	t.Cleanup(func() {
		gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee = savedLabel, savedProject, savedMilestone, savedAssignee
	})

	gitlabFilterLabel = ""
	gitlabFilterProject = ""
	gitlabFilterMilestone = ""
	gitlabFilterAssignee = ""

	filter := buildCLIFilter()
	if filter != nil {
		t.Errorf("buildCLIFilter() = %+v, want nil when no flags set", filter)
	}
}

// TestBuildCLIFilter_WithFlags verifies filter is built from flags.
func TestBuildCLIFilter_WithFlags(t *testing.T) {
	savedLabel, savedProject, savedMilestone, savedAssignee := gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee
	t.Cleanup(func() {
		gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee = savedLabel, savedProject, savedMilestone, savedAssignee
	})

	gitlabFilterLabel = "bug,backend"
	gitlabFilterProject = "42"
	gitlabFilterMilestone = "Sprint 1"
	gitlabFilterAssignee = "kyriakos"

	filter := buildCLIFilter()
	if filter == nil {
		t.Fatal("buildCLIFilter() = nil, want non-nil")
	}
	if filter.Labels != "bug,backend" {
		t.Errorf("Labels = %q, want %q", filter.Labels, "bug,backend")
	}
	if filter.ProjectID != 42 {
		t.Errorf("ProjectID = %d, want 42", filter.ProjectID)
	}
	if filter.Milestone != "Sprint 1" {
		t.Errorf("Milestone = %q, want %q", filter.Milestone, "Sprint 1")
	}
	if filter.Assignee != "kyriakos" {
		t.Errorf("Assignee = %q, want %q", filter.Assignee, "kyriakos")
	}
}

// TestBuildCLIFilter_PartialFlags verifies filter works with some flags.
func TestBuildCLIFilter_PartialFlags(t *testing.T) {
	savedLabel, savedProject, savedMilestone, savedAssignee := gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee
	t.Cleanup(func() {
		gitlabFilterLabel, gitlabFilterProject, gitlabFilterMilestone, gitlabFilterAssignee = savedLabel, savedProject, savedMilestone, savedAssignee
	})

	gitlabFilterLabel = "frontend"
	gitlabFilterProject = ""
	gitlabFilterMilestone = ""
	gitlabFilterAssignee = ""

	filter := buildCLIFilter()
	if filter == nil {
		t.Fatal("buildCLIFilter() = nil, want non-nil")
	}
	if filter.Labels != "frontend" {
		t.Errorf("Labels = %q, want %q", filter.Labels, "frontend")
	}
	if filter.ProjectID != 0 {
		t.Errorf("ProjectID = %d, want 0", filter.ProjectID)
	}
}

// TestSyncCmdHasFilterFlags verifies filter flags are registered on sync command.
func TestSyncCmdHasFilterFlags(t *testing.T) {
	flags := []string{"label", "project", "milestone", "assignee", "type", "exclude-type", "no-ephemeral"}
	for _, name := range flags {
		f := gitlabSyncCmd.Flags().Lookup(name)
		if f == nil {
			t.Errorf("sync command missing --%s flag", name)
		}
	}
}

// TestParseTypeList verifies comma-separated type parsing.
func TestParseTypeList(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"epic", 1},
		{"epic,task,feature", 3},
		{" epic , task ", 2},
	}
	for _, tt := range tests {
		got := parseTypeList(tt.input)
		if len(got) != tt.want {
			t.Errorf("parseTypeList(%q) len = %d, want %d", tt.input, len(got), tt.want)
		}
	}
}

// TestNoEphemeralDefaultTrue verifies --no-ephemeral defaults to true.
func TestNoEphemeralDefaultTrue(t *testing.T) {
	f := gitlabSyncCmd.Flags().Lookup("no-ephemeral")
	if f == nil {
		t.Fatal("missing --no-ephemeral flag")
	}
	if f.DefValue != "true" {
		t.Errorf("--no-ephemeral default = %q, want %q", f.DefValue, "true")
	}
}

func TestFilterGitLabLinkScopedIssues(t *testing.T) {
	issues := []*types.Issue{
		{ID: "bd-parent", IssueType: types.TypeFeature, Status: types.StatusOpen},
		{ID: "bd-child", IssueType: types.TypeTask, Status: types.StatusOpen},
		{ID: "bd-other", IssueType: types.TypeTask, Status: types.StatusOpen},
		{ID: "bd-mol", IssueType: types.TypeMolecule, Status: types.StatusOpen},
		{ID: "bd-wisp", IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true},
	}

	t.Run("issues flag limits dependency owners", func(t *testing.T) {
		got := filterGitLabLinkScopedIssues(issues, tracker.SyncOptions{
			IssueIDs: []string{"bd-child", "bd-other"},
		}, nil)

		assertIssueIDs(t, got, []string{"bd-child", "bd-other"})
	})

	t.Run("parent scope limits to descendants", func(t *testing.T) {
		got := filterGitLabLinkScopedIssues(issues, tracker.SyncOptions{}, map[string]bool{
			"bd-parent": true,
			"bd-child":  true,
		})

		assertIssueIDs(t, got, []string{"bd-parent", "bd-child"})
	})

	t.Run("type and internal filters apply", func(t *testing.T) {
		got := filterGitLabLinkScopedIssues(issues, tracker.SyncOptions{
			TypeFilter:       []types.IssueType{types.TypeTask},
			ExcludeTypes:     []types.IssueType{types.TypeMolecule},
			ExcludeEphemeral: true,
		}, nil)

		assertIssueIDs(t, got, []string{"bd-child", "bd-other"})
	})
}

func TestCollectGitLabLinkSyncDataScopesEndpoints(t *testing.T) {
	child := gitLabSyncIssue("bd-child", "https://gitlab.example.com/group/project/-/issues/10", types.TypeTask)
	blocker := gitLabSyncIssue("bd-blocker", "https://gitlab.example.com/group/project/-/issues/20", types.TypeTask)
	outside := gitLabSyncIssue("bd-outside", "https://gitlab.example.com/group/project/-/issues/30", types.TypeTask)
	molecule := gitLabSyncIssue("bd-mol", "https://gitlab.example.com/group/project/-/issues/40", types.TypeMolecule)
	parent := gitLabSyncIssue("bd-parent", "", types.TypeFeature)

	st := &gitLabSyncFakeStore{
		issues: []*types.Issue{parent, child, blocker, outside, molecule},
		deps: map[string][]*types.IssueWithDependencyMetadata{
			child.ID: {
				gitLabSyncDep(blocker, types.DepBlocks),
				gitLabSyncDep(outside, types.DepBlocks),
				gitLabSyncDep(molecule, types.DepBlocks),
			},
		},
		dependents: map[string][]*types.IssueWithDependencyMetadata{
			parent.ID: {gitLabSyncDep(child, types.DepParentChild)},
		},
	}

	t.Run("issues requires both endpoints", func(t *testing.T) {
		data, warnings := collectGitLabLinkSyncData(context.Background(), st, tracker.SyncOptions{
			IssueIDs: []string{child.ID, blocker.ID},
		})
		if len(warnings) != 0 {
			t.Fatalf("warnings = %v", warnings)
		}
		if len(data.DesiredLinks) != 1 {
			t.Fatalf("DesiredLinks len = %d, want 1", len(data.DesiredLinks))
		}
		link := data.DesiredLinks[0]
		if link.SourceIID != 20 || link.TargetIID != 10 || link.LinkType != "blocks" {
			t.Fatalf("link = %+v, want #20 blocks #10", link)
		}
	})

	t.Run("issues excludes out of scope targets", func(t *testing.T) {
		data, warnings := collectGitLabLinkSyncData(context.Background(), st, tracker.SyncOptions{
			IssueIDs: []string{child.ID},
		})
		if len(warnings) != 0 {
			t.Fatalf("warnings = %v", warnings)
		}
		if len(data.DesiredLinks) != 0 {
			t.Fatalf("DesiredLinks len = %d, want 0", len(data.DesiredLinks))
		}
	})

	t.Run("parent excludes targets outside subtree", func(t *testing.T) {
		data, warnings := collectGitLabLinkSyncData(context.Background(), st, tracker.SyncOptions{
			ParentID: parent.ID,
		})
		if len(warnings) != 0 {
			t.Fatalf("warnings = %v", warnings)
		}
		if len(data.DesiredLinks) != 0 {
			t.Fatalf("DesiredLinks len = %d, want 0", len(data.DesiredLinks))
		}
	})

	t.Run("type filter excludes target endpoint", func(t *testing.T) {
		data, warnings := collectGitLabLinkSyncData(context.Background(), st, tracker.SyncOptions{
			IssueIDs:     []string{child.ID, molecule.ID},
			ExcludeTypes: []types.IssueType{types.TypeMolecule},
		})
		if len(warnings) != 0 {
			t.Fatalf("warnings = %v", warnings)
		}
		if len(data.DesiredLinks) != 0 {
			t.Fatalf("DesiredLinks len = %d, want 0", len(data.DesiredLinks))
		}
	})
}

func TestGitLabSyncResultJSONIncludesLinksPushed(t *testing.T) {
	data, err := json.Marshal(gitlabSyncResult{LinksPushed: 2})
	if err != nil {
		t.Fatalf("Marshal gitlabSyncResult: %v", err)
	}
	if !strings.Contains(string(data), `"links_pushed":2`) {
		t.Fatalf("JSON = %s, want links_pushed", string(data))
	}
}

func TestGitLabSyncResultJSONLicenseSkipped(t *testing.T) {
	// Present when non-zero (distinct machine-readable signal from warnings/errors).
	data, err := json.Marshal(gitlabSyncResult{LinksLicenseSkipped: 3})
	if err != nil {
		t.Fatalf("Marshal gitlabSyncResult: %v", err)
	}
	if !strings.Contains(string(data), `"links_license_skipped":3`) {
		t.Fatalf("JSON = %s, want links_license_skipped", string(data))
	}
	// Omitted when zero so a normal sync doesn't carry noise.
	data, _ = json.Marshal(gitlabSyncResult{LinksPushed: 1})
	if strings.Contains(string(data), "links_license_skipped") {
		t.Fatalf("JSON = %s, should omit links_license_skipped when zero", string(data))
	}
}

func TestGitLabLicenseSkipMessage(t *testing.T) {
	one := gitLabLicenseSkipMessage(1)
	if !strings.Contains(one, "1 dependency 'blocks' link:") {
		t.Fatalf("singular message = %q, want singular 'link'", one)
	}
	many := gitLabLicenseSkipMessage(2)
	if !strings.Contains(many, "2 dependency 'blocks' links:") {
		t.Fatalf("plural message = %q, want plural 'links'", many)
	}
	// Must be actionable: name the tier and reassure the rest applied.
	for _, want := range []string{"Premium/Ultimate", "relates_to", "milestones"} {
		if !strings.Contains(many, want) {
			t.Fatalf("message = %q, missing %q", many, want)
		}
	}
}

type gitLabSyncFakeStore struct {
	storage.Storage
	issues     []*types.Issue
	deps       map[string][]*types.IssueWithDependencyMetadata
	dependents map[string][]*types.IssueWithDependencyMetadata
}

func (s *gitLabSyncFakeStore) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	return s.issues, nil
}

func (s *gitLabSyncFakeStore) GetDependenciesWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.deps[issueID], nil
}

func (s *gitLabSyncFakeStore) GetDependentsWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.dependents[issueID], nil
}

func gitLabSyncIssue(id, ref string, issueType types.IssueType) *types.Issue {
	issue := &types.Issue{
		ID:        id,
		IssueType: issueType,
		Status:    types.StatusOpen,
	}
	if ref != "" {
		issue.ExternalRef = &ref
	}
	return issue
}

func gitLabSyncDep(issue *types.Issue, depType types.DependencyType) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{
		Issue:          *issue,
		DependencyType: depType,
	}
}

func assertIssueIDs(t *testing.T, issues []*types.Issue, want []string) {
	t.Helper()
	if len(issues) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(issues), len(want), want)
	}
	for i, issue := range issues {
		if issue.ID != want[i] {
			t.Fatalf("issue[%d] = %s, want %s", i, issue.ID, want[i])
		}
	}
}
