package ownershiphandoff

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeHandoffToken = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestDefaultGCHandoffTimeoutLeavesStopResponseHeadroom(t *testing.T) {
	// GC's default managed-Dolt SIGTERM grace is 30 seconds. The protocol
	// caller must leave time for GC to persist the stop state and write its
	// response after that grace; otherwise a legitimate forced stop is made
	// ambiguous when the client kills the protocol process at the same instant.
	if defaultGCHandoffTimeout < 2*time.Minute {
		t.Fatalf("default GC handoff timeout = %s, want coverage for GC's default 30s stop grace and 1m lock-release window", defaultGCHandoffTimeout)
	}
}

func TestGCProviderCompletesOnlyThroughTrustedProtocol(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(city, "gc.log")
	errLogPath := filepath.Join(city, "gc.err")
	binary := fakeGCProtocol(t)
	t.Setenv("GC_HANDOFF_LOG", logPath)
	t.Setenv("GC_HANDOFF_ERR_LOG", errLogPath)
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws",
		Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), request, filepath.Join(scope, "ownership-handoff.json"), provider, false)
	if err != nil || result.Phase != PhaseCommitted || result.Owner != OwnerBD || !result.Mutates {
		errLog, _ := os.ReadFile(errLogPath)
		t.Fatalf("result=%+v err=%v, want committed bd-owned handoff; fake stderr=%q", result, err, errLog)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(log)); got != "handoff-inspect\nhandoff-stop\nhandoff-inspect\nhandoff-inspect" {
		t.Fatalf("protocol operations=%q, want inspect, stop, then two post-stop checks", got)
	}
}

func TestGCProviderRollbackRestartUsesExistingManagedCommand(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(city, "gc.log")
	t.Setenv("GC_HANDOFF_LOG", logPath)
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	provider, err := NewGCProvider(fakeGCProtocol(t))
	if err != nil {
		t.Fatal(err)
	}
	r := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if hooks.RestartLegacy == nil {
		t.Fatal("GC provider did not expose legacy restart hook")
	}
	if err := hooks.RestartLegacy(context.Background(), r, Snapshot{}); err != nil {
		t.Fatalf("restart legacy: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "start-managed" {
		t.Fatalf("GC restart operations=%q, want existing start-managed command", got)
	}
}

func TestScrubGCAmbientEnvironmentRemovesEndpointOverrides(t *testing.T) {
	got := scrubGCAmbientEnvironment([]string{"KEEP=yes", "BEADS_DOLT_SERVER_PORT=9999", "DOLT_ROOT=/wrong", "BEADS_OTHER=no"})
	if strings.Join(got, ",") != "KEEP=yes" {
		t.Fatalf("scrubbed environment=%q, want only unrelated entries", got)
	}
}

func TestGCProviderRejectsDirtyEligibleInspectBeforeConfigure(t *testing.T) {
	for name, env := range map[string]string{
		"mutates":      "GC_HANDOFF_INSPECT_MUTATES=1",
		"error_code":   "GC_HANDOFF_INSPECT_ERROR_CODE=process_missing",
		"wrong_holder": "GC_HANDOFF_INSPECT_WRONG_HOLDER=1",
	} {
		t.Run(name, func(t *testing.T) {
			city := canonicalTestDir(t)
			scope := filepath.Join(city, "scope")
			if err := os.Mkdir(scope, 0o700); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(city, "gc.log")
			t.Setenv("GC_HANDOFF_LOG", logPath)
			t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
			name, value, _ := strings.Cut(env, "=")
			t.Setenv(name, value)
			provider, err := NewGCProvider(fakeGCProtocol(t))
			if err != nil {
				t.Fatal(err)
			}
			r := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
			result, err := Run(context.Background(), r, filepath.Join(scope, "ownership-handoff.json"), provider, false)
			if err == nil || result.Phase != PhasePrepared || result.Mutates {
				t.Fatalf("result=%+v err=%v, want clean pre-configure refusal", result, err)
			}
			log, readErr := os.ReadFile(logPath)
			if readErr != nil || strings.TrimSpace(string(log)) != "handoff-inspect" {
				t.Fatalf("protocol operations=%q read=%v, want inspect only", log, readErr)
			}
		})
	}
}

func TestGCProviderRefusalNeverInvokesStop(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(city, "gc.log")
	errLogPath := filepath.Join(city, "gc.err")
	binary := fakeGCProtocol(t)
	t.Setenv("GC_HANDOFF_LOG", logPath)
	t.Setenv("GC_HANDOFF_ERR_LOG", errLogPath)
	t.Setenv("GC_HANDOFF_REFUSE", "1")
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws",
		Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), request, filepath.Join(scope, "ownership-handoff.json"), provider, false)
	if err == nil || result.ErrorCode != "process_unowned" || result.Mutates {
		errLog, _ := os.ReadFile(errLogPath)
		t.Fatalf("result=%+v err=%v, want process_unowned refusal; fake stderr=%q", result, err, errLog)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(log)); got != "handoff-inspect" {
		t.Fatalf("protocol operations=%q, want inspect only", got)
	}
}

func TestGCProviderProcessMissingInspectPreservesCodeForRollbackRecovery(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(city, "gc.log")
	t.Setenv("GC_HANDOFF_LOG", logPath)
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	if err := os.WriteFile(filepath.Join(scope, ".gc-handoff-stopped"), []byte("stopped"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := NewGCProvider(fakeGCProtocol(t))
	if err != nil {
		t.Fatal(err)
	}
	r := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hooks.Snapshot(context.Background(), r)
	var coded interface{ HandoffErrorCode() string }
	if !errors.As(err, &coded) || coded.HandoffErrorCode() != "process_missing" {
		t.Fatalf("snapshot error=%v, want preserved process_missing for rollback recovery", err)
	}
	log, readErr := os.ReadFile(logPath)
	if readErr != nil || strings.TrimSpace(string(log)) != "handoff-inspect" {
		t.Fatalf("protocol operations=%q read=%v, want inspect only", log, readErr)
	}
}

func TestGCProviderRejectsUntrustedBinary(t *testing.T) {
	if _, err := NewGCProvider("gc"); err == nil {
		t.Fatal("relative GC binary accepted")
	}
}

// TestGCProviderCanonicalizesSymlinkedAncestor pins the EvalSymlinks step in
// canonicalGCExecutable. Both directories come from canonicalTestDir: where the
// temp root is itself reached through a symlink (macOS resolves /var to
// /private/var, and any TMPDIR may be), a raw t.TempDir() makes this assertion
// compare two already-divergent paths, so the test fails whether or not the
// provider canonicalises — which is how the fence ended up unguarded.
func TestGCProviderCanonicalizesSymlinkedAncestor(t *testing.T) {
	physical := canonicalTestDir(t)
	link := filepath.Join(canonicalTestDir(t), "gc-bin-dir")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(physical, "gc")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	provider, err := NewGCProvider(filepath.Join(link, "gc"))
	if err != nil {
		t.Fatalf("NewGCProvider through a symlinked ancestor: %v", err)
	}
	if provider.(*GCProvider).Binary != binary {
		t.Fatalf("provider binary = %q, want canonical %q", provider.(*GCProvider).Binary, binary)
	}
}

func TestGCProviderRejectsIdentityAndTokenSubstitution(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_HANDOFF_DATABASE", "other")
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.Snapshot(context.Background(), request); err == nil {
		t.Fatal("provider accepted substituted identity")
	}
	t.Setenv("GC_HANDOFF_DATABASE", "")
	t.Setenv("GC_HANDOFF_TOKEN", "not-a-token")
	if _, err := hooks.Snapshot(context.Background(), request); err == nil {
		t.Fatal("provider accepted malformed identity token")
	}
}

func TestGCProviderRefusesCommitWhenStopDidNotReleaseOwner(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_STOPPED_FILE", filepath.Join(city, "stopped"))
	t.Setenv("GC_HANDOFF_STOP_LEAVES_LIVE", "1")
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), request, filepath.Join(scope, "ownership-handoff.json"), provider, false)
	if err == nil || result.Phase != PhaseOldOwnerStopped || result.Owner != OwnerLegacyGC || result.ErrorCode != "verification_failed" {
		t.Fatalf("result=%+v err=%v, want uncommitted verification failure", result, err)
	}
}

func TestGCProviderBoundsProtocolCommand(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_SLEEP", "1")
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	provider.(*GCProvider).timeout = 10 * time.Millisecond
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.Snapshot(context.Background(), request); err == nil {
		t.Fatal("provider accepted a timed-out protocol command")
	}
}

func TestGCProviderStopRefusalReportsProviderMutation(t *testing.T) {
	for _, mutates := range []bool{false, true} {
		t.Run(strconv.FormatBool(mutates), func(t *testing.T) {
			city := canonicalTestDir(t)
			scope := filepath.Join(city, "scope")
			if err := os.Mkdir(scope, 0700); err != nil {
				t.Fatal(err)
			}
			binary := fakeGCProtocol(t)
			t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
			t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
			t.Setenv("GC_HANDOFF_STOP_REFUSE", "1")
			t.Setenv("GC_HANDOFF_STOP_MUTATES", strconv.FormatBool(mutates))
			request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
			provider, err := NewGCProvider(binary)
			if err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(scope, "ownership-handoff.json")
			result, err := Run(context.Background(), request, journalPath, provider, false)
			if err == nil || result.Owner != OwnerLegacyGC || result.Phase != PhaseTargetConfigured || result.ErrorCode != "process_unowned" || result.Mutates != mutates {
				t.Fatalf("result=%+v err=%v, want typed stop refusal with mutates=%t", result, err, mutates)
			}
			journal, err := Load(journalPath)
			if err != nil || journal.MutationOccurred != mutates {
				t.Fatalf("journal=%+v err=%v, want mutation_occurred=%t", journal, err, mutates)
			}
		})
	}
}

func TestGCProviderRetryRetainsReportedPartialMutation(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_STOP_REFUSE", "1")
	t.Setenv("GC_HANDOFF_STOP_MUTATES", "true")
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(scope, "ownership-handoff.json")
	first, err := Run(context.Background(), request, journalPath, provider, false)
	if err == nil || !first.Mutates || first.Phase != PhaseTargetConfigured {
		t.Fatalf("first result=%+v err=%v, want partial mutation", first, err)
	}
	t.Setenv("GC_HANDOFF_STOP_MUTATES", "false")
	second, err := Run(context.Background(), request, journalPath, provider, false)
	if err == nil || !second.Mutates || second.Phase != PhaseTargetConfigured {
		t.Fatalf("second result=%+v err=%v, want retained partial mutation", second, err)
	}
	journal, err := Load(journalPath)
	if err != nil || !journal.MutationOccurred {
		t.Fatalf("journal=%+v err=%v, want retained mutation_occurred", journal, err)
	}
}

func TestGCProviderResumeUsesPersistedSnapshotToken(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	logPath := filepath.Join(city, "gc.log")
	t.Setenv("GC_HANDOFF_LOG", logPath)
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_STOP_REFUSE", "1")
	t.Setenv("GC_HANDOFF_STOP_MUTATES", "false")
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(scope, "ownership-handoff.json")
	first, err := Run(context.Background(), request, journalPath, provider, false)
	if err == nil || first.Phase != PhaseTargetConfigured || first.Mutates {
		t.Fatalf("first result=%+v err=%v, want non-mutating stop refusal", first, err)
	}
	t.Setenv("GC_HANDOFF_STOP_REFUSE", "")
	t.Setenv("GC_HANDOFF_TOKEN", "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	second, err := Run(context.Background(), request, journalPath, provider, false)
	if err == nil || second.ErrorCode != "identity_changed" || !second.Mutates {
		t.Fatalf("second result=%+v err=%v, want mutated identity-changed refusal", second, err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(log)); got != "handoff-inspect\nhandoff-stop\nhandoff-stop" {
		t.Fatalf("protocol operations=%q, want retry to use persisted snapshot without re-inspect", got)
	}
}

// stopCrashWindow parks a handoff at target_configured without mutating
// anything and then makes the legacy owner gone: journal at target_configured
// with mutation_occurred=false, owner actually released. That is the resume
// state a crash between a successful handoff-stop and its old_owner_stopped
// checkpoint leaves behind, minus the stop reservation an interrupted stop
// also leaves set (TestStopRetryAfterUncheckpointedStopSucceeds covers that
// variant); both resume through the same retry. It returns the request and
// journal path for the retry that resumes from there.
func stopCrashWindow(t *testing.T, city, scope, stoppedFile string, provider Provider) (Request, string) {
	t.Helper()
	t.Setenv("GC_HANDOFF_STOP_REFUSE", "1")
	t.Setenv("GC_HANDOFF_STOP_MUTATES", "false")
	t.Setenv("GC_HANDOFF_STOP_LEAVES_LIVE", "1")
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws",
		Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	journalPath := filepath.Join(scope, "ownership-handoff.json")
	first, err := Run(context.Background(), request, journalPath, provider, false)
	if err == nil || first.Phase != PhaseTargetConfigured || first.Mutates {
		t.Fatalf("first result=%+v err=%v, want a non-mutating stop refusal", first, err)
	}
	if err := os.WriteFile(stoppedFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	journal, err := Load(journalPath)
	if err != nil || journal.Phase != PhaseTargetConfigured || journal.MutationOccurred {
		t.Fatalf("journal=%+v err=%v, want an unmutated target_configured resume state", journal, err)
	}
	t.Setenv("GC_HANDOFF_STOP_REFUSE", "")
	t.Setenv("GC_HANDOFF_STOP_LEAVES_LIVE", "")
	return request, journalPath
}

// TestGCProviderResumeCompletesWhenStopTreatsMissingOwnerAsStopped pins the
// simplest way out of the stop crash window: a provider that can answer an
// already-gone identity-matching process with result=stopped/mutates=true
// converges on the spot. The in-tree fake models that provider, so this is
// what the rest of the suite assumes; the companion tests below cover the real
// responder, which refuses process_missing instead and converges anyway.
func TestGCProviderResumeCompletesWhenStopTreatsMissingOwnerAsStopped(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	logPath := filepath.Join(city, "gc.log")
	stoppedFile := filepath.Join(city, "stopped")
	t.Setenv("GC_HANDOFF_LOG", logPath)
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_STOPPED_FILE", stoppedFile)
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	request, journalPath := stopCrashWindow(t, city, scope, stoppedFile, provider)
	second, err := Run(context.Background(), request, journalPath, provider, false)
	if err != nil || second.Phase != PhaseCommitted || second.Owner != OwnerBD || !second.Mutates {
		t.Fatalf("second result=%+v err=%v, want a committed resume across the stop crash window", second, err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(log)); got != "handoff-inspect\nhandoff-stop\nhandoff-stop\nhandoff-inspect\nhandoff-inspect" {
		t.Fatalf("protocol operations=%q, want the retry to re-stop from the persisted snapshot and then verify", got)
	}
}

// TestGCProviderResumeConvergesWhenStopRefusesMissingOwner is the case the
// real Gas City responder produces, and the one that used to strand a city.
// GC cannot honestly answer "stopped" for a process it never signaled, so it
// refuses process_missing — and a retry that meets that refusal has the legacy
// server already down. Absence is what the stop was for, so the refusal
// settles the stop rather than wedging at target_configured forever.
func TestGCProviderResumeConvergesWhenStopRefusesMissingOwner(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	logPath := filepath.Join(city, "gc.log")
	stoppedFile := filepath.Join(city, "stopped")
	t.Setenv("GC_HANDOFF_LOG", logPath)
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_STOPPED_FILE", stoppedFile)
	t.Setenv("GC_HANDOFF_STOP_STRICT_MISSING", "1")
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	request, journalPath := stopCrashWindow(t, city, scope, stoppedFile, provider)
	second, err := Run(context.Background(), request, journalPath, provider, false)
	if err != nil || second.Phase != PhaseCommitted || second.Owner != OwnerBD || !second.Mutates {
		t.Fatalf("second result=%+v err=%v, want a committed resume through the refused missing-owner stop", second, err)
	}
	journal, err := Load(journalPath)
	if err != nil || journal.Phase != PhaseCommitted || journal.LegacyStopInProgress {
		t.Fatalf("journal=%+v err=%v, want a committed journal with its stop reservation retired", journal, err)
	}
}

// TestGCProviderResumeConvergesWhenStopReportsNonMutatingStop covers the other
// truthful answer to the same situation: a responder that calls the no-op a
// "stop" but declines to claim it mutated anything. bd must not read that as a
// completed stop it performed, and must not wedge on it either.
func TestGCProviderResumeConvergesWhenStopReportsNonMutatingStop(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	stoppedFile := filepath.Join(city, "stopped")
	t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_STOPPED_FILE", stoppedFile)
	t.Setenv("GC_HANDOFF_STOP_NONMUTATING", "1")
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	request, journalPath := stopCrashWindow(t, city, scope, stoppedFile, provider)
	second, err := Run(context.Background(), request, journalPath, provider, false)
	if err != nil || second.Phase != PhaseCommitted || second.Owner != OwnerBD {
		t.Fatalf("second result=%+v err=%v, want a committed resume through the non-mutating stop", second, err)
	}
}

// TestGCProviderResumeStillRefusesStopOfAnotherIdentity is the other half of
// that relaxation, and the reason it is not simply "ignore stop refusals": the
// convergence above is for an absent owner only. A process that is present but
// cannot be proven to be the legacy owner is a refusal the handoff must keep,
// with the legacy owner left authoritative and the reservation intact.
func TestGCProviderResumeStillRefusesStopOfAnotherIdentity(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	stoppedFile := filepath.Join(city, "stopped")
	t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_STOPPED_FILE", stoppedFile)
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	request, journalPath := stopCrashWindow(t, city, scope, stoppedFile, provider)
	t.Setenv("GC_HANDOFF_STOP_REFUSE", "1")
	t.Setenv("GC_HANDOFF_STOP_MUTATES", "false")
	second, err := Run(context.Background(), request, journalPath, provider, false)
	if err == nil || second.Phase != PhaseTargetConfigured || second.Owner != OwnerLegacyGC ||
		second.ErrorCode != "process_unowned" {
		t.Fatalf("second result=%+v err=%v, want an unproven-identity stop to stay refused", second, err)
	}
	journal, err := Load(journalPath)
	if err != nil || journal.Phase != PhaseTargetConfigured || journal.Owner != OwnerLegacyGC {
		t.Fatalf("journal=%+v err=%v, want the handoff to stay uncommitted and legacy-owned", journal, err)
	}
}

func TestGCProviderResumeAfterStoppedOwnerDoesNotReinspectSnapshot(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	logPath := filepath.Join(city, "gc.log")
	t.Setenv("GC_HANDOFF_LOG", logPath)
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_VERIFY_REFUSE_ONCE", filepath.Join(city, "verify-once"))
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(scope, "ownership-handoff.json")
	first, err := Run(context.Background(), request, journalPath, provider, false)
	if err == nil || first.Phase != PhaseOldOwnerStopped || first.Owner != OwnerLegacyGC || !first.Mutates {
		t.Fatalf("first result=%+v err=%v, want stopped-owner verification failure", first, err)
	}
	second, err := Run(context.Background(), request, journalPath, provider, false)
	if err != nil || second.Phase != PhaseCommitted || second.Owner != OwnerBD || !second.Mutates {
		t.Fatalf("second result=%+v err=%v, want committed resume", second, err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(log)); got != "handoff-inspect\nhandoff-stop\nhandoff-inspect\nhandoff-inspect\nhandoff-inspect" {
		t.Fatalf("protocol operations=%q, want stop resume without a new eligible inspect", got)
	}
}

// TestGCProviderTimeoutKillsPipeHoldingDescendant pins the Setpgid plus
// kill-the-process-group cancel in gcprovider_command_unix.go: an expired
// protocol command must take the whole group with it, or a descendant holding
// the inherited stdout pipe outlives the deadline.
//
// The deadline is gated on the fixture's pid file appearing rather than on a
// fixed wall clock. A short provider timeout races /bin/sh startup, so the fake
// was regularly killed before it had forked the descendant this test is about,
// and the test then failed reading the pid file whether or not the fence was
// intact. The provider timeout stays as a generous backstop so a fixture that
// never becomes ready fails rather than hangs.
func TestGCProviderTimeoutKillsPipeHoldingDescendant(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	pidPath := filepath.Join(city, "descendant.pid")
	t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_DESCENDANT", "1")
	t.Setenv("GC_HANDOFF_DESCENDANT_PID", pidPath)
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	provider.(*GCProvider).timeout = defaultGCHandoffTimeout
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan descendantReady, 1)
	go func() {
		ready <- awaitDescendant(pidPath, 10*time.Second)
		cancel()
	}()
	if _, err := hooks.Snapshot(ctx, request); err == nil {
		t.Fatal("provider accepted an expired protocol command")
	}
	returned := time.Now()
	descendant := <-ready
	if descendant.pid <= 0 {
		t.Fatalf("fixture never recorded a pipe-holding descendant at %s", pidPath)
	}
	if elapsed := returned.Sub(descendant.at); elapsed > 500*time.Millisecond {
		t.Fatalf("expired command returned %s after its deadline, want bounded pipe drain", elapsed)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for processAlive(descendant.pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(descendant.pid) {
		t.Fatalf("pipe-holding descendant %d survived timeout", descendant.pid)
	}
}

// descendantReady reports the fixture's pipe-holding descendant and the moment
// it became observable, which is when the command's deadline is released.
type descendantReady struct {
	pid int
	at  time.Time
}

// awaitDescendant polls for a fully written pid file. It requires a parseable
// positive pid, so a shell redirect caught mid-write is retried rather than
// read as a failure.
func awaitDescendant(path string, timeout time.Duration) descendantReady {
	deadline := time.Now().Add(timeout)
	for {
		if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // test fixture path
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return descendantReady{pid: pid, at: time.Now()}
			}
		}
		if time.Now().After(deadline) {
			return descendantReady{at: time.Now()}
		}
		time.Sleep(time.Millisecond)
	}
}

// TestGCProviderRejectsOversizedProtocolOutput pins maxGCHandoffProtocolOutput
// itself, not just the plumbing: the fake answers a well-formed, decodable
// response whose only fault is size, so raising the cap makes this succeed. The
// oversized width is stated here as a literal rather than derived from the
// constant, or raising the constant would merely make the fixture slower.
func TestGCProviderRejectsOversizedProtocolOutput(t *testing.T) {
	const documentedCap = 1 << 20
	if maxGCHandoffProtocolOutput != documentedCap {
		t.Fatalf("output cap = %d, want the documented %d bytes", maxGCHandoffProtocolOutput, documentedCap)
	}
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	t.Setenv("GC_HANDOFF_PAD", strconv.Itoa(documentedCap+1024))
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.Snapshot(context.Background(), request); err == nil {
		t.Fatalf("provider accepted a response larger than the %d byte output cap", documentedCap)
	}
	// The same response under the cap must still be accepted, so the test fails
	// on the size rule rather than on the padding itself.
	t.Setenv("GC_HANDOFF_PAD", "1024")
	if _, err := hooks.Snapshot(context.Background(), request); err != nil {
		t.Fatalf("provider refused a padded but in-bounds response: %v", err)
	}
}

// TestGCProviderStopRejectsForeignSnapshotSentinel pins the sentinel match in
// snapshotIdentityToken. The journal's snapshot metadata and its sentinel are
// two durable fields that a tampered or truncated journal can disagree on;
// without the match, the stop would proceed on a token the snapshot never
// witnessed.
func TestGCProviderStopRejectsForeignSnapshotSentinel(t *testing.T) {
	city := canonicalTestDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	binary := fakeGCProtocol(t)
	t.Setenv("GC_HANDOFF_LOG", filepath.Join(city, "gc.log"))
	t.Setenv("GC_HANDOFF_ERR_LOG", filepath.Join(city, "gc.err"))
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := hooks.Snapshot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Sentinel != fakeHandoffToken {
		t.Fatalf("snapshot sentinel = %q, want the inspected identity token", snapshot.Sentinel)
	}
	forged := Snapshot{Metadata: snapshot.Metadata,
		Sentinel: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
	err = hooks.StopLegacy(context.Background(), request, forged)
	if handoffErrorCode(err, "") != "identity_changed" {
		t.Fatalf("stop with a mismatched sentinel returned %v, want identity_changed", err)
	}
	log, readErr := os.ReadFile(filepath.Join(city, "gc.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.TrimSpace(string(log)); got != "handoff-inspect" {
		t.Fatalf("protocol operations=%q, want the stop refused before it was invoked", got)
	}
}

func TestDecodeGCResponseRejectsUnknownFieldsAndTrailingObjects(t *testing.T) {
	response := gcHandoffResponse{SchemaVersion: gcHandoffSchemaVersion, Operation: "handoff-inspect",
		Result: "eligible", Owner: OwnerLegacyGC, IdentityToken: fakeHandoffToken}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGCResponse(append(raw, []byte("{}")...)); err == nil {
		t.Fatal("trailing JSON object accepted")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["extra"] = json.RawMessage("true")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGCResponse(raw); err == nil {
		t.Fatal("unknown response field accepted")
	}
}

func canonicalTestDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func fakeGCProtocol(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gc")
	script := `#!/bin/sh
set -e
exec 2>"$GC_HANDOFF_ERR_LOG"
operation="$2"
city=""
scope=""
database=""
workspace=""
host=""
port=""
socket=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --city) city="$2"; shift 2 ;;
    --scope-root) scope="$2"; shift 2 ;;
    --database) database="$2"; shift 2 ;;
    --workspace) workspace="$2"; shift 2 ;;
    --host) host="$2"; shift 2 ;;
    --port) port="$2"; shift 2 ;;
    --socket) socket="$2"; shift 2 ;;
    --identity-token) shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\n' "$operation" >> "$GC_HANDOFF_LOG"
if [ "$GC_HANDOFF_SLEEP" = "1" ]; then sleep 1; fi
if [ "$GC_HANDOFF_DESCENDANT" = "1" ]; then
  sleep 5 &
  printf '%s\n' "$!" > "$GC_HANDOFF_DESCENDANT_PID"
  wait
fi
stopped_file="${GC_HANDOFF_STOPPED_FILE:-$scope/.gc-handoff-stopped}"
already_stopped=0
if [ -f "$stopped_file" ]; then already_stopped=1; fi
result=eligible
mutates=false
error_code=""
if [ "$operation" = "handoff-inspect" ] && [ -f "$stopped_file" ]; then
  result=refused
  error_code=process_missing
  if [ -n "$GC_HANDOFF_VERIFY_REFUSE_ONCE" ] && [ ! -f "$GC_HANDOFF_VERIFY_REFUSE_ONCE" ]; then
    : > "$GC_HANDOFF_VERIFY_REFUSE_ONCE"
    error_code=lifecycle_busy
  fi
fi
if [ "$GC_HANDOFF_REFUSE" = "1" ]; then
  result=refused
  error_code=process_unowned
fi
if [ "$operation" = "handoff-inspect" ] && [ "$GC_HANDOFF_INSPECT_MUTATES" = "1" ]; then mutates=true; fi
if [ "$operation" = "handoff-inspect" ] && [ -n "$GC_HANDOFF_INSPECT_ERROR_CODE" ]; then error_code="$GC_HANDOFF_INSPECT_ERROR_CODE"; fi
if [ "$operation" = "handoff-stop" ]; then
  result=stopped
  mutates=true
  if [ "$GC_HANDOFF_STOP_LEAVES_LIVE" != "1" ]; then : > "$stopped_file"; fi
fi
if [ "$operation" = "handoff-stop" ] && [ "$GC_HANDOFF_STOP_REFUSE" = "1" ]; then
  result=refused
  mutates="${GC_HANDOFF_STOP_MUTATES:-false}"
  error_code=process_unowned
fi
# A stop that truthfully reports it changed nothing: the owner was already gone
# when GC looked, so there was nothing to signal.
if [ "$operation" = "handoff-stop" ] && [ "$GC_HANDOFF_STOP_NONMUTATING" = "1" ]; then mutates=false; fi
# Variant provider that answers the stop-hook obligation in
# engdocs/design/ownership-handoff-contract.md the way the real responder does:
# by refusing to call an identity-matching process that is already gone
# "stopped".
if [ "$operation" = "handoff-stop" ] && [ "$GC_HANDOFF_STOP_STRICT_MISSING" = "1" ] && [ "$already_stopped" = "1" ]; then
  result=refused
  mutates=false
  error_code=process_missing
fi
if [ -n "$socket" ]; then
  endpoint=$(printf '{"host":"","port":0,"socket":"%s"}' "$socket")
else
  endpoint=$(printf '{"host":"%s","port":%s,"socket":""}' "$host" "$port")
fi
if [ -n "$GC_HANDOFF_DATABASE" ]; then database="$GC_HANDOFF_DATABASE"; fi
if [ -n "$GC_HANDOFF_TOKEN" ]; then token="$GC_HANDOFF_TOKEN"; else token="sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"; fi
# GC_HANDOFF_PAD inflates an otherwise valid response past the output cap.
if [ -n "$GC_HANDOFF_PAD" ]; then
  start_identity=$(head -c "$GC_HANDOFF_PAD" /dev/zero | tr '\0' 'x')
else
  start_identity=fake
fi
holder="$$"
if [ "$operation" = "handoff-inspect" ] && [ "$GC_HANDOFF_INSPECT_WRONG_HOLDER" = "1" ]; then holder=1; fi
printf '{"schema_version":1,"operation":"%s","result":"%s","owner":"legacy-gc","mutates":%s,"identity":{"city_root":"%s","scope_root":"%s","database":"%s","workspace":"%s","endpoint":%s,"data_dir":"%s/.gc/data","config_file":"%s/.gc/config","pid":%s,"start_identity":"%s","start_time_ticks":1,"port_holder_pid":%s},"identity_token":"%s","error_code":"%s"}\n' "$operation" "$result" "$mutates" "$city" "$scope" "$database" "$workspace" "$endpoint" "$city" "$city" "$$" "$start_identity" "$holder" "$token" "$error_code"
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
