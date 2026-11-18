package doltutil

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
)

// GH#6233: the CLI mirror registers no URL the allowlist rejects, and a
// refused replacement leaves the mirror's existing remote in place.
func TestCLIRemoteRegistrationEnforcesAllowedRemotePatterns(t *testing.T) {
	dir := initDoltRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_DIR", filepath.Join(t.TempDir(), ".beads"))
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	allowed := "file://" + t.TempDir()
	if err := AddCLIRemote(dir, "origin", allowed); err != nil {
		t.Fatalf("AddCLIRemote: %v", err)
	}
	config.Set(remotecache.AllowedRemotePatternsKey, []string{allowed})
	rejected := "file://" + t.TempDir()

	if err := EnsureCLIRemote(dir, "origin", rejected, ""); !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
		t.Fatalf("EnsureCLIRemote(rejected) = %v, want a policy refusal", err)
	}
	if got := FindCLIRemote(dir, "origin"); !RemoteURLsMatch(got, allowed) {
		t.Fatalf("origin = %q after a refused replacement, want the prior %q", got, allowed)
	}
	if err := AddCLIRemoteWithRef(dir, "team", rejected, ""); !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
		t.Fatalf("AddCLIRemoteWithRef(rejected) = %v, want a policy refusal", err)
	}
	if got := FindCLIRemote(dir, "team"); got != "" {
		t.Fatalf("team = %q, want no remote registered", got)
	}
	if err := EnsureCLIRemote(dir, "origin", allowed, ""); err != nil {
		t.Fatalf("EnsureCLIRemote(allowed) = %v, want the matching mirror accepted", err)
	}
}
