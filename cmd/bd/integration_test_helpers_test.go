//go:build cgo && integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	igl "github.com/steveyegge/beads/internal/gitlab"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type TestMode string

const (
	ServerMode TestMode = "server"
	DirectMode TestMode = ServerMode
)

type DualModeTestEnv struct {
	ctx   context.Context
	mode  TestMode
	store *dolt.DoltStore
}

func (e *DualModeTestEnv) Mode() TestMode           { return e.mode }
func (e *DualModeTestEnv) Context() context.Context { return e.ctx }
func (e *DualModeTestEnv) Store() *dolt.DoltStore   { return e.store }

func (e *DualModeTestEnv) CreateIssue(issue *types.Issue) error {
	return e.store.CreateIssue(e.ctx, issue, "test")
}

func (e *DualModeTestEnv) GetIssue(id string) (*types.Issue, error) {
	return e.store.GetIssue(e.ctx, id)
}

func (e *DualModeTestEnv) UpdateIssue(id string, updates map[string]interface{}) error {
	return e.store.UpdateIssue(e.ctx, id, updates, "test")
}

func RunServerModeTest(t *testing.T, _ string, testFn func(t *testing.T, env *DualModeTestEnv)) {
	t.Helper()

	t.Run(string(ServerMode), func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepoForIntegration(t, tmpDir)

		testDBPath := filepath.Join(tmpDir, ".beads", "beads.db")
		testStore := newTestStore(t, testDBPath)
		t.Cleanup(func() { _ = testStore.Close() })

		env := &DualModeTestEnv{
			ctx:   context.Background(),
			mode:  ServerMode,
			store: testStore,
		}
		testFn(t, env)
	})
}

<<<<<<< HEAD
func execBDTestEnv(overrides ...string) []string {
	env := runtimeMatrixFilterEnv(
		os.Environ(),
		[]string{"BEADS_TEST_MODE", "GT_ROOT"},
		[]string{"BEADS_DOLT_"},
	)
	return append(env, overrides...)
}

func RunDualModeTest(t *testing.T, name string, testFn func(t *testing.T, env *DualModeTestEnv)) {
	t.Helper()
	RunServerModeTest(t, name, testFn)
}

func setupGitRepoForIntegration(t *testing.T, dir string) {
	t.Helper()
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test User")
	runGitCmd(t, dir, "config", "remote.origin.url", "https://github.com/test/repo.git")
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := runCommandInDir(dir, "git", args...); err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
}

func isDoltBackendUnavailable(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "dolt") &&
		(strings.Contains(lower, "not supported") ||
			strings.Contains(lower, "not available") ||
			strings.Contains(lower, "unknown"))
}

func removeIssueFromJSONL(id string) error {
	if dbPath == "" {
		return fmt.Errorf("dbPath is empty")
	}

	jsonlPath := filepath.Join(filepath.Dir(dbPath), "issues.jsonl")
	data, err := os.ReadFile(jsonlPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	lines := bytes.Split(data, []byte("\n"))
	filtered := make([][]byte, 0, len(lines))
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal(trimmed, &issue); err != nil || issue.ID != id {
			filtered = append(filtered, append([]byte(nil), trimmed...))
		}
	}

	output := make([]byte, 0, len(data))
	for _, line := range filtered {
		output = append(output, line...)
		output = append(output, '\n')
	}
	return os.WriteFile(jsonlPath, output, 0o600)
}

func doPullFromGitLab(ctx context.Context, client *igl.Client, _ *igl.MappingConfig, dryRun bool, state string, _ interface{}) (igl.PullStats, error) {
	engine, err := gitLabTestSyncEngine(ctx, client)
	if err != nil {
		return igl.PullStats{}, err
	}

	lastSync, _ := store.GetConfig(ctx, "gitlab.last_sync")
	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Pull:   true,
		DryRun: dryRun,
		State:  state,
	})
	if err != nil {
		return igl.PullStats{}, err
	}

	return igl.PullStats{
		Created:     result.Stats.Created,
		Updated:     result.Stats.Updated,
		Skipped:     result.Stats.Skipped,
		Incremental: lastSync != "",
		SyncedSince: lastSync,
		Warnings:    result.Warnings,
	}, nil
}

func doPushToGitLab(ctx context.Context, client *igl.Client, _ *igl.MappingConfig, _ []*types.Issue, dryRun bool, createOnly bool, _ map[string]bool, _ map[string]bool) (igl.PushStats, error) {
	engine, err := gitLabTestSyncEngine(ctx, client)
	if err != nil {
		return igl.PushStats{}, err
	}

	result, err := engine.Sync(ctx, tracker.SyncOptions{
		Push:             true,
		DryRun:           dryRun,
		CreateOnly:       createOnly,
		ExcludeEphemeral: true,
	})
	if err != nil {
		return igl.PushStats{}, err
	}

	return igl.PushStats{
		Created: result.Stats.Created,
		Updated: result.Stats.Updated,
		Skipped: result.Stats.Skipped,
		Errors:  result.Stats.Errors,
	}, nil
}

func detectGitLabConflicts(ctx context.Context, client *igl.Client, localIssues []*types.Issue) ([]igl.Conflict, error) {
	conflicts := make([]igl.Conflict, 0, len(localIssues))
	for _, issue := range localIssues {
		if issue == nil {
			continue
		}

		projectID, iid, ok := parseGitLabSourceSystem(issue.SourceSystem)
		if !ok || fmt.Sprintf("%d", projectID) != client.ProjectID {
			continue
		}

		remote, err := client.FetchIssueByIID(ctx, iid)
		if err != nil {
			return nil, err
		}
		if remote == nil || remote.UpdatedAt == nil || !remote.UpdatedAt.After(issue.UpdatedAt) {
			continue
		}

		conflicts = append(conflicts, igl.Conflict{
			IssueID:           issue.ID,
			LocalUpdated:      issue.UpdatedAt,
			GitLabUpdated:     *remote.UpdatedAt,
			GitLabExternalRef: remote.WebURL,
			GitLabIID:         iid,
			GitLabID:          remote.ID,
		})
	}
	return conflicts, nil
}

func gitLabTestSyncEngine(ctx context.Context, client *igl.Client) (*tracker.Engine, error) {
	if store == nil {
		return nil, fmt.Errorf("global store is nil")
	}

	for key, value := range map[string]string{
		"gitlab.token":      client.Token,
		"gitlab.url":        client.BaseURL,
		"gitlab.project_id": client.ProjectID,
	} {
		if err := store.SetConfig(ctx, key, value); err != nil {
			return nil, fmt.Errorf("set %s: %w", key, err)
		}
	}

	gt := &igl.Tracker{}
	if err := gt.Init(ctx, store); err != nil {
		return nil, err
	}

	engine := tracker.NewEngine(gt, store, "test")
	engine.PullHooks = buildGitLabPullHooks(ctx)
	return engine, nil
}
