//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// GH#6233: dolt auto-push under an allowlist that rejects origin keeps the
// local write, leaves the remote as it was, and reports the refusal through
// auto-push's existing warning (which names the remote, never its URL),
// recording the attempt but not a pushed commit.
func TestAutoPushUnderRejectedOriginKeepsLocalWrite(t *testing.T) {
	ctx := context.Background()
	saveAndRestoreGlobals(t)
	resetCommandContext()
	pinStorageMode(t, false)
	oldJSON, oldQuiet := jsonOutput, quietFlag
	t.Cleanup(func() { jsonOutput, quietFlag = oldJSON, oldQuiet })
	jsonOutput, quietFlag = false, false

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BD_DOLT_AUTO_PUSH", "true")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	st, err := embeddeddolt.Open(ctx, filepath.Join(t.TempDir(), ".beads"), "autopush", "main")
	if err != nil {
		t.Fatalf("embeddeddolt.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetConfig(ctx, "issue_prefix", "ap"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := st.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteURL := "file://" + remoteDir
	if err := st.AddRemote(ctx, "origin", remoteURL); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := st.Push(ctx); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	// The local write auto-push runs after.
	if err := st.CreateIssue(ctx, &types.Issue{ID: "ap-local", Title: "local", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := st.Commit(ctx, "add ap-local"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	head, err := st.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatal(err)
	}

	config.Set(remotecache.AllowedRemotePatternsKey, []string{"dolthub://myorg/*"})
	store = st
	_, stderr := captureStdio(t, func() { maybeAutoPush(ctx) })

	refusal := (&remotecache.RemotePolicyError{Subject: remotecache.RemoteSubject("origin")}).Error()
	if want := "Warning: dolt auto-push failed: "; !strings.HasPrefix(stderr, want) || !strings.Contains(stderr, refusal) {
		t.Errorf("stderr = %q, want auto-push's warning carrying the policy refusal %q", stderr, refusal)
	}
	if strings.Contains(stderr, remoteDir) {
		t.Errorf("stderr names the remote URL: %q", stderr)
	}
	ps, err := loadPushState()
	if err != nil || ps == nil {
		t.Fatalf("loadPushState = (%v, %v), want the failed attempt recorded", ps, err)
	}
	if ps.LastPush == "" || ps.LastCommit != "" {
		t.Errorf("push state = %+v, want LastPush set and no LastCommit for a refused push", ps)
	}

	// The local write is intact.
	if got, err := st.GetCurrentCommit(ctx); err != nil || got != head {
		t.Fatalf("HEAD = (%s, %v) after refused auto-push, want %s", got, err, head)
	}
	if _, err := st.GetIssue(ctx, "ap-local"); err != nil {
		t.Fatalf("GetIssue(ap-local) after refused auto-push: %v", err)
	}

	// Lift the policy and fetch: the remote is still one commit behind.
	config.Set(remotecache.AllowedRemotePatternsKey, []string{})
	if err := st.Fetch(ctx, "origin"); err != nil {
		t.Fatalf("Fetch with no policy: %v", err)
	}
	status, err := st.SyncStatus(ctx, "origin")
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if status.LocalAhead != 1 || status.LocalBehind != 0 {
		t.Fatalf("local is %d ahead / %d behind origin, want 1 / 0: the refused auto-push reached the remote", status.LocalAhead, status.LocalBehind)
	}
}
