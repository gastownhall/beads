package ownershiphandoff

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/lockfile"
)

func validRequest(t *testing.T) Request {
	t.Helper()
	root := t.TempDir()
	// macOS exposes temporary directories through a symlinked /var prefix;
	// callers of the handoff API must provide the canonical real path.
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = canonical
	return Request{Root: root, Database: "beads", Workspace: "ws-1", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
}

func TestValidateRejectsSymlinkRootAndExternalEndpoint(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Request)
	}{
		{name: "symlinked root", mutate: func(t *testing.T, r *Request) {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(r.Root, link); err != nil {
				t.Fatal(err)
			}
			r.Root = link
		}},
		{name: "non-canonical root", mutate: func(_ *testing.T, r *Request) {
			r.Root += string(filepath.Separator) + "."
		}},
		{name: "root is a regular file", mutate: func(t *testing.T, r *Request) {
			file := filepath.Join(r.Root, "root-file")
			if err := os.WriteFile(file, nil, 0600); err != nil {
				t.Fatal(err)
			}
			r.Root = file
		}},
		{name: "external host", mutate: func(_ *testing.T, r *Request) { r.Endpoint.Host = "10.0.0.8" }},
		{name: "empty host", mutate: func(_ *testing.T, r *Request) { r.Endpoint.Host = "" }},
		{name: "port zero", mutate: func(_ *testing.T, r *Request) { r.Endpoint.Port = 0 }},
		{name: "port negative", mutate: func(_ *testing.T, r *Request) { r.Endpoint.Port = -1 }},
		{name: "port above range", mutate: func(_ *testing.T, r *Request) { r.Endpoint.Port = 65536 }},
		{name: "external unix endpoint", mutate: func(_ *testing.T, r *Request) {
			r.Endpoint = Endpoint{Socket: filepath.Join(filepath.Dir(r.Root), "outside.sock")}
		}},
		{name: "socket with host", mutate: func(_ *testing.T, r *Request) {
			r.Endpoint = Endpoint{Host: "127.0.0.1", Socket: filepath.Join(r.Root, "beads.sock")}
		}},
		{name: "socket through symlinked directory", mutate: func(t *testing.T, r *Request) {
			if err := os.Symlink(t.TempDir(), filepath.Join(r.Root, "run")); err != nil {
				t.Fatal(err)
			}
			r.Endpoint = Endpoint{Socket: filepath.Join(r.Root, "run", "beads.sock")}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := validRequest(t)
			tc.mutate(t, &r)
			if err := ValidateRequest(r); err == nil {
				t.Fatalf("%s accepted: %+v", tc.name, r)
			}
		})
	}
}

// TestValidateNamesTheIncompleteEndpoint pins the message, not just the
// rejection: an endpoint with no host at all is incomplete, and reporting it as
// "external" misdirects the reader.
func TestValidateNamesTheIncompleteEndpoint(t *testing.T) {
	r := validRequest(t)
	r.Endpoint.Host = ""
	err := ValidateRequest(r)
	if err == nil || !strings.Contains(err.Error(), "host is required") {
		t.Fatalf("empty host error = %v, want an incomplete-endpoint message", err)
	}
}

// TestValidateNamesTheNonDirectoryRoot pins both halves of the root
// precondition. A root that exists but is not a directory has no underlying
// error to report, so it must read as a plain rejection instead of a formatting
// artifact; a root that is simply missing must still carry its real cause.
func TestValidateNamesTheNonDirectoryRoot(t *testing.T) {
	base := validRequest(t)
	file := filepath.Join(base.Root, "root-file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}

	r := base
	r.Root = file
	err := ValidateRequest(r)
	if err == nil || err.Error() != "root must be an existing directory" {
		t.Fatalf("non-directory root error = %v, want a plain existing-directory message", err)
	}

	r = base
	r.Root = filepath.Join(base.Root, "absent")
	if err := ValidateRequest(r); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing root error = %v, want the underlying cause wrapped", err)
	}
}

func TestValidateRejectsIncompleteIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{name: "owner", mutate: func(r *Request) { r.Owner = OwnerBD }},
		{name: "database", mutate: func(r *Request) { r.Database = "" }},
		{name: "workspace", mutate: func(r *Request) { r.Workspace = "" }},
		{name: "root", mutate: func(r *Request) { r.Root = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := validRequest(t)
			tc.mutate(&r)
			if err := ValidateRequest(r); err == nil {
				t.Fatalf("invalid %s identity accepted: %+v", tc.name, r)
			}
		})
	}
}

func TestDryRunDoesNotInvokeHooks(t *testing.T) {
	r := validRequest(t)
	called := false
	h := Hooks{Snapshot: func(context.Context, Request) (Snapshot, error) { called = true; return Snapshot{}, nil }}
	got, err := Execute(context.Background(), r, filepath.Join(r.Root, "handoff.json"), h, true)
	if err != nil {
		t.Fatal(err)
	}
	if called || got.Mutates || got.Phase != PhasePrepared {
		t.Fatalf("dry run result=%+v called=%v", got, called)
	}
}

func TestExecutePersistsPhasesAndCommittedReplayIsNoop(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	var calls int
	h := Hooks{
		Snapshot:   func(context.Context, Request) (Snapshot, error) { calls++; return Snapshot{Sentinel: "s"}, nil },
		Configure:  func(context.Context, Request, Snapshot) error { calls++; return nil },
		StopLegacy: func(context.Context, Request, Snapshot) error { calls++; return nil },
		Verify:     func(context.Context, Request, Snapshot) error { calls++; return nil },
		Commit:     func(context.Context, Request, Snapshot) error { calls++; return nil },
	}
	got, err := Execute(context.Background(), r, path, h, false)
	if err != nil || got.Phase != PhaseCommitted || !got.Mutates {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	before := calls
	got, err = Execute(context.Background(), r, path, h, false)
	if err != nil || got.Phase != PhaseCommitted || calls != before {
		t.Fatalf("replay result=%+v calls %d->%d err=%v", got, before, calls, err)
	}
}

func TestExecuteHookOrderIsExact(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	var order []string
	h := Hooks{
		Snapshot: func(context.Context, Request) (Snapshot, error) {
			order = append(order, "snapshot")
			return Snapshot{Sentinel: "s"}, nil
		},
		Configure: func(context.Context, Request, Snapshot) error {
			order = append(order, "configure")
			return nil
		},
		StopLegacy: func(context.Context, Request, Snapshot) error {
			order = append(order, "stop")
			return nil
		},
		Verify: func(context.Context, Request, Snapshot) error {
			order = append(order, "verify")
			return nil
		},
		Commit: func(context.Context, Request, Snapshot) error {
			order = append(order, "commit")
			return nil
		},
	}
	if _, err := Execute(context.Background(), r, path, h, false); err != nil {
		t.Fatal(err)
	}
	want := []string{"snapshot", "configure", "stop", "verify", "commit"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("hook order = %v, want %v", order, want)
	}
}

func TestConfigureRetryPreservesDurableSnapshot(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	var snapshots, configures int
	first := Hooks{
		Snapshot: func(context.Context, Request) (Snapshot, error) {
			snapshots++
			return Snapshot{Sentinel: "before-mutation"}, nil
		},
		Configure: func(context.Context, Request, Snapshot) error {
			configures++
			return os.ErrPermission
		},
	}
	if _, err := Execute(context.Background(), r, path, first, false); err == nil {
		t.Fatal("first configure failure unexpectedly succeeded")
	}
	second := first
	second.Configure = func(_ context.Context, _ Request, s Snapshot) error {
		if s.Sentinel != "before-mutation" {
			t.Fatalf("retry snapshot = %+v, want durable pre-mutation snapshot", s)
		}
		return nil
	}
	second.StopLegacy = func(context.Context, Request, Snapshot) error { return nil }
	second.Verify = func(context.Context, Request, Snapshot) error { return nil }
	second.Commit = func(context.Context, Request, Snapshot) error { return nil }
	if _, err := Execute(context.Background(), r, path, second, false); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 || configures != 1 {
		t.Fatalf("hooks snapshot=%d configure=%d, want one snapshot and one failed configure", snapshots, configures)
	}
}

func TestStaleEmptyLockIsRecovered(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	h := Hooks{
		Snapshot:   func(context.Context, Request) (Snapshot, error) { return Snapshot{}, nil },
		Configure:  func(context.Context, Request, Snapshot) error { return nil },
		StopLegacy: func(context.Context, Request, Snapshot) error { return nil },
		Verify:     func(context.Context, Request, Snapshot) error { return nil },
		Commit:     func(context.Context, Request, Snapshot) error { return nil },
	}
	got, err := Execute(context.Background(), r, path, h, false)
	if err != nil || got.Phase != PhaseCommitted {
		t.Fatalf("stale lock recovery result=%+v err=%v", got, err)
	}
}

func TestConcurrentLockHoldersHaveSingleWinner(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json.lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	held, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = lockfile.FlockUnlock(held)
		_ = held.Close()
	}()
	if _, err := acquireLock(path); err == nil {
		t.Fatalf("second stale reclaimer err=%v, want concurrent refusal", err)
	}
}

func TestVerifyFailureReportsMutation(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	h := Hooks{
		Snapshot:   func(context.Context, Request) (Snapshot, error) { return Snapshot{}, nil },
		Configure:  func(context.Context, Request, Snapshot) error { return nil },
		StopLegacy: func(context.Context, Request, Snapshot) error { return nil },
		Verify:     func(context.Context, Request, Snapshot) error { return os.ErrClosed },
	}
	got, err := Execute(context.Background(), r, path, h, false)
	if err == nil || !got.Mutates || got.Phase != PhaseOldOwnerStopped {
		t.Fatalf("verify failure result=%+v err=%v, want mutation=true at old_owner_stopped", got, err)
	}
}

func TestPostHookCompletionCheckpointAvoidsDuplicateHook(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	j := Journal{Request: r, Snapshot: Snapshot{Sentinel: "s"}, SnapshotCaptured: true,
		CommitHookRan: true, Phase: PhaseVerified, Owner: OwnerLegacyGC}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h := Hooks{Commit: func(context.Context, Request, Snapshot) error { calls++; return nil }}
	got, err := Execute(context.Background(), r, path, h, false)
	if err != nil || got.Phase != PhaseCommitted || calls != 0 {
		t.Fatalf("checkpoint replay result=%+v err=%v commit calls=%d", got, err, calls)
	}
}

func TestCommitInProgressRequiresIdempotentReplay(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	j := Journal{Request: r, Snapshot: Snapshot{Sentinel: "s"}, SnapshotCaptured: true,
		CommitHookInProgress: true, Phase: PhaseVerified, Owner: OwnerLegacyGC}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	h := Hooks{Commit: func(context.Context, Request, Snapshot) error { calls++; return nil }}
	got, err := Execute(context.Background(), r, path, h, false)
	if err == nil || got.Owner != OwnerLegacyGC || got.Phase != PhaseVerified || got.ErrorCode != "commit_recovery_required" || calls != 0 {
		t.Fatalf("ambiguous commit result=%+v err=%v commit calls=%d", got, err, calls)
	}
	h.CommitReplay = func(context.Context, Request, Snapshot) error { calls++; return nil }
	got, err = Execute(context.Background(), r, path, h, false)
	if err != nil || got.Phase != PhaseCommitted || calls != 1 {
		t.Fatalf("replayed commit result=%+v err=%v commit calls=%d", got, err, calls)
	}
}

func TestCommitErrorDoesNotRetryNonIdempotently(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	calls := 0
	h := Hooks{
		Snapshot:   func(context.Context, Request) (Snapshot, error) { return Snapshot{}, nil },
		Configure:  func(context.Context, Request, Snapshot) error { return nil },
		StopLegacy: func(context.Context, Request, Snapshot) error { return nil },
		Verify:     func(context.Context, Request, Snapshot) error { return nil },
		Commit: func(context.Context, Request, Snapshot) error {
			calls++
			return os.ErrPermission
		},
	}
	first, err := Execute(context.Background(), r, path, h, false)
	if err == nil || first.ErrorCode != "commit_failed" || calls != 1 {
		t.Fatalf("first commit error result=%+v err=%v calls=%d", first, err, calls)
	}
	second, err := Execute(context.Background(), r, path, h, false)
	if err == nil || second.Owner != OwnerLegacyGC || second.Phase != PhaseVerified || calls != 1 {
		t.Fatalf("retry commit error result=%+v err=%v calls=%d", second, err, calls)
	}
}

func TestLiveLockRefusesConcurrentHandoff(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	lock, err := acquireLock(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = lockfile.FlockUnlock(lock)
		_ = lock.Close()
	}()
	got, err := Execute(context.Background(), r, path, Hooks{}, false)
	if err == nil || got.ErrorCode != "concurrent_handoff" {
		t.Fatalf("live lock result=%+v err=%v", got, err)
	}
}

func TestFailureLeavesLegacyOwnerAndRecordsError(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	h := Hooks{Snapshot: func(context.Context, Request) (Snapshot, error) { return Snapshot{}, nil }, Configure: func(context.Context, Request, Snapshot) error { return os.ErrPermission }}
	got, err := Execute(context.Background(), r, path, h, false)
	if err == nil || got.Owner != OwnerLegacyGC || got.Mutates {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	j, loadErr := Load(path)
	if loadErr != nil || j.Phase != PhasePrepared || j.ErrorCode == "" {
		t.Fatalf("journal=%+v err=%v", j, loadErr)
	}
}

func TestJournalIdentityConflictIsTyped(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	h := Hooks{Snapshot: func(context.Context, Request) (Snapshot, error) {
		return Snapshot{Sentinel: "s"}, nil
	}}
	if _, err := Execute(context.Background(), r, path, h, false); err == nil {
		t.Fatal("incomplete handoff unexpectedly succeeded")
	}
	other := r
	other.Database = "other"
	got, err := Execute(context.Background(), other, path, Hooks{}, true)
	if err == nil || got.ErrorCode != "identity_conflict" || got.Mutates {
		t.Fatalf("identity conflict result=%+v err=%v", got, err)
	}
}

func TestLoadRejectsUnknownPhase(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	if err := os.WriteFile(path, []byte(`{"request":{},"phase":"bogus","owner":"legacy-gc"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("accepted unknown journal phase")
	}
}

func TestLoadRejectsOwnerPhaseMismatch(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	for _, raw := range []string{
		`{"request":{},"phase":"prepared","owner":"bd"}`,
		`{"request":{},"phase":"committed","owner":"legacy-gc"}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted owner/phase mismatch %s", raw)
		}
	}
}

func TestLoadRejectsCommitCheckpointInEarlyPhase(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	for _, raw := range []string{
		`{"request":{},"phase":"prepared","owner":"legacy-gc","commit_hook_ran":true}`,
		`{"request":{},"phase":"target_configured","owner":"legacy-gc","commit_hook_in_progress":true}`,
		`{"request":{},"phase":"verified","owner":"legacy-gc","commit_hook_ran":true,"commit_hook_in_progress":true}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted impossible commit checkpoint %s", raw)
		}
	}
}

func TestLoadRejectsStopCheckpointInInvalidPhase(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	for _, raw := range []string{
		`{"request":{},"phase":"prepared","owner":"legacy-gc","legacy_stop_in_progress":true}`,
		`{"request":{},"phase":"old_owner_stopped","owner":"legacy-gc","legacy_stop_in_progress":true}`,
		`{"request":{},"phase":"committed","owner":"bd","legacy_stop_in_progress":true}`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted impossible stop checkpoint %s", raw)
		}
	}
}

// TestStopFailureReportsMutation pins the pre-stop reservation: once StopLegacy
// has been entered its effect cannot be assumed absent, so a failed stop must
// not report the legacy owner as untouched.
func TestStopFailureReportsMutation(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	h := Hooks{
		Snapshot:   func(context.Context, Request) (Snapshot, error) { return Snapshot{}, nil },
		Configure:  func(context.Context, Request, Snapshot) error { return nil },
		StopLegacy: func(context.Context, Request, Snapshot) error { return os.ErrPermission },
	}
	got, err := Execute(context.Background(), r, path, h, false)
	if err == nil || !got.Mutates || got.Phase != PhaseTargetConfigured || got.ErrorCode != "owner_stop_failed" {
		t.Fatalf("stop failure result=%+v err=%v, want mutation=true at target_configured", got, err)
	}
	j, loadErr := Load(path)
	if loadErr != nil || !j.LegacyStopInProgress {
		t.Fatalf("journal=%+v err=%v, want a durable stop reservation", j, loadErr)
	}
}

// TestStopRetryAfterUncheckpointedStopSucceeds covers the crash window between
// a successful StopLegacy and its checkpoint: the retry re-invokes the hook
// against an already-absent legacy owner, which the contract defines as
// success rather than a wedge.
func TestStopRetryAfterUncheckpointedStopSucceeds(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	j := Journal{Request: r, Snapshot: Snapshot{Sentinel: "s"}, SnapshotCaptured: true,
		LegacyStopInProgress: true, Phase: PhaseTargetConfigured, Owner: OwnerLegacyGC}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	stops := 0
	h := Hooks{
		StopLegacy: func(context.Context, Request, Snapshot) error { stops++; return nil },
		Verify:     func(context.Context, Request, Snapshot) error { return nil },
		Commit:     func(context.Context, Request, Snapshot) error { return nil },
	}
	got, execErr := Execute(context.Background(), r, path, h, false)
	if execErr != nil || got.Phase != PhaseCommitted || stops != 1 {
		t.Fatalf("stop retry result=%+v err=%v stops=%d", got, execErr, stops)
	}
	final, loadErr := Load(path)
	if loadErr != nil || final.LegacyStopInProgress {
		t.Fatalf("journal=%+v err=%v, want the stop reservation retired", final, loadErr)
	}
}

func TestExecuteRefusesCorruptJournal(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		r := validRequest(t)
		path := filepath.Join(r.Root, "handoff.json")
		if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
			t.Fatal(err)
		}
		called := false
		h := Hooks{Snapshot: func(context.Context, Request) (Snapshot, error) { called = true; return Snapshot{}, nil }}
		got, err := Execute(context.Background(), r, path, h, dryRun)
		if err == nil || got.ErrorCode != "journal_unreadable" || called {
			t.Fatalf("corrupt journal (dryRun=%v) result=%+v err=%v called=%v", dryRun, got, err, called)
		}
	}
}

// TestDryRunReportsMidFlightJournalPhase keeps the dry-run probe truthful: an
// operator inspecting a handoff that already stopped the legacy owner must not
// be told the scope is still prepared.
func TestDryRunReportsMidFlightJournalPhase(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	j := Journal{Request: r, Snapshot: Snapshot{Sentinel: "s"}, SnapshotCaptured: true,
		Phase: PhaseOldOwnerStopped, Owner: OwnerLegacyGC}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	h := Hooks{Verify: func(context.Context, Request, Snapshot) error { called = true; return nil }}
	got, execErr := Execute(context.Background(), r, path, h, true)
	if execErr != nil || called || got.Mutates || got.Phase != PhaseOldOwnerStopped {
		t.Fatalf("dry run result=%+v err=%v called=%v, want the journal's real phase", got, execErr, called)
	}
}

// TestIdentityConflictReportsAccumulatedMutation pins one refusal semantic: a
// conflict against a journal that already stopped the legacy owner reports that
// mutation, whichever side of the lock the conflict is detected on.
func TestIdentityConflictReportsAccumulatedMutation(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	j := Journal{Request: r, Snapshot: Snapshot{Sentinel: "s"}, SnapshotCaptured: true,
		Phase: PhaseOldOwnerStopped, Owner: OwnerLegacyGC}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	other := r
	other.Database = "other"
	got, execErr := Execute(context.Background(), other, path, Hooks{}, false)
	if execErr == nil || got.ErrorCode != "identity_conflict" || !got.Mutates {
		t.Fatalf("identity conflict result=%+v err=%v, want mutation=true", got, execErr)
	}
}

// TestUnopenableLockIsNotReportedAsConcurrency keeps an incident honest: only
// real contention may send an operator hunting a concurrent handoff.
func TestUnopenableLockIsNotReportedAsConcurrency(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "missing-dir", "handoff.json")
	got, err := Execute(context.Background(), r, path, Hooks{}, false)
	if err == nil || got.ErrorCode != "lock_unavailable" {
		t.Fatalf("unopenable lock result=%+v err=%v, want lock_unavailable", got, err)
	}
}

// TestCommitRecoveryMessageDoesNotAccumulate pins both bounds of the recovery
// message: it must not re-concatenate onto the journaled error across retries,
// and it must not buy that bound by discarding the original commit failure. A
// fourth attempt reads a journal the recovery path itself wrote, so it proves
// the message is a fixed point rather than merely bounded on the first rebuild.
// The journal is what an operator reads when a handoff is wedged at `verified`,
// so the cause has to survive exactly when a recovery loop has set in.
func TestCommitRecoveryMessageDoesNotAccumulate(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	h := Hooks{
		Snapshot:   func(context.Context, Request) (Snapshot, error) { return Snapshot{}, nil },
		Configure:  func(context.Context, Request, Snapshot) error { return nil },
		StopLegacy: func(context.Context, Request, Snapshot) error { return nil },
		Verify:     func(context.Context, Request, Snapshot) error { return nil },
		Commit:     func(context.Context, Request, Snapshot) error { return os.ErrPermission },
	}
	var lastErr error
	for i := 0; i < 4; i++ {
		if _, lastErr = Execute(context.Background(), r, path, h, false); lastErr == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", i)
		}
	}
	j, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(j.Error, "commit hook outcome is unknown"); n != 1 {
		t.Fatalf("journaled error repeated the recovery message %d times: %q", n, j.Error)
	}
	if !strings.Contains(j.Error, os.ErrPermission.Error()) {
		t.Fatalf("journaled error dropped the original commit failure cause: %q", j.Error)
	}
	if !strings.Contains(lastErr.Error(), os.ErrPermission.Error()) {
		t.Fatalf("returned error dropped the original commit failure cause: %v", lastErr)
	}
}

func TestJournalSaveErrorIsTypedAndTruthful(t *testing.T) {
	j := Journal{Phase: PhaseOldOwnerStopped, Owner: OwnerLegacyGC}
	got, err := journalSaveError(j, os.ErrPermission)
	if err == nil || got.ErrorCode != "journal_save_failed" || !got.Mutates {
		t.Fatalf("journal save failure result=%+v err=%v", got, err)
	}
}
