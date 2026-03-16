package fix

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

func TestRepoFingerprint_ReinitializeFallsBackWhenMetadataMalformed(t *testing.T) {
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.WriteFile(filepath.Join(beadsDir, configfile.ConfigFileName), []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write malformed metadata: %v", err)
	}

	oldReadLine := repoFingerprintReadLine
	oldReinitialize := repoFingerprintReinitialize
	defer func() {
		repoFingerprintReadLine = oldReadLine
		repoFingerprintReinitialize = oldReinitialize
	}()

	responses := []string{"2", "y"}
	repoFingerprintReadLine = func() (string, error) {
		response := responses[0]
		responses = responses[1:]
		return response, nil
	}

	var gotPlan *repoFingerprintReinitPlan
	repoFingerprintReinitialize = func(_ context.Context, plan *repoFingerprintReinitPlan) error {
		gotPlan = plan
		return nil
	}

	if err := RepoFingerprint(dir, false); err != nil {
		t.Fatalf("RepoFingerprint returned error: %v", err)
	}
	if gotPlan == nil {
		t.Fatal("expected reinitialize plan to be executed")
	}
	if gotPlan.database != configfile.DefaultDoltDatabase {
		t.Fatalf("plan.database = %q, want %q", gotPlan.database, configfile.DefaultDoltDatabase)
	}
	wantDeleteTarget := filepath.Join(beadsDir, "dolt", configfile.DefaultDoltDatabase)
	if gotPlan.deleteTarget != wantDeleteTarget {
		t.Fatalf("plan.deleteTarget = %q, want %q", gotPlan.deleteTarget, wantDeleteTarget)
	}
}
