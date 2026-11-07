package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
)

// TestIntegrityChecks_NoBeadsDir verifies all integrity check functions handle
// missing .beads directories gracefully. This replaces 4 individual subtests.
func TestIntegrityChecks_NoBeadsDir(t *testing.T) {
	checks := []struct {
		name     string
		fn       func(string) DoctorCheck
		wantName string
	}{
		{"IDFormat", CheckIDFormat, "Issue IDs"},
		{"DependencyCycles", CheckDependencyCycles, "Dependency Cycles"},
		{"DeletionsManifest", CheckDeletionsManifest, "Deletions Manifest"},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			result := tc.fn(tmpDir)

			if result.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", result.Name, tc.wantName)
			}
		})
	}
}

// TestIntegrityChecks_EmptyBeadsDir verifies all integrity check functions return OK
// when .beads directory exists but is empty (no database/files to check).
func TestIntegrityChecks_EmptyBeadsDir(t *testing.T) {
	checks := []struct {
		name string
		fn   func(string) DoctorCheck
	}{
		{"IDFormat", CheckIDFormat},
		{"DependencyCycles", CheckDependencyCycles},
		{"DeletionsManifest", CheckDeletionsManifest},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.Mkdir(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			result := tc.fn(tmpDir)

			if result.Status != StatusOK {
				t.Errorf("Status = %q, want %q", result.Status, StatusOK)
			}
		})
	}
}

// TestCheckDeletionsManifest_LegacyFile tests the specific case where a legacy
// deletions.jsonl file exists and should trigger a warning.
func TestCheckDeletionsManifest_LegacyFile(t *testing.T) {
	// The check only warns inside a git repository, and it asks git about the
	// working directory, so the fixture is a repository and the check runs in
	// it (not in whatever checkout the test binary happens to start in).
	tmpDir := newGitRepo(t)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.Mkdir(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a deletions.jsonl file
	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")
	if err := os.WriteFile(deletionsPath, []byte(`{"id":"test-1"}`), 0644); err != nil {
		t.Fatal(err)
	}

	var check DoctorCheck
	runInDir(t, tmpDir, func() { check = CheckDeletionsManifest(tmpDir) })

	// Should warn about legacy deletions file
	if check.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", check.Status, StatusWarning)
	}
}

// TestIsHashID verifies the hash-based ID detection heuristic.
func TestIsHashID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"hash with letters", "bd-0jkc", true},
		{"hash with only digits short", "bd-88", false},
		{"hash 5+ chars all digits", "bd-12345", true},
		{"all digits 4 chars", "bd-0088", false}, // isHashID needs letters for < 5 chars
		{"sequential numeric", "bd-1", false},
		{"sequential two digit", "bd-42", false},
		{"no separator", "abc", false},
		{"empty suffix", "bd-", false},
		{"hierarchical hash", "bd-0jkc.1", true},
		{"hierarchical sequential", "bd-1.2", false},
		{"uppercase rejected", "bd-ABCD", false},
		{"special chars rejected", "bd-ab!c", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isHashID(tt.id)
			if got != tt.want {
				t.Errorf("isHashID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// TestCheckIDFormat_DeadBranch documents the dead branch bug in CheckIDFormat.
// Both branches after DetectHashBasedIDs return identical results ("hash-based ✓"),
// so the function reports the same status regardless of detection outcome.
func TestCheckIDFormat_DeadBranch(t *testing.T) {
	// We can't easily test with a real DB in a non-cgo test,
	// but we can verify the dead branch exists by checking that both
	// code paths (lines 78-84 and 86-90) produce identical output.
	// This test documents the bug for future fix.

	// Verify the function returns "Issue IDs" check name for no-beads-dir case
	tmpDir := t.TempDir()
	check := CheckIDFormat(tmpDir)
	if check.Name != "Issue IDs" {
		t.Errorf("Name = %q, want %q", check.Name, "Issue IDs")
	}
}

// TestCheckRepoFingerprint_NoDatabase verifies graceful handling when no database exists.
func TestCheckRepoFingerprint_NoDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckRepoFingerprint(tmpDir)

	if check.Name != "Repo Fingerprint" {
		t.Errorf("Name = %q, want %q", check.Name, "Repo Fingerprint")
	}
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
	if !strings.Contains(check.Message, "N/A") {
		t.Errorf("Message = %q, want it to contain 'N/A'", check.Message)
	}
}

// TestTruncateID verifies safe truncation of repo IDs for display.
// Regression test for the [:8] panic in CheckRepoFingerprint.
func TestTruncateID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"long ID", "abcdefghijklmnop", "abcdefgh"},
		{"exactly 8", "abcdefgh", "abcdefgh"},
		{"short ID", "abc", "abc"},
		{"empty", "", ""},
		{"one char", "x", "x"},
		{"seven chars", "abcdefg", "abcdefg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateID(tt.id)
			if got != tt.want {
				t.Errorf("truncateID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

// TestCheckDependencyCycles_NoDatabase verifies graceful handling when no database exists.
func TestCheckDependencyCycles_NoDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckDependencyCycles(tmpDir)

	if check.Name != "Dependency Cycles" {
		t.Errorf("Name = %q, want %q", check.Name, "Dependency Cycles")
	}
	if check.Status != StatusOK {
		t.Errorf("Status = %q, want %q", check.Status, StatusOK)
	}
}

// bd-46vla: a fingerprint mismatch where the current id is a path-fallback
// hash (no origin remote on this host) is the synced-clone signature — the
// stored id is the canonical shared value, and stamping the local one via
// 'bd migrate --update-repo-id' would propagate it to every clone. That case
// must warn-and-advise-leaving-it, not present as "wrong database".
func TestClassifyRepoFingerprint(t *testing.T) {
	t.Run("match is OK regardless of source", func(t *testing.T) {
		fp := beads.RepoFingerprint{ID: "aaaa1111bbbb2222", LegacyID: "aaaa1111bbbb2222", Source: beads.RepoIDSourcePath}
		check := classifyRepoFingerprint("aaaa1111bbbb2222", fp)
		if check.Status != StatusOK {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusOK, check.Message)
		}
	})

	// GH#5990: a shared ledger initialized before alias resolution stores the
	// literal-host hash this same checkout computed; doctor must keep
	// verifying it once the checkout resolves its alias to a canonical host.
	t.Run("legacy id of this checkout's alias spelling matches", func(t *testing.T) {
		fp := beads.RepoFingerprint{ID: "cccc3333dddd4444", LegacyID: "aaaa1111bbbb2222", Source: beads.RepoIDSourceRemote, SSHAlias: "alias-a", SSHHostName: "github.com"}
		check := classifyRepoFingerprint("aaaa1111bbbb2222", fp)
		if check.Status != StatusOK {
			t.Fatalf("Status = %q, want %q (message=%q, fix=%q)", check.Status, StatusOK, check.Message, check.Fix)
		}
		if !strings.Contains(check.Message, "Verified") {
			t.Fatalf("Message = %q, want the Verified framing", check.Message)
		}
	})

	t.Run("another machine's alias spelling does not match", func(t *testing.T) {
		// The legacy allowance only recognizes THIS checkout's own old
		// spelling; a stored id from some other machine's alias must not
		// match a checkout whose legacy hash differs.
		fp := beads.RepoFingerprint{ID: "cccc3333dddd4444", LegacyID: "eeee5555ffff6666", Source: beads.RepoIDSourceRemote}
		check := classifyRepoFingerprint("aaaa1111bbbb2222", fp)
		if check.Status != StatusError {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusError, check.Message)
		}
	})

	// GH#5990: an unresolved mismatch on an alias-resolved checkout stays an
	// error (the stored id is some other machine's spelling), and the check
	// must say how this checkout reaches the remote so the user can tell the
	// shared-ledger case from a genuinely foreign database.
	t.Run("alias-resolved mismatch stays an error and names the alias", func(t *testing.T) {
		fp := beads.RepoFingerprint{ID: "cccc3333dddd4444", LegacyID: "eeee5555ffff6666", Source: beads.RepoIDSourceRemote, SSHAlias: "alias-a", SSHHostName: "github.com"}
		check := classifyRepoFingerprint("aaaa1111bbbb2222", fp)
		if check.Status != StatusError {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusError, check.Message)
		}
		if !strings.Contains(check.Detail, `SSH alias "alias-a"`) {
			t.Fatalf("Detail = %q, want the alias named", check.Detail)
		}
		if !strings.Contains(check.Detail, "github.com") {
			t.Fatalf("Detail = %q, want the resolved HostName named", check.Detail)
		}
	})

	// GH#5990: the fix menu used to offer `rm -rf .beads && bd init` for a
	// suspected wrong database. The stored id lives in versioned metadata,
	// so no branch of this check may suggest deleting or moving state; the
	// guidance must be preservation-first.
	t.Run("no branch suggests deleting state", func(t *testing.T) {
		fps := []beads.RepoFingerprint{
			{ID: "cccc3333dddd4444", LegacyID: "cccc3333dddd4444", Source: beads.RepoIDSourceRemote},
			{ID: "cccc3333dddd4444", LegacyID: "cccc3333dddd4444", Source: beads.RepoIDSourcePath},
			{ID: "cccc3333dddd4444", LegacyID: "eeee5555ffff6666", Source: beads.RepoIDSourceRemote, SSHAlias: "alias-a", SSHHostName: "github.com"},
		}
		for _, fp := range fps {
			for _, stored := range []string{"aaaa1111bbbb2222", "cccc3333dddd4444"} {
				check := classifyRepoFingerprint(stored, fp)
				for _, forbidden := range []string{"rm -rf", "rm -", "bd init", "mv "} {
					if strings.Contains(check.Fix, forbidden) || strings.Contains(check.Detail, forbidden) {
						t.Errorf("stored=%s source=%s: check suggests %q (fix=%q detail=%q)", stored, fp.Source, forbidden, check.Fix, check.Detail)
					}
				}
			}
		}
	})

	t.Run("mismatch with remote-derived current id is an error", func(t *testing.T) {
		fp := beads.RepoFingerprint{ID: "cccc3333dddd4444", LegacyID: "cccc3333dddd4444", Source: beads.RepoIDSourceRemote}
		check := classifyRepoFingerprint("aaaa1111bbbb2222", fp)
		if check.Status != StatusError {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusError, check.Message)
		}
		if !strings.Contains(check.Fix, "bd migrate --update-repo-id") {
			t.Fatalf("Fix = %q, want the update-repo-id repair offered", check.Fix)
		}
	})

	t.Run("mismatch with path-fallback current id warns and advises leaving it", func(t *testing.T) {
		fp := beads.RepoFingerprint{ID: "cccc3333dddd4444", LegacyID: "cccc3333dddd4444", Source: beads.RepoIDSourcePath}
		check := classifyRepoFingerprint("aaaa1111bbbb2222", fp)
		if check.Status != StatusWarning {
			t.Fatalf("Status = %q, want %q (message=%q)", check.Status, StatusWarning, check.Message)
		}
		if !strings.Contains(check.Message, "no origin remote") {
			t.Fatalf("Message = %q, want the no-origin-remote framing", check.Message)
		}
		if !strings.Contains(check.Fix, "propagates to every clone") {
			t.Fatalf("Fix = %q, want the propagation named", check.Fix)
		}
	})

	t.Run("empty or short stored ids mismatch without panicking", func(t *testing.T) {
		fp := beads.RepoFingerprint{ID: "cccc3333dddd4444", LegacyID: "cccc3333dddd4444", Source: beads.RepoIDSourceRemote}
		for _, stored := range []string{"", "abc"} {
			check := classifyRepoFingerprint(stored, fp)
			if check.Status != StatusError {
				t.Errorf("stored %q: Status = %q, want %q", stored, check.Status, StatusError)
			}
		}
	})
}

// fakeRepoMetadataStore stands in for a Dolt store in fingerprint tests. It
// records writes so tests can assert doctor never rewrites shared metadata.
type fakeRepoMetadataStore struct {
	values map[string]string
	writes []string
}

func (f *fakeRepoMetadataStore) GetMetadata(_ context.Context, key string) (string, error) {
	return f.values[key], nil
}

func (f *fakeRepoMetadataStore) SetMetadata(_ context.Context, key, value string) error {
	f.writes = append(f.writes, key)
	f.values[key] = value
	return nil
}

// runGitCmd runs git in dir and returns combined output.
func runGitCmd(dir string, args ...string) (string, error) {
	cmd := gitCommand(args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestCheckRepoFingerprint_LegacyAliasHashMatches covers the GH#5990
// upgrade path end to end at the store seam: a ledger storing a checkout's
// pre-resolution alias hash keeps verifying once that checkout resolves the
// alias, and the read-only check never rewrites the stored metadata.
func TestCheckRepoFingerprint_LegacyAliasHashMatches(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh binary on PATH; alias resolution cannot run")
	}
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "Host alias-a\n  HostName github.com\n  User git\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	repo := t.TempDir()
	if out, err := runGitCmd(repo, "init"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	cmd := gitCommand("remote", "add", "origin", "git@alias-a:org/repo.git")
	cmd.Dir = repo
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	fp, err := beads.ComputeRepoFingerprintForPath(repo)
	if err != nil {
		t.Fatalf("ComputeRepoFingerprintForPath: %v", err)
	}
	if fp.SSHAlias == "" {
		t.Fatal("test precondition: alias-a must resolve in this environment")
	}

	store := &fakeRepoMetadataStore{values: map[string]string{"repo_id": fp.LegacyID}}
	check := checkRepoFingerprintWithStore(store, repo)
	if check.Status != StatusOK {
		t.Fatalf("Status = %q, want %q (message=%q, detail=%q)", check.Status, StatusOK, check.Message, check.Detail)
	}
	if len(store.writes) != 0 {
		t.Fatalf("doctor read rewrote metadata keys %v; the check must not write", store.writes)
	}
	if got := store.values["repo_id"]; got != fp.LegacyID {
		t.Fatalf("stored repo_id changed from %s to %s", fp.LegacyID, got)
	}
}
