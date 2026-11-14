package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
