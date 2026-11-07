package fix

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
)

func TestRepoFingerprint_AutoYesSkipsPrompt(t *testing.T) {
	dir := setupTestWorkspace(t)

	oldReadLine := repoFingerprintReadLine
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()

	readCalled := false
	repoFingerprintReadLine = func() (string, error) {
		readCalled = true
		return "", nil
	}

	// autoYes=true should not call readLine for prompts.
	// It will fail to open the Dolt database (test environment),
	// but it should NOT have called readLine.
	_ = RepoFingerprint(dir, true)

	if readCalled {
		t.Fatal("expected autoYes path to skip interactive stdin read")
	}
}

func TestRepoFingerprint_SkipChoiceDoesNothing(t *testing.T) {
	dir := setupTestWorkspace(t)

	oldReadLine := repoFingerprintReadLine
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()

	repoFingerprintReadLine = func() (string, error) { return "s", nil }

	// Choice "s" should skip without error
	if err := RepoFingerprint(dir, false); err != nil {
		t.Fatalf("RepoFingerprint(autoYes=false, choice=s) returned error: %v", err)
	}
}

func TestRepoFingerprint_UnrecognizedInputSkips(t *testing.T) {
	dir := setupTestWorkspace(t)

	oldReadLine := repoFingerprintReadLine
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()

	repoFingerprintReadLine = func() (string, error) { return "x", nil }

	// Unrecognized input should skip without error
	if err := RepoFingerprint(dir, false); err != nil {
		t.Fatalf("RepoFingerprint(autoYes=false, choice=x) returned error: %v", err)
	}
}

// captureRepoFingerprintStdout runs fn with os.Stdout redirected and returns
// what it printed. Tests using it must not run in parallel.
func captureRepoFingerprintStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	defer func() { os.Stdout = oldStdout }()
	fn()
	_ = w.Close()
	os.Stdout = oldStdout
	return <-done
}

// GH#5990: a fingerprint mismatch is no evidence the database is
// disposable, and in a shared-ledger deployment it is not. The former
// reinitialize choice [2] must skip like any unrecognized input, and the
// menu must offer only update and skip.
func TestRepoFingerprint_ReinitializeChoicePreservesFiles(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	dbPath := configfile.DefaultConfig().DatabasePath(beadsDir)
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dbPath, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldReadLine := repoFingerprintReadLine
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()
	answers := []string{"2", "yes"}
	reads := 0
	repoFingerprintReadLine = func() (string, error) {
		answer := answers[reads]
		reads++
		return answer, nil
	}

	var runErr error
	out := captureRepoFingerprintStdout(t, func() {
		runErr = RepoFingerprint(dir, false)
	})
	if runErr != nil {
		t.Fatalf("RepoFingerprint(autoYes=false, choice=2) returned error: %v", runErr)
	}
	if reads != 1 {
		t.Fatalf("read %d answers, want only the menu choice (no destructive confirmation)\n%s", reads, out)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("database sentinel = %q, %v; want it preserved\n%s", data, err, out)
	}
	for _, bad := range []string{"[2]", "Reinitializ", "DELETE", "Removing"} {
		if strings.Contains(out, bad) {
			t.Fatalf("output offers or runs reinitialization (%q):\n%s", bad, out)
		}
	}
	for _, want := range []string{"[1] Update repo ID", "[s] Skip", "Choice [1/s]", "every clone", "skipping"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// fakeRepoFingerprintStore stands in for the Dolt store in the update path.
type fakeRepoFingerprintStore struct {
	values map[string]string
	writes int
	closed bool
}

func (f *fakeRepoFingerprintStore) GetMetadata(_ context.Context, key string) (string, error) {
	return f.values[key], nil
}

func (f *fakeRepoFingerprintStore) SetMetadata(_ context.Context, key, value string) error {
	f.values[key] = value
	f.writes++
	return nil
}

func (f *fakeRepoFingerprintStore) Close() error {
	f.closed = true
	return nil
}

// setupAliasFingerprintWorkspace creates a workspace whose origin is
// git@alias-a:org/repo.git, with git config isolated and an ssh first on
// PATH whose configuration maps alias-a to HostName github.com.
func setupAliasFingerprintWorkspace(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh is a shell script")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_SSH", "")
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH_VARIANT", "")

	sshDir := t.TempDir()
	script := `#!/bin/sh
for dest; do :; done
case "${dest#*@}" in
alias-a) echo "hostname github.com" ;;
*) echo "hostname ${dest#*@}" ;;
esac
`
	if err := os.WriteFile(filepath.Join(sshDir, "ssh"), []byte(script), 0o700); err != nil { // #nosec G306 -- test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := setupTestWorkspace(t)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "core.hooksPath", t.TempDir()},
		{"remote", "add", "origin", "git@alias-a:org/repo.git"},
	} {
		cmd := gitCommand(args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// GH#5990: on a ledger stamped with this checkout's old alias hash, the
// repair's update writes the newly computed (resolved) id, not the legacy
// alias hash, exactly once, closes the store, and says the id is shared.
func TestRepoFingerprint_UpdateWritesResolvedIDForSharedLedger(t *testing.T) {
	dir := setupAliasFingerprintWorkspace(t)
	fp, err := beads.ComputeRepoFingerprintForPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fp.AliasResolved() || fp.LegacyID == "" || fp.ID == fp.LegacyID {
		t.Fatalf("test precondition: alias-a must resolve to a new id, got %+v", fp)
	}
	wantID, err := beads.ComputeRepoIDForPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if wantID != fp.ID {
		t.Fatalf("ComputeRepoIDForPath = %s, want the resolved id %s", wantID, fp.ID)
	}

	for _, tc := range []struct {
		name    string
		autoYes bool
		answers []string
	}{
		{name: "choice 1 confirmed", answers: []string{"1", "y"}},
		{name: "--yes", autoYes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRepoFingerprintStore{values: map[string]string{"repo_id": fp.LegacyID}}
			oldNewStore := newRepoFingerprintStore
			newRepoFingerprintStore = func(context.Context, string) (repoFingerprintStore, error) {
				return fake, nil
			}
			t.Cleanup(func() { newRepoFingerprintStore = oldNewStore })

			answers := tc.answers
			oldReadLine := repoFingerprintReadLine
			repoFingerprintReadLine = func() (string, error) {
				if len(answers) == 0 {
					t.Fatal("unexpected prompt")
				}
				answer := answers[0]
				answers = answers[1:]
				return answer, nil
			}
			t.Cleanup(func() { repoFingerprintReadLine = oldReadLine })

			var runErr error
			out := captureRepoFingerprintStdout(t, func() {
				runErr = RepoFingerprint(dir, tc.autoYes)
			})
			if runErr != nil {
				t.Fatalf("RepoFingerprint returned error: %v\n%s", runErr, out)
			}
			if len(answers) != 0 {
				t.Fatalf("unused answers %v\n%s", answers, out)
			}
			if got := fake.values["repo_id"]; got != fp.ID {
				t.Fatalf("repo_id after update = %s, want the resolved id %s (legacy %s)", got, fp.ID, fp.LegacyID)
			}
			if fake.writes != 1 || !fake.closed {
				t.Fatalf("store writes = %d, closed = %v; want exactly one write and a close", fake.writes, fake.closed)
			}
			if !strings.Contains(out, "The repository ID is shared") {
				t.Fatalf("output must explain the shared-ID consequence:\n%s", out)
			}
		})
	}
}
