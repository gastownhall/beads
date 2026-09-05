package ownershiphandoff

import (
	"context"
	"encoding/json"
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
	return Request{CityRoot: root, Root: root, Database: "beads", Workspace: "ws-1", Endpoint: Endpoint{Host: "127.0.0.1", Port: 3307}, Owner: OwnerLegacyGC}
}

func TestValidateRejectsSymlinkRootAndExternalEndpoint(t *testing.T) {
	r := validRequest(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(r.Root, link); err != nil {
		t.Fatal(err)
	}
	r.Root = link
	if err := ValidateRequest(r); err == nil {
		t.Fatal("symlink root accepted")
	}
	r = validRequest(t)
	r.Endpoint.Host = "10.0.0.8"
	if err := ValidateRequest(r); err == nil {
		t.Fatal("external endpoint accepted")
	}
	r = validRequest(t)
	r.Endpoint = Endpoint{Socket: filepath.Join(filepath.Dir(r.Root), "outside.sock")}
	if err := ValidateRequest(r); err == nil {
		t.Fatal("external unix endpoint accepted")
	}
	r = validRequest(t)
	r.Endpoint = Endpoint{Host: "127.0.0.1", Socket: filepath.Join(r.Root, "beads.sock")}
	if err := ValidateRequest(r); err == nil {
		t.Fatal("socket endpoint with host accepted")
	}
	for _, port := range []int{0, -1, 65536} {
		r = validRequest(t)
		r.Endpoint.Port = port
		if err := ValidateRequest(r); err == nil {
			t.Fatalf("invalid port %d accepted", port)
		}
	}
	r = validRequest(t)
	r.Root = r.Root + string(filepath.Separator) + "."
	if err := ValidateRequest(r); err == nil {
		t.Fatal("non-canonical root accepted")
	}
	r = validRequest(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(r.Root, "run")); err != nil {
		t.Fatal(err)
	}
	r.Endpoint = Endpoint{Socket: filepath.Join(r.Root, "run", "beads.sock")}
	if err := ValidateRequest(r); err == nil {
		t.Fatal("socket through symlinked directory accepted")
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

func TestRunRejectsUnsafeJournalBeforeProviderOrMutation(t *testing.T) {
	tests := []struct {
		name string
		path func(*testing.T, Request) string
	}{
		{name: "relative", path: func(_ *testing.T, _ Request) string { return "handoff.json" }},
		{name: "noncanonical", path: func(_ *testing.T, r Request) string {
			return r.Root + string(filepath.Separator) + "." + string(filepath.Separator) + "handoff.json"
		}},
		{name: "outside root", path: func(t *testing.T, _ Request) string {
			return filepath.Join(validRequest(t).Root, "handoff.json")
		}},
		{name: "missing parent", path: func(_ *testing.T, r Request) string {
			return filepath.Join(r.Root, "missing", "handoff.json")
		}},
		{name: "symlinked parent", path: func(t *testing.T, r Request) string {
			parent := filepath.Join(r.Root, "alias")
			if err := os.Symlink(validRequest(t).Root, parent); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(parent, "handoff.json")
		}},
		{name: "symlinked journal", path: func(t *testing.T, r Request) string {
			path := filepath.Join(r.Root, "handoff.json")
			if err := os.Symlink(filepath.Join(validRequest(t).Root, "handoff.json"), path); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "symlinked lock", path: func(t *testing.T, r Request) string {
			path := filepath.Join(r.Root, "handoff.json")
			if err := os.Symlink(filepath.Join(validRequest(t).Root, "handoff.json.lock"), path+".lock"); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := validRequest(t)
			path := tc.path(t, r)
			before, err := os.ReadDir(r.Root)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			provider := ProviderFunc(func(context.Context, Request) (Hooks, error) {
				called = true
				return Hooks{}, nil
			})
			for _, dryRun := range []bool{true, false} {
				got, err := Run(context.Background(), r, path, provider, dryRun)
				if err == nil || got.ErrorCode != "invalid_journal" || got.Mutates || called {
					t.Fatalf("dry-run=%v result=%+v err=%v provider=%v, want preflight refusal", dryRun, got, err, called)
				}
			}
			after, err := os.ReadDir(r.Root)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("journal preflight mutated root: before=%v after=%v", before, after)
			}
		})
	}
}

func TestRunLockConflictPreservesExistingJournal(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	seed := Journal{Request: r, Snapshot: Snapshot{Sentinel: "s"}, SnapshotCaptured: true,
		Phase: PhaseTargetConfigured, Owner: OwnerLegacyGC}
	b, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLock(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = lockfile.FlockUnlock(lock)
		_ = lock.Close()
	}()
	called := false
	got, err := Run(context.Background(), r, path, ProviderFunc(func(context.Context, Request) (Hooks, error) {
		called = true
		return Hooks{}, nil
	}), false)
	if err == nil || got.Phase != PhaseTargetConfigured || got.Owner != OwnerLegacyGC || !got.Mutates ||
		got.ErrorCode != "concurrent_handoff" || called {
		t.Fatalf("result=%+v err=%v provider=%v, want existing state and lock refusal", got, err, called)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ErrorCode != "" || loaded.Phase != PhaseTargetConfigured {
		t.Fatalf("lock conflict rewrote journal: %+v", loaded)
	}
}

func TestDryRunReportsExistingJournalPhase(t *testing.T) {
	r := validRequest(t)
	path := filepath.Join(r.Root, "handoff.json")
	j := Journal{Request: r, Snapshot: Snapshot{Sentinel: "s"}, SnapshotCaptured: true,
		Phase: PhaseTargetConfigured, Owner: OwnerLegacyGC}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Execute(context.Background(), r, path, Hooks{}, true)
	if err != nil || got.Phase != PhaseTargetConfigured || got.Owner != OwnerLegacyGC || got.Mutates {
		t.Fatalf("dry-run result=%+v err=%v, want existing target_configured state without mutation", got, err)
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

func TestJournalSaveErrorIsTypedAndTruthful(t *testing.T) {
	j := Journal{Phase: PhaseOldOwnerStopped, Owner: OwnerLegacyGC}
	got, err := journalSaveError(j, os.ErrPermission)
	if err == nil || got.ErrorCode != "journal_save_failed" || !got.Mutates {
		t.Fatalf("journal save failure result=%+v err=%v", got, err)
	}
}
