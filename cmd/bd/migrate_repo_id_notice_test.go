package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
)

// bd-ek28z: the path-hash propagation notice must fire exactly when an
// existing repository ID would be replaced by a different, path-derived one —
// the case --yes/--json used to stamp silently (GH#4361 recurrence hole).
func TestPathHashRepoIDStampNotice(t *testing.T) {
	tests := []struct {
		name       string
		oldRepoID  string
		newRepoID  string
		source     beads.RepoIDSource
		wantNotice bool
	}{
		{"path hash replacing different id", "aaaa1111", "bbbb2222", beads.RepoIDSourcePath, true},
		{"no stored id (bootstrap)", "", "bbbb2222", beads.RepoIDSourcePath, false},
		{"unchanged id", "aaaa1111", "aaaa1111", beads.RepoIDSourcePath, false},
		{"remote-derived new id", "aaaa1111", "bbbb2222", beads.RepoIDSourceRemote, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathHashRepoIDStampNotice(tt.oldRepoID, tt.newRepoID, tt.source)
			if (got != "") != tt.wantNotice {
				t.Errorf("pathHashRepoIDStampNotice(%q, %q, %q) = %q, want notice=%v",
					tt.oldRepoID, tt.newRepoID, tt.source, got, tt.wantNotice)
			}
		})
	}
}

// GH#5990 review (#8212): the unresolved-alias notice fires exactly when an
// existing repository ID would be replaced by a different one that hashes an
// SSH host alias this session could not resolve — the case --yes and --json
// refuse, because resolving clones compute another ID.
func TestUnresolvedAliasRepoIDNotice(t *testing.T) {
	unresolved := beads.RepoFingerprint{
		ID:                 "bbbb2222",
		Source:             beads.RepoIDSourceRemote,
		SSHAlias:           "alias-a",
		SSHAliasUnresolved: "GIT_SSH_COMMAND selects a custom SSH transport",
	}
	resolved := beads.RepoFingerprint{
		ID:          "bbbb2222",
		Source:      beads.RepoIDSourceRemote,
		LegacyID:    "cccc3333",
		SSHAlias:    "alias-a",
		SSHHostName: "github.com",
	}
	// Review (#8212): ssh -G maps a bare host to itself also when it is an
	// alias this machine's SSH config lacks.
	unmapped := beads.RepoFingerprint{
		ID:               "bbbb2222",
		Source:           beads.RepoIDSourceRemote,
		SSHAlias:         "alias-a",
		SSHAliasUnmapped: true,
	}
	tests := []struct {
		name       string
		oldRepoID  string
		fp         beads.RepoFingerprint
		wantReason string // "": no notice
	}{
		{"unresolved alias replacing different id", "aaaa1111", unresolved, "GIT_SSH_COMMAND"},
		{"unmapped bare host replacing different id", "aaaa1111", unmapped, "no other HostName"},
		{"no stored id (bootstrap)", "", unresolved, ""},
		{"unchanged id", "bbbb2222", unresolved, ""},
		{"unmapped bare host, unchanged id", "bbbb2222", unmapped, ""},
		{"resolved alias", "aaaa1111", resolved, ""},
		{"never looked up", "aaaa1111", beads.RepoFingerprint{ID: "bbbb2222", Source: beads.RepoIDSourceRemote}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unresolvedAliasRepoIDNotice(tt.oldRepoID, tt.fp)
			if (got != "") != (tt.wantReason != "") {
				t.Fatalf("unresolvedAliasRepoIDNotice(%q, %+v) = %q, want notice=%v", tt.oldRepoID, tt.fp, got, tt.wantReason != "")
			}
			if tt.wantReason != "" && (!strings.Contains(got, `"alias-a"`) || !strings.Contains(got, tt.wantReason)) {
				t.Fatalf("notice = %q, want the alias and the reason (%q) named", got, tt.wantReason)
			}
		})
	}
}

// GH#5990 review (#8212): bd migrate --update-repo-id refuses the unresolved
// alias ID only for scripted updates (--yes or --json), saying why; an
// interactive update goes on to the prompt, which warns instead. Tests here
// set jsonOutput and must not run in parallel.
func TestRefuseUnresolvedAliasRepoID(t *testing.T) {
	unresolved := beads.RepoFingerprint{
		ID:                 "bbbb2222",
		Source:             beads.RepoIDSourceRemote,
		SSHAlias:           "alias-a",
		SSHAliasUnresolved: "GIT_SSH_COMMAND selects a custom SSH transport",
	}
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })

	t.Run("--yes", func(t *testing.T) {
		jsonOutput = false
		var err error
		stderr := captureStderr(t, func() { err = refuseUnresolvedAliasRepoID("aaaa1111", unresolved, true) })
		if err == nil || !strings.Contains(stderr, "not updating repo_id") || !strings.Contains(stderr, "GIT_SSH_COMMAND") {
			t.Fatalf("refusal = %v, stderr %q; want an error naming the reason", err, stderr)
		}
	})
	t.Run("--json", func(t *testing.T) {
		jsonOutput = true
		stdout, err := captureStdoutWithError(t, func() error { return refuseUnresolvedAliasRepoID("aaaa1111", unresolved, false) })
		if err == nil || !strings.Contains(stdout, `"error": "unresolved_ssh_alias"`) {
			t.Fatalf("refusal = %v, stdout %q; want the unresolved_ssh_alias JSON error", err, stdout)
		}
	})
	for _, tt := range []struct {
		name      string
		oldRepoID string
		autoYes   bool
	}{
		{"interactive", "aaaa1111", false},
		{"--yes with no stored id", "", true},
		{"--yes with the same id", "bbbb2222", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jsonOutput = false
			if err := refuseUnresolvedAliasRepoID(tt.oldRepoID, unresolved, tt.autoYes); err != nil {
				t.Fatalf("refuseUnresolvedAliasRepoID = %v, want nil", err)
			}
		})
	}
}

// repoIDMetadataStore stands in for the store 'bd migrate --update-repo-id'
// reads and writes repo_id through. It embeds a nil DoltStorage, so any other
// call panics.
type repoIDMetadataStore struct {
	storage.DoltStorage
	values map[string]string
	writes int
}

func (s *repoIDMetadataStore) GetMetadata(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *repoIDMetadataStore) SetMetadata(_ context.Context, key, value string) error {
	s.values[key] = value
	s.writes++
	return nil
}

// aliasRepoIDSSHScript answers ssh -G like OpenSSH with a config mapping
// alias-a to HostName github.com and every other host to itself;
// unconfiguredRepoIDSSHScript answers as one with no Host entries.
const (
	aliasRepoIDSSHScript = `#!/bin/sh
for dest; do :; done
case "${dest#*@}" in
alias-a) echo "hostname github.com" ;;
*) echo "hostname ${dest#*@}" ;;
esac
echo "port 22"
`
	unconfiguredRepoIDSSHScript = `#!/bin/sh
for dest; do :; done
echo "hostname ${dest#*@}"
echo "port 22"
`
)

// useRepoIDSSH puts an ssh running script first on PATH.
func useRepoIDSSH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil { // #nosec G306 -- test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// setupRepoIDUpdateWorkspace selects through BEADS_DIR a workspace in a git
// checkout whose origin is remote, with git config isolated and alias-a
// configured in a fake ssh, and returns the checkout.
func setupRepoIDUpdateWorkspace(t *testing.T, remote string) string {
	t.Helper()
	isolateBeadsDirForTest(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_SSH", "")
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH_VARIANT", "")
	useRepoIDSSH(t, aliasRepoIDSSHScript)

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "core.hooksPath", t.TempDir()},
		{"remote", "add", "origin", remote},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	return repo
}

// Review (#8212): 'bd migrate --update-repo-id' itself, not only its
// helpers, refuses under --yes and under --json to write over a converged
// shared id an id that hashes the alias as written, because this session
// could not resolve it or this machine's SSH config lacks it; it exits 1 and
// writes nothing. A forge host, which a custom transport leaves unflagged,
// is still written. Tests here set package globals and must not run in
// parallel.
func TestHandleUpdateRepoID_ScriptedUpdateKeepsConvergedID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh is a shell script")
	}
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	oldDidWrite := commandDidWrite.Load()
	t.Cleanup(func() { commandDidWrite.Store(oldDidWrite) })

	for _, session := range []struct {
		name   string
		enter  func(t *testing.T)
		reason string
	}{
		{
			name:   "custom SSH transport",
			enter:  func(t *testing.T) { t.Setenv("GIT_SSH_COMMAND", "ssh -o BatchMode=yes") },
			reason: "GIT_SSH_COMMAND selects a custom SSH transport",
		},
		{
			name:   "alias missing from this machine's SSH config",
			enter:  func(t *testing.T) { useRepoIDSSH(t, unconfiguredRepoIDSSHScript) },
			reason: "this machine's SSH config maps it to no other HostName",
		},
	} {
		for _, mode := range []string{"--yes", "--json"} {
			t.Run(session.name+" "+mode, func(t *testing.T) {
				repo := setupRepoIDUpdateWorkspace(t, "git@alias-a:org/repo.git")
				resolved, err := beads.ComputeRepoFingerprintForPath(repo)
				if err != nil {
					t.Fatal(err)
				}
				if !resolved.AliasResolved() {
					t.Fatalf("test precondition: alias-a must resolve, got %+v", resolved)
				}
				fake := &repoIDMetadataStore{values: map[string]string{"repo_id": resolved.ID}}
				store = fake
				session.enter(t)

				var out string
				var runErr error
				want := []string{session.reason}
				if mode == "--json" {
					jsonOutput = true
					out, runErr = captureStdoutWithError(t, func() error { return handleUpdateRepoID(false, false) })
					want = append(want, `"error": "unresolved_ssh_alias"`)
				} else {
					jsonOutput = false
					out = captureStderr(t, func() { runErr = handleUpdateRepoID(false, true) })
					want = append(want, "not updating repo_id")
				}
				if code, ok := exitCodeFromError(runErr); !ok || code != 1 {
					t.Fatalf("handleUpdateRepoID = %v, want exit 1\n%s", runErr, out)
				}
				for _, w := range want {
					if !strings.Contains(out, w) {
						t.Fatalf("output missing %q:\n%s", w, out)
					}
				}
				if fake.writes != 0 || fake.values["repo_id"] != resolved.ID {
					t.Fatalf("repo_id = %s after %d writes, want the converged %s untouched", fake.values["repo_id"], fake.writes, resolved.ID)
				}
			})
		}
	}

	t.Run("forge host under a custom SSH transport", func(t *testing.T) {
		repo := setupRepoIDUpdateWorkspace(t, "git@gitlab.example.com:org/repo.git")
		t.Setenv("GIT_SSH_COMMAND", "ssh -o BatchMode=yes")
		fp, err := beads.ComputeRepoFingerprintForPath(repo)
		if err != nil {
			t.Fatal(err)
		}
		if fp.AliasUncertainty() != "" {
			t.Fatalf("test precondition: gitlab.example.com must be no alias, got %+v", fp)
		}
		const stored = "0123456789abcdef0123456789abcdef"
		fake := &repoIDMetadataStore{values: map[string]string{"repo_id": stored}}
		store = fake
		jsonOutput = false

		out, runErr := captureStdoutWithError(t, func() error { return handleUpdateRepoID(false, true) })
		if runErr != nil {
			t.Fatalf("handleUpdateRepoID(--yes) = %v, want the update\n%s", runErr, out)
		}
		if fake.writes != 1 || fake.values["repo_id"] != fp.ID {
			t.Fatalf("repo_id = %s after %d writes, want %s written once (stored %s)", fake.values["repo_id"], fake.writes, fp.ID, stored)
		}
	})
}
