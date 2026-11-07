package fix

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
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

// TestRepoFingerprint_ReinitializeChoicePreservesFiles pins the GH#5990
// removal of the destructive menu option: a user answering the old "2"
// (reinitialize) input — even confirming with "y" — must not delete the
// database directory, and the menu must no longer offer reinitialization.
func TestRepoFingerprint_ReinitializeChoicePreservesFiles(t *testing.T) {
	dir := setupTestWorkspace(t)
	doltDir := filepath.Join(dir, ".beads", "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create .beads/dolt: %v", err)
	}
	sentinel := filepath.Join(doltDir, "sentinel.json")
	if err := os.WriteFile(sentinel, []byte("{}"), 0o644); err != nil {
		t.Fatalf("failed to write sentinel: %v", err)
	}

	answers := []string{"2", "y"}
	oldReadLine := repoFingerprintReadLine
	repoFingerprintReadLine = func() (string, error) {
		if len(answers) == 0 {
			return "", nil
		}
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	runErr := RepoFingerprint(dir, false)
	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured output: %v", err)
	}
	os.Stdout = oldStdout

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("answering the old reinitialize choice destroyed the database directory: %v", err)
	}
	if runErr != nil {
		t.Fatalf("RepoFingerprint(old choice 2 + confirm) returned error: %v", runErr)
	}
	if strings.Contains(string(out), "Reinitialize") {
		t.Fatalf("output must not offer reinitialization:\n%s", string(out))
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

// TestRepoFingerprint_UpdateWritesResolvedIDForSharedLedger pins the
// GH#5990 repair end to end: choosing [1] on a checkout whose SSH alias
// now resolves writes the resolved (canonical) repo id — not the legacy
// alias hash — into the metadata store, after a confirm that explains the
// shared-ID consequence.
func TestRepoFingerprint_UpdateWritesResolvedIDForSharedLedger(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh binary on PATH; alias resolution cannot run")
	}
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sshConfig := "Host alias-a\n  HostName github.com\n  User git\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(sshConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "git@alias-a:org/repo.git"}} {
		cmd := gitCommand(args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	fp, err := beads.ComputeRepoFingerprintForPath(repo)
	if err != nil {
		t.Fatalf("ComputeRepoFingerprintForPath: %v", err)
	}
	if fp.SSHAlias == "" || fp.ID == fp.LegacyID {
		t.Fatal("test precondition: alias-a must resolve to a changed host in this environment")
	}

	fake := &fakeRepoFingerprintStore{values: map[string]string{"repo_id": fp.LegacyID}}
	oldNewStore := newRepoFingerprintStore
	newRepoFingerprintStore = func(ctx context.Context, beadsDir string) (repoFingerprintStore, error) {
		return fake, nil
	}
	defer func() { newRepoFingerprintStore = oldNewStore }()

	answers := []string{"1", "y"}
	oldReadLine := repoFingerprintReadLine
	repoFingerprintReadLine = func() (string, error) {
		if len(answers) == 0 {
			return "", nil
		}
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	defer func() {
		repoFingerprintReadLine = oldReadLine
	}()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	runErr := RepoFingerprint(repo, false)
	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured output: %v", err)
	}
	os.Stdout = oldStdout

	if runErr != nil {
		t.Fatalf("RepoFingerprint(choice 1, confirm y) returned error: %v", runErr)
	}
	if got := fake.values["repo_id"]; got != fp.ID {
		t.Fatalf("repo_id after update = %s, want the resolved id %s", got, fp.ID)
	}
	if fake.writes != 1 || !fake.closed {
		t.Fatalf("store writes = %d, closed = %v; want exactly one write and a close", fake.writes, fake.closed)
	}
	if !strings.Contains(string(out), "every clone") {
		t.Fatalf("output must explain the shared-ID consequence:\n%s", string(out))
	}
}
