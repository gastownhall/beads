package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestApplyHooksNoDrift(t *testing.T) {
	result := applyHooks(false, false)
	if result.Status != applyStatusOK {
		t.Errorf("expected status %q, got %q", applyStatusOK, result.Status)
	}
	if result.Action != "none" {
		t.Errorf("expected action %q, got %q", "none", result.Action)
	}
}

func TestApplyHooksDryRun(t *testing.T) {
	result := applyHooks(true, true)
	if result.Status != applyStatusDryRun {
		t.Errorf("expected status %q, got %q", applyStatusDryRun, result.Status)
	}
	if result.Action != "reinstall" {
		t.Errorf("expected action %q, got %q", "reinstall", result.Action)
	}
}

func TestApplyRemoteNoDrift(t *testing.T) {
	result := applyRemote(false, false)
	if result.Status != applyStatusOK {
		t.Errorf("expected status %q, got %q", applyStatusOK, result.Status)
	}
	if result.Action != "none" {
		t.Errorf("expected action %q, got %q", "none", result.Action)
	}
}

func TestApplyRemoteDryRun(t *testing.T) {
	// When drifted but no beads dir, should skip
	result := applyRemote(true, true)
	if result.Status != applyStatusSkipped && result.Status != applyStatusDryRun {
		t.Errorf("expected status %q or %q, got %q", applyStatusSkipped, applyStatusDryRun, result.Status)
	}
}

func TestRemoteApplyStoreConfigUsesReadOnlyForDryRun(t *testing.T) {
	dryRunConfig := remoteApplyStoreConfig(true)
	if !dryRunConfig.ReadOnly {
		t.Fatal("dry-run remote apply must open the Dolt store read-only")
	}
	if !dryRunConfig.DisableAutoStart {
		t.Fatal("remote apply diagnostics must not auto-start Dolt")
	}

	applyConfig := remoteApplyStoreConfig(false)
	if applyConfig.ReadOnly {
		t.Fatal("non-dry-run remote apply must remain writable")
	}
	if !applyConfig.DisableAutoStart {
		t.Fatal("remote apply writes must not auto-start Dolt")
	}
}

func TestRemoteURLMatchesConfigNormalizesEquivalentGitURLs(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		configured string
		want       bool
	}{
		{
			name:       "https normalized to git https",
			current:    "git+https://github.com/gastownhall/beads.git",
			configured: "https://github.com/gastownhall/beads.git",
			want:       true,
		},
		{
			name:       "ssh normalized to git ssh",
			current:    "git+ssh://github.com/gastownhall/beads.git",
			configured: "ssh://github.com/gastownhall/beads.git",
			want:       true,
		},
		{
			name:       "different repos still differ",
			current:    "git+https://github.com/gastownhall/beads.git",
			configured: "https://github.com/gastownhall/other.git",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := remoteURLMatchesConfig(tt.current, tt.configured); got != tt.want {
				t.Fatalf("remoteURLMatchesConfig(%q, %q) = %v, want %v", tt.current, tt.configured, got, tt.want)
			}
		})
	}
}

func TestApplyServerNoDrift(t *testing.T) {
	result := applyServer(false, false)
	if result.Status != applyStatusOK {
		t.Errorf("expected status %q, got %q", applyStatusOK, result.Status)
	}
	if result.Action != "none" {
		t.Errorf("expected action %q, got %q", "none", result.Action)
	}
}

func TestApplyServerDriftedButNotConfigured(t *testing.T) {
	// Server running but config doesn't say shared-server=true
	// Should skip (not stop the server)
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	result := applyServer(true, false)
	// Without a .beads dir or with shared-server not set, should skip
	if result.Status != applyStatusSkipped {
		t.Errorf("expected status %q, got %q", applyStatusSkipped, result.Status)
	}
}

func TestApplyServerDryRun(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	result := applyServer(true, true)
	// Without beads dir, should skip even in dry-run
	if result.Status != applyStatusSkipped {
		t.Errorf("expected status %q, got %q", applyStatusSkipped, result.Status)
	}
}

func TestApplyServerSkipsWhenAutoStartDisabled(t *testing.T) {
	tests := []struct {
		name         string
		autoStartEnv string
		autoStartYML string
	}{
		{
			name:         "environment disables auto-start",
			autoStartEnv: "0",
			autoStartYML: "true",
		},
		{
			name:         "workspace config disables auto-start",
			autoStartEnv: "",
			autoStartYML: "false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			beadsDir := filepath.Join(root, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatalf("create .beads: %v", err)
			}
			configYAML := "dolt:\n  shared-server: true\n  auto-start: " + tt.autoStartYML + "\n"
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
				t.Fatalf("write config.yaml: %v", err)
			}
			sharedDir := filepath.Join(root, "shared-server")
			t.Setenv("BEADS_DIR", beadsDir)
			t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
			t.Setenv("BEADS_DOLT_AUTO_START", tt.autoStartEnv)
			initConfigForTest(t)

			for _, dryRun := range []bool{false, true} {
				name := "apply"
				if dryRun {
					name = "dry-run"
				}
				t.Run(name, func(t *testing.T) {
					result := applyServer(true, dryRun)
					if result.Status != applyStatusSkipped {
						t.Fatalf("status = %q, want %q: %+v", result.Status, applyStatusSkipped, result)
					}
					if result.Action != "start" {
						t.Fatalf("action = %q, want %q", result.Action, "start")
					}
					if !strings.Contains(result.Message, "auto-start is disabled") || !strings.Contains(result.Message, "externally managed") {
						t.Fatalf("message does not explain skip policy: %q", result.Message)
					}
					if _, err := os.Stat(sharedDir); !os.IsNotExist(err) {
						t.Fatalf("applyServer created shared server state despite disabled auto-start: %v", err)
					}
				})
			}
		})
	}
}

func TestRunApplyAllOK(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	// In a test environment with no drift, all results should be ok or skipped
	results := runApply(false)
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Status == applyStatusError {
			t.Errorf("unexpected error for check %q: %s", r.Check, r.Error)
		}
	}
}

func TestRunApplyDryRun(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	results := runApply(true)
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	// In dry-run, no actions should be "applied"
	for _, r := range results {
		if r.Status == applyStatusApplied {
			t.Errorf("dry-run should not apply actions, but check %q was applied", r.Check)
		}
	}
}

func TestDriftDomainsGroupsDottedChecks(t *testing.T) {
	items := []DriftItem{
		{Check: "hooks.missing", Status: driftStatusDrift},
		{Check: "remote", Status: driftStatusDrift},
		{Check: "server", Status: driftStatusOK},
	}
	got := driftDomains(items)
	if !got["hooks"] {
		t.Fatal("expected hooks.missing drift to mark hooks domain drifted")
	}
	if !got["remote"] {
		t.Fatal("expected remote drift to mark remote domain drifted")
	}
	if got["server"] {
		t.Fatal("did not expect ok server check to mark server domain drifted")
	}
}

func TestShouldRecheckRemoteAfterServerStart(t *testing.T) {
	appliedServer := ApplyResult{Check: "server", Status: applyStatusApplied}
	if !shouldRecheckRemoteAfterServerStart(true, appliedServer) {
		t.Fatal("expected remote recheck after skipped remote drift and applied server start")
	}
	dryRunServer := ApplyResult{Check: "server", Status: applyStatusDryRun}
	if shouldRecheckRemoteAfterServerStart(true, dryRunServer) {
		t.Fatal("did not expect remote recheck when server was not actually started")
	}
	if shouldRecheckRemoteAfterServerStart(false, appliedServer) {
		t.Fatal("did not expect remote recheck when initial remote drift was not skipped")
	}
}

func TestRemoteApplyResultPreservesSkippedRemoteWithoutRecheck(t *testing.T) {
	skippedRemote := DriftItem{
		Check:   "remote",
		Status:  driftStatusSkipped,
		Message: "Cannot open Dolt store: server unavailable",
	}
	serverOK := ApplyResult{Check: "server", Status: applyStatusOK}

	result := remoteApplyResult([]DriftItem{skippedRemote}, serverOK, false, func() []DriftItem {
		t.Fatal("remote should not be rechecked unless the server was started")
		return nil
	})

	if result.Check != "remote" {
		t.Fatalf("expected remote result, got %q", result.Check)
	}
	if result.Status != applyStatusSkipped {
		t.Fatalf("expected skipped remote result, got %q", result.Status)
	}
	if result.Message != skippedRemote.Message {
		t.Fatalf("message = %q, want %q", result.Message, skippedRemote.Message)
	}
}

func TestRemoteApplyResultPreservesSkippedRemoteAfterRecheck(t *testing.T) {
	initialSkipped := DriftItem{
		Check:   "remote",
		Status:  driftStatusSkipped,
		Message: "Cannot open Dolt store: server unavailable",
	}
	recheckedSkipped := DriftItem{
		Check:   "remote",
		Status:  driftStatusSkipped,
		Message: "Cannot list remotes: access denied",
	}
	serverApplied := ApplyResult{Check: "server", Status: applyStatusApplied}
	rechecked := false

	result := remoteApplyResult([]DriftItem{initialSkipped}, serverApplied, false, func() []DriftItem {
		rechecked = true
		return []DriftItem{recheckedSkipped}
	})

	if !rechecked {
		t.Fatal("expected remote recheck after server start")
	}
	if result.Status != applyStatusSkipped {
		t.Fatalf("expected skipped remote result, got %q", result.Status)
	}
	if result.Message != recheckedSkipped.Message {
		t.Fatalf("message = %q, want %q", result.Message, recheckedSkipped.Message)
	}
}

func TestRemoteApplyResultUsesSuccessfulRemoteRecheck(t *testing.T) {
	initialSkipped := DriftItem{
		Check:   "remote",
		Status:  driftStatusSkipped,
		Message: "Cannot open Dolt store: server unavailable",
	}
	serverApplied := ApplyResult{Check: "server", Status: applyStatusApplied}
	rechecked := false

	result := remoteApplyResult([]DriftItem{initialSkipped}, serverApplied, false, func() []DriftItem {
		rechecked = true
		return []DriftItem{{
			Check:   "remote",
			Status:  driftStatusOK,
			Message: "Dolt origin remote matches federation.remote",
		}}
	})

	if !rechecked {
		t.Fatal("expected remote recheck after server start")
	}
	if result.Status != applyStatusOK {
		t.Fatalf("expected ok remote result after successful recheck, got %q", result.Status)
	}
}

func TestPrintApplyResults(t *testing.T) {
	// Smoke test — just ensure no panic
	results := []ApplyResult{
		{Check: "hooks", Action: "none", Status: applyStatusOK, Message: "up to date"},
		{Check: "remote", Action: "add_remote", Status: applyStatusApplied, Message: "added"},
		{Check: "server", Action: "start", Status: applyStatusError, Message: "failed", Error: "no dolt"},
		{Check: "hooks", Action: "reinstall", Status: applyStatusDryRun, Message: "would reinstall"},
		{Check: "remote", Action: "none", Status: applyStatusSkipped, Message: "skipped"},
	}
	// Redirect stdout to avoid test noise
	old := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = old }()
	printApplyResults(results)
	printApplyResults(nil)
}

// fakeOriginRemoteEvidence simulates the two evidence sources
// currentOriginRemoteURL consults: the SQL-visible dolt_remotes listing and
// the on-disk .dolt/repo_state.json enumeration.
type fakeOriginRemoteEvidence struct {
	listed    []storage.RemoteInfo
	listErr   error
	persisted []storage.RemoteInfo
}

func (f *fakeOriginRemoteEvidence) ListRemotes(ctx context.Context) ([]storage.RemoteInfo, error) {
	return f.listed, f.listErr
}

func (f *fakeOriginRemoteEvidence) PersistedRemoteInfos() []storage.RemoteInfo {
	return f.persisted
}

// TestCurrentOriginRemoteURL pins the wy-6k7f7 evidence rule for
// `bd config apply`: an empty dolt_remotes listing alone is not proof there
// is no origin — a cold-started sql-server hides a persisted remote
// (GH#2118), and blind-adding over it was the defect. The SQL listing wins
// when populated; the persisted enumeration is the fallback; a failed
// listing is an error, never evidence.
func TestCurrentOriginRemoteURL(t *testing.T) {
	ctx := context.Background()

	t.Run("listing_wins_when_populated", func(t *testing.T) {
		url, err := currentOriginRemoteURL(ctx, &fakeOriginRemoteEvidence{
			listed:    []storage.RemoteInfo{{Name: "origin", URL: "https://sql.example/repo"}},
			persisted: []storage.RemoteInfo{{Name: "origin", URL: "https://disk.example/repo"}},
		})
		if err != nil || url != "https://sql.example/repo" {
			t.Fatalf("got (%q, %v), want the SQL-visible URL", url, err)
		}
	})

	t.Run("cold_start_recovers_persisted_origin", func(t *testing.T) {
		url, err := currentOriginRemoteURL(ctx, &fakeOriginRemoteEvidence{
			persisted: []storage.RemoteInfo{{Name: "origin", URL: "https://disk.example/repo"}},
		})
		if err != nil || url != "https://disk.example/repo" {
			t.Fatalf("got (%q, %v), want the persisted on-disk URL", url, err)
		}
	})

	t.Run("no_evidence_means_no_origin", func(t *testing.T) {
		url, err := currentOriginRemoteURL(ctx, &fakeOriginRemoteEvidence{
			persisted: []storage.RemoteInfo{{Name: "peer-mini", URL: "https://disk.example/other"}},
		})
		if err != nil || url != "" {
			t.Fatalf("got (%q, %v), want empty with no error", url, err)
		}
	})

	t.Run("list_failure_is_an_error_not_evidence", func(t *testing.T) {
		_, err := currentOriginRemoteURL(ctx, &fakeOriginRemoteEvidence{
			listErr:   errors.New("connection refused"),
			persisted: []storage.RemoteInfo{{Name: "origin", URL: "https://disk.example/repo"}},
		})
		if err == nil {
			t.Fatal("a failed listing must surface as an error, not fall through to disk")
		}
	})
}

// fakeOriginRemoteWriter records the remote mutations replaceOriginRemote
// makes, so the ref carried onto the new origin and the restore after a
// failed add are both visible.
type fakeOriginRemoteWriter struct {
	removeErr error
	addErr    error
	calls     []string
}

func (f *fakeOriginRemoteWriter) RemoveRemote(ctx context.Context, name string) error {
	f.calls = append(f.calls, "remove "+name)
	return f.removeErr
}

func (f *fakeOriginRemoteWriter) AddRemoteWithRef(ctx context.Context, name, url, ref string) error {
	f.calls = append(f.calls, "add "+name+" "+url+" ref="+ref)
	if f.addErr != nil {
		err := f.addErr
		f.addErr = nil // the restore succeeds
		return err
	}
	return nil
}

// TestCurrentOriginRemoteKeepsRef: the evidence rule of
// currentOriginRemoteURL, with the git data ref read alongside the URL from
// either source.
func TestCurrentOriginRemoteKeepsRef(t *testing.T) {
	ctx := context.Background()
	got, err := currentOriginRemote(ctx, &fakeOriginRemoteEvidence{
		listed: []storage.RemoteInfo{{Name: "origin", URL: "git+file:///srv/ledgers.git", Ref: "refs/dolt/units/team-12542"}},
	})
	if err != nil || got.Ref != "refs/dolt/units/team-12542" {
		t.Fatalf("listed: got %+v, %v", got, err)
	}
	got, err = currentOriginRemote(ctx, &fakeOriginRemoteEvidence{
		persisted: []storage.RemoteInfo{{Name: "origin", URL: "git+file:///srv/ledgers.git", Ref: "refs/heads/issue-data"}},
	})
	if err != nil || got.Ref != "refs/heads/issue-data" {
		t.Fatalf("persisted: got %+v, %v", got, err)
	}
}

// TestReplaceOriginRemote: `bd config apply` moving origin to a new URL
// keeps the ref the old origin lived on when the new URL can carry one,
// drops it for a URL that cannot, and restores the old remote on its own
// ref when the add fails.
func TestReplaceOriginRemote(t *testing.T) {
	ctx := context.Background()
	current := storage.RemoteInfo{Name: "origin", URL: "git+file:///srv/old.git", Ref: "refs/heads/issue-data"}

	t.Run("ref carried onto a git-backed URL", func(t *testing.T) {
		st := &fakeOriginRemoteWriter{}
		ref, removed, err := replaceOriginRemote(ctx, st, current, "git+ssh://git@host/org/ledgers.git")
		if err != nil || !removed || ref != "refs/heads/issue-data" {
			t.Fatalf("got ref %q removed %v err %v", ref, removed, err)
		}
		want := []string{"remove origin", "add origin git+ssh://git@host/org/ledgers.git ref=refs/heads/issue-data"}
		if strings.Join(st.calls, ";") != strings.Join(want, ";") {
			t.Fatalf("calls = %q, want %q", st.calls, want)
		}
	})

	t.Run("ref dropped for a URL dolt cannot carry one on", func(t *testing.T) {
		st := &fakeOriginRemoteWriter{}
		ref, _, err := replaceOriginRemote(ctx, st, current, "dolthub://org/repo")
		if err != nil || ref != "" {
			t.Fatalf("got ref %q err %v", ref, err)
		}
		if st.calls[1] != "add origin dolthub://org/repo ref=" {
			t.Fatalf("calls = %q", st.calls)
		}
	})

	t.Run("failed add restores the old remote on its ref", func(t *testing.T) {
		st := &fakeOriginRemoteWriter{addErr: errors.New("refused")}
		_, removed, err := replaceOriginRemote(ctx, st, current, "git+file:///srv/new.git")
		if err == nil || !removed {
			t.Fatalf("want the add error with removed=true, got %v %v", err, removed)
		}
		if st.calls[2] != "add origin git+file:///srv/old.git ref=refs/heads/issue-data" {
			t.Fatalf("restore did not carry the ref: %q", st.calls)
		}
	})

	t.Run("failed remove touches nothing else", func(t *testing.T) {
		st := &fakeOriginRemoteWriter{removeErr: errors.New("busy")}
		_, removed, err := replaceOriginRemote(ctx, st, current, "git+file:///srv/new.git")
		if err == nil || removed || len(st.calls) != 1 {
			t.Fatalf("got removed %v err %v calls %q", removed, err, st.calls)
		}
	})
}
