package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
)

// GH#6233: a clone reads federation.allowed-remote-patterns from the target
// workspace (here only its config.yaml sets it, after the process config was
// loaded) and refuses a source it rejects before any database is created.
func TestCloneFromRemoteUsesTargetWorkspaceAllowedRemotePatterns(t *testing.T) {
	beadsDir := newForgeSyncRemoteWorkspace(t, "")
	f, err := os.OpenFile(filepath.Join(beadsDir, "config.yaml"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\nfederation:\n  allowed-remote-patterns:\n    - \"git+https://github.com/myorg/*\"\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := config.AllowedRemotePatterns(); len(got) != 0 {
		t.Fatalf("fixture: process policy = %q, want none so only the target's config can refuse", got)
	}

	source := "file://" + filepath.Join(t.TempDir(), "remote")
	err = cloneFromRemoteWithMode(context.Background(), beadsDir, source, "beads", nil, remoteCloneCLI)
	if !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
		t.Fatalf("cloneFromRemoteWithMode error = %v, want a %s refusal", err, remotecache.AllowedRemotePatternsKey)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "dolt")); !os.IsNotExist(statErr) {
		t.Errorf("refused clone left %s/dolt behind (stat: %v)", beadsDir, statErr)
	}
}

// errCloneDispatched is the sentinel the swapped clone funnels return:
// reaching a funnel proves dispatch, and no real clone work should follow.
var errCloneDispatched = errors.New("clone dispatched")

// swapRemoteCloneSeams replaces all four clone funnels with recorders and
// returns the dispatch counter.
func swapRemoteCloneSeams(t *testing.T) *int {
	t.Helper()
	calls := new(int)
	oldEmbedded, oldServer := remoteCloneViaEmbedded, remoteCloneViaServer
	oldDataDir, oldCLI := remoteCloneIntoDataDir, remoteCloneViaCLI
	remoteCloneViaEmbedded = func(context.Context, string, string, string) error {
		*calls++
		return errCloneDispatched
	}
	remoteCloneViaServer = func(context.Context, string, string, bootstrapServerProbeConfig) error {
		*calls++
		return errCloneDispatched
	}
	remoteCloneIntoDataDir = func(context.Context, string, string, string) error {
		*calls++
		return errCloneDispatched
	}
	remoteCloneViaCLI = func(context.Context, string, string, string) error {
		*calls++
		return errCloneDispatched
	}
	t.Cleanup(func() {
		remoteCloneViaEmbedded, remoteCloneViaServer = oldEmbedded, oldServer
		remoteCloneIntoDataDir, remoteCloneViaCLI = oldDataDir, oldCLI
	})
	return calls
}

// writeClonePolicyConfig writes a federation.allowed-remote-patterns list
// into the target workspace's config.yaml, the file the clone gate reads
// through config.WithBeadsDir.
func writeClonePolicyConfig(t *testing.T, beadsDir string, patterns []string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("federation:\n  allowed-remote-patterns:\n")
	for _, p := range patterns {
		b.WriteString("    - \"" + p + "\"\n")
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// GH#6233: an out-of-policy remote is refused before any clone funnel is
// picked, in every clone mode: no dispatch, no transfer, nothing created.
func TestCloneFromRemoteRefusedByAllowedRemotePatterns(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	for _, mode := range []remoteCloneMode{remoteCloneAuto, remoteCloneEmbedded, remoteCloneExternalServer, remoteCloneCLI} {
		beadsDir := t.TempDir()
		writeClonePolicyConfig(t, beadsDir, []string{"git+https://github.com/myorg/*"})
		calls := swapRemoteCloneSeams(t)

		err := cloneFromRemoteWithMode(context.Background(), beadsDir, "https://evil.example.com/attacker/repo.git", "beads", nil, mode)
		if !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
			t.Fatalf("mode %v: cloneFromRemoteWithMode error = %v, want a %s refusal", mode, err, remotecache.AllowedRemotePatternsKey)
		}
		if *calls != 0 {
			t.Errorf("mode %v: the clone dispatched to a funnel (%d call(s)) despite the policy refusal", mode, *calls)
		}
	}
}

// The benign companion: a list that matches the remote (typed https spelling,
// git+https pattern) leaves the funnel unchanged: dispatch happens exactly as
// with no list.
func TestCloneFromRemoteDispatchesUnderMatchingList(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	beadsDir := t.TempDir()
	writeClonePolicyConfig(t, beadsDir, []string{"git+https://github.com/myorg/*"})
	calls := swapRemoteCloneSeams(t)

	err := cloneFromRemote(context.Background(), beadsDir, "https://github.com/myorg/registry", "beads", nil)
	if !errors.Is(err, errCloneDispatched) {
		t.Fatalf("cloneFromRemote error = %v, want the dispatch sentinel (a matching list must not refuse)", err)
	}
	if *calls != 1 {
		t.Errorf("dispatch count = %d, want 1", *calls)
	}
}
