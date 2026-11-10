package dolt

import (
	"context"
	"testing"
)

// TestServerTransferEntryPointsRejectLeadingDash is the server-store boundary
// of the option-injection fix (GH#7876): push, pull, fetch and sync entry
// points must refuse an option-shaped remote or peer name before any
// database, subprocess, remote or staging work. The store's pool is a
// fail-on-use sqlmock (see newFailingPublishStore), so a transfer that runs
// anyway fails with a dependency error instead of the refusal — and the
// exact-message assertion fails.
func TestServerTransferEntryPointsRejectLeadingDash(t *testing.T) {
	ctx := context.Background()
	const wantErr = `invalid remote name "--force": must not start with '-'`

	t.Run("push_remote_named", func(t *testing.T) {
		s := newFailingPublishStore(t, false, false)
		if err := s.PushRemote(ctx, "--force", false); err == nil || err.Error() != wantErr {
			t.Fatalf("PushRemote(--force) = %v, want exactly %q", err, wantErr)
		}
	})
	t.Run("push_to_peer", func(t *testing.T) {
		s := newFailingPublishStore(t, false, false)
		if err := s.PushTo(ctx, "--force"); err == nil || err.Error() != wantErr {
			t.Fatalf("PushTo(--force) = %v, want exactly %q", err, wantErr)
		}
	})
	t.Run("pull_remote_named", func(t *testing.T) {
		s := newFailingPublishStore(t, false, false)
		if err := s.PullRemote(ctx, "--force"); err == nil || err.Error() != wantErr {
			t.Fatalf("PullRemote(--force) = %v, want exactly %q", err, wantErr)
		}
	})
	t.Run("pull_from_peer", func(t *testing.T) {
		s := newFailingPublishStore(t, false, false)
		if _, err := s.PullFrom(ctx, "--force"); err == nil || err.Error() != wantErr {
			t.Fatalf("PullFrom(--force) = %v, want exactly %q", err, wantErr)
		}
	})
	t.Run("fetch_peer", func(t *testing.T) {
		s := newFailingPublishStore(t, false, false)
		if err := s.Fetch(ctx, "--force"); err == nil || err.Error() != wantErr {
			t.Fatalf("Fetch(--force) = %v, want exactly %q", err, wantErr)
		}
	})
	t.Run("sync_peer", func(t *testing.T) {
		s := newFailingPublishStore(t, false, false)
		result, err := s.Sync(ctx, "--force", "")
		if err == nil || err.Error() != wantErr {
			t.Fatalf("Sync(--force) = %v, want exactly %q", err, wantErr)
		}
		if result == nil || result.Error == nil {
			t.Fatal("Sync must record the refusal on its result alongside returning it")
		}
		if result.Fetched || result.Merged || result.Pushed {
			t.Errorf("refused Sync still reported work: fetched=%v merged=%v pushed=%v",
				result.Fetched, result.Merged, result.Pushed)
		}
	})

	// Controls: valid operands pass the validation and die on the store's
	// deliberately unusable dependencies instead — proving the refusal above
	// was the name check, not the fail-on-use plumbing.
	t.Run("valid_remote_reaches_transport", func(t *testing.T) {
		s := newFailingPublishStore(t, false, false)
		err := s.PushRemote(ctx, "1st", false)
		if err == nil {
			t.Fatal("PushRemote on the fail-on-use store succeeded; the control lost its teeth")
		}
		if err.Error() == wantErr {
			t.Fatalf("valid remote 1st was refused as option-shaped: %v", err)
		}
	})
}
