//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/memoryops"
)

type fakeConfigCommitStore struct {
	storage.DoltStorage
	configOnlyCalls int
	userKVCalls     int
	messages        []string
	err             error
}

func (f *fakeConfigCommitStore) CommitConfigOnly(_ context.Context, message string) error {
	f.configOnlyCalls++
	f.messages = append(f.messages, message)
	return f.err
}

func (f *fakeConfigCommitStore) CommitConfigUserKVOnly(_ context.Context, message string) error {
	f.userKVCalls++
	f.messages = append(f.messages, message)
	return f.err
}

// GH#4078: server-mode config writes (bd remember, bd config set) must be
// committed immediately and scoped — maybeAutoCommit skips server mode
// entirely and generic Commit() excludes config, so nothing else ever
// commits them.
func TestCommitConfigWriteServerModeCommitsScoped(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{}
	if err := commitConfigWrite(context.Background(), fake, "remember"); err != nil {
		t.Fatalf("commitConfigWrite: %v", err)
	}
	if fake.configOnlyCalls != 1 {
		t.Fatalf("CommitConfigOnly calls = %d, want 1 in server mode", fake.configOnlyCalls)
	}
	if fake.userKVCalls != 0 {
		t.Fatalf("CommitConfigUserKVOnly calls = %d, want 0 for a config write", fake.userKVCalls)
	}
	if !strings.HasPrefix(fake.messages[0], "bd: remember (auto-commit) by ") {
		t.Fatalf("commit message = %q, want prefix %q", fake.messages[0], "bd: remember (auto-commit) by ")
	}
}

// A memory write commits through the kv.*-screened variant, so a concurrent
// writer's dirty internal config key is refused instead of swept in.
func TestCommitMemoryWriteServerModeCommitsUserKVScreened(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{}
	if err := commitMemoryWrite(context.Background(), fake, "remember"); err != nil {
		t.Fatalf("commitMemoryWrite: %v", err)
	}
	if fake.userKVCalls != 1 {
		t.Fatalf("CommitConfigUserKVOnly calls = %d, want 1 in server mode", fake.userKVCalls)
	}
	if fake.configOnlyCalls != 0 {
		t.Fatalf("CommitConfigOnly calls = %d, want 0 for a memory write", fake.configOnlyCalls)
	}
}

// GH#7523: a kv write is user kv.* data like a memory, so it commits through
// the same kv.*-screened variant.
func TestCommitKVWriteServerModeCommitsUserKVScreened(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{}
	if err := commitKVWrite(context.Background(), fake, "kv set"); err != nil {
		t.Fatalf("commitKVWrite: %v", err)
	}
	if fake.userKVCalls != 1 || fake.configOnlyCalls != 0 {
		t.Fatalf("CommitConfigUserKVOnly/CommitConfigOnly calls = %d/%d, want 1/0 for a kv write", fake.userKVCalls, fake.configOnlyCalls)
	}
	if !strings.HasPrefix(fake.messages[0], "bd: kv set (auto-commit) by ") {
		t.Fatalf("commit message = %q, want prefix %q", fake.messages[0], "bd: kv set (auto-commit) by ")
	}
}

// The screen's refusal reaches the user rather than being taken for a
// nothing-to-commit no-op, and keeps its type through the wrap: the --json
// payload names the refused keys from it.
func TestCommitMemoryWriteSurfacesScreenRefusal(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{err: &storage.ConfigCommitRefusedError{Keys: []string{"issue_prefix"}}}
	err := commitMemoryWrite(context.Background(), fake, "forget")
	var refused *storage.ConfigCommitRefusedError
	if !errors.As(err, &refused) || !slices.Equal(refused.Keys, []string{"issue_prefix"}) {
		t.Fatalf("commitMemoryWrite error = %v, want the screen's refusal naming issue_prefix", err)
	}
}

// memoryCommitFields is how --json learns that a memory write landed but was
// not committed, which the exit code no longer says. A committed write adds
// nothing, so its payload keeps the shipped shape. A commit that may have
// landed is qualified, not reported as committed: committed stays false.
func TestMemoryCommitFields(t *testing.T) {
	refusal := fmt.Errorf("committing memory write: %w",
		&storage.ConfigCommitRefusedError{Keys: []string{"issue_prefix", "sync.branch"}})
	failure := errors.New("committing memory write: connection refused")
	indeterminate := fmt.Errorf("committing memory write: %w", errIndeterminateCommit)
	for _, tc := range []struct {
		name      string
		commitErr error
		want      map[string]interface{}
	}{
		{name: "committed", want: map[string]interface{}{"key": "k"}},
		{name: "refused by the kv.* screen", commitErr: refusal, want: map[string]interface{}{
			"key":               "k",
			"committed":         false,
			"commit_error":      refusal.Error(),
			"dirty_config_keys": []string{"issue_prefix", "sync.branch"},
		}},
		{name: "failed for another reason", commitErr: failure, want: map[string]interface{}{
			"key":          "k",
			"committed":    false,
			"commit_error": failure.Error(),
		}},
		{name: "may have landed", commitErr: indeterminate, want: map[string]interface{}{
			"key":                  "k",
			"committed":            false,
			"commit_error":         indeterminate.Error(),
			"commit_indeterminate": true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]interface{}{"key": "k"}
			memoryCommitFields(payload, tc.commitErr)
			if !reflect.DeepEqual(payload, tc.want) {
				t.Fatalf("payload = %#v, want %#v", payload, tc.want)
			}
		})
	}
}

// errIndeterminateCommit is what the SQL-server store returns when the
// connection drops during DOLT_COMMIT: the commit may or may not have landed.
var errIndeterminateCommit = fmt.Errorf("dolt commit after SQL mutation: %w: %w",
	errors.New("invalid connection"), storage.ErrCommitIndeterminate)

// refusingMemoryStore is a SQL-server store on which a memory write lands and
// its kv.*-screened commit is refused: what a dirty internal key, such as the
// issue_prefix `bd rename-prefix` leaves, makes of every remember and forget.
type refusingMemoryStore struct {
	storage.DoltStorage
	memories  *landingMemories
	commitErr error
	commits   int
}

func (s *refusingMemoryStore) Memories() (memoryops.Memories, error) { return s.memories, nil }

func (s *refusingMemoryStore) CommitConfigUserKVOnly(context.Context, string) error {
	s.commits++
	return s.commitErr
}

type landingMemories struct {
	memoryops.Memories
	stored map[string]string
}

func (m *landingMemories) Remember(_ context.Context, req memoryops.RememberRequest) (memoryops.RememberResult, error) {
	_, replaced := m.stored[req.Key]
	m.stored[req.Key] = req.Content
	return memoryops.RememberResult{Key: req.Key, Value: req.Content, Replaced: replaced}, nil
}

func (m *landingMemories) Forget(_ context.Context, req memoryops.ForgetRequest) (memoryops.ForgetResult, error) {
	value, found := m.stored[req.Key]
	delete(m.stored, req.Key)
	return memoryops.ForgetResult{Key: req.Key, Value: value, Found: found}, nil
}

// A refused memory commit must not turn a write that landed into exit 1:
// remember and forget warn on stderr, print their normal result and, under
// --json, add committed:false and the refused keys. v1.0.1 (#3052) warned and
// carried on the same way.
func TestMemoryCommandsWarnAndSucceedWhenTheCommitIsRefused(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", asJSON), func(t *testing.T) {
			t.Setenv("BEADS_DIR", t.TempDir())
			saveAndRestoreGlobals(t)
			saveStorageMode(t)
			serverMode = true
			fake := &refusingMemoryStore{
				memories:  &landingMemories{stored: map[string]string{}},
				commitErr: &storage.ConfigCommitRefusedError{Keys: []string{"issue_prefix"}},
			}
			store = fake
			setStoreActive(true)
			savedKey, savedJSON, savedConfigInt := memoryKeyFlag, jsonOutput, memoryConfigInt
			t.Cleanup(func() { memoryKeyFlag, jsonOutput, memoryConfigInt = savedKey, savedJSON, savedConfigInt })
			memoryKeyFlag, jsonOutput = "gh4078", asJSON
			memoryConfigInt = func(string) int { return 0 }

			for _, step := range []struct {
				cmd  string
				run  func() error
				text string
				json map[string]interface{}
			}{
				{
					cmd:  "remember",
					run:  func() error { return rememberCmd.RunE(rememberCmd, []string{"a memory that lands"}) },
					text: "Remembered [gh4078]: a memory that lands",
					json: map[string]interface{}{"key": "gh4078", "value": "a memory that lands", "action": "remembered"},
				},
				{
					cmd:  "forget",
					run:  func() error { return forgetCmd.RunE(forgetCmd, []string{"gh4078"}) },
					text: "Forgot [gh4078]: a memory that lands",
					json: map[string]interface{}{"key": "gh4078", "deleted": "true"},
				},
			} {
				var runErr error
				stdout, stderr := captureStdio(t, func() { runErr = step.run() })
				if runErr != nil {
					t.Fatalf("bd %s returned %v after its write landed, want success with a warning", step.cmd, runErr)
				}
				if !strings.Contains(stderr, "Warning: committing memory write: refusing to commit") ||
					!strings.Contains(stderr, "issue_prefix") {
					t.Errorf("bd %s stderr does not warn about the refused commit naming issue_prefix:\n%s", step.cmd, stderr)
				}
				if !asJSON {
					if !strings.Contains(stdout, step.text) {
						t.Errorf("bd %s stdout = %q, want the normal result %q", step.cmd, stdout, step.text)
					}
					continue
				}
				var got map[string]interface{}
				if err := json.Unmarshal([]byte(stdout), &got); err != nil {
					t.Fatalf("bd %s --json stdout is not one JSON object: %v\n%s", step.cmd, err, stdout)
				}
				want := step.json
				want["schema_version"] = float64(JSONSchemaVersion)
				want["committed"] = false
				want["commit_error"] = "committing memory write: " + fake.commitErr.Error()
				want["dirty_config_keys"] = []interface{}{"issue_prefix"}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("bd %s --json = %#v, want %#v", step.cmd, got, want)
				}
			}
			if fake.commits != 2 {
				t.Errorf("CommitConfigUserKVOnly calls = %d, want one per command", fake.commits)
			}
			if len(fake.memories.stored) != 0 {
				t.Errorf("stored memories = %v, want the forget to have landed too", fake.memories.stored)
			}
		})
	}
}

// The proxied route's role commits each config write inside its own unit of
// work, so committing again here would be a second commit for the same write.
func TestCommitConfigWriteProxiedServerModeIsNoOp(t *testing.T) {
	saveStorageMode(t)
	proxiedServerMode = true

	fake := &fakeConfigCommitStore{}
	if err := commitConfigWrite(context.Background(), fake, "config set"); err != nil {
		t.Fatalf("commitConfigWrite: %v", err)
	}
	if fake.configOnlyCalls != 0 {
		t.Fatalf("CommitConfigOnly calls = %d, want 0 in proxied server mode", fake.configOnlyCalls)
	}
}

// batch and off defer server-mode version commits to `bd dolt commit`, whose
// CommitAll includes config, so a config write must defer with them.
func TestCommitConfigWriteHonorsBatchAndOffModes(t *testing.T) {
	for _, mode := range []doltAutoCommitMode{doltAutoCommitBatch, doltAutoCommitOff} {
		t.Run(string(mode), func(t *testing.T) {
			saveStorageMode(t)
			serverMode = true
			doltAutoCommit = string(mode)

			fake := &fakeConfigCommitStore{}
			if err := commitConfigWrite(context.Background(), fake, "remember"); err != nil {
				t.Fatalf("commitConfigWrite: %v", err)
			}
			if fake.configOnlyCalls != 0 {
				t.Fatalf("CommitConfigOnly calls = %d, want 0 in %s mode", fake.configOnlyCalls, mode)
			}
		})
	}
}

// Embedded mode already commits config through the PersistentPostRun
// auto-commit, so the helper must not double-commit there.
func TestCommitConfigWriteEmbeddedModeIsNoOp(t *testing.T) {
	saveStorageMode(t)
	serverMode = false
	proxiedServerMode = false

	fake := &fakeConfigCommitStore{}
	if err := commitConfigWrite(context.Background(), fake, "remember"); err != nil {
		t.Fatalf("commitConfigWrite: %v", err)
	}
	if fake.configOnlyCalls != 0 {
		t.Fatalf("CommitConfigOnly calls = %d, want 0 in embedded mode", fake.configOnlyCalls)
	}
}

// A real commit failure must surface — silently losing the commit is the
// exact bug class this helper exists to fix (GH#4078's silent no-op half).
func TestCommitConfigWriteSurfacesCommitError(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{err: errors.New("connection refused")}
	err := commitConfigWrite(context.Background(), fake, "remember")
	if err == nil {
		t.Fatal("commitConfigWrite returned nil, want the commit error surfaced")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %q, want it to wrap %q", err.Error(), "connection refused")
	}
}

// "Nothing to commit" is a benign race (e.g. the same key re-written to an
// identical value) and must not fail the user's write.
func TestCommitConfigWriteSwallowsNothingToCommit(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	fake := &fakeConfigCommitStore{err: errors.New("nothing to commit")}
	if err := commitConfigWrite(context.Background(), fake, "remember"); err != nil {
		t.Fatalf("commitConfigWrite: %v, want nothing-to-commit swallowed", err)
	}
	if fake.configOnlyCalls != 1 {
		t.Fatalf("CommitConfigOnly calls = %d, want 1", fake.configOnlyCalls)
	}
}

// The yaml-only secret branch of `bd config unset` deletes a credential row an
// older bd left in the config table. On the SQL-server route only this commit
// takes the delete to HEAD, the commit `bd dolt push` publishes, so it must
// happen; and since the delete has landed by then, a failed commit warns
// rather than failing the command.
func TestConfigUnsetCommitsTheLeakedRowDelete(t *testing.T) {
	setup := func(t *testing.T, stored map[string]string, commitErr error) *leakedRowStore {
		t.Helper()
		newLeakedSecretWorkspace(t, "notion:\n  token: yaml-copy\n", true)
		installLeakedRowSettings(t, &recordingWorkspaceConfig{stored: stored})
		saveStorageMode(t)
		serverMode = true
		fake := store.(*leakedRowStore)
		fake.commitErr = commitErr
		return fake
	}

	t.Run("a removed row is committed", func(t *testing.T) {
		fake := setup(t, map[string]string{"notion.token": "leaked-db-value"}, nil)
		stdout, stderr, err := runConfigUnset(t, "notion.token", false)
		if err != nil {
			t.Fatalf("config unset notion.token: %v", err)
		}
		if len(fake.commits) != 1 || !strings.HasPrefix(fake.commits[0], "bd: config unset (auto-commit) by ") {
			t.Fatalf("commits = %q, want one config unset auto-commit — HEAD still publishes the credential", fake.commits)
		}
		if !strings.Contains(stdout, "Also removed a stored notion.token row") {
			t.Errorf("the removal was not reported; stdout:\n%s", stdout)
		}
		if strings.Contains(stderr, "not committed") {
			t.Errorf("a committed removal was reported as uncommitted; stderr:\n%s", stderr)
		}
	})

	t.Run("no row: nothing to commit", func(t *testing.T) {
		fake := setup(t, map[string]string{}, nil)
		if _, _, err := runConfigUnset(t, "notion.token", false); err != nil {
			t.Fatalf("config unset notion.token: %v", err)
		}
		if len(fake.commits) != 0 {
			t.Errorf("commits = %q, want none when no row was removed", fake.commits)
		}
	})

	t.Run("a failed commit warns and the unset still succeeds", func(t *testing.T) {
		fake := setup(t, map[string]string{"notion.token": "leaked-db-value"}, errors.New("connection refused"))
		stdout, stderr, err := runConfigUnset(t, "notion.token", false)
		if err != nil {
			t.Fatalf("config unset notion.token returned %v, want success: the delete and the YAML unset both landed", err)
		}
		if len(fake.commits) != 1 {
			t.Fatalf("commits = %q, want the one attempt", fake.commits)
		}
		if !strings.Contains(stdout, "Also removed a stored notion.token row") {
			t.Errorf("the removal was not reported; stdout:\n%s", stdout)
		}
		for _, want := range []string{"The removal is not committed", "connection refused", "bd dolt commit"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr does not carry %q:\n%s", want, stderr)
			}
		}
	})

	t.Run("--json reports a failed commit", func(t *testing.T) {
		setup(t, map[string]string{"notion.token": "leaked-db-value"}, errors.New("connection refused"))
		stdout, _, err := runConfigUnset(t, "notion.token", true)
		if err != nil {
			t.Fatalf("config unset notion.token --json: %v", err)
		}
		var got map[string]interface{}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
		}
		if got["database_row_removed"] != true {
			t.Errorf("database_row_removed = %v, want true", got["database_row_removed"])
		}
		if msg, _ := got["database_commit_failed"].(string); !strings.Contains(msg, "connection refused") {
			t.Errorf("database_commit_failed = %v, want the commit error", got["database_commit_failed"])
		}
		if v, ok := got["database_commit_indeterminate"]; ok {
			t.Errorf("database_commit_indeterminate = %v for a commit that failed outright, want it absent", v)
		}
	})

	// After storage.ErrCommitIndeterminate the commit may have landed, so the
	// report must not assert that HEAD still carries the row; and --json keeps
	// database_commit_failed beside the qualifier, so a script that reads only
	// that field is still told to commit.
	t.Run("an indeterminate commit is hedged, not reported as failed", func(t *testing.T) {
		setup(t, map[string]string{"notion.token": "leaked-db-value"}, errIndeterminateCommit)
		_, stderr, err := runConfigUnset(t, "notion.token", false)
		if err != nil {
			t.Fatalf("config unset notion.token returned %v, want success: the delete and the YAML unset both landed", err)
		}
		for _, want := range []string{"The removal may not be committed", "write commit result indeterminate", "bd dolt commit"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr does not carry %q:\n%s", want, stderr)
			}
		}
		if strings.Contains(stderr, "The removal is not committed") {
			t.Errorf("a commit that may have landed was reported as not committed; stderr:\n%s", stderr)
		}
	})

	t.Run("--json qualifies an indeterminate commit", func(t *testing.T) {
		setup(t, map[string]string{"notion.token": "leaked-db-value"}, errIndeterminateCommit)
		stdout, _, err := runConfigUnset(t, "notion.token", true)
		if err != nil {
			t.Fatalf("config unset notion.token --json: %v", err)
		}
		var got map[string]interface{}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout)
		}
		if msg, _ := got["database_commit_failed"].(string); !strings.Contains(msg, "write commit result indeterminate") {
			t.Errorf("database_commit_failed = %v, want the commit error kept beside the qualifier", got["database_commit_failed"])
		}
		if got["database_commit_indeterminate"] != true {
			t.Errorf("database_commit_indeterminate = %v, want true", got["database_commit_indeterminate"])
		}
	})
}

// rowNote folds the database half into the error a failed YAML unset returns,
// so it hedges an indeterminate commit the same way describe does.
func TestSecretRowCleanupRowNoteHedgesAnIndeterminateCommit(t *testing.T) {
	for _, tc := range []struct {
		name          string
		commitErr     error
		want, notWant string
	}{
		{name: "failed", commitErr: errors.New("connection refused"),
			want: "the removal is not committed", notWant: "may not be committed"},
		{name: "indeterminate", commitErr: errIndeterminateCommit,
			want: "the removal may not be committed", notWant: "is not committed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note := secretRowCleanup{present: true, removed: true, commitErr: tc.commitErr}.rowNote("notion.token")
			if !strings.Contains(note, tc.want) || strings.Contains(note, tc.notWant) {
				t.Errorf("rowNote = %q, want %q and not %q", note, tc.want, tc.notWant)
			}
		})
	}
}

func TestCommitConfigWriteNilStoreIsNoOp(t *testing.T) {
	saveStorageMode(t)
	serverMode = true

	if err := commitConfigWrite(context.Background(), nil, "remember"); err != nil {
		t.Fatalf("commitConfigWrite with nil store: %v, want no-op", err)
	}
}
