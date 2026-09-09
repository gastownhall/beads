package ownershiphandoff

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeHandoffToken = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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

func TestGCProviderRejectsUntrustedBinary(t *testing.T) {
	if _, err := NewGCProvider("gc"); err == nil {
		t.Fatal("relative GC binary accepted")
	}
}

func TestGCProviderCanonicalizesSymlinkedAncestor(t *testing.T) {
	physical := t.TempDir()
	link := filepath.Join(t.TempDir(), "gc-bin-dir")
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
// anything and then makes the legacy owner gone. That is byte-for-byte the
// state a crash between a successful handoff-stop and its old_owner_stopped
// checkpoint leaves behind: journal at target_configured with
// mutation_occurred=false, owner actually released. It returns the request and
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
// stop-hook obligation stated in engdocs/design/ownership-handoff-contract.md:
// a provider that answers an already-gone identity-matching process with
// result=stopped/mutates=true lets the handoff converge out of the stop crash
// window. The in-tree fake models that provider, so this is what the rest of
// the suite silently assumes; the companion test below prices the alternative.
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

// TestGCProviderResumeWedgesWhenStopRefusesMissingOwner is the counterfactual
// for the obligation above: a strict provider that refuses to stop a process it
// can no longer see leaves the handoff wedged at target_configured with the
// legacy server actually down and mutation_occurred=false. The refusal is
// fail-closed and journaled, so nothing is silently committed — but the handoff
// cannot complete, which is why the contract states the obligation the in-tree
// fake cannot enforce.
func TestGCProviderResumeWedgesWhenStopRefusesMissingOwner(t *testing.T) {
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
	t.Setenv("GC_HANDOFF_STOP_STRICT_MISSING", "1")
	provider, err := NewGCProvider(binary)
	if err != nil {
		t.Fatal(err)
	}
	request, journalPath := stopCrashWindow(t, city, scope, stoppedFile, provider)
	second, err := Run(context.Background(), request, journalPath, provider, false)
	if err == nil || second.Phase != PhaseTargetConfigured || second.Owner != OwnerLegacyGC ||
		second.ErrorCode != "process_missing" || second.Mutates {
		t.Fatalf("second result=%+v err=%v, want a wedged, fail-closed target_configured refusal", second, err)
	}
	journal, err := Load(journalPath)
	if err != nil || journal.Phase != PhaseTargetConfigured || journal.Owner != OwnerLegacyGC || journal.MutationOccurred {
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
	provider.(*GCProvider).timeout = 10 * time.Millisecond
	request := Request{CityRoot: city, Root: scope, Database: "beads", Workspace: "ws", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
	started := time.Now()
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.Snapshot(context.Background(), request); err == nil {
		t.Fatal("provider accepted a timed-out protocol command")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("timed-out command returned after %s, want bounded pipe drain", elapsed)
	}
	rawPID, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read descendant pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatalf("parse descendant pid %q: %v", rawPID, err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("pipe-holding descendant %d survived timeout", pid)
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
# Variant provider that violates the stop-hook obligation in
# engdocs/design/ownership-handoff-contract.md by refusing to "stop" an
# identity-matching process that is already gone.
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
printf '{"schema_version":1,"operation":"%s","result":"%s","owner":"legacy-gc","mutates":%s,"identity":{"city_root":"%s","scope_root":"%s","database":"%s","workspace":"%s","endpoint":%s,"data_dir":"%s/.gc/data","config_file":"%s/.gc/config","pid":%s,"start_identity":"fake","start_time_ticks":1,"port_holder_pid":%s},"identity_token":"%s","error_code":"%s"}\n' "$operation" "$result" "$mutates" "$city" "$scope" "$database" "$workspace" "$endpoint" "$city" "$city" "$$" "$$" "$token" "$error_code"
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
