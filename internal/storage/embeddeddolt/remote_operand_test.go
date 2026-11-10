//go:build cgo

package embeddeddolt

import (
	"context"
	"strings"
	"testing"
)

// embeddedRemoteOperandCalls are the embedded-store entry points that take a
// remote or peer name.
func embeddedRemoteOperandCalls(name string) []struct {
	name string
	call func(context.Context, *EmbeddedDoltStore) error
} {
	return []struct {
		name string
		call func(context.Context, *EmbeddedDoltStore) error
	}{
		{name: "PushRemote", call: func(ctx context.Context, s *EmbeddedDoltStore) error { return s.PushRemote(ctx, name, false) }},
		{name: "PushRemote/force", call: func(ctx context.Context, s *EmbeddedDoltStore) error { return s.PushRemote(ctx, name, true) }},
		{name: "PullRemote", call: func(ctx context.Context, s *EmbeddedDoltStore) error { return s.PullRemote(ctx, name) }},
		{name: "PullRemoteWithStrategy", call: func(ctx context.Context, s *EmbeddedDoltStore) error {
			return s.PullRemoteWithStrategy(ctx, name, "theirs")
		}},
		{name: "PushTo", call: func(ctx context.Context, s *EmbeddedDoltStore) error { return s.PushTo(ctx, name) }},
		{name: "PullFrom", call: func(ctx context.Context, s *EmbeddedDoltStore) error { _, err := s.PullFrom(ctx, name); return err }},
		{name: "Fetch", call: func(ctx context.Context, s *EmbeddedDoltStore) error { return s.Fetch(ctx, name) }},
		{name: "Sync", call: func(ctx context.Context, s *EmbeddedDoltStore) error { _, err := s.Sync(ctx, name, ""); return err }},
	}
}

// closedEmbeddedStore returns a store every engine-touching path refuses with
// errClosed before it opens the engine: CommitPending, credential lookup and
// the transfer itself. Only a check that runs ahead of all of them can return
// a different error.
func closedEmbeddedStore() *EmbeddedDoltStore {
	s := &EmbeddedDoltStore{branch: "main"}
	s.closed.Store(true)
	return s
}

// TestRemoteOperandRefusedBeforeEmbeddedWork pins AC-6 for the embedded store
// (GH#7876): an option-shaped remote or peer name is refused before
// CommitPending, credentials or the engine.
func TestRemoteOperandRefusedBeforeEmbeddedWork(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"--force", "-f", "-"} {
		for _, tc := range embeddedRemoteOperandCalls(name) {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				err := tc.call(ctx, closedEmbeddedStore())
				want := `invalid remote name "` + name + `": must not start with '-'`
				if err == nil || err.Error() != want {
					t.Fatalf("err = %v, want %q (the name check must run before any engine work)", err, want)
				}
			})
		}
	}
}

// TestRemoteOperandAllowsOrdinaryEmbeddedNames is the control: a
// numeric-leading name passes the check and reaches the closed engine.
func TestRemoteOperandAllowsOrdinaryEmbeddedNames(t *testing.T) {
	ctx := context.Background()
	for _, tc := range embeddedRemoteOperandCalls("1st") {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(ctx, closedEmbeddedStore())
			if err == nil || strings.Contains(err.Error(), "must not start with '-'") {
				t.Fatalf("err = %v, want the closed-store error from past the name check", err)
			}
		})
	}
}
