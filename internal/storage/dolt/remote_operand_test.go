package dolt

import (
	"context"
	"strings"
	"testing"
)

// remoteOperandCalls are the server-store entry points that hand a remote or
// peer name to Dolt, each driven with name. The default-remote variants read
// it from s.remote, the configured name.
func remoteOperandCalls(name string) []struct {
	name string
	call func(context.Context, *DoltStore) error
} {
	return []struct {
		name string
		call func(context.Context, *DoltStore) error
	}{
		{name: "Push", call: func(ctx context.Context, s *DoltStore) error { s.remote = name; return s.Push(ctx) }},
		{name: "ForcePush", call: func(ctx context.Context, s *DoltStore) error { s.remote = name; return s.ForcePush(ctx) }},
		{name: "PushRemote", call: func(ctx context.Context, s *DoltStore) error { return s.PushRemote(ctx, name, false) }},
		{name: "Pull", call: func(ctx context.Context, s *DoltStore) error { s.remote = name; return s.Pull(ctx) }},
		{name: "PullRemote", call: func(ctx context.Context, s *DoltStore) error { return s.PullRemote(ctx, name) }},
		{name: "PushTo", call: func(ctx context.Context, s *DoltStore) error { return s.PushTo(ctx, name) }},
		{name: "pushRefToPeer", call: func(ctx context.Context, s *DoltStore) error {
			return s.pushRefToPeer(ctx, name, "staging:main")
		}},
		{name: "PullFrom", call: func(ctx context.Context, s *DoltStore) error { _, err := s.PullFrom(ctx, name); return err }},
		{name: "Fetch", call: func(ctx context.Context, s *DoltStore) error { return s.Fetch(ctx, name) }},
		{name: "Sync", call: func(ctx context.Context, s *DoltStore) error { _, err := s.Sync(ctx, name, ""); return err }},
	}
}

const remoteOperandRefusal = "must not start with '-'"

// TestServerRemoteOperandRefusedBeforeWork pins AC-6 for the server store
// (GH#7876): an option-shaped remote or peer name, passed in or configured,
// is refused before any commit, connection, subprocess or transport work.
// The store's database refuses every connection, so a zero connection count
// shows the refusal came first.
func TestServerRemoteOperandRefusedBeforeWork(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"--force", "-f", "-"} {
		for _, tc := range remoteOperandCalls(name) {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				store, conn := newPublishGuardStore(t, false, false)
				err := tc.call(ctx, store)
				want := `invalid remote name "` + name + `": ` + remoteOperandRefusal
				if err == nil || err.Error() != want {
					t.Fatalf("err = %v, want %q", err, want)
				}
				if n := conn.connects.Load(); n != 0 {
					t.Fatalf("database connection attempts = %d, want 0 (the name check must run first)", n)
				}
			})
		}
	}
}

// TestServerRemoteOperandAllowsOrdinaryNames is the control: a numeric-leading
// name is not refused and reaches the store's normal routing.
func TestServerRemoteOperandAllowsOrdinaryNames(t *testing.T) {
	ctx := context.Background()
	for _, tc := range remoteOperandCalls("1st") {
		t.Run(tc.name, func(t *testing.T) {
			store, conn := newPublishGuardStore(t, false, false)
			err := tc.call(ctx, store)
			if err != nil && strings.Contains(err.Error(), remoteOperandRefusal) {
				t.Fatalf("err = %v, the name check must not refuse %q", err, "1st")
			}
			if n := conn.connects.Load(); n == 0 {
				t.Fatalf("no database connection attempt: the call never reached routing (err = %v)", err)
			}
		})
	}
}
