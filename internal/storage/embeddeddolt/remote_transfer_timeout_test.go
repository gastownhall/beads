//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// blockingTransferDB is a DBConn whose push/fetch statements block until
// their context ends, standing in for a transfer hung on a dead transport
// (GH#7989's git/ssh orphan class).
type blockingTransferDB struct{}

func (blockingTransferDB) ExecContext(ctx context.Context, query string, _ ...any) (sql.Result, error) {
	if strings.Contains(query, "DOLT_PUSH") || strings.Contains(query, "DOLT_FETCH") {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, nil
}

func (blockingTransferDB) QueryContext(_ context.Context, query string, _ ...any) (*sql.Rows, error) {
	return nil, errors.New("unexpected query: " + query)
}

func (blockingTransferDB) QueryRowContext(_ context.Context, query string, _ ...any) *sql.Row {
	return nil
}

// installTransferTimeoutSeams swaps every remote entry-point seam for one
// that replays the REAL versioncontrolops operation against a blocked fake,
// so the test observes exactly what context the embedded entry point handed
// down — including the transfer deadline it attached.
func installTransferTimeoutSeams(t *testing.T) {
	t.Helper()
	fake := blockingTransferDB{}
	prevPush, prevForce := vcPush, vcForcePush
	prevPull, prevStrategy := vcPull, vcPullWithStrategy
	prevFetch := vcFetch
	t.Cleanup(func() {
		vcPush, vcForcePush = prevPush, prevForce
		vcPull, vcPullWithStrategy = prevPull, prevStrategy
		vcFetch = prevFetch
	})
	vcPush = func(ctx context.Context, _ versioncontrolops.DBConn, remote, branch, user string) error {
		return versioncontrolops.Push(ctx, fake, remote, branch, user)
	}
	vcForcePush = func(ctx context.Context, _ versioncontrolops.DBConn, remote, branch, user string) error {
		return versioncontrolops.ForcePush(ctx, fake, remote, branch, user)
	}
	vcPull = func(ctx context.Context, _ versioncontrolops.DBConn, remote, branch, user string) error {
		return versioncontrolops.Pull(ctx, fake, remote, branch, user)
	}
	vcPullWithStrategy = func(ctx context.Context, _ versioncontrolops.DBConn, remote, branch, user, strategy string) error {
		return versioncontrolops.PullWithStrategy(ctx, fake, remote, branch, user, strategy)
	}
	vcFetch = func(ctx context.Context, _ versioncontrolops.DBConn, peer, user string) error {
		return versioncontrolops.Fetch(ctx, fake, peer, user)
	}
}

// TestRemoteEntryPointsAttachTransferTimeout pins GH#7989 AC-7: every embedded
// remote entry point attaches the configured dolt.remote-timeout deadline to
// the context it passes its push/fetch statements, so a positive configured
// duration applies at every one of the ten verbs. Each seam replays the real
// versioncontrolops operation against a blocked fake; the transfer must come
// back inside the configured duration as a TransferTimeoutError carrying it.
func TestRemoteEntryPointsAttachTransferTimeout(t *testing.T) {
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Setenv("BD_DOLT_REMOTE_TIMEOUT", "250ms")
	installTransferTimeoutSeams(t)
	store := newPeerAuthTestStore(t)
	ctx := t.Context()

	wantTimeout := config.DoltRemoteTimeout()
	if wantTimeout <= 0 || wantTimeout >= transferProbeCeiling {
		t.Fatalf("BD_DOLT_REMOTE_TIMEOUT=250ms resolved to %s", wantTimeout)
	}

	cases := []struct {
		name string
		call func(*EmbeddedDoltStore, context.Context) error
	}{
		{"Push", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.Push(ctx) }},
		{"ForcePush", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.ForcePush(ctx) }},
		{"PushRemote", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.PushRemote(ctx, "team", false) }},
		{"PushRemote force", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.PushRemote(ctx, "team", true) }},
		{"PushTo", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.PushTo(ctx, "team") }},
		{"Pull", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.Pull(ctx) }},
		{"PullRemote", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.PullRemote(ctx, "team") }},
		{"PullFrom", func(s *EmbeddedDoltStore, ctx context.Context) error {
			_, err := s.PullFrom(ctx, "team")
			return err
		}},
		{"PullWithStrategy", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.PullWithStrategy(ctx, "ours") }},
		{"PullRemoteWithStrategy", func(s *EmbeddedDoltStore, ctx context.Context) error {
			return s.PullRemoteWithStrategy(ctx, "team", "ours")
		}},
		{"Fetch", func(s *EmbeddedDoltStore, ctx context.Context) error { return s.Fetch(ctx, "team") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A watchdog deadline only a regression trips: without the
			// attached transfer deadline the blocked statement would
			// otherwise hang on the test context until Go's timeout.
			caseCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			err := tc.call(store, caseCtx)
			if err == nil {
				t.Fatal("blocked transfer returned nil")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("blocked transfer error is %v, want context.DeadlineExceeded", err)
			}
			var tte *versioncontrolops.TransferTimeoutError
			if !errors.As(err, &tte) {
				t.Fatalf("blocked transfer error is %T, want *versioncontrolops.TransferTimeoutError: %v", err, err)
			}
			if tte.Timeout != wantTimeout {
				t.Fatalf("transfer deadline is %s, want the configured %s", tte.Timeout, wantTimeout)
			}
		})
	}
}

// transferProbeCeiling is an upper sanity bound on the configured test
// deadline: the assertions here need the env's 250ms value, not the
// five-minute default (which would hang every blocked case).
const transferProbeCeiling = time.Minute