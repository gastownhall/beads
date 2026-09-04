package ownershiphandoff

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
