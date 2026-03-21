// Package main provides the bd CLI commands.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

// adoStdioMutex serializes tests that redirect os.Stdout/os.Stderr.
// Mirrors stdioMutex from test_helpers_test.go (cgo-only) so that
// ado_test.go can run without the cgo build tag.
var adoStdioMutex sync.Mutex

// TestADOConfigFromEnv verifies config is read from environment variables.
func TestADOConfigFromEnv(t *testing.T) {
	// Clear global state to avoid stale connections from prior tests
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("AZURE_DEVOPS_PAT", "test-pat-value")
	t.Setenv("AZURE_DEVOPS_ORG", "myorg")
	t.Setenv("AZURE_DEVOPS_PROJECT", "myproject")

	cfg := getADOConfig()

	if cfg.PAT != "test-pat-value" {
		t.Errorf("PAT = %q, want %q", cfg.PAT, "test-pat-value")
	}
	if cfg.Org != "myorg" {
		t.Errorf("Org = %q, want %q", cfg.Org, "myorg")
	}
	if cfg.Project != "myproject" {
		t.Errorf("Project = %q, want %q", cfg.Project, "myproject")
	}
}

// TestADOConfigFromEnvWithURL verifies custom URL is read from environment.
func TestADOConfigFromEnvWithURL(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("AZURE_DEVOPS_PAT", "pat")
	t.Setenv("AZURE_DEVOPS_URL", "https://tfs.corp.com/DefaultCollection")
	t.Setenv("AZURE_DEVOPS_PROJECT", "proj")

	cfg := getADOConfig()

	if cfg.URL != "https://tfs.corp.com/DefaultCollection" {
		t.Errorf("URL = %q, want %q", cfg.URL, "https://tfs.corp.com/DefaultCollection")
	}
}

// TestADOConfigValidation verifies validation catches missing required fields.
func TestADOConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		config    ADOConfig
		wantError string
	}{
		{
			name:      "missing PAT",
			config:    ADOConfig{Org: "org", Project: "proj"},
			wantError: "ado.pat",
		},
		{
			name:      "missing org and URL",
			config:    ADOConfig{PAT: "tok", Project: "proj"},
			wantError: "ado.org",
		},
		{
			name:      "missing project",
			config:    ADOConfig{PAT: "tok", Org: "org"},
			wantError: "ado.project",
		},
		{
			name:   "all present",
			config: ADOConfig{PAT: "tok", Org: "org", Project: "proj"},
		},
		{
			name:   "URL substitutes for org",
			config: ADOConfig{PAT: "tok", URL: "https://tfs.corp.com", Project: "proj"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateADOConfig(tt.config)
			if tt.wantError == "" {
				if err != nil {
					t.Errorf("validateADOConfig() = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Error("validateADOConfig() = nil, want error")
				} else if !strings.Contains(err.Error(), tt.wantError) {
					t.Errorf("validateADOConfig() = %v, want error containing %q", err, tt.wantError)
				}
			}
		})
	}
}

// TestMaskADOToken verifies token masking for display.
func TestMaskADOToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{name: "normal token", token: "abcdefghijklmnop", want: "abcd****"},
		{name: "short token", token: "abc", want: "****"},
		{name: "exactly 4 chars", token: "abcd", want: "****"},
		{name: "5 chars", token: "abcde", want: "abcd****"},
		{name: "empty token", token: "", want: "(not set)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskADOToken(tt.token)
			if got != tt.want {
				t.Errorf("maskADOToken(%q) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

// TestADOConfigEnvVar verifies environment variable mapping.
func TestADOConfigEnvVar(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"ado.pat", "AZURE_DEVOPS_PAT"},
		{"ado.org", "AZURE_DEVOPS_ORG"},
		{"ado.project", "AZURE_DEVOPS_PROJECT"},
		{"ado.url", "AZURE_DEVOPS_URL"},
		{"ado.unknown", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := adoConfigToEnvVar(tt.key)
			if got != tt.want {
				t.Errorf("adoConfigToEnvVar(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// TestADOConflictStrategy verifies mutual exclusion of conflict resolution flags.
func TestADOConflictStrategy(t *testing.T) {
	tests := []struct {
		name        string
		preferLocal bool
		preferADO   bool
		preferNewer bool
		want        ADOConflictStrategy
		wantErr     bool
	}{
		{
			name: "default is prefer-newer",
			want: ADOConflictPreferNewer,
		},
		{
			name:        "prefer-local",
			preferLocal: true,
			want:        ADOConflictPreferLocal,
		},
		{
			name:      "prefer-ado",
			preferADO: true,
			want:      ADOConflictPreferADO,
		},
		{
			name:        "prefer-newer explicit",
			preferNewer: true,
			want:        ADOConflictPreferNewer,
		},
		{
			name:        "local and ado conflict",
			preferLocal: true,
			preferADO:   true,
			wantErr:     true,
		},
		{
			name:        "local and newer conflict",
			preferLocal: true,
			preferNewer: true,
			wantErr:     true,
		},
		{
			name:        "ado and newer conflict",
			preferADO:   true,
			preferNewer: true,
			wantErr:     true,
		},
		{
			name:        "all three conflict",
			preferLocal: true,
			preferADO:   true,
			preferNewer: true,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getADOConflictStrategy(tt.preferLocal, tt.preferADO, tt.preferNewer)
			if tt.wantErr {
				if err == nil {
					t.Error("getADOConflictStrategy() = nil error, want error")
				}
			} else {
				if err != nil {
					t.Errorf("getADOConflictStrategy() error = %v, want nil", err)
				}
				if got != tt.want {
					t.Errorf("getADOConflictStrategy() = %q, want %q", got, tt.want)
				}
			}
		})
	}
}

// TestADOCmdRegistration verifies the ado command and subcommands are registered.
func TestADOCmdRegistration(t *testing.T) {
	subcommands := adoCmd.Commands()

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
		t.Error("adoCmd missing 'sync' subcommand")
	}
	if !hasStatus {
		t.Error("adoCmd missing 'status' subcommand")
	}
	if !hasProjects {
		t.Error("adoCmd missing 'projects' subcommand")
	}
}

// TestADOSyncFlagParsing verifies sync command flags are registered and parsed.
func TestADOSyncFlagParsing(t *testing.T) {
	flags := []string{
		"dry-run", "pull-only", "push-only",
		"prefer-local", "prefer-ado", "prefer-newer",
		"bootstrap-match", "no-create", "reconcile",
	}

	for _, flag := range flags {
		t.Run(flag, func(t *testing.T) {
			f := adoSyncCmd.Flags().Lookup(flag)
			if f == nil {
				t.Errorf("sync command missing flag --%s", flag)
			}
		})
	}
}

// TestADOSyncPullPushConflict verifies --pull-only and --push-only cannot be combined.
func TestADOSyncPullPushConflict(t *testing.T) {
	// Clear global state
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	// Set valid config so we get past config validation
	t.Setenv("AZURE_DEVOPS_PAT", "test-pat")
	t.Setenv("AZURE_DEVOPS_ORG", "testorg")
	t.Setenv("AZURE_DEVOPS_PROJECT", "testproj")

	// Save and restore the global flag state
	oldPullOnly := adoSyncPullOnly
	oldPushOnly := adoSyncPushOnly
	oldDryRun := adoSyncDryRun
	t.Cleanup(func() {
		adoSyncPullOnly = oldPullOnly
		adoSyncPushOnly = oldPushOnly
		adoSyncDryRun = oldDryRun
	})

	adoSyncPullOnly = true
	adoSyncPushOnly = true
	adoSyncDryRun = true // avoid readonly check

	err := runADOSync(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("runADOSync() should fail with both --pull-only and --push-only")
	}
	if !strings.Contains(err.Error(), "pull-only") || !strings.Contains(err.Error(), "push-only") {
		t.Errorf("error = %v, want mention of pull-only and push-only", err)
	}
}

// TestADOSyncMissingConfig verifies sync fails gracefully when config is missing.
func TestADOSyncMissingConfig(t *testing.T) {
	// Clear global state and env
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	// Ensure env vars are cleared
	t.Setenv("AZURE_DEVOPS_PAT", "")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "")
	t.Setenv("AZURE_DEVOPS_URL", "")

	err := runADOSync(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("runADOSync() should fail with missing config")
	}
	if !strings.Contains(err.Error(), "ado.pat") {
		t.Errorf("error = %v, want mention of ado.pat", err)
	}
}

// TestADOSyncMissingOrg verifies sync fails when only PAT is set.
func TestADOSyncMissingOrg(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("AZURE_DEVOPS_PAT", "test-pat")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "")
	t.Setenv("AZURE_DEVOPS_URL", "")

	err := runADOSync(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("runADOSync() should fail with missing org")
	}
	if !strings.Contains(err.Error(), "ado.org") {
		t.Errorf("error = %v, want mention of ado.org", err)
	}
}

// TestADOSyncMissingProject verifies sync fails when project is missing.
func TestADOSyncMissingProject(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("AZURE_DEVOPS_PAT", "test-pat")
	t.Setenv("AZURE_DEVOPS_ORG", "testorg")
	t.Setenv("AZURE_DEVOPS_PROJECT", "")
	t.Setenv("AZURE_DEVOPS_URL", "")

	err := runADOSync(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("runADOSync() should fail with missing project")
	}
	if !strings.Contains(err.Error(), "ado.project") {
		t.Errorf("error = %v, want mention of ado.project", err)
	}
}

// TestADOStatusTextOutput verifies the ado status command text output.
func TestADOStatusTextOutput(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	oldJSON := jsonOutput
	dbPath, store = "", nil
	jsonOutput = false
	t.Cleanup(func() {
		dbPath, store = oldDBPath, oldStore
		jsonOutput = oldJSON
	})

	t.Setenv("AZURE_DEVOPS_PAT", "abcdefghij")
	t.Setenv("AZURE_DEVOPS_ORG", "myorg")
	t.Setenv("AZURE_DEVOPS_PROJECT", "myproject")
	t.Setenv("AZURE_DEVOPS_URL", "")

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := runADOStatus(cmd, nil)
	if err != nil {
		t.Fatalf("runADOStatus() error = %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "myorg") {
		t.Errorf("output missing org, got:\n%s", output)
	}
	if !strings.Contains(output, "myproject") {
		t.Errorf("output missing project, got:\n%s", output)
	}
	if !strings.Contains(output, "abcd****") {
		t.Errorf("output missing masked PAT, got:\n%s", output)
	}
	if !strings.Contains(output, "Configured") {
		t.Errorf("output missing configured status, got:\n%s", output)
	}
}

// TestADOStatusUnconfigured verifies the status command output when not configured.
func TestADOStatusUnconfigured(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	oldJSON := jsonOutput
	dbPath, store = "", nil
	jsonOutput = false
	t.Cleanup(func() {
		dbPath, store = oldDBPath, oldStore
		jsonOutput = oldJSON
	})

	t.Setenv("AZURE_DEVOPS_PAT", "")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "")
	t.Setenv("AZURE_DEVOPS_URL", "")

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := runADOStatus(cmd, nil)
	if err != nil {
		t.Fatalf("runADOStatus() error = %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "Not configured") {
		t.Errorf("output missing 'Not configured', got:\n%s", output)
	}
	if !strings.Contains(output, "(not set)") {
		t.Errorf("output missing '(not set)' for empty PAT, got:\n%s", output)
	}
}

// TestADOStatusJSONConfigured verifies JSON output for a configured status.
func TestADOStatusJSONConfigured(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	oldJSON := jsonOutput
	dbPath, store = "", nil
	jsonOutput = true
	t.Cleanup(func() {
		dbPath, store = oldDBPath, oldStore
		jsonOutput = oldJSON
	})

	t.Setenv("AZURE_DEVOPS_PAT", "test-pat-1234")
	t.Setenv("AZURE_DEVOPS_ORG", "testorg")
	t.Setenv("AZURE_DEVOPS_PROJECT", "testproj")
	t.Setenv("AZURE_DEVOPS_URL", "")

	// outputJSON writes to os.Stdout, so we capture it
	output := captureADOStdout(t, func() {
		err := runADOStatus(&cobra.Command{}, nil)
		if err != nil {
			t.Fatalf("runADOStatus() error = %v", err)
		}
	})

	var result adoStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nraw: %s", err, output)
	}

	if !result.Configured {
		t.Error("expected Configured=true")
	}
	if !result.HasToken {
		t.Error("expected HasToken=true")
	}
	if result.Org != "testorg" {
		t.Errorf("Org = %q, want %q", result.Org, "testorg")
	}
	if result.Project != "testproj" {
		t.Errorf("Project = %q, want %q", result.Project, "testproj")
	}
	if result.Error != "" {
		t.Errorf("Error = %q, want empty", result.Error)
	}
}

// TestADOStatusJSONUnconfigured verifies JSON output when not configured.
func TestADOStatusJSONUnconfigured(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	oldJSON := jsonOutput
	dbPath, store = "", nil
	jsonOutput = true
	t.Cleanup(func() {
		dbPath, store = oldDBPath, oldStore
		jsonOutput = oldJSON
	})

	t.Setenv("AZURE_DEVOPS_PAT", "")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "")
	t.Setenv("AZURE_DEVOPS_URL", "")

	output := captureADOStdout(t, func() {
		err := runADOStatus(&cobra.Command{}, nil)
		if err != nil {
			t.Fatalf("runADOStatus() error = %v", err)
		}
	})

	var result adoStatusResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nraw: %s", err, output)
	}

	if result.Configured {
		t.Error("expected Configured=false")
	}
	if result.HasToken {
		t.Error("expected HasToken=false")
	}
	if result.Error == "" {
		t.Error("expected non-empty Error")
	}
}

// TestADOStatusWithCustomURL verifies status output includes custom URL.
func TestADOStatusWithCustomURL(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	oldJSON := jsonOutput
	dbPath, store = "", nil
	jsonOutput = false
	t.Cleanup(func() {
		dbPath, store = oldDBPath, oldStore
		jsonOutput = oldJSON
	})

	t.Setenv("AZURE_DEVOPS_PAT", "test-pat-1234")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "proj")
	t.Setenv("AZURE_DEVOPS_URL", "https://tfs.corp.com/DefaultCollection")

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := runADOStatus(cmd, nil)
	if err != nil {
		t.Fatalf("runADOStatus() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "https://tfs.corp.com/DefaultCollection") {
		t.Errorf("output missing custom URL, got:\n%s", output)
	}
}

// TestADOProjectsWithMockServer tests ado projects using a mock HTTP server.
func TestADOProjectsWithMockServer(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	oldJSON := jsonOutput
	dbPath, store = "", nil
	jsonOutput = false
	t.Cleanup(func() {
		dbPath, store = oldDBPath, oldStore
		jsonOutput = oldJSON
	})

	// Mock ADO projects endpoint
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The ListProjects call hits {baseURL}/_apis/projects
		if strings.Contains(r.URL.Path, "/projects") {
			w.Header().Set("Content-Type", "application/json")
			resp := `{
				"count": 2,
				"value": [
					{"id": "1", "name": "Project Alpha", "description": "First project", "state": "wellFormed"},
					{"id": "2", "name": "Project Beta", "description": "", "state": "wellFormed"}
				]
			}`
			fmt.Fprint(w, resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	// Set env to point at mock server
	t.Setenv("AZURE_DEVOPS_PAT", "test-pat")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "proj")
	t.Setenv("AZURE_DEVOPS_URL", server.URL)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := runADOProjects(cmd, nil)
	if err != nil {
		t.Fatalf("runADOProjects() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Project Alpha") {
		t.Errorf("output missing 'Project Alpha', got:\n%s", output)
	}
	if !strings.Contains(output, "Project Beta") {
		t.Errorf("output missing 'Project Beta', got:\n%s", output)
	}
	if !strings.Contains(output, "First project") {
		t.Errorf("output missing description 'First project', got:\n%s", output)
	}
}

// TestADOProjectsJSONOutput tests ado projects JSON output with mock server.
func TestADOProjectsJSONOutput(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	oldJSON := jsonOutput
	dbPath, store = "", nil
	jsonOutput = true
	t.Cleanup(func() {
		dbPath, store = oldDBPath, oldStore
		jsonOutput = oldJSON
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/projects") {
			w.Header().Set("Content-Type", "application/json")
			resp := `{
				"count": 1,
				"value": [
					{"id": "1", "name": "TestProject", "description": "A test", "state": "wellFormed"}
				]
			}`
			fmt.Fprint(w, resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	t.Setenv("AZURE_DEVOPS_PAT", "test-pat")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "proj")
	t.Setenv("AZURE_DEVOPS_URL", server.URL)

	output := captureADOStdout(t, func() {
		err := runADOProjects(&cobra.Command{}, nil)
		if err != nil {
			t.Fatalf("runADOProjects() error = %v", err)
		}
	})

	// Verify it's valid JSON array
	var projects []json.RawMessage
	if err := json.Unmarshal([]byte(output), &projects); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nraw: %s", err, output)
	}
	if len(projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(projects))
	}

	// Verify project content
	var proj struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(projects[0], &proj); err != nil {
		t.Fatalf("failed to parse project: %v", err)
	}
	if proj.Name != "TestProject" {
		t.Errorf("project name = %q, want %q", proj.Name, "TestProject")
	}
}

// TestADOProjectsEmptyList tests ado projects with no projects found.
func TestADOProjectsEmptyList(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	oldJSON := jsonOutput
	dbPath, store = "", nil
	jsonOutput = false
	t.Cleanup(func() {
		dbPath, store = oldDBPath, oldStore
		jsonOutput = oldJSON
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/projects") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"count": 0, "value": []}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	t.Setenv("AZURE_DEVOPS_PAT", "test-pat")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "proj")
	t.Setenv("AZURE_DEVOPS_URL", server.URL)

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := runADOProjects(cmd, nil)
	if err != nil {
		t.Fatalf("runADOProjects() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No projects found") {
		t.Errorf("output missing 'No projects found', got:\n%s", output)
	}
}

// TestADOProjectsMissingPAT verifies projects fails without PAT.
func TestADOProjectsMissingPAT(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("AZURE_DEVOPS_PAT", "")
	t.Setenv("AZURE_DEVOPS_ORG", "org")
	t.Setenv("AZURE_DEVOPS_PROJECT", "proj")
	t.Setenv("AZURE_DEVOPS_URL", "")

	err := runADOProjects(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("runADOProjects() should fail with missing PAT")
	}
	if !strings.Contains(err.Error(), "ado.pat") {
		t.Errorf("error = %v, want mention of ado.pat", err)
	}
}

// TestADOProjectsMissingOrg verifies projects fails without org.
func TestADOProjectsMissingOrg(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	t.Setenv("AZURE_DEVOPS_PAT", "test-pat")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "proj")
	t.Setenv("AZURE_DEVOPS_URL", "")

	err := runADOProjects(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("runADOProjects() should fail with missing org")
	}
	if !strings.Contains(err.Error(), "ado.org") {
		t.Errorf("error = %v, want mention of ado.org", err)
	}
}

// TestADOClientCreation verifies client is created with correct config.
func TestADOClientCreation(t *testing.T) {
	cfg := ADOConfig{
		PAT:     "test-pat",
		Org:     "myorg",
		Project: "myproject",
	}
	client, err := getADOClient(cfg)
	if err != nil {
		t.Fatalf("getADOClient() returned error: %v", err)
	}
	if client == nil {
		t.Fatal("getADOClient() returned nil")
	}
	if client.Org != "myorg" {
		t.Errorf("client.Org = %q, want %q", client.Org, "myorg")
	}
	if client.Project != "myproject" {
		t.Errorf("client.Project = %q, want %q", client.Project, "myproject")
	}
}

// TestADOClientCreationWithURL verifies client with custom base URL.
func TestADOClientCreationWithURL(t *testing.T) {
	cfg := ADOConfig{
		PAT:     "test-pat",
		Org:     "",
		Project: "proj",
		URL:     "https://tfs.corp.com/DefaultCollection",
	}
	client, err := getADOClient(cfg)
	if err != nil {
		t.Fatalf("getADOClient() returned error: %v", err)
	}
	if client == nil {
		t.Fatal("getADOClient() returned nil")
	}
	if client.BaseURL != "https://tfs.corp.com/DefaultCollection" {
		t.Errorf("client.BaseURL = %q, want %q", client.BaseURL, "https://tfs.corp.com/DefaultCollection")
	}
}

// TestADOProjectsHTTPError verifies error handling for HTTP failures.
func TestADOProjectsHTTPError(t *testing.T) {
	oldDBPath, oldStore := dbPath, store
	dbPath, store = "", nil
	t.Cleanup(func() { dbPath, store = oldDBPath, oldStore })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message": "unauthorized"}`)
	}))
	defer server.Close()

	t.Setenv("AZURE_DEVOPS_PAT", "bad-pat")
	t.Setenv("AZURE_DEVOPS_ORG", "")
	t.Setenv("AZURE_DEVOPS_PROJECT", "proj")
	t.Setenv("AZURE_DEVOPS_URL", server.URL)

	err := runADOProjects(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("runADOProjects() should fail with HTTP error")
	}
	if !strings.Contains(err.Error(), "failed to list projects") {
		t.Errorf("error = %v, want mention of 'failed to list projects'", err)
	}
}

// captureADOStdout captures stdout output from fn and returns it as a string.
func captureADOStdout(t *testing.T, fn func()) string {
	t.Helper()

	adoStdioMutex.Lock()
	defer adoStdioMutex.Unlock()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	os.Stdout = old
	<-done
	_ = r.Close()

	return buf.String()
}
