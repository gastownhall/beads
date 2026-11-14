package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/remotecache"
)

// GH#6233: bd init and bd bootstrap clone from a remote before any remote is
// registered, so the clone funnel itself must enforce
// federation.allowed-remote-patterns — against the TARGET workspace's list,
// because bd bootstrap can run from outside the workspace it clones into.

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
// into the target workspace's config.yaml — the file the clone gate reads
// through config.WithBeadsDir.
func writeClonePolicyConfig(t *testing.T, beadsDir string, patterns []string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("federation:\n  allowed-remote-patterns:\n")
	for _, p := range patterns {
		b.WriteString("    - " + p + "\n")
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// An out-of-policy remote is refused before any funnel is picked: no
// dispatch, no transfer, nothing created.
func TestCloneFromRemoteRefusedByAllowedRemotePatterns(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	beadsDir := t.TempDir()
	writeClonePolicyConfig(t, beadsDir, []string{"git+https://github.com/myorg/*"})
	calls := swapRemoteCloneSeams(t)

	err := cloneFromRemote(context.Background(), beadsDir, "https://evil.example.com/attacker/repo.git", "beads", nil)
	var policyErr *remotecache.RemotePolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("cloneFromRemote error = %v, want *RemotePolicyError", err)
	}
	if *calls != 0 {
		t.Errorf("the clone dispatched to a funnel (%d call(s)) despite the policy refusal", *calls)
	}
}

// The benign companion: a list that matches the remote leaves the funnel
// unchanged — dispatch happens exactly as with no list.
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
