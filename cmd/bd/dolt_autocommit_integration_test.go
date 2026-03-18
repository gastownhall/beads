//go:build cgo && integration

package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
)

func doltHeadCommit(t *testing.T, dir string, env []string) string {
	t.Helper()
	out, err := runBDExecAllowErrorWithEnv(t, dir, env, "--json", "vc", "status")
	if err != nil {
		t.Fatalf("bd vc status failed: %v\n%s", err, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		// Some commands can emit warnings; try from first '{'
		if idx := strings.Index(out, "{"); idx >= 0 {
			if err2 := json.Unmarshal([]byte(out[idx:]), &m); err2 != nil {
				t.Fatalf("failed to parse vc status JSON: %v\n%s", err2, out)
			}
		} else {
			t.Fatalf("failed to parse vc status JSON: %v\n%s", err, out)
		}
	}
	commit, _ := m["commit"].(string)
	if commit == "" {
		t.Fatalf("missing commit in vc status output:\n%s", out)
	}
	return commit
}

func runCommandInDirCombinedOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...) // #nosec G204 -- test helper executes trusted binaries
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func findDoltRepoDir(t *testing.T, dir string) string {
	t.Helper()

	beadsDir := filepath.Join(dir, ".beads")
	base := filepath.Join(dir, ".beads", "dolt")

	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		dbDir := filepath.Join(base, cfg.GetDoltDatabase())
		if _, err := os.Stat(filepath.Join(dbDir, ".dolt")); err == nil {
			return dbDir
		}
	}

	// Fallback for older layouts: prefer the first child repo under .beads/dolt/*
	// and only fall back to the server root itself if there is no child database.
	var found string
	var rootRepo string
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".dolt" {
			repoDir := filepath.Dir(path)
			if repoDir == base {
				rootRepo = repoDir
				return nil
			}
			found = repoDir
			return fs.SkipDir
		}
		return nil
	})
	if found == "" {
		found = rootRepo
	}
	if found == "" {
		t.Fatalf("could not find Dolt repo dir under %s", base)
	}
	return found
}

func doltHeadAuthor(t *testing.T, dir string) string {
	t.Helper()

	doltDir := findDoltRepoDir(t, dir)
	out, err := runCommandInDirCombinedOutput(doltDir, "dolt", "log", "-n", "1")
	if err != nil {
		t.Fatalf("dolt log failed: %v\n%s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Author:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Author:"))
		}
	}
	t.Fatalf("missing Author in dolt log output:\n%s", out)
	return ""
}

func createIssueIDFromOutput(t *testing.T, output string) string {
	t.Helper()

	var m map[string]any
	if err := json.Unmarshal([]byte(output), &m); err != nil {
		if idx := strings.Index(output, "{"); idx >= 0 {
			if err2 := json.Unmarshal([]byte(output[idx:]), &m); err2 != nil {
				t.Fatalf("failed to parse create JSON: %v\n%s", err2, output)
			}
		} else {
			t.Fatalf("failed to parse create JSON: %v\n%s", err, output)
		}
	}
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("missing id in create output:\n%s", output)
	}
	return id
}

func TestDoltAutoCommit_On_WritesAdvanceHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt integration test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, tmpDir)

	env := execBDTestEnv()

	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	before := doltHeadCommit(t, tmpDir, env)

	// Explicitly enable auto-commit=on (default is now "batch" in embedded mode).
	out, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "on", "create", "Auto-commit test", "--json")
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}

	after := doltHeadCommit(t, tmpDir, env)
	if after == before {
		t.Fatalf("expected Dolt HEAD to change after write; before=%s after=%s", before, after)
	}

	// Commit author should be deterministic (not the authenticated SQL user like root@%).
	expectedName := os.Getenv("GIT_AUTHOR_NAME")
	if expectedName == "" {
		expectedName = "beads"
	}
	expectedEmail := os.Getenv("GIT_AUTHOR_EMAIL")
	if expectedEmail == "" {
		expectedEmail = "beads@local"
	}
	expectedAuthor := fmt.Sprintf("%s <%s>", expectedName, expectedEmail)
	if got := doltHeadAuthor(t, tmpDir); got != expectedAuthor {
		t.Fatalf("expected Dolt commit author %q, got %q", expectedAuthor, got)
	}

	// A read-only command should not create another commit.
	out, err = runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "on", "list")
	if err != nil {
		t.Fatalf("bd list failed: %v\n%s", err, out)
	}
	afterList := doltHeadCommit(t, tmpDir, env)
	if afterList != after {
		t.Fatalf("expected Dolt HEAD unchanged after read command; before=%s after=%s", after, afterList)
	}
}

func TestDoltAutoCommit_Batch_DefersCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt integration test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, tmpDir)

	env := execBDTestEnv()

	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	before := doltHeadCommit(t, tmpDir, env)

	// In batch mode (default for embedded), writes should NOT advance HEAD.
	out, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "batch", "create", "Batch test 1", "--json")
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}

	afterCreate := doltHeadCommit(t, tmpDir, env)
	if afterCreate != before {
		t.Fatalf("expected Dolt HEAD unchanged in batch mode; before=%s after=%s", before, afterCreate)
	}

	// Create another issue — still deferred.
	out, err = runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "batch", "create", "Batch test 2", "--json")
	if err != nil {
		t.Fatalf("bd create (2) failed: %v\n%s", err, out)
	}

	afterCreate2 := doltHeadCommit(t, tmpDir, env)
	if afterCreate2 != before {
		t.Fatalf("expected Dolt HEAD still unchanged; before=%s after=%s", before, afterCreate2)
	}

	// An explicit "bd dolt commit" should commit all accumulated changes.
	out, err = runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "batch", "dolt", "commit")
	if err != nil {
		t.Fatalf("bd dolt commit failed: %v\n%s", err, out)
	}

	afterCommit := doltHeadCommit(t, tmpDir, env)
	if afterCommit == before {
		t.Fatalf("expected Dolt HEAD to advance after explicit commit; before=%s after=%s", before, afterCommit)
	}
}

func TestDoltAutoCommit_Off_DoesNotAdvanceHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt integration test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, tmpDir)

	env := execBDTestEnv()

	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	before := doltHeadCommit(t, tmpDir, env)

	// Disable auto-commit via persistent flag (must come before subcommand).
	out, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "off", "create", "Auto-commit off", "--json")
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}

	after := doltHeadCommit(t, tmpDir, env)
	if after != before {
		t.Fatalf("expected Dolt HEAD unchanged with auto-commit off; before=%s after=%s", before, after)
	}
}

func TestDoltAutoCommit_Off_GitHubSyncDoesNotAdvanceHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt integration test not supported on windows")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":         101,
				"number":     7,
				"title":      "Imported from GitHub",
				"body":       "sync me locally",
				"state":      "open",
				"created_at": now,
				"updated_at": now,
				"html_url":   "https://github.com/test/repo/issues/7",
				"labels": []map[string]any{
					{"name": "type::task"},
				},
			},
		})
	}))
	defer server.Close()

	tmpDir := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, tmpDir)

	env := execBDTestEnv(
		"GITHUB_TOKEN=test-token",
		"GITHUB_OWNER=test",
		"GITHUB_REPO=repo",
		"GITHUB_API_URL="+server.URL,
	)

	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	before := doltHeadCommit(t, tmpDir, env)

	out, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "off", "github", "sync", "--pull-only")
	if err != nil {
		t.Fatalf("bd github sync failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Pulled 1 issues") {
		t.Fatalf("expected sync output to report one pull:\n%s", out)
	}

	after := doltHeadCommit(t, tmpDir, env)
	if after != before {
		t.Fatalf("expected Dolt HEAD unchanged in off mode after github sync; before=%s after=%s", before, after)
	}

	listOut, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "list")
	if err != nil {
		t.Fatalf("bd list failed: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, "Imported from GitHub") {
		t.Fatalf("expected imported GitHub issue in list output:\n%s", listOut)
	}
}

func TestDoltAutoCommit_Batch_GitLabSyncDoesNotAdvanceHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt integration test not supported on windows")
	}

	now := time.Now().UTC().Format(time.RFC3339)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":          100,
				"iid":         1,
				"project_id":  123,
				"title":       "Imported from GitLab",
				"description": "sync me locally",
				"state":       "opened",
				"created_at":  now,
				"updated_at":  now,
				"web_url":     "https://gitlab.example.com/project/-/issues/1",
				"labels":      []string{"type::task"},
			},
		})
	}))
	defer server.Close()

	tmpDir := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, tmpDir)

	env := execBDTestEnv(
		"GITLAB_URL="+server.URL,
		"GITLAB_TOKEN=test-token",
		"GITLAB_PROJECT_ID=123",
	)

	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	before := doltHeadCommit(t, tmpDir, env)

	out, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "batch", "gitlab", "sync", "--pull-only")
	if err != nil {
		t.Fatalf("bd gitlab sync failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Pulled 1 issues") {
		t.Fatalf("expected sync output to report one pull:\n%s", out)
	}

	after := doltHeadCommit(t, tmpDir, env)
	if after != before {
		t.Fatalf("expected Dolt HEAD unchanged in batch mode after gitlab sync; before=%s after=%s", before, after)
	}

	listOut, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "list")
	if err != nil {
		t.Fatalf("bd list failed: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, "Imported from GitLab") {
		t.Fatalf("expected imported GitLab issue in list output:\n%s", listOut)
	}
}

func TestDoltAutoCommit_Batch_RenameDoesNotAdvanceHead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow integration test in short mode")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("dolt integration test not supported on windows")
	}

	tmpDir := createTempDirWithCleanup(t)
	setupGitRepoForIntegration(t, tmpDir)

	env := execBDTestEnv()

	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		if isDoltBackendUnavailable(initOut) {
			t.Skipf("dolt backend not available: %s", initOut)
		}
		t.Fatalf("bd init --backend dolt failed: %v\n%s", initErr, initOut)
	}

	createOut, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "batch", "create", "Rename target", "--json")
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, createOut)
	}
	oldID := createIssueIDFromOutput(t, createOut)

	before := doltHeadCommit(t, tmpDir, env)

	newID := oldID + "-renamed"
	renameOut, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "--dolt-auto-commit", "batch", "rename", oldID, newID)
	if err != nil {
		t.Fatalf("bd rename failed: %v\n%s", err, renameOut)
	}

	after := doltHeadCommit(t, tmpDir, env)
	if after != before {
		t.Fatalf("expected Dolt HEAD unchanged in batch mode after rename; before=%s after=%s", before, after)
	}

	showOut, err := runBDExecAllowErrorWithEnv(t, tmpDir, env, "show", newID)
	if err != nil {
		t.Fatalf("bd show failed for renamed issue: %v\n%s", err, showOut)
	}
	if !strings.Contains(showOut, "Rename target") {
		t.Fatalf("expected renamed issue to remain readable:\n%s", showOut)
	}
}
