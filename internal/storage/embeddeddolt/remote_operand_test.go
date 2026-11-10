//go:build cgo

package embeddeddolt

import (
	"context"
	"errors"
	"testing"
)

// newClosedTransferStore builds an EmbeddedDoltStore that fails fast on any
// use: the closed flag makes every connection-opening helper (withConn,
// commitConn, withDBConn) refuse before opening an engine. A transfer entry
// point that lets an option-shaped name through therefore returns the
// closed-store error instead of the validation refusal — and this test
// fails. No embedded engine, file or network is ever touched.
func newClosedTransferStore() *EmbeddedDoltStore {
	s := &EmbeddedDoltStore{branch: "main"}
	s.closed.Store(true)
	return s
}

// TestEmbeddedTransferEntryPointsRejectLeadingDash is the embedded-store
// boundary of the option-injection fix (GH#7876): every named transfer and
// sync entry point must refuse an option-shaped remote or peer name BEFORE
// CommitPending, credentials or any connection work — a closed store proves
// nothing else ran.
func TestEmbeddedTransferEntryPointsRejectLeadingDash(t *testing.T) {
	ctx := context.Background()
	wantErr := func(name string) string {
		return "invalid remote name \"" + name + "\": must not start with '-'"
	}

	cases := []struct {
		name   string
		operand string
		run    func(*EmbeddedDoltStore) error
	}{
		{name: "push_remote", operand: "--force", run: func(s *EmbeddedDoltStore) error { return s.PushRemote(ctx, "--force", false) }},
		{name: "push_remote_forced", operand: "--force", run: func(s *EmbeddedDoltStore) error { return s.PushRemote(ctx, "--force", true) }},
		{name: "pull_remote", operand: "-f", run: func(s *EmbeddedDoltStore) error { return s.PullRemote(ctx, "-f") }},
		{name: "pull_remote_with_strategy", operand: "--force", run: func(s *EmbeddedDoltStore) error { return s.PullRemoteWithStrategy(ctx, "--force", "ours") }},
		{name: "push_to_peer", operand: "--force", run: func(s *EmbeddedDoltStore) error { return s.PushTo(ctx, "--force") }},
		{name: "pull_from_peer", operand: "--force", run: func(s *EmbeddedDoltStore) error {
			_, err := s.PullFrom(ctx, "--force")
			return err
		}},
		{name: "fetch_peer", operand: "--force", run: func(s *EmbeddedDoltStore) error { return s.Fetch(ctx, "--force") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(newClosedTransferStore())
			if err == nil {
				t.Fatal("transfer entry point accepted an option-shaped operand; want the leading-dash refusal")
			}
			if err.Error() != wantErr(tc.operand) {
				t.Fatalf("refusal = %q, want exactly %q", err.Error(), wantErr(tc.operand))
			}
		})
	}

	t.Run("sync_peer", func(t *testing.T) {
		result, err := newClosedTransferStore().Sync(ctx, "--force", "")
		if err == nil {
			t.Fatal("Sync accepted an option-shaped peer; want the leading-dash refusal")
		}
		if err.Error() != wantErr("--force") {
			t.Fatalf("refusal = %q, want exactly %q", err.Error(), wantErr("--force"))
		}
		if result == nil || result.Error == nil {
			t.Fatal("Sync must record the refusal on its result alongside returning it")
		}
	})

	t.Run("valid_operand_control", func(t *testing.T) {
		// A legal name passes validation and dies on the closed store instead
		// — proof the refusals above were the name check.
		err := newClosedTransferStore().PushRemote(ctx, "1st", false)
		if err == nil {
			t.Fatal("PushRemote on the closed store succeeded; the control lost its teeth")
		}
		if err.Error() == wantErr("1st") {
			t.Fatalf("valid remote 1st was refused as option-shaped: %v", err)
		}
	})
}

// TestEmbeddedReadOnlyTransferStillRefused pins the preservation requirement:
// the new operand validation must not weaken the existing strict read-only
// refusal (ErrReadOnly) on a read-only embedded store.
func TestEmbeddedReadOnlyTransferStillRefused(t *testing.T) {
	ctx := context.Background()
	s := &EmbeddedDoltStore{branch: "main", readOnly: true}
	if err := s.PushRemote(ctx, "origin", false); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("PushRemote on a read-only embedded store = %v, want ErrReadOnly", err)
	}
}
