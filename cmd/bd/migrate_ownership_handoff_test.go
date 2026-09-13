package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/ownershiphandoff"
	"github.com/steveyegge/beads/internal/procid"
)

func TestRetireVerifiedDirectTargetAllowsPrelistenChildWithoutStateFiles(t *testing.T) {
	if !doltserver.SupportsStrictLaunchRecovery() {
		t.Skip("platform cannot safely capture process-birth identity")
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	birth, err := procid.Capture(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	oldState := handoffDoltIsRunning
	handoffDoltIsRunning = func(string) (*doltserver.State, error) {
		t.Fatal("strict retirement must not call mutable IsRunning")
		return nil, nil
	}
	t.Cleanup(func() { handoffDoltIsRunning = oldState })
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	request := ownershiphandoff.Request{Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: port}}
	snapshot := ownershiphandoff.Snapshot{TargetPID: child.Process.Pid, TargetBirth: string(birth), TargetDataDir: filepath.Join(beadsDir, "dolt")}
	if err := retireVerifiedDirectTarget(beadsDir, request, snapshot); err != nil {
		t.Fatalf("retire prelisten child: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		alive, verifyErr := procid.Verify(child.Process.Pid, birth)
		if verifyErr != nil || !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retired prelisten child remained alive")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRetireUncheckpointedStrictTargetCapturesThenRetiresExactRecovery(t *testing.T) {
	if !doltserver.SupportsStrictLaunchRecovery() {
		t.Skip("platform cannot safely capture process-birth identity")
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	oldState, oldStart := handoffDoltIsRunning, handoffStrictStart
	birth, err := procid.Capture(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	handoffDoltIsRunning = func(string) (*doltserver.State, error) {
		t.Fatal("strict retirement must not call mutable IsRunning")
		return nil, nil
	}
	handoffStrictStart = func(beadsDir string, options doltserver.StartOptions) (*doltserver.State, error) {
		if !options.RequireFresh || !options.RecoverOnly || options.ExpectedPort != port {
			t.Fatalf("rollback strict recovery options=%+v", options)
		}
		return &doltserver.State{Running: true, PID: child.Process.Pid, Birth: string(birth), Port: port, DataDir: filepath.Join(beadsDir, "dolt")}, nil
	}
	t.Cleanup(func() { handoffDoltIsRunning, handoffStrictStart = oldState, oldStart })
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	request := ownershiphandoff.Request{Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: port}}
	snapshot := ownershiphandoff.Snapshot{TargetLaunchID: "0123456789abcdef0123456789abcdef", TargetLaunchConfig: filepath.Join(beadsDir, "dolt-handoff-0123456789abcdef0123456789abcdef.yaml"), TargetLaunchExecutable: "/usr/bin/dolt"}
	if err := retireUncheckpointedStrictTarget(beadsDir, request, snapshot); err != nil {
		t.Fatalf("recover then retire exact strict target: %v", err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("recovered strict target exited cleanly after SIGKILL, want signal exit")
	}
}

func TestOwnershipHandoffCommandIsExplicitAndSkipsStore(t *testing.T) {
	if ownershipHandoffCmd == nil {
		t.Fatal("ownership-handoff command is not registered")
	}
	if ownershipHandoffCmd.Parent() != migrateCmd {
		t.Fatalf("parent = %v, want migrate", ownershipHandoffCmd.Parent())
	}
	if ownershipHandoffCmd.Annotations[skipStoreAnnotation] != "1" {
		t.Fatal("ownership-handoff must skip ordinary store/provider startup")
	}
	for _, command := range rootCmd.Commands() {
		if command == ownershipHandoffCmd {
			t.Fatal("ownership-handoff must not be an ordinary top-level bd command")
		}
	}
	for _, name := range []string{"city", "root", "database", "workspace", "host", "port", "socket", "journal", "dry-run"} {
		if ownershipHandoffCmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
	// A handoff always resumes from its journal, so there is no resume mode to
	// select. Accepting these spellings and ignoring them told operators the
	// front door had a retry knob it never had.
	for _, name := range []string{"resume", "retry"} {
		if ownershipHandoffCmd.Flags().Lookup(name) != nil {
			t.Errorf("--%s is a no-op flag and must not be offered", name)
		}
	}
	if !strings.Contains(ownershipHandoffCmd.Long, "resumes") {
		t.Errorf("help must state that a handoff resumes from its journal: %q", ownershipHandoffCmd.Long)
	}
}

func TestPendingHandoffFencesDoltStartAndStopUnderSharedGate(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	request := ownershiphandoff.Request{CityRoot: root, Root: root, Database: "beads", Workspace: "ws", Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	journal, err := json.Marshal(ownershiphandoff.Journal{Request: request, Phase: ownershiphandoff.PhaseTargetConfigured, Owner: ownershiphandoff.OwnerLegacyGC})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "ownership-handoff.json"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []*cobra.Command{doltStartCmd, doltStopCmd, doctorCmd} {
		t.Run(cmd.Name(), func(t *testing.T) {
			name := cmd.Name()
			if !isSkipStoreHandoffLifecycleCommand(cmd) {
				t.Fatalf("%s is not recognized as a fenced lifecycle command", name)
			}
			if err := acquireHandoffFenceWorkspaceGates(context.Background(), beadsDir); err != nil {
				t.Fatalf("acquire shared fence: %v", err)
			}
			err := guardNormalOwnershipHandoff(cmd, beadsDir)
			releaseWorkspaceGates()
			var coded interface{ HandoffErrorCode() string }
			if !errors.As(err, &coded) || coded.HandoffErrorCode() != "lifecycle_busy" {
				t.Fatalf("%s guard error=%v, want lifecycle_busy before lifecycle invocation", name, err)
			}
		})
	}
}

func TestOwnershipHandoffCommandMissingIdentityUsesTypedJSON(t *testing.T) {
	setOwnershipHandoffFlag(t, "root", "")
	setOwnershipHandoffFlag(t, "city", "")
	setOwnershipHandoffFlag(t, "database", "")
	setOwnershipHandoffFlag(t, "workspace", "")
	setOwnershipHandoffFlag(t, "journal", "")
	setOwnershipHandoffFlag(t, "socket", "")
	setOwnershipHandoffFlag(t, "host", "127.0.0.1")
	setOwnershipHandoffFlag(t, "port", "3307")
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	var runErr error
	out := captureStdout(t, func() error {
		runErr = runOwnershipHandoffCommand(ownershipHandoffCmd, nil)
		return nil
	})
	if runErr == nil {
		t.Fatal("missing identity unexpectedly succeeded")
	}
	var got ownershipHandoffOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode strict handoff JSON %q: %v", out, err)
	}
	if got.ErrorCode != "invalid_request" || got.Phase != ownershiphandoff.PhasePrepared {
		t.Fatalf("output=%+v, want typed invalid_request", got)
	}
}

func TestOwnershipHandoffRunRejectsInvalidRequestBeforeProvider(t *testing.T) {
	root := canonicalTempDir(t)
	setOwnershipHandoffFlag(t, "city", root)
	called := 0
	provider := ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		called++
		return ownershiphandoff.Hooks{}, nil
	})
	request := ownershiphandoff.Request{
		CityRoot:  root,
		Root:      root,
		Database:  "beads",
		Workspace: "workspace",
		Endpoint:  ownershiphandoff.Endpoint{Host: "203.0.113.7", Port: 3307},
		Owner:     ownershiphandoff.OwnerLegacyGC,
	}
	result, err := ownershiphandoff.Run(context.Background(), request, filepath.Join(root, "handoff.json"), provider, false)
	if err == nil || result.ErrorCode != "invalid_request" {
		t.Fatalf("result=%+v err=%v, want invalid_request", result, err)
	}
	if called != 0 {
		t.Fatalf("provider opened %d times for invalid request", called)
	}
}

// TestOwnershipHandoffDefaultProviderFailsClosed pins the shipped provider
// seam.  With no explicit GC_BIN adapter configured, a valid request must be
// refused before any provider hooks can run; in particular, the provider must
// not silently return empty hooks and let Run create a handoff journal.
func TestOwnershipHandoffDefaultProviderFailsClosed(t *testing.T) {
	city := canonicalTempDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	before := handoffDirectorySnapshot(t, city)
	t.Setenv("GC_BIN", "")
	request := ownershiphandoff.Request{
		CityRoot:  city,
		Root:      scope,
		Database:  "beads",
		Workspace: "workspace",
		Endpoint:  ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		Owner:     ownershiphandoff.OwnerLegacyGC,
	}
	_, err := ownershipHandoffProvider.OwnershipHandoffHooks(context.Background(), request)
	if err == nil {
		t.Fatal("default ownership handoff provider unexpectedly returned hooks without GC_BIN")
	}
	var coded interface{ HandoffErrorCode() string }
	wantCode := "provider_unavailable"
	if !doltserver.SupportsStrictLaunchRecovery() {
		wantCode = "unsupported_platform"
	}
	if !errors.As(err, &coded) || coded.HandoffErrorCode() != wantCode {
		t.Fatalf("provider error = %v, want typed %s", err, wantCode)
	}
	if after := handoffDirectorySnapshot(t, city); !reflect.DeepEqual(after, before) {
		t.Fatalf("default provider mutated its root: before=%v after=%v", before, after)
	}
}

func TestDirectHandoffUnsupportedPlatformRefusesFreshAndResumedBeforeHooks(t *testing.T) {
	oldSupported := handoffSupportsStrict
	handoffSupportsStrict = func() bool { return false }
	t.Cleanup(func() { handoffSupportsStrict = oldSupported })
	city := canonicalTempDir(t)
	beadsDir := filepath.Join(city, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	request := ownershiphandoff.Request{CityRoot: city, Root: city, Database: "beads", Workspace: "workspace", Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	calls := 0
	provider := directHandoffProvider{legacy: ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		calls++
		return ownershiphandoff.Hooks{Configure: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { calls++; return nil }, StopLegacy: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { calls++; return nil }}, nil
	})}
	for _, phase := range []ownershiphandoff.Phase{"", ownershiphandoff.PhaseTargetConfigured} {
		t.Run(string(phase), func(t *testing.T) {
			path := filepath.Join(beadsDir, "ownership-handoff.json")
			if phase == "" {
				_ = os.Remove(path)
			} else {
				data, err := json.Marshal(ownershiphandoff.Journal{Request: request, Phase: phase, Owner: ownershiphandoff.OwnerLegacyGC, SnapshotCaptured: true})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := ownershiphandoff.Run(context.Background(), request, path, provider, false)
			if err == nil || result.ErrorCode != "unsupported_platform" {
				t.Fatalf("phase %q result=%+v err=%v, want unsupported platform refusal", phase, result, err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("unsupported handoff reached provider/configure/stop hooks %d times", calls)
	}
}

func TestDirectHandoffConfigureStagesWithoutRetiringGCControls(t *testing.T) {
	if !doltserver.SupportsStrictLaunchRecovery() {
		t.Skip("pure direct hook test requires a supported strict platform")
	}
	city := canonicalTempDir(t)
	beadsDir := filepath.Join(city, ".beads")
	if err := os.Mkdir(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("custom: preserved\ngc.endpoint_origin: managed_city\ngc.endpoint_status: verified\ndolt.auto-start: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0700); err != nil {
		t.Fatal(err)
	}
	request := ownershiphandoff.Request{CityRoot: city, Root: city, Database: "beads", Workspace: "workspace",
		Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	provider := directHandoffProvider{legacy: ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		return ownershiphandoff.Hooks{
			Snapshot: func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Snapshot, error) {
				return ownershiphandoff.Snapshot{}, nil
			},
			Configure:  func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			StopLegacy: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			Verify: func(_ context.Context, _ ownershiphandoff.Request, snapshot ownershiphandoff.Snapshot, _ func(ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
				return snapshot, nil
			},
		}, nil
	})}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	physicalBeadsDir, err := filepath.EvalSymlinks(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{
		"identity":       map[string]any{"data_dir": filepath.Join(physicalBeadsDir, "dolt")},
		"identity_token": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ownershiphandoff.Snapshot{
		Metadata:                 metadata,
		Sentinel:                 "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		WorkspaceConfig:          configData,
		WorkspaceConfigPresent:   true,
		WorkspaceConfigMode:      0o600,
		WorkspaceMetadataPresent: false,
		WorkspacePortPresent:     false,
	}
	if err := hooks.Configure(context.Background(), request, snapshot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"custom: preserved", "gc.endpoint_origin: managed_city", "gc.endpoint_status: verified", "dolt.auto-start: false"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("staged config missing %q:\n%s", want, data)
		}
	}
}

func TestDirectHandoffReconcilesMissingLegacyAfterAmbiguousStop(t *testing.T) {
	if !doltserver.SupportsStrictLaunchRecovery() {
		t.Skip("pure direct hook test requires a supported strict platform")
	}
	request := ownershiphandoff.Request{CityRoot: "/city", Root: "/city", Database: "beads", Workspace: "workspace",
		Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	verified := 0
	provider := directHandoffProvider{legacy: ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		return ownershiphandoff.Hooks{
			Snapshot: func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Snapshot, error) {
				return ownershiphandoff.Snapshot{}, nil
			},
			Configure: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			StopLegacy: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error {
				return ownershiphandoff.CodedError{Code: "process_missing", Err: errors.New("legacy process already stopped")}
			},
			Verify: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot, func(ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
				verified++
				return ownershiphandoff.Snapshot{}, nil
			},
		}, nil
	})}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := hooks.StopLegacy(context.Background(), request, ownershiphandoff.Snapshot{}); err != nil {
		t.Fatalf("reconcile missing legacy stop: %v", err)
	}
	if verified != 1 {
		t.Fatalf("legacy absence verifications = %d, want 1", verified)
	}
}

func TestDirectHandoffReportsMissingLegacyProofFailure(t *testing.T) {
	if !doltserver.SupportsStrictLaunchRecovery() {
		t.Skip("pure direct hook test requires a supported strict platform")
	}
	request := ownershiphandoff.Request{CityRoot: "/city", Root: "/city", Database: "beads", Workspace: "workspace",
		Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	proofErr := ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("another owner appeared")}
	provider := directHandoffProvider{legacy: ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		return ownershiphandoff.Hooks{
			Snapshot: func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Snapshot, error) {
				return ownershiphandoff.Snapshot{}, nil
			},
			Configure: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			StopLegacy: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error {
				return ownershiphandoff.CodedError{Code: "process_missing", Err: errors.New("legacy process already stopped")}
			},
			Verify: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot, func(ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
				return ownershiphandoff.Snapshot{}, proofErr
			},
		}, nil
	})}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	err = hooks.StopLegacy(context.Background(), request, ownershiphandoff.Snapshot{})
	if !errors.Is(err, proofErr) {
		t.Fatalf("missing legacy proof error = %v, want %v", err, proofErr)
	}
}

func TestDirectHandoffCommitRefusesReplacementWithoutStartProof(t *testing.T) {
	if !doltserver.SupportsStrictLaunchRecovery() {
		t.Skip("commit recovery requires a supported strict platform")
	}
	oldStart := handoffStrictStart
	t.Cleanup(func() { handoffStrictStart = oldStart })
	city := canonicalTempDir(t)
	beadsDir := filepath.Join(city, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0700); err != nil {
		t.Fatal(err)
	}
	configData := []byte("dolt.auto-start: true\ngc.endpoint_origin: city_canonical\ngc.endpoint_status: verified\ndolt.host: 127.0.0.1\ndolt.port: 3307\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), configData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, doltserver.PortFileName), []byte("3307"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{"identity": map[string]any{"data_dir": filepath.Join(beadsDir, "dolt")}})
	if err != nil {
		t.Fatal(err)
	}
	request := ownershiphandoff.Request{CityRoot: city, Root: city, Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}}
	snapshot := ownershiphandoff.Snapshot{
		Metadata: metadata, TargetPID: 101, TargetBirth: "captured-birth", TargetDataDir: filepath.Join(beadsDir, "dolt"),
		TargetLaunchID: "0123456789abcdef0123456789abcdef", TargetLaunchConfig: filepath.Join(beadsDir, "dolt-handoff-0123456789abcdef0123456789abcdef.yaml"), TargetLaunchExecutable: "/usr/bin/dolt",
		WorkspaceConfig: configData, WorkspaceConfigPresent: true, WorkspaceConfigMode: 0o600,
		WorkspacePort: []byte("3307"), WorkspacePortPresent: true, WorkspacePortMode: 0o600,
	}
	recoveries := 0
	handoffStrictStart = func(gotBeadsDir string, options doltserver.StartOptions) (*doltserver.State, error) {
		recoveries++
		if gotBeadsDir != beadsDir || !options.RequireFresh || !options.RecoverOnly || !options.RequireReady {
			t.Fatalf("commit recovery options=%+v beadsDir=%q", options, gotBeadsDir)
		}
		return &doltserver.State{Running: true, PID: 202, Birth: "replacement-birth", Port: 3307, DataDir: filepath.Join(beadsDir, "dolt")}, nil
	}
	provider := directHandoffProvider{legacy: ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		return ownershiphandoff.Hooks{
			Snapshot: func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Snapshot, error) {
				return snapshot, nil
			},
			Configure:  func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			StopLegacy: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			Verify: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot, func(ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
				return snapshot, nil
			},
		}, nil
	})}
	hooks, err := provider.OwnershipHandoffHooks(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	err = hooks.Commit(context.Background(), request, snapshot)
	var coded interface{ HandoffErrorCode() string }
	if err == nil || !errors.As(err, &coded) || coded.HandoffErrorCode() != "identity_changed" {
		t.Fatalf("commit replacement error=%v, want typed identity_changed refusal", err)
	}
	if recoveries != 1 {
		t.Fatalf("strict recovery calls=%d, want 1", recoveries)
	}
}

func TestDirectHandoffCheckpointsStartedTargetBeforeStoreProbe(t *testing.T) {
	oldState := handoffDoltIsRunning
	t.Cleanup(func() { handoffDoltIsRunning = oldState })
	handoffDoltIsRunning = func(string) (*doltserver.State, error) {
		return &doltserver.State{Running: true, PID: os.Getpid(), Port: 3307, DataDir: "/scope/.beads/dolt"}, nil
	}
	request := ownershiphandoff.Request{Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}}
	checkpointed := ownershiphandoff.Snapshot{}
	birth, err := procid.Capture(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	strictState := &doltserver.State{Running: true, PID: os.Getpid(), Birth: string(birth), Port: 3307, DataDir: "/scope/.beads/dolt"}
	got, err := checkpointDirectHandoffTarget(request, "/scope/.beads/dolt", strictState, ownershiphandoff.Snapshot{}, func(snapshot ownershiphandoff.Snapshot) error {
		checkpointed = snapshot
		return nil
	})
	if err != nil {
		t.Fatalf("checkpoint started target: %v", err)
	}
	if got.TargetPID != os.Getpid() || got.TargetBirth == "" || got.TargetDataDir != "/scope/.beads/dolt" {
		t.Fatalf("checkpointed snapshot=%+v, want current process identity", got)
	}
	if !reflect.DeepEqual(checkpointed, got) {
		t.Fatalf("durable checkpoint=%+v, want %v", checkpointed, got)
	}
}

func TestPrepareStrictHandoffLaunchPersistsCompleteIntent(t *testing.T) {
	beadsDir := filepath.Join(canonicalTempDir(t), ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var checkpointed ownershiphandoff.Snapshot
	got, err := prepareStrictHandoffLaunch(beadsDir, ownershiphandoff.Snapshot{}, func(snapshot ownershiphandoff.Snapshot) error {
		checkpointed = snapshot
		return nil
	})
	if err != nil {
		t.Fatalf("prepare strict launch: %v", err)
	}
	if got.TargetLaunchID == "" || got.TargetLaunchConfig == "" || got.TargetLaunchExecutable == "" || !reflect.DeepEqual(got, checkpointed) {
		t.Fatalf("launch intent=%+v checkpoint=%+v, want complete durable identity", got, checkpointed)
	}
	if want := filepath.Join(beadsDir, "dolt-handoff-"+got.TargetLaunchID+".yaml"); got.TargetLaunchConfig != want {
		t.Fatalf("launch config=%q, want %q", got.TargetLaunchConfig, want)
	}
	partial := got
	partial.TargetLaunchExecutable = ""
	if _, err := prepareStrictHandoffLaunch(beadsDir, partial, func(ownershiphandoff.Snapshot) error { return nil }); err == nil {
		t.Fatal("partial durable launch intent was accepted")
	}
}

func TestDirectHandoffRollbackRetryRestartsMissingLegacyOwnerOnce(t *testing.T) {
	root := canonicalTempDir(t)
	beadsDir := filepath.Join(root, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configData := []byte("legacy: true\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), configData, 0o600); err != nil {
		t.Fatal(err)
	}
	oldVerify := verifyRestoredSentinel
	verifyRestoredSentinel = func(context.Context, string, ownershiphandoff.Snapshot, ownershiphandoff.Request) error { return nil }
	t.Cleanup(func() { verifyRestoredSentinel = oldVerify })
	snapshotCalls, restartCalls := 0, 0
	legacy := ownershiphandoff.Hooks{
		Snapshot: func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Snapshot, error) {
			snapshotCalls++
			if snapshotCalls == 1 {
				return ownershiphandoff.Snapshot{}, ownershiphandoff.CodedError{Code: "process_missing", Err: errors.New("GC did not start before crash")}
			}
			return ownershiphandoff.Snapshot{Metadata: []byte("restarted"), Sentinel: "restarted-token"}, nil
		},
		RestartLegacy: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error {
			restartCalls++
			return nil
		},
	}
	request := ownershiphandoff.Request{CityRoot: root, Root: root, Database: "beads", Workspace: "workspace", Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	snapshot := ownershiphandoff.Snapshot{WorkspaceConfig: configData, WorkspaceConfigPresent: true, WorkspaceConfigMode: 0o600}
	got, err := recoverRestoredLegacyOwner(context.Background(), beadsDir, request, snapshot, legacy)
	if err != nil {
		t.Fatalf("recover restored owner: %v", err)
	}
	if restartCalls != 1 || snapshotCalls != 2 {
		t.Fatalf("restart=%d snapshots=%d, want one restart and post-restart inspection", restartCalls, snapshotCalls)
	}
	if string(got.Metadata) != "restarted" || got.Sentinel != "restarted-token" {
		t.Fatalf("recovered proof metadata=%q sentinel=%q, want restarted owner identity", got.Metadata, got.Sentinel)
	}
}

func TestDirectHandoffArtifactChecksPreservePresenceModesAndRegularFiles(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(metadataPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("custom: preserved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := ownershiphandoff.Snapshot{
		WorkspaceMetadata:        []byte{},
		WorkspaceMetadataPresent: true,
		WorkspaceMetadataMode:    0o600,
		WorkspaceConfig:          []byte("custom: preserved\n"),
		WorkspaceConfigPresent:   true,
		WorkspaceConfigMode:      0o600,
	}
	if err := requireCapturedHandoffArtifacts(beadsDir, snapshot); err != nil {
		t.Fatalf("captured artifacts = %v, want accepted", err)
	}
	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := requireCapturedHandoffArtifacts(beadsDir, snapshot); err == nil {
		t.Fatal("empty present metadata replaced by absent file was accepted")
	}
	if err := os.WriteFile(metadataPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(beadsDir, "metadata-payload")
	if err := os.WriteFile(payload, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(payload, metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := requireCapturedHandoffArtifacts(beadsDir, snapshot); err == nil {
		t.Fatal("metadata symlink was accepted as a captured regular artifact")
	}
	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := requireHandoffConfig(beadsDir, snapshot, map[string]any{"dolt.auto-start": false}); err == nil {
		t.Fatal("staged config with a changed mode was accepted")
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		t.Fatal(err)
	}
	portPayload := filepath.Join(beadsDir, "port-payload")
	if err := os.WriteFile(portPayload, []byte("3307"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(portPayload, filepath.Join(beadsDir, doltserver.PortFileName)); err != nil {
		t.Fatal(err)
	}
	if err := requireHandoffPort(beadsDir, snapshot, 3307); err == nil {
		t.Fatal("port symlink was accepted before handoff mutation")
	}
}

func TestDirectHandoffRollbackRestoresEmptyArtifactPresenceAndMode(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"metadata.json", "config.yaml", doltserver.PortFileName} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := ownershiphandoff.Snapshot{
		WorkspaceMetadata:        []byte{},
		WorkspaceMetadataPresent: true,
		WorkspaceMetadataMode:    0o640,
		WorkspaceConfigPresent:   false,
		WorkspacePortPresent:     false,
	}
	if err := restoreHandoffArtifacts(beadsDir, snapshot); err != nil {
		t.Fatalf("restore artifacts: %v", err)
	}
	data, present, mode, err := readHandoffArtifact(filepath.Join(beadsDir, "metadata.json"))
	if err != nil || !present || len(data) != 0 || mode != 0o640 {
		t.Fatalf("restored metadata data=%q present=%t mode=%o err=%v, want empty present 0640", data, present, mode, err)
	}
	for _, name := range []string{"config.yaml", doltserver.PortFileName} {
		if _, err := os.Lstat(filepath.Join(beadsDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restored absent %s err=%v, want not exist", name, err)
		}
	}
}

func TestDirectHandoffRollbackAcceptsLifecycleRemovedPortFile(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := ownershiphandoff.Snapshot{WorkspacePort: []byte("3307"), WorkspacePortPresent: true, WorkspacePortMode: 0o644}
	if err := requireRollbackHandoffPort(beadsDir, snapshot, 3307); err != nil {
		t.Fatalf("removed direct lifecycle port file = %v, want accepted for restoration", err)
	}
	if err := os.Symlink(filepath.Join(beadsDir, "elsewhere"), filepath.Join(beadsDir, doltserver.PortFileName)); err != nil {
		t.Fatal(err)
	}
	if err := requireRollbackHandoffPort(beadsDir, snapshot, 3307); err == nil {
		t.Fatal("rollback accepted a replacement port symlink")
	}
}

func TestDirectHandoffDefersUncheckpointedTargetToStrictLaunchProof(t *testing.T) {
	// The strict launcher runs under its own exclusive Start lock and is the
	// only place allowed to recover a nonce-bound child that survived between
	// spawn and the journal PID checkpoint. This preflight must not reject it.
	request := ownershiphandoff.Request{Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}}
	if err := handoffTargetMayStart(request, ownershiphandoff.Snapshot{}); err != nil {
		t.Fatalf("uncheckpointed target preflight error=%v, want strict-launch decision", err)
	}
}

func TestDirectHandoffRefusesReplacementAfterCheckpointedTargetDies(t *testing.T) {
	oldState := handoffDoltIsRunning
	oldVerify := handoffProcessVerify
	t.Cleanup(func() {
		handoffDoltIsRunning = oldState
		handoffProcessVerify = oldVerify
	})
	handoffDoltIsRunning = func(string) (*doltserver.State, error) {
		return &doltserver.State{Running: true, PID: 992, Port: 3307, DataDir: "/scope/.beads/dolt"}, nil
	}
	handoffProcessVerify = func(int, procid.Token) (bool, error) { return false, nil }
	request := ownershiphandoff.Request{Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}}
	snapshot := ownershiphandoff.Snapshot{TargetPID: 991, TargetBirth: "dead", TargetDataDir: "/scope/.beads/dolt"}
	err := handoffTargetMayStart(request, snapshot)
	var coded interface{ HandoffErrorCode() string }
	if err == nil || !errors.As(err, &coded) || coded.HandoffErrorCode() != "identity_changed" {
		t.Fatalf("replacement target error=%v, want typed identity_changed refusal", err)
	}
}

// TestOwnershipHandoffLegacyGuardBypass pins the explicit command-only
// escape hatch.  A metadata-less historical SQLite workspace is refused for
// an ordinary no-store command, while the ownership-handoff front door is
// admitted so it can perform its own canonical identity validation.
func TestOwnershipHandoffLegacyGuardBypass(t *testing.T) {
	repo := canonicalTempDir(t)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.Mkdir(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("SQLite format 3\x00historical"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := guardLegacyNoStoreCommand(ownershipHandoffCmd, beadsDir); err != nil {
		t.Fatalf("ownership-handoff legacy guard = %v, want explicit bypass", err)
	}

	control := &cobra.Command{Use: "ordinary-historical", RunE: func(*cobra.Command, []string) error { return nil }}
	migrateCmd.AddCommand(control)
	t.Cleanup(func() { migrateCmd.RemoveCommand(control) })
	if err := guardLegacyNoStoreCommand(control, beadsDir); !isLegacyUpgradeRefusal(err) {
		t.Fatalf("ordinary command legacy guard = %v, want historical SQLite refusal", err)
	}
}

func TestOwnershipHandoffDryRunDoesNotOpenProvider(t *testing.T) {
	root := canonicalTempDir(t)
	called := 0
	provider := ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		called++
		return ownershiphandoff.Hooks{}, errors.New("must not open in dry-run")
	})
	request := ownershiphandoff.Request{
		CityRoot:  root,
		Root:      root,
		Database:  "beads",
		Workspace: "workspace",
		Endpoint:  ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		Owner:     ownershiphandoff.OwnerLegacyGC,
	}
	result, err := ownershiphandoff.Run(context.Background(), request, filepath.Join(root, "handoff.json"), provider, true)
	if err != nil || result.Phase != ownershiphandoff.PhasePrepared || result.Mutates {
		t.Fatalf("result=%+v err=%v, want prepared non-mutating dry-run", result, err)
	}
	if called != 0 {
		t.Fatalf("provider opened %d times for dry-run", called)
	}
}

func TestOwnershipHandoffResolvesProviderUnderJournalLock(t *testing.T) {
	root := canonicalTempDir(t)
	journal := filepath.Join(root, "handoff.json")
	request := ownershiphandoff.Request{
		CityRoot:  root,
		Root:      root,
		Database:  "beads",
		Workspace: "workspace",
		Endpoint:  ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		Owner:     ownershiphandoff.OwnerLegacyGC,
	}
	provider := ownershiphandoff.ProviderFunc(func(ctx context.Context, got ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		// A nested attempt must see the live lock rather than opening a second
		// provider while the outer call is resolving hooks.
		nested, err := ownershiphandoff.Run(ctx, got, journal, nil, false)
		if err == nil || nested.ErrorCode != "concurrent_handoff" {
			t.Fatalf("nested run result=%+v err=%v, want concurrent_handoff", nested, err)
		}
		return ownershiphandoff.Hooks{
			Snapshot: func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Snapshot, error) {
				return ownershiphandoff.Snapshot{}, nil
			},
			Configure:  func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			StopLegacy: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			Verify: func(_ context.Context, _ ownershiphandoff.Request, snapshot ownershiphandoff.Snapshot, _ func(ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
				return snapshot, nil
			},
			Commit: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
		}, nil
	})
	result, err := ownershiphandoff.Run(context.Background(), request, journal, provider, false)
	if err != nil || result.Phase != ownershiphandoff.PhaseCommitted {
		t.Fatalf("result=%+v err=%v, want committed", result, err)
	}
}

func TestOwnershipHandoffProviderErrorPreservesJournalState(t *testing.T) {
	root := canonicalTempDir(t)
	journal := filepath.Join(root, "handoff.json")
	request := ownershiphandoff.Request{
		CityRoot: root,
		Root:     root, Database: "beads", Workspace: "workspace",
		Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		Owner:    ownershiphandoff.OwnerLegacyGC,
	}
	seed := ownershiphandoff.Journal{Request: request, Snapshot: ownershiphandoff.Snapshot{Sentinel: "s"}, SnapshotCaptured: true,
		Phase: ownershiphandoff.PhaseOldOwnerStopped, Owner: ownershiphandoff.OwnerLegacyGC}
	b, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	provider := ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		return ownershiphandoff.Hooks{}, errors.New("provider unavailable")
	})
	result, err := ownershiphandoff.Run(context.Background(), request, journal, provider, false)
	if err == nil || result.Phase != ownershiphandoff.PhaseOldOwnerStopped || result.Owner != ownershiphandoff.OwnerLegacyGC || !result.Mutates || result.ErrorCode != "provider_unavailable" {
		t.Fatalf("result=%+v err=%v, want preserved old_owner_stopped mutation state", result, err)
	}
}

func TestOwnershipHandoffJSONShapeIsStable(t *testing.T) {
	result := ownershipHandoffOutput{
		Phase:   ownershiphandoff.PhasePrepared,
		Owner:   ownershiphandoff.OwnerLegacyGC,
		Mutates: false,
		Identity: ownershipHandoffIdentity{
			CityRoot:  "/srv/city",
			Root:      "/srv/beads",
			Database:  "beads",
			Workspace: "workspace",
			Endpoint:  ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		},
		ErrorCode: "",
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"phase", "owner", "mutates", "identity", "error_code", "error"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("JSON missing required field %q: %s", field, b)
		}
	}
	var identity map[string]json.RawMessage
	if err := json.Unmarshal(fields["identity"], &identity); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"city_root", "root", "database", "workspace", "endpoint"} {
		if _, ok := identity[field]; !ok {
			t.Errorf("identity JSON missing required field %q: %s", field, b)
		}
	}
	if _, ok := fields["schema_version"]; ok {
		t.Errorf("handoff JSON must be strict result JSON, got schema wrapper: %s", b)
	}
}

func TestOwnershipHandoffCommandDryRunJSONFrontDoor(t *testing.T) {
	root := canonicalTempDir(t)
	if err := os.Mkdir(filepath.Join(root, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	setOwnershipHandoffFlag(t, "city", root)
	setOwnershipHandoffFlag(t, "root", root)
	setOwnershipHandoffFlag(t, "database", "beads")
	setOwnershipHandoffFlag(t, "workspace", "workspace")
	setOwnershipHandoffFlag(t, "host", "127.0.0.1")
	setOwnershipHandoffFlag(t, "port", "3307")
	setOwnershipHandoffFlag(t, "socket", "")
	setOwnershipHandoffFlag(t, "journal", filepath.Join(root, ".beads", ownershiphandoff.JournalName))
	setOwnershipHandoffFlag(t, "dry-run", "true")
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return runOwnershipHandoffCommand(ownershipHandoffCmd, nil)
	})
	var got ownershipHandoffOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode strict handoff JSON %q: %v", out, err)
	}
	if got.Phase != ownershiphandoff.PhasePrepared || got.Owner != ownershiphandoff.OwnerLegacyGC || got.ErrorCode != "" {
		t.Fatalf("output=%+v, want prepared legacy-gc success", got)
	}
}

func TestOwnershipHandoffCommandSocketEndpoint(t *testing.T) {
	for _, explicit := range []string{"", "host", "port"} {
		t.Run("explicit_"+explicit, func(t *testing.T) {
			root := canonicalTempDir(t)
			if err := os.Mkdir(filepath.Join(root, ".beads"), 0o700); err != nil {
				t.Fatal(err)
			}
			setOwnershipHandoffFlag(t, "city", root)
			setOwnershipHandoffFlag(t, "root", root)
			setOwnershipHandoffFlag(t, "database", "beads")
			setOwnershipHandoffFlag(t, "workspace", "workspace")
			setOwnershipHandoffFlag(t, "journal", "")
			setOwnershipHandoffFlag(t, "socket", filepath.Join(root, "beads.sock"))
			setOwnershipHandoffFlag(t, "dry-run", "true")
			if explicit == "host" {
				setOwnershipHandoffFlag(t, "host", "127.0.0.1")
			}
			if explicit == "port" {
				setOwnershipHandoffFlag(t, "port", "3307")
			}
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			var runErr error
			out := captureStdout(t, func() error {
				runErr = runOwnershipHandoffCommand(ownershipHandoffCmd, nil)
				return nil
			})
			var got ownershipHandoffOutput
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("decode handoff JSON %q: %v", out, err)
			}
			if explicit == "" {
				if runErr != nil || got.ErrorCode != "" || got.Identity.Endpoint.Host != "" || got.Identity.Endpoint.Port != 0 {
					t.Fatalf("socket-only result=%+v err=%v, want socket without implicit TCP defaults", got, runErr)
				}
			} else if runErr == nil || got.ErrorCode != "invalid_request" {
				t.Fatalf("socket with --%s result=%+v err=%v, want invalid_request", explicit, got, runErr)
			}
		})
	}
}

func TestOwnershipHandoffCommandRejectsAlternateJournal(t *testing.T) {
	root := canonicalTempDir(t)
	setOwnershipHandoffFlag(t, "city", root)
	setOwnershipHandoffFlag(t, "root", root)
	setOwnershipHandoffFlag(t, "database", "beads")
	setOwnershipHandoffFlag(t, "workspace", "workspace")
	setOwnershipHandoffFlag(t, "host", "127.0.0.1")
	setOwnershipHandoffFlag(t, "port", "3307")
	setOwnershipHandoffFlag(t, "socket", "")
	setOwnershipHandoffFlag(t, "journal", filepath.Join(root, "alternate.json"))
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	var runErr error
	out := captureStdout(t, func() error {
		runErr = runOwnershipHandoffCommand(ownershipHandoffCmd, nil)
		return nil // captureStdout treats a command error as a test failure.
	})
	if runErr == nil {
		t.Fatal("alternate journal unexpectedly succeeded")
	}
	var got ownershipHandoffOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode strict handoff JSON %q: %v", out, err)
	}
	if got.ErrorCode != "invalid_journal" {
		t.Fatalf("output=%+v, want invalid_journal", got)
	}
	if _, err := os.Stat(filepath.Join(root, "alternate.json")); !os.IsNotExist(err) {
		t.Fatalf("alternate journal was created: %v", err)
	}
}

// TestOwnershipHandoffCommandJSONCarriesRefusalText pins the one refusal whose
// remediation lives only in prose: an identity conflict names the journal it is
// refusing and says whether discarding it would lose a recorded mutation.
// Without an error field that guidance reached text callers only, which are the
// callers least likely to need it.
func TestOwnershipHandoffCommandJSONCarriesRefusalText(t *testing.T) {
	root := canonicalTempDir(t)
	if err := os.Mkdir(filepath.Join(root, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, ".beads", ownershiphandoff.JournalName)
	foreign := ownershiphandoff.Request{
		CityRoot: root, Root: root, Database: "other-database", Workspace: "workspace",
		Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		Owner:    ownershiphandoff.OwnerLegacyGC,
	}
	seed := ownershiphandoff.Journal{Request: foreign, Phase: ownershiphandoff.PhasePrepared,
		Owner: ownershiphandoff.OwnerLegacyGC}
	b, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	setOwnershipHandoffFlag(t, "city", root)
	setOwnershipHandoffFlag(t, "root", root)
	setOwnershipHandoffFlag(t, "database", "beads")
	setOwnershipHandoffFlag(t, "workspace", "workspace")
	setOwnershipHandoffFlag(t, "host", "127.0.0.1")
	setOwnershipHandoffFlag(t, "port", "3307")
	setOwnershipHandoffFlag(t, "socket", "")
	setOwnershipHandoffFlag(t, "journal", "")
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	var runErr error
	out := captureStdout(t, func() error {
		runErr = runOwnershipHandoffCommand(ownershipHandoffCmd, nil)
		return nil // captureStdout treats a command error as a test failure.
	})
	if runErr == nil {
		t.Fatal("conflicting journal unexpectedly succeeded")
	}
	var got ownershipHandoffOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode strict handoff JSON %q: %v", out, err)
	}
	if got.ErrorCode != "identity_conflict" {
		t.Fatalf("output=%+v, want identity_conflict", got)
	}
	for _, want := range []string{journalPath, "other-database", "safe"} {
		if !strings.Contains(got.Error, want) {
			t.Errorf("JSON error %q does not name %q", got.Error, want)
		}
	}
}

func setOwnershipHandoffFlag(t *testing.T, name, value string) {
	t.Helper()
	flag := ownershipHandoffCmd.Flags().Lookup(name)
	if flag == nil {
		t.Fatalf("missing handoff flag --%s", name)
	}
	old := flag.Value.String()
	oldChanged := flag.Changed
	if err := ownershipHandoffCmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = flag.Value.Set(old)
		flag.Changed = oldChanged
	})
}

type handoffFileSnapshot struct {
	Mode     os.FileMode
	Contents string
}

func handoffDirectorySnapshot(t *testing.T, root string) map[string]handoffFileSnapshot {
	t.Helper()
	files := make(map[string]handoffFileSnapshot)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot := handoffFileSnapshot{Mode: info.Mode()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot.Contents = string(data)
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot.Contents = target
		}
		files[rel] = snapshot
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot handoff root: %v", err)
	}
	return files
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(canonical) || strings.TrimSpace(canonical) == "" {
		t.Fatalf("bad canonical temp dir %q", canonical)
	}
	return canonical
}

func TestDoctorPositionalPendingHandoffRefusesBeforeMaintenanceFromOtherDirectory(t *testing.T) {
	bd, target := setupMigrationFreezeWorkspace(t)
	outside := t.TempDir()
	beadsDir := filepath.Join(target, ".beads")
	physicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	request := ownershiphandoff.Request{CityRoot: physicalTarget, Root: physicalTarget, Database: "beads", Workspace: "doctor-target", Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	body, err := json.Marshal(ownershiphandoff.Journal{Request: request, Phase: ownershiphandoff.PhaseTargetConfigured, Owner: ownershiphandoff.OwnerLegacyGC})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "ownership-handoff.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	before := doctorWorkspaceFingerprint(t, target)
	cmd := exec.Command(bd, "doctor", target)
	cmd.Dir = outside
	cmd.Env = migrationFreezeEnv(target)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("bd doctor %q succeeded with pending handoff:\n%s", target, out)
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("bd doctor failed to run: %v", runErr)
	}
	if !strings.Contains(string(out), "ownership handoff") {
		t.Fatalf("doctor refusal did not identify pending handoff:\n%s", out)
	}
	assertDoctorWorkspaceUnchanged(t, target, before)
}

func TestDirectHandoffUnsupportedPlatformDoesNotArchiveRolledBackJournal(t *testing.T) {
	oldSupported := handoffSupportsStrict
	handoffSupportsStrict = func() bool { return false }
	t.Cleanup(func() { handoffSupportsStrict = oldSupported })
	city := canonicalTempDir(t)
	beadsDir := filepath.Join(city, ".beads")
	if err := os.Mkdir(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	request := ownershiphandoff.Request{CityRoot: city, Root: city, Database: "beads", Workspace: "workspace", Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: ownershiphandoff.OwnerLegacyGC}
	path := filepath.Join(beadsDir, "ownership-handoff.json")
	seed := ownershiphandoff.Journal{Request: request, SnapshotCaptured: true, MutationOccurred: true, Phase: ownershiphandoff.PhaseRolledBack, Owner: ownershiphandoff.OwnerLegacyGC}
	body, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	provider := directHandoffProvider{legacy: ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		calls++
		return ownershiphandoff.Hooks{}, nil
	})}
	result, err := ownershiphandoff.Run(context.Background(), request, path, provider, false)
	if err == nil || result.ErrorCode != "unsupported_platform" {
		t.Fatalf("result=%+v err=%v, want unsupported preflight", result, err)
	}
	after, readErr := os.ReadFile(path)
	afterInfo, statErr := os.Stat(path)
	if readErr != nil || statErr != nil || !bytes.Equal(before, after) || beforeInfo.Mode().Perm() != afterInfo.Mode().Perm() {
		t.Fatalf("rolled-back journal changed: read=%v stat=%v before=%q after=%q modes=%#o/%#o", readErr, statErr, before, after, beforeInfo.Mode().Perm(), afterInfo.Mode().Perm())
	}
	archives, globErr := filepath.Glob(path + ".rolled-back-*")
	if globErr != nil || len(archives) != 0 {
		t.Fatalf("archives=%v err=%v, want none", archives, globErr)
	}
	if calls != 0 {
		t.Fatalf("unsupported platform invoked legacy provider %d times", calls)
	}
}
